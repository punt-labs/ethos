//go:build darwin

package hook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/stretchr/testify/require"
)

func TestDarwinHandoffLoopbackVerdict(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	root, _ := postEditFixture(t)
	writeFile(t, filepath.Join(root, ".gitignore"), ".punt-labs/local/\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", fmt.Sprintf(`  - name: loopback
    stage: handoff
    files: all
    run: [/usr/bin/nc, -z, -w, "1", 127.0.0.1, "%d"]
`, listener.Addr().(*net.TCPAddr).Port))
	sidecar := filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml")
	const profile = `checks:
  loopback:
    profile: linux-worktree-v1
    epoch: loopback
    sources: {mode: worktree}
`
	var logs bytes.Buffer
	ctx := diagnostic.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))
	writeFile(t, sidecar, profile)
	invocation, _ := diagnostic.WithHookInvocation(ctx)
	require.NotEmpty(t, RunStageChecks(invocation, root, "worker", "handoff").Blocking)
	writeFile(t, sidecar, profile+"    network: loopback\n")
	for range 2 {
		invocation, _ := diagnostic.WithHookInvocation(ctx)
		require.Empty(t, RunStageChecks(invocation, root, "worker", "handoff").Blocking)
	}
	require.Contains(t, logs.String(), `outcome=completed`)
	require.Contains(t, logs.String(), `outcome=reused`)
	require.Equal(t, 2, strings.Count(logs.String(), `isolation="residual (host localhost services reachable, shared port space)"`))
}

