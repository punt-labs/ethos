//go:build darwin

package hook

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/stretchr/testify/require"
)

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
	done := make(chan error, 1)
	go func() {
		for _, phase := range []struct{ event, content string }{{"editing", "changed"}, {"reverting", "original"}} {
			for {
				events, globErr := filepath.Glob(filepath.Join(scratch, "*", "tmp", phase.event))
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
	    shift
	    for p; do
	        if /bin/cat "${p#path:}" >/dev/null 2>&1; then
	            echo "unexpected live read: $p"; exit 1
	        fi
	    done
	    if (echo corrupted > input) 2>/dev/null; then exit 1; fi
	    for phase in editing reverting; do
	        : > "$TMPDIR/$phase"
	        while [ ! -e "$TMPDIR/$phase-ack" ]; do /bin/sleep .01; done
	        test "$(/bin/cat input)" = original
	    done`
	check := editCheck{name: "isolation", cwd: checkout, timeout: 10 * time.Second,
		argv: []string{"/bin/sh", "-c", script, "sh", live, "path:" + live, "path:" + symlink, "path:" + hardlink,
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