func TestDarwinHandoffSnapshotIsolation(t *testing.T) {
	if !handoffSandboxSupported() {
		t.Fatal("Darwin sandbox self-test failed")
	}
	checkout, scratch, outside := t.TempDir(), t.TempDir(), t.TempDir()
	live := filepath.Join(checkout, "input")
	if err := os.WriteFile(live, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink, hardlink := filepath.Join(outside, "symlink"), filepath.Join(outside, "hardlink")
	if err := os.Symlink(live, symlink); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(live, hardlink); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	proof, err := digestGateInputs(ctx, checkout, "", []string{"input"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Base(filepath.Dir(checkout))
	done := make(chan error, 1)
	go func() {
		for _, phase := range []struct{ event, content string }{{"editing", "changed"}, {"reverting", "original"}} {
			for {
				events, globErr := filepath.Glob(filepath.Join("/private/tmp", "eh-*", "s", marker+"-"+phase.event))
				if globErr != nil {
					done <- globErr
					return
				}
				if len(events) == 1 {
					if writeErr := os.WriteFile(live, []byte(phase.content), 0o600); writeErr != nil {
						done <- writeErr
						return
					}
					done <- os.WriteFile(events[0]+"-ack", nil, 0o600)
					break
				}
				select {
				case <-ctx.Done():
					done <- ctx.Err()
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
		}
	}()
	physical, err := filepath.EvalSymlinks(live)
	if err != nil {
		t.Fatal(err)
	}
	script := `set -eu
	    test "$PWD/input" = "$1"
	    marker=$2
	    shift 2
	    for p; do
	        if /bin/cat "${p#path:}" >/dev/null 2>&1; then
	            echo "unexpected live read: $p"; exit 1
	        fi
	    done
	    if (echo corrupted > input) 2>/dev/null; then exit 1; fi
	    for phase in editing reverting; do
	        : > "$TMPDIR/$marker-$phase"
	        while [ ! -e "$TMPDIR/$marker-$phase-ack" ]; do /bin/sleep .01; done
	        test "$(/bin/cat input)" = original
	    done`
	check := editCheck{name: "isolation", cwd: checkout, timeout: 10 * time.Second,
		argv: []string{"/bin/sh", "-c", script, "sh", live, marker, "path:" + live, "path:" + symlink, "path:" + hardlink,
			"path:/System/Volumes/Data" + physical}}
	caseAlias := strings.Replace(physical, "/TestDarwin", "/testdarwin", 1)
	if info, err := os.Stat(caseAlias); err == nil && info.Mode().IsRegular() && caseAlias != physical {
		check.argv = append(check.argv, "path:"+caseAlias)
	}
	result, err := executeHandoffSnapshot(ctx, scratch, checkout, proof, []string{"input"}, check)
	if err != nil || result != "" {
		t.Fatalf("snapshot check: result=%q err=%v", result, err)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDarwinHandoffRejectsLiveAliasOperand(t *testing.T) {
	checkout, scratch, outside := t.TempDir(), t.TempDir(), t.TempDir()
	live := filepath.Join(checkout, "input")
	if err := os.WriteFile(live, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(outside, "alias")
	if err := os.Symlink(live, alias); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	inputs := []string{"input"}
	proof, err := digestGateInputs(ctx, checkout, "", inputs)
	if err != nil {
		t.Fatal(err)
	}
	check := editCheck{cwd: gateRoot, argv: []string{"/bin/sh", "-c", "exit 1", "probe", alias}}
	got, err := executeHandoffSnapshot(ctx, scratch, checkout, proof, inputs, check)
	if err == nil || !strings.Contains(err.Error(), "a literal argument refers to the live checkout") || got != "" {
		t.Fatalf("wanted live alias deferral, got result=%q err=%v", got, err)
	}
}

func TestDarwinHandoffSnapshotDefersOnLaunchFailure(t *testing.T) {
	checkout, scratch := t.TempDir(), t.TempDir()
	proof, err := digestGateInputs(context.Background(), checkout, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	check := editCheck{name: "missing executable", argv: []string{"ethos-no-such-handoff-command"}}
	got, err := executeHandoffSnapshot(context.Background(), scratch, checkout, proof, nil, check)
	var deferred *darwinHandoffDeferred
	if !errors.As(err, &deferred) || got != "" || !strings.Contains(err.Error(), "sandbox launch") {
		t.Fatalf("wanted sandbox launch deferral, got result=%q err=%v", got, err)
	}
}

func TestDarwinHandoffSnapshotExit125IsCheckFailure(t *testing.T) {
	checkout, scratch := t.TempDir(), t.TempDir()
	ctx := context.Background()
	proof, err := digestGateInputs(ctx, checkout, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	check := editCheck{name: "exit 125", cwd: gateRoot, argv: []string{"/bin/sh", "-c", "echo check-output; exit 125"}}
	got, err := executeHandoffSnapshot(ctx, scratch, checkout, proof, nil, check)
	if err != nil || !strings.Contains(got, "exit status 125") || !strings.Contains(got, "check-output") {
		t.Fatalf("wanted an ordinary exit-125 check failure, got result=%q err=%v", got, err)
	}
}

func TestDarwinHandoffSnapshotRetriesInterruptedExits(t *testing.T) {
	for _, tc := range []struct {
		name, script, reason string
	}{
		{"129", "exit 129", "check exited 129 (likely killed or interrupted); not cached"},
		{"130", "exit 130", "check exited 130 (likely killed or interrupted); not cached"},
		{"137", "exit 137", "check exited 137 (likely killed or interrupted); not cached"},
		{"143", "exit 143", "check exited 143 (likely killed or interrupted); not cached"},
		{"signal", "kill -KILL $$", "snapshot check did not complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, root, "project", "  - name: snapshot-check\n    stage: handoff\n    files: all\n    run: [/bin/sh, -c, '"+tc.script+"']\n    snapshot: {profile: linux-shell-v1, inputs: [README], epoch: v1}\n")
			var logs bytes.Buffer
			ctx := diagnostic.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))
			for range 2 {
				invocation, _ := diagnostic.WithHookInvocation(ctx)
				feedback := RunStageChecks(invocation, root, "worker", "handoff")
				require.Contains(t, feedback.Blocking, tc.reason)
				require.Equal(t, feedback.Blocking, feedback.Notice)
				slots, err := filepath.Glob(filepath.Join(root, ".punt-labs", "local", "ethos", "handoff-v1", "*", "*.json"))
				require.NoError(t, err)
				require.Empty(t, slots)
			}
			require.Equal(t, 2, strings.Count(logs.String(), "outcome=launched"))
			require.NotContains(t, logs.String(), "outcome=reused")
		})
	}
}

func TestDarwinHandoffSnapshotSetupUsesCheckDeadline(t *testing.T) {
	checkout, scratch := t.TempDir(), t.TempDir()
	ctx := context.Background()
	inputs := []string{"input"}
	input := filepath.Join(checkout, inputs[0])
	if err := os.WriteFile(input, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	proof, err := digestGateInputs(ctx, checkout, "", inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := editCheck{cwd: gateRoot, argv: []string{"/bin/sh", "-c", "exit 0"}, timeout: time.Nanosecond}
	got, err := executeHandoffSnapshot(ctx, scratch, checkout, proof, inputs, check)
	var deferred *darwinHandoffDeferred
	if !errors.As(err, &deferred) || !errors.Is(err, context.DeadlineExceeded) || got != "" {
		t.Fatalf("wanted setup deadline deferral before proof comparison, got result=%q err=%v", got, err)
	}
}

func TestDarwinHandoffSnapshotUsesShortPrivateRoot(t *testing.T) {
	checkout := t.TempDir()
	scratch := filepath.Join(checkout, "scratch")
	if err := os.Mkdir(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	proof, err := digestGateInputs(ctx, checkout, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	check := editCheck{name: "root", cwd: gateRoot, failOnOutput: true,
		argv: []string{"/bin/sh", "-c", `test "$PWD" = "$1" && printf 'root=%s\n' "$1"`, "sh", "{root}"}}
	first := ""
	for range 2 {
		got, err := executeHandoffSnapshot(ctx, scratch, checkout, proof, nil, check)
		_, path, ok := strings.Cut(got, "\nroot=")
		path = strings.TrimSpace(path)
		if err != nil || !ok {
			t.Fatalf("wanted a snapshot root from the check, got result=%q err=%v", got, err)
		}
		const psStartTimeAndPadding = 29
		if psStartTimeAndPadding+len(path+"/.venv/bin/python ./voxd") > 80 {
			t.Fatalf("Vox command at snapshot root %q would exceed 80 columns", path)
		}
		if path == first {
			t.Fatalf("reused snapshot root %q", path)
		}
		first = path
		if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("private run directory was not removed: %v", err)
		}
	}
}

func TestDarwinHandoffLogicalRoots(t *testing.T) {
	checkout := t.TempDir()
	ctx := context.Background()
	proof, err := digestGateInputs(ctx, checkout, "", nil)
	require.NoError(t, err)
	check := editCheck{name: "roots", cwd: gateRoot, failOnOutput: true,
		argv: []string{"/bin/sh", "-ec", `
			test "$1" = "$PWD" && test "$1" = "$ETHOS_HANDOFF_ROOT"
			test "$2" = "$ETHOS_HANDOFF_RUNTIME" && test -d "$2"
			test "$3" = "$ETHOS_HANDOFF_SCRATCH" && test "$3" = "$TMPDIR"
			test "${1%/*}/r" = "$2" && test "${1%/*}/s" = "$3"
			if (echo bad > "$2/denied") 2>/dev/null; then exit 1; fi
			echo ok > "$3/result"
			/bin/cat "$3/result"
		`, "sh", "{root}", "{runtime}", "{scratch}"}}
	result, err := executeHandoffSnapshot(ctx, checkout, checkout, proof, nil, check)
	require.NoError(t, err)
	require.Contains(t, result, "\nok\n")

	check.argv = []string{"/bin/sh", "-ec", `
		test "$1" = "$RUNTIME_PROBE"
		test "$RUNTIME_PROBE" = "$ETHOS_HANDOFF_RUNTIME/bin/probe"
		"$1"
	`, "sh", "{runtime}/bin/probe"}
	plan := darwinHandoffPlan{
		env: map[string]string{"RUNTIME_PROBE": "{runtime}/bin/probe"},
		prepareRuntime: func(_ context.Context, runtime string) error {
			bin := filepath.Join(runtime, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(bin, "probe"), []byte("#!/bin/sh\necho runtime-ok\n"), 0o700)
		},
	}
	result, err = runDarwinHandoffPlan(ctx, checkout, proof, nil, check, plan)
	require.NoError(t, err)
	require.Contains(t, result, "\nruntime-ok\n")
}

func TestDarwinHandoffScratchSupportsUnixSocket(t *testing.T) {
	checkout, scratch := t.TempDir(), t.TempDir()
	ctx := context.Background()
	proof, err := digestGateInputs(ctx, checkout, "", nil)
	require.NoError(t, err)
	check := editCheck{name: "socket", cwd: gateRoot, failOnOutput: true,
		argv: []string{"/usr/bin/perl", "-MSocket", "-MFile::Path=make_path", "-e", `
			my $tmp = $ENV{TMPDIR};
			my $dir = "$tmp/pytest-of-devin/pytest-100/popen-gw0/test_worker_environment0";
			my $path = "$dir/worker.sock";
			die "socket path too long: $path\n" if length($path) >= 104;
			make_path($dir);
			socket(my $socket, PF_UNIX, SOCK_STREAM, 0) or die "socket: $!\n";
			bind($socket, pack_sockaddr_un($path)) or die "bind: $!\n";
			listen($socket, 1) or die "listen: $!\n";
			print "socket-ok\n";
		`}}
	result, err := executeHandoffSnapshot(ctx, scratch, checkout, proof, nil, check)
	require.NoError(t, err)
	require.Contains(t, result, "\nsocket-ok\n")
}

func TestDarwinHandoffWorktreeContract(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "README"), "input")
	writeFile(t, filepath.Join(root, "scratch", "live-only"), "not declared")
	inputs := []string{"README"}
	require.NoError(t, os.Chmod(filepath.Join(root, "README"), 0o640))
	proof, err := digestGateInputsWithMode(ctx, root, "", inputs, true)
	require.NoError(t, err)
	check := editCheck{
		name: "worktree", cwd: gateRoot, failOnOutput: true,
		policy: &checkset.Check{WorktreeSnapshot: &checkset.WorktreeSnapshot{
			Path:    []string{"{root}/bin"},
			Env:     map[string]string{"CACHE": "{scratch}/cache dir"},
			Scratch: checkset.SnapshotScratch{Checkout: []string{"scratch"}},
		}},
		argv: []string{"sh", "-ec", `
			test "$(pwd -P)" = "$ETHOS_HANDOFF_ROOT"
			test "$ETHOS_HANDOFF_CHECKOUT_NAME" = "$1"
			test "$2" = "$ETHOS_HANDOFF_ROOT/one space"
			test "$HOME" = "$ETHOS_HANDOFF_SCRATCH/home"
			test "$TMPDIR" = "$ETHOS_HANDOFF_SCRATCH"
			case "$TMPDIR" in /private/tmp/eh-????????/s) ;; *) exit 3 ;; esac
			test "$PATH" = "$ETHOS_HANDOFF_ROOT/bin:/usr/local/bin:/usr/bin:/bin"
			test "$CACHE" = "$ETHOS_HANDOFF_SCRATCH/cache dir"
			test -f README
			test "$(stat -f %Lp README)" = 640
			test ! -e .git
			test ! -e scratch/live-only
			touch scratch/local "$HOME/available" "$TMPDIR/temporary"
			test -f scratch/local
			if (echo changed > README) 2>/dev/null; then exit 4; fi
			if (echo leaked > "/$3/marker") 2>/dev/null; then exit 5; fi
			echo worktree-ok
		`, "proof", filepath.Base(root), "{root}/one space", strings.TrimPrefix(root, "/")},
	}
	result, err := executeHandoffSnapshot(ctx, t.TempDir(), root, proof, inputs, check)
	require.NoError(t, err)
	require.Contains(t, result, "\nworktree-ok\n")
	require.NoFileExists(t, filepath.Join(root, "marker"))
	require.NoFileExists(t, filepath.Join(root, "scratch", "local"))
	check.policy.WorktreeSnapshot.Network = "loopback"
	result, err = executeHandoffSnapshot(ctx, t.TempDir(), root, proof, inputs, check)
	require.NoError(t, err)
	require.Contains(t, result, "\nworktree-ok\n")
	check.policy.WorktreeSnapshot.Network = ""
	check.policy.WorktreeSnapshot.Git = &checkset.SnapshotGit{Refs: []string{"refs/heads/release"}}
	result, err = executeHandoffSnapshot(ctx, t.TempDir(), root, proof, inputs, check)
	require.ErrorContains(t, err, "worktree profile field git not supported by this runner yet")
	require.Empty(t, result)
	check.policy.WorktreeSnapshot.Git = nil
	check.policy.WorktreeSnapshot.Scratch.Checkout = []string{"README"}
	result, err = executeHandoffSnapshot(ctx, t.TempDir(), root, proof, inputs, check)
	require.ErrorIs(t, err, errHandoffScratchShadowsInput)
	require.Empty(t, result)
}

func TestDarwinHandoffWorktreeDirectCommand(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", `  - name: full-gate
    stage: handoff
    files: ['**/input.txt']
    cwd: file_dir
    run: [sh, -ec, 'test "$1" = "$PWD/input.txt" && grep -qx clean "$1" || (echo broken-shell >&2; exit 23)', probe, '{files}']
`)
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), `checks:
  full-gate:
    profile: linux-worktree-v1
    epoch: direct-argv
    sources: {mode: worktree}
`)
	path := filepath.Join(root, "sub", "input.txt")
	other := filepath.Join(root, "second", "input.txt")
	writeFile(t, path, "clean\n")
	writeFile(t, other, "clean\n")
	run := func() StageFeedback {
		return runStageChecks(context.Background(), root, "worker", "handoff", func() ([]string, error) {
			return []string{path, other}, nil
		})
	}
	require.Empty(t, run().Blocking)
	writeFile(t, path, "broken\n")
	feedback := run()
	require.Contains(t, feedback.Blocking, "exit status 23")
	require.Contains(t, feedback.Blocking, "broken-shell")
	require.NotContains(t, feedback.Blocking, "deferred")
}

func TestDarwinHandoffStripe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stripe")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pin, waited, err := pinHandoffStripe(ctx, path)
	if err != nil || waited {
		t.Fatalf("pin: waited=%v err=%v", waited, err)
	}
	defer pin.Close()
	second, waited, err := pinHandoffStripe(ctx, path)
	if err != nil || waited {
		t.Fatalf("second pin: waited=%v err=%v", waited, err)
	}
	if busy, err := withUnpinnedStripe(pin, func() error {
		t.Error("pruned while another owner held a pin")
		return nil
	}); err != nil || !busy {
		t.Errorf("prune: busy=%v err=%v", busy, err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	pruneErr := errors.New("prune failed")
	if busy, err := withUnpinnedStripe(pin, func() error { return pruneErr }); busy || !errors.Is(err, pruneErr) {
		t.Errorf("prune: busy=%v err=%v", busy, err)
	}
}

func TestDarwinHandoffSnapshotDefersOnSetupFailure(t *testing.T) {
	checkout, scratch := t.TempDir(), t.TempDir()
	path := filepath.Join(checkout, "input")
	if err := os.WriteFile(path, []byte("declared"), 0o600); err != nil {
		t.Fatal(err)
	}
	proof, err := digestGateInputs(context.Background(), checkout, "", []string{"input"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	check := editCheck{name: "must not run", cwd: checkout, argv: []string{"/bin/sh", "-c", "exit 7"}}
	got, err := executeHandoffSnapshot(context.Background(), scratch, checkout, proof, []string{"input"}, check)
	var deferred *darwinHandoffDeferred
	if !errors.As(err, &deferred) || got != "" || !strings.Contains(err.Error(), "proof") {
		t.Fatalf("wanted proof deferral, got result=%q err=%v", got, err)
	}
}

func TestDarwinOpenGateInputRejectsSymlink(t *testing.T) {
	checkout := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(checkout, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "input"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("input", filepath.Join(checkout, "inside")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(checkout)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"inside", "alias/input", "../input"} {
		if file, err := openGateInput(root, name); err == nil {
			file.Close()
			t.Errorf("opened forbidden input %q", name)
		}
	}
}
