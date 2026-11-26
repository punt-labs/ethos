package hook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/stretchr/testify/require"
)

func TestHandoffSnapshotAcceptsLargeInputManifest(t *testing.T) {
	live, scratch := t.TempDir(), t.TempDir()
	ctx := context.Background()
	check := editCheck{cwd: gateRoot, timeout: 5 * time.Second, argv: []string{"/bin/sh", "-ec", "test -f input"}}
	writeFile(t, filepath.Join(live, "input"), "fixture")
	inputs := []string{"input"}
	proof, err := digestGateInputs(ctx, live, "", inputs)
	require.NoError(t, err)
	failure, err := executeHandoffSnapshot(ctx, scratch, live, proof, inputs, check)
	skipIfHandoffSandboxUnavailable(t, err)
	require.NoError(t, err)
	require.Empty(t, failure)

	for i := range 640 {
		name := fmt.Sprintf("%0230d", i)
		writeFile(t, filepath.Join(live, name), "fixture")
		inputs = append(inputs, name)
	}
	proof, err = digestGateInputs(ctx, live, "", inputs)
	require.NoError(t, err)
	failure, err = executeHandoffSnapshot(ctx, t.TempDir(), live, proof, inputs, check)
	require.NoError(t, err)
	require.Empty(t, failure)
}

func TestHandoffWorktreeProofRejectsStaleInputSelection(t *testing.T) {
	root, _ := postEditFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), gateInputTime)
	defer cancel()
	writeFile(t, filepath.Join(root, "first"), "original")
	inputs, err := worktreeSnapshotInputs(ctx, root)
	require.NoError(t, err)
	writeFile(t, filepath.Join(root, "new-input"), "appeared after discovery")
	_, _, err = handoffGateInputs(ctx, root, inputs, true)
	require.ErrorContains(t, err, "snapshot input selection changed")
}

func TestHandoffWaiterKeepsOwnerResultAcrossStripeCollision(t *testing.T) {
	root, _ := postEditFixture(t)
	p, err := (resolve.Located{Checkout: root, Store: root}).Layout()
	require.NoError(t, err)
	cache := filepath.Join(p.LocalRoot(), "handoff-v1")
	a := gateResult{Key: "abc" + strings.Repeat("1", 61), Proof: "proof", Failure: "first result"}
	b := gateResult{Key: "abc" + strings.Repeat("2", 61), Proof: "proof", Failure: "another result"}
	aLock, aSlot := handoffGatePaths(cache, a.Key)
	bLock, bSlot := handoffGatePaths(cache, b.Key)
	for _, dir := range []string{filepath.Dir(aSlot), filepath.Dir(bSlot)} {
		require.NoError(t, layout.EnsureLocalDir(p, dir))
	}

	busy, err := audit.TryLock(aLock, func() error {
		waiting, err := audit.TryLock(aLock, func() error {
			t.Error("same-key waiter acquired the owner's lease")
			return nil
		})
		require.NoError(t, err)
		require.True(t, waiting)
		colliding, err := audit.TryLock(bLock, func() error {
			t.Error("colliding request acquired the owner's lease")
			return nil
		})
		require.NoError(t, err)
		require.True(t, colliding)
		return writeGateResult(p, aSlot, a)
	})
	require.NoError(t, err)
	require.False(t, busy)

	busy, err = audit.TryLock(bLock, func() error { return writeGateResult(p, bSlot, b) })
	require.NoError(t, err)
	require.False(t, busy)

	busy, err = audit.TryLock(aLock, func() error {
		result, ok := readGateResult(aSlot)
		require.True(t, ok)
		require.Equal(t, a, result)
		return nil
	})
	require.NoError(t, err)
	require.False(t, busy)
}

func TestHandoffRetentionPreservesLiveWaiters(t *testing.T) {
	cache := t.TempDir()
	a := fmt.Sprintf("abc%061x", 1)
	next := fmt.Sprintf("abc%061x", gateRecordsPerStripe+1)
	lock, slot := handoffGatePaths(cache, next)
	require.NoError(t, os.Mkdir(filepath.Dir(slot), 0o700))
	for i := 1; i <= gateRecordsPerStripe; i++ {
		key := fmt.Sprintf("abc%061x", i)
		_, record := handoffGatePaths(cache, key)
		data, err := json.Marshal(gateResult{Key: key, Proof: "proof", Failure: "owner result"})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(record, data, 0o600))
	}
	_, owed := handoffGatePaths(cache, a)
	old := time.Now().Add(-2 * gateRecordAge)

	var waiter *os.File
	busy, err := audit.TryLock(lock, func() error {
		var err error
		waiter, _, err = pinHandoffStripe(context.Background(), lock+".pin")
		if err != nil {
			return err
		}
		waiting, err := audit.TryLock(lock, func() error {
			t.Error("same-key waiter acquired the owner's lease")
			return nil
		})
		require.True(t, waiting)
		require.NoError(t, err)
		return os.Chtimes(owed, old, old)
	})
	require.NoError(t, err)
	require.False(t, busy)
	t.Cleanup(func() { _ = waiter.Close() })
	prune := func() bool {
		t.Helper()
		pin, _, err := pinHandoffStripe(context.Background(), lock+".pin")
		require.NoError(t, err)
		defer pin.Close()
		var room bool
		busy, err := audit.TryLock(lock, func() error {
			room, err = retainHandoffResults(pin, slot, false, time.Now())
			return err
		})
		require.NoError(t, err)
		require.False(t, busy)
		return room
	}
	require.False(t, prune(), "a full stripe must defer rather than evict a live waiter's result")
	busy, err = audit.TryLock(lock, func() error {
		room, err := retainHandoffResults(waiter, owed, true, time.Now())
		require.NoError(t, err)
		require.True(t, room)
		saved, ok := readGateResult(owed)
		require.True(t, ok)
		require.Equal(t, "owner result", saved.Failure)
		return nil
	})
	require.NoError(t, err)
	require.False(t, busy)
	require.NoError(t, waiter.Close())
	require.True(t, prune())
	require.NoFileExists(t, owed)
}

func TestHandoffPinWaitIsCancellable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	busy, err := audit.TryLock(path, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		pin, waited, err := pinHandoffStripe(ctx, path)
		require.Nil(t, pin)
		require.True(t, waited)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		return nil
	})
	require.NoError(t, err)
	require.False(t, busy)
	pin, waited, err := pinHandoffStripe(context.Background(), path)
	require.NoError(t, err)
	require.False(t, waited)
	require.NoError(t, pin.Close())
	pin, _, err = pinHandoffStripe(context.Background(), filepath.Join(path, "unreachable"))
	require.Nil(t, pin)
	require.Error(t, err)
}

func TestHandoffOversizedResultRemainsReadable(t *testing.T) {
	root, _ := postEditFixture(t)
	p, err := (resolve.Located{Checkout: root, Store: root}).Layout()
	require.NoError(t, err)
	slot := filepath.Join(t.TempDir(), "result.json")
	original := gateResult{Key: "key", Proof: "proof", Failure: strings.Repeat("<\n", 100000)}
	require.NoError(t, writeGateResult(p, slot, original))
	result, ok := readGateResult(slot)
	require.True(t, ok)
	require.Equal(t, original.Key, result.Key)
	require.Equal(t, original.Proof, result.Proof)
	require.NotEmpty(t, result.Failure)
	require.Less(t, len(result.Failure), len(original.Failure))
}

func TestHandoffResultKeepsPendingOnPublishFailure(t *testing.T) {
	root, _ := postEditFixture(t)
	p, err := (resolve.Located{Checkout: root, Store: root}).Layout()
	require.NoError(t, err)
	key := strings.Repeat("1", 64)
	_, slot := handoffGatePaths(filepath.Join(p.LocalRoot(), "handoff-v1"), key)
	require.NoError(t, layout.EnsureLocalDir(p, filepath.Dir(slot)))
	original := gateResult{Key: key, Proof: "proof", Pending: true}
	require.NoError(t, writeGateResult(p, slot, original))

	terminal := gateResult{Key: key, Proof: "proof"}
	t.Run("redaction guard", func(t *testing.T) {
		unredacted := terminal
		unredacted.Failure = "failed to read /home/alice/work"
		require.ErrorContains(t, writeGateResult(p, slot, unredacted), "failed the redaction guard")
	})
	t.Run("home unavailable", func(t *testing.T) {
		t.Setenv("HOME", "")
		require.Error(t, writeGateResult(p, slot, terminal))
	})
	t.Run("staging unavailable", func(t *testing.T) {
		require.NoError(t, os.Remove(p.TempDir()))
		require.NoError(t, os.WriteFile(p.TempDir(), nil, 0o600))
		require.Error(t, writeGateResult(p, slot, terminal))
	})
	saved, ok := readGateResult(slot)
	require.True(t, ok)
	require.Equal(t, original, saved)
}

func TestHandoffMaterializesDeclaredRegularFiles(t *testing.T) {
	root := t.TempDir()
	dest := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "pkg"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "gate"), []byte("exit 0\n"), 0o700))
	require.NoError(t, os.Symlink("pkg", filepath.Join(root, "linked")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "undeclared"), []byte("other"), 0o600))
	ctx := context.Background()
	inputs := []string{"pkg/gate", "pkg/gate"}
	proof, err := digestGateInputs(ctx, root, "", inputs)
	require.NoError(t, err)
	digest, err := digestGateInputs(ctx, root, dest, inputs)
	require.NoError(t, err)
	require.Equal(t, proof, digest)
	data, err := os.ReadFile(filepath.Join(dest, "pkg", "gate"))
	require.NoError(t, err)
	require.Equal(t, "exit 0\n", string(data))
	info, err := os.Stat(filepath.Join(dest, "pkg", "gate"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o500), info.Mode().Perm())
	require.NoFileExists(t, filepath.Join(dest, "undeclared"))
	require.True(t, gatePlanInputsPresent(dest, []string{"/bin/sh", "/checkout/pkg/gate"}))
	require.False(t, gatePlanInputsPresent(dest, []string{"/bin/sh", "/checkout/undeclared"}))
	_, err = digestGateInputs(ctx, root, "", []string{"linked/gate"})
	require.Error(t, err)
	_, err = digestGateInputs(ctx, root, "", []string{"pkg"})
	require.ErrorContains(t, err, "not a regular file")
	_, err = digestGateInputs(ctx, root, dest, inputs)
	require.ErrorIs(t, err, os.ErrExist)
	src, err := os.OpenRoot(root)
	require.NoError(t, err)
	defer src.Close()
	_, err = copyGateInput(ctx, sha256.New(), src, "", "pkg/gate", 1, false)
	require.ErrorContains(t, err, "within budget")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = digestGateInputs(cancelled, root, "", inputs)
	require.ErrorIs(t, err, context.Canceled)
}

func TestHandoffCheckoutFingerprintBudget(t *testing.T) {
	root, _ := postEditFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := checkoutStateBudgeted(root, &gateHashBudget{ctx: ctx, remaining: 1})
	require.ErrorContains(t, err, "git fingerprint output exceeds byte budget")
	_, err = checkoutStateWithBudget(context.Background(), root)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var out bytes.Buffer
	budget := gateHashBudget{ctx: ctx, dst: &out, remaining: 2}
	_, err = budget.Write([]byte("abc"))
	require.ErrorContains(t, err, "exceeds byte budget")
	require.Empty(t, out.String())
}

func TestHandoffCheckoutFingerprintSeesStatusHiddenEdits(t *testing.T) {
	root, _ := postEditFixture(t)
	path := filepath.Join(root, "README")
	stageGit(t, root, "update-index", "--assume-unchanged", "README")
	fingerprint := func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		state, err := checkoutStateWithBudget(ctx, root)
		require.NoError(t, err)
		return state
	}
	before := fingerprint()
	require.NoError(t, os.Remove(path))
	missing := fingerprint()
	require.NotEqual(t, before, missing)
	require.NoError(t, os.Symlink("one", path))
	linked := fingerprint()
	require.NotEqual(t, missing, linked)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink("two", path))
	require.NotEqual(t, linked, fingerprint())
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o700))
	directory := fingerprint()
	stageGit(t, path, "init", "-q")
	stageGit(t, path, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "nested")
	nested := fingerprint()
	require.NotEqual(t, directory, nested)
	writeFile(t, filepath.Join(path, "untracked"), "nested edit\n")
	require.NotEqual(t, nested, fingerprint())
}

func TestHandoffDefersWhenInputsCannotBeVerified(t *testing.T) {
	root, _ := postEditFixture(t)
	var logs bytes.Buffer
	ctx := diagnostic.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))
	ctx, _ = diagnostic.WithHookInvocation(ctx)
	check := checkset.Check{
		Name: "gate", Files: checkset.Patterns{All: true},
		Run: checkset.Strings{"/bin/sh", filepath.Join(root, "README")}, OnFail: "block",
		Snapshot: &checkset.Snapshot{Profile: "linux-shell-v1", Inputs: checkset.Strings{"README"}, Epoch: "test"},
	}
	run := func(ctx context.Context) StageFeedback {
		return runHandoffGate(ctx, root, "worker", []string{"README"}, check, []checkset.Check{check})
	}
	require.Contains(t, run(ctx).Blocking, "a literal argument refers to the live checkout")
	check.Run = checkset.Strings{"/bin/sh", "-c", "exit 0"}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.Contains(t, run(cancelled).Blocking, "cannot verify declared snapshot inputs")
	check.Snapshot.Inputs = checkset.Strings{"missing"}
	require.Contains(t, run(ctx).Blocking, "cannot verify declared snapshot inputs")
	require.NotContains(t, logs.String(), "outcome=launched")
}

func TestHandoffSandboxRejectsInvalidPlan(t *testing.T) {
	root := t.TempDir()
	require.ErrorContains(t, enterHandoffSandbox(root, root, gateRoot, "", nil, []string{"/bin/bash"}, nil, nil), "invalid sandbox plan")
	require.ErrorContains(t, enterHandoffSandbox(root, root, "/etc", "", nil, []string{"/bin/sh"}, nil, nil), "invalid sandbox plan")
	_, err := executeHandoffSnapshot(context.Background(), root, "/usr", "", nil, editCheck{})
	require.ErrorContains(t, err, "checkout overlaps the sandbox system runtime")
	_, err = handoffSandboxIdentity(filepath.Join(root, "missing"))
	require.ErrorIs(t, err, os.ErrNotExist)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, hashWorktreePath(&bytes.Buffer{}, root, &gateHashBudget{ctx: ctx}), context.Canceled)
}

func TestHandoffSandboxReportsSetupPhase(t *testing.T) {
	root := t.TempDir()
	_, err := executeHandoffSnapshot(context.Background(), root, t.TempDir(), "", nil, editCheck{
		argv: []string{"/bin/sh"}, cwd: "/invalid", timeout: time.Second,
	})
	if err != nil && strings.Contains(err.Error(), "isolation unavailable on this host") {
		t.Skip(err.Error())
	}
	require.ErrorContains(t, err, "validate sandbox plan")
}

func TestHandoffSandboxReportsMissingSnapshotCwd(t *testing.T) {
	live, scratch := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(live, "README"), "snapshot input\n")
	require.NoError(t, os.Mkdir(filepath.Join(live, "not-declared"), 0o700))
	inputs := []string{"README"}
	proof, err := digestGateInputs(context.Background(), live, "", inputs)
	require.NoError(t, err)
	_, err = executeHandoffSnapshot(context.Background(), scratch, live, proof, inputs, editCheck{
		argv: []string{"/bin/sh", "-c", "exit 0"}, cwd: gateRoot + "/not-declared", timeout: 5 * time.Second,
	})
	skipIfHandoffSandboxUnavailable(t, err)
	require.ErrorContains(t, err, "check cwd missing from snapshot")
}

func TestHandoffWorktreeSandboxBashProcessSubstitution(t *testing.T) {
	live := t.TempDir()
	writeFile(t, filepath.Join(live, "README"), "snapshot input\n")
	inputs := []string{"README"}
	ctx := context.Background()
	proof, err := digestGateInputsWithMode(ctx, live, "", inputs, true)
	require.NoError(t, err)
	failure, err := executeHandoffSnapshot(ctx, t.TempDir(), live, proof, inputs, editCheck{
		name: "process-substitution", cwd: gateRoot, timeout: 5 * time.Second,
		policy: &checkset.Check{WorktreeSnapshot: &checkset.WorktreeSnapshot{}},
		argv:   []string{"/bin/bash", "-ec", `test "$(cat <(echo ok))" = ok`},
	})
	skipIfHandoffSandboxUnavailable(t, err)
	require.NoError(t, err)
	require.Empty(t, failure)
}

func TestHandoffWorktreeSandboxKeepsSocketTMPDIRShortAndPrivate(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("test requires system python3")
	}
	live, sandbox, ambient := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), strings.Repeat("deep", 30))
	require.NoError(t, os.MkdirAll(ambient, 0o700))
	t.Setenv("TMPDIR", ambient)
	writeFile(t, filepath.Join(ambient, "ambient-only"), "not a sandbox input")
	writeFile(t, filepath.Join(live, "README"), "snapshot input\n")
	inputs := []string{"README"}
	proof, err := digestGateInputsWithMode(context.Background(), live, "", inputs, true)
	require.NoError(t, err)
	failure, err := executeHandoffSnapshot(context.Background(), sandbox, live, proof, inputs, editCheck{
		name: "socket", cwd: gateRoot, timeout: 5 * time.Second,
		policy: &checkset.Check{WorktreeSnapshot: &checkset.WorktreeSnapshot{}},
		argv: []string{"/bin/sh", "-c", `/usr/bin/python3 - <<'PY'
import os
import socket

tmp = os.environ["TMPDIR"]
assert tmp == "/tmp"
assert not os.listdir(tmp)
assert not os.listdir(os.environ["HOME"])
assert not os.path.exists(os.environ["HOME"] + "/ambient-only")
sock = socket.socket(socket.AF_UNIX)
sock.bind(os.path.join(tmp, "s" * 40))
with open(tmp + "/disk-marker", "w") as marker:
    marker.write("private")
PY`},
	})
	skipIfHandoffSandboxUnavailable(t, err)
	require.NoError(t, err)
	require.Empty(t, failure)
	marker, err := os.ReadFile(filepath.Join(sandbox, "tmp", "disk-marker"))
	require.NoError(t, err)
	require.Equal(t, "private", string(marker))
	require.NoFileExists(t, filepath.Join(ambient, "disk-marker"))
}

func TestHandoffWorktreeSandboxUsesInvokingIdentity(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires a non-root invoking UID")
	}
	live := t.TempDir()
	writeFile(t, filepath.Join(live, "README"), "snapshot input\n")
	inputs := []string{"README"}
	proof, err := digestGateInputsWithMode(context.Background(), live, "", inputs, true)
	require.NoError(t, err)
	failure, err := executeHandoffSnapshot(context.Background(), t.TempDir(), live, proof, inputs, editCheck{
		name: "identity", cwd: gateRoot, timeout: 5 * time.Second,
		policy: &checkset.Check{WorktreeSnapshot: &checkset.WorktreeSnapshot{}},
		argv: []string{"/bin/sh", "-ec", `
printf 'uid=%s gid=%s\n' "$(id -u)" "$(id -g)"
test "$(id -u)" = "$1"
test "$(id -g)" = "$2"
while read -r field value; do
  if test "$field" = CapEff:; then
    test "$value" = 0000000000000000
    exit
  fi
done < /proc/self/status
exit 1
`, "identity", fmt.Sprint(os.Geteuid()), fmt.Sprint(os.Getegid())},
	})
	skipIfHandoffSandboxUnavailable(t, err)
	require.NoError(t, err)
	require.Empty(t, failure)
}

func TestHandoffWorktreeSandboxExpandsLogicalPathPrefixes(t *testing.T) {
	live := t.TempDir()
	writeFile(t, filepath.Join(live, "README"), "snapshot input\n")
	writeFile(t, filepath.Join(live, "sub", "input"), "snapshot input\n")
	inputs := []string{"README", "sub/input"}
	proof, err := digestGateInputsWithMode(context.Background(), live, "", inputs, true)
	require.NoError(t, err)
	ctx := context.Background()
	invocation := editCheck{
		name: "paths", cwd: gateRoot, timeout: 5 * time.Second,
		policy: &checkset.Check{WorktreeSnapshot: &checkset.WorktreeSnapshot{
			Path: []string{"{scratch}/bin"},
			Env:  map[string]string{"RUFF_CACHE_DIR": "{scratch}/fallback", "GOCACHE": "{scratch}/go-build"},
			Runtime: &checkset.SnapshotRuntime{Platforms: map[string]checkset.SnapshotPlatform{
				runtime.GOOS + "-" + runtime.GOARCH: {
					Path: []string{"{runtime}/bin"},
					Env: map[string]string{
						"LANG":           "C.UTF-8",
						"RUFF_CACHE_DIR": "{scratch}/ruff-cache",
						"MYPY_CACHE_DIR": "{scratch}/mypy-cache",
					},
				},
			}},
		}},
		argv: []string{"/bin/sh", "-c", `test "$#" = 4 &&
test "$1" = "/scratch/socket test" &&
test "$2" = "/runtime/bin" &&
test "$3" = "/checkout/sub" &&
test "$RUFF_CACHE_DIR" = "/scratch/ruff-cache" &&
test "$MYPY_CACHE_DIR" = "/scratch/mypy-cache" &&
test "$GOCACHE" = "/scratch/go-build" &&
test "$PATH" = "/scratch/bin:/runtime/bin:/usr/local/bin:/usr/bin:/bin" &&
test "$(/usr/bin/locale charmap)" = "UTF-8" &&
test "$ETHOS_HANDOFF_ROOT" = "/checkout" &&
test "$ETHOS_HANDOFF_RUNTIME" = "/runtime" &&
test "$ETHOS_HANDOFF_SCRATCH" = "/scratch" &&
test "$HOME" = "$ETHOS_HANDOFF_SCRATCH/home" &&
test "$ETHOS_HANDOFF_CHECKOUT_NAME" = "$4"`,
			"paths", "{scratch}/socket test", "{runtime}/bin", "{root}/sub", filepath.Base(live)},
	}
	system, err := prepareHandoffSystem(ctx, live, invocation.policy.WorktreeSnapshot)
	require.NoError(t, err)
	failure, err := executeHandoffSnapshot(ctx, t.TempDir(), live, proof, inputs, invocation,
		handoffWorktreePlan{system: system, runtimeDir: t.TempDir()})
	skipIfHandoffSandboxUnavailable(t, err)
	require.NoError(t, err)
	require.Empty(t, failure)
}

func TestHandoffWorktreeSandboxMountsPrivateCheckoutScratch(t *testing.T) {
	live := t.TempDir()
	writeFile(t, filepath.Join(live, "README"), "snapshot input\n")
	writeFile(t, filepath.Join(live, ".venv", "ambient-only"), "not a snapshot input\n")
	inputs := []string{"README"}
	proof, err := digestGateInputsWithMode(context.Background(), live, "", inputs, true)
	require.NoError(t, err)
	policy := &checkset.Check{WorktreeSnapshot: &checkset.WorktreeSnapshot{
		Scratch: checkset.SnapshotScratch{Checkout: []string{".venv", ".pytest_cache", ".tmp/api-site"}},
	}}
	failure, err := executeHandoffSnapshot(context.Background(), t.TempDir(), live, proof, inputs, editCheck{
		name: "writable-dirs", cwd: gateRoot, timeout: 5 * time.Second, policy: policy,
		argv: []string{"/bin/sh", "-ec", `test ! -e .venv/ambient-only
for dir in .venv .pytest_cache .tmp/api-site; do
  test -d "$dir"
  touch "$dir/created"
done
test ! -w README
test ! -w .tmp
test ! -w .`},
	})
	skipIfHandoffSandboxUnavailable(t, err)
	require.NoError(t, err)
	require.Empty(t, failure)
	require.NoFileExists(t, filepath.Join(live, ".venv", "created"))
	require.NoDirExists(t, filepath.Join(live, ".pytest_cache"))
	require.NoDirExists(t, filepath.Join(live, ".tmp"))

	inputs = append(inputs, ".venv/ambient-only")
	proof, err = digestGateInputsWithMode(context.Background(), live, "", inputs, true)
	require.NoError(t, err)
	_, err = executeHandoffSnapshot(context.Background(), t.TempDir(), live, proof, inputs, editCheck{
		name: "conflict", cwd: gateRoot, timeout: 5 * time.Second, policy: policy,
		argv: []string{"/bin/sh", "-c", "exit 0"},
	})
	require.ErrorContains(t, err, "checkout scratch shadows snapshot content")
}

func TestHandoffWorktreeRunsDirectCommands(t *testing.T) {
	for _, tc := range []struct{ name, check, path, good, broken string }{
		{"make adapter", "    files: all\n    adapter: make\n    target: check\n", "Makefile",
			"check:\n\t@test \"$$PWD\" = /checkout\n",
			"check:\n\t@echo broken-make >&2; exit 23\n"},
		{"bare shell", "    files: ['**/input.txt']\n    cwd: file_dir\n    run: [sh, -ec, 'test \"$1\" = \"$PWD/input.txt\" && test \"$PWD\" != /checkout && grep -qx clean \"$1\" || (echo broken-shell >&2; exit 23)', probe, '{files}']\n", "sub/input.txt",
			"clean\n", "broken\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, root, "project", "  - name: full-gate\n    stage: handoff\n"+tc.check)
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), `checks:
  full-gate:
    profile: linux-worktree-v1
    epoch: direct-argv
    sources: {mode: worktree}
`)
			path := filepath.Join(root, tc.path)
			files := []string{path}
			if tc.name == "bare shell" {
				other := filepath.Join(root, "second", "input.txt")
				writeFile(t, other, tc.good)
				files = append(files, other)
			}
			run := func() StageFeedback {
				return runStageChecks(context.Background(), root, "worker", "handoff", func() ([]string, error) {
					return files, nil
				})
			}
			writeFile(t, path, tc.good)
			feedback := run()
			skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
			require.Empty(t, feedback.Blocking)
			writeFile(t, path, tc.broken)
			feedback = run()
			require.Contains(t, feedback.Blocking, "exit status")
			require.Contains(t, feedback.Blocking, "broken-")
			require.NotContains(t, feedback.Blocking, "deferred")
		})
	}
}

func TestHandoffWorktreeSystemRunsCgoRace(t *testing.T) {
	for _, tool := range []string{"go", "gcc", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(err)
		}
	}
	out, err := exec.Command("go", "env", "GOROOT").Output()
	require.NoError(t, err)
	goroot := strings.TrimSpace(string(out))
	if !filepath.IsAbs(goroot) {
		t.Skip("absolute Go toolchain required")
	}
	allowHandoffSystemFixtureRoot(t, goroot)
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	host := handoffSystemFixtureDir(t)
	hostFile := filepath.Join(host, "file")
	hostDir := filepath.Join(host, "dir")
	writeFile(t, hostFile, "host-file\n")
	writeFile(t, filepath.Join(hostDir, "marker"), "host-dir\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: cgo-race\n    stage: handoff\n    files: all\n    timeout: 3m\n    run: [sh, gate.sh]\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), fmt.Sprintf(`checks:
  cgo-race:
    profile: linux-worktree-v1
    epoch: cgo-race
    sources: {mode: worktree}
    path: [%q]
    env: {CGO_ENABLED: "1", GOPROXY: "off", GOTOOLCHAIN: "local", GOMAXPROCS: "4"}
    system:
      files: [%q]
      platforms:
        %s-%s: {dirs: [%q, %q], tools: [go, gcc, awk]}
`, filepath.Join(goroot, "bin"), hostFile, runtime.GOOS, runtime.GOARCH, goroot, hostDir))
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/proof\n\ngo 1.23\n")
	writeFile(t, filepath.Join(root, "cgo.go"), "package proof\n// #include <stdint.h>\nimport \"C\"\nfunc size() int { return int(C.sizeof_uint64_t) }\n")
	writeFile(t, filepath.Join(root, "cgo_test.go"), "package proof\nimport \"testing\"\nfunc TestSize(t *testing.T) { if size() != 8 { t.Fatal(size()) } }\n")
	marker := filepath.Join("/dev/shm", filepath.Base(host)+"-cgo")
	writeFile(t, filepath.Join(root, "gate.sh"), fmt.Sprintf(`set -eu
test "$(awk 'NR == 1' %q)" = host-file
grep -qx host-dir %q
if (echo write > %q) || (touch %q); then exit 31; fi
test ! -e %q
echo sandbox > %q
grep -qx sandbox %q
exec go test -race .
`, hostFile, filepath.Join(hostDir, "marker"), hostFile, filepath.Join(hostDir, "new"), marker, marker, marker))
	feedback := RunStageChecks(context.Background(), root, "worker", "handoff")
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Empty(t, feedback.Blocking)
	require.NoFileExists(t, filepath.Join(hostDir, "new"))
	require.NoFileExists(t, marker)
}

func TestHandoffWorktreeSystemFileChangeInvalidatesResult(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	file := filepath.Join(handoffSystemFixtureDir(t), "status")
	writeFile(t, file, "good\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: system-file\n    stage: handoff\n    files: all\n    run: [sh, gate.sh]\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), fmt.Sprintf(`checks:
  system-file:
    profile: linux-worktree-v1
    epoch: system-file-proof
    sources: {mode: worktree}
    system: {files: [%q]}
`, file))
	writeFile(t, filepath.Join(root, "gate.sh"), fmt.Sprintf("grep -qx good %q || (echo system-file-changed >&2; exit 42)\n", file))
	run := func() StageFeedback { return RunStageChecks(context.Background(), root, "worker", "handoff") }
	feedback := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
	require.Empty(t, feedback.Blocking)
	info, err := os.Stat(file)
	require.NoError(t, err)
	writeFile(t, file, "fail\n")
	require.NoError(t, os.Chtimes(file, info.ModTime(), info.ModTime()))
	feedback = run()
	require.Contains(t, feedback.Blocking, "exit status 42")
	require.Contains(t, feedback.Blocking, "system-file-changed")
}

func handoffSystemFixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "ethos-system-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	allowHandoffSystemFixtureRoot(t, dir)
	return dir
}

func allowHandoffSystemFixtureRoot(t *testing.T, dir string) {
	t.Helper()
	physical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	previous := worktreeSystemRoots
	worktreeSystemRoots = append([]string{dir, physical}, previous...)
	t.Cleanup(func() { worktreeSystemRoots = previous })
}

func TestHandoffWorktreeHasPrivatePTY(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip(err)
	}
	live := t.TempDir()
	writeFile(t, filepath.Join(live, "README"), "snapshot input\n")
	inputs := []string{"README"}
	proof, err := digestGateInputsWithMode(context.Background(), live, "", inputs, true)
	require.NoError(t, err)
	failure, err := executeHandoffSnapshot(context.Background(), t.TempDir(), live, proof, inputs, editCheck{
		name: "pty", cwd: gateRoot, timeout: 5 * time.Second,
		policy: &checkset.Check{WorktreeSnapshot: &checkset.WorktreeSnapshot{}},
		argv: []string{"/usr/bin/python3", "-c", `import os
assert not (set(os.listdir("/dev/pts")) - {"ptmx"})
master, slave = os.openpty()
assert os.ttyname(slave) == "/dev/pts/0"
os.write(slave, b"private")
assert os.read(master, 7) == b"private"
`},
	})
	skipIfHandoffSandboxUnavailable(t, err)
	require.NoError(t, err)
	require.Empty(t, failure)
}

func TestHandoffWorktreeRequires(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip(err)
	}
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	const policy = "  - name: full-gate\n    stage: handoff\n    files: all\n    run: [/usr/bin/python3, gate.py]\n"
	checkPolicy(t, root, "project", policy)
	const sidecar = ".punt-labs/ethos/checks/snapshots/project.yaml"
	writeFile(t, filepath.Join(root, ".gitignore"), "/"+sidecar+"\n/.punt-labs/local/\n")
	writeFile(t, filepath.Join(root, "gate.py"), `import os
assert not (set(os.listdir("/dev/pts")) - {"ptmx"})
master, slave = os.openpty()
assert os.ttyname(slave) == "/dev/pts/0"
with open("/dev/shm/ethos-probe", "x") as probe:
    probe.write("private")
`)
	path := filepath.Join(root, sidecar)
	const profile = `checks:
  full-gate:
    profile: linux-worktree-v1
    epoch: requires-probe
    sources: {mode: worktree}
`
	var logs bytes.Buffer
	ctx := diagnostic.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))
	run := func() StageFeedback {
		invocation, _ := diagnostic.WithHookInvocation(ctx)
		return RunStageChecks(invocation, root, "worker", "handoff")
	}
	writeFile(t, path, profile+"    requires: [mach_lookup]\n")
	required := run()
	require.Contains(t, required.Blocking, "mach_lookup unavailable in linux snapshot")
	require.Contains(t, required.Blocking, "Run the explicit project gate:")
	require.Contains(t, required.Blocking, "/usr/bin/python3 gate.py")
	require.Equal(t, required.Blocking, required.Notice)
	checkPolicy(t, root, "project", policy+"    on_fail: advisory\n")
	advisory := run()
	require.Empty(t, advisory.Blocking)
	require.Contains(t, advisory.Advisory, "mach_lookup unavailable in linux snapshot")
	require.NotContains(t, logs.String(), "outcome=launched")
	checkPolicy(t, root, "project", policy)

	writeFile(t, path, profile)
	inputs, err := worktreeSnapshotInputs(ctx, root)
	require.NoError(t, err)
	require.NotContains(t, inputs, sidecar)
	first := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(first.Blocking))
	require.Equal(t, StageFeedback{}, first)
	writeFile(t, path, profile+"    requires: [pty, posix_shm]\n")
	require.Equal(t, StageFeedback{}, run())
	require.Equal(t, StageFeedback{}, run())
	require.Equal(t, 2, strings.Count(logs.String(), "outcome=launched"), logs.String())
	require.Equal(t, 1, strings.Count(logs.String(), "outcome=reused"), logs.String())
}

func TestHandoffWorktreeNetwork(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip(err)
	}
	hostNetwork, err := os.Readlink("/proc/self/ns/net")
	require.NoError(t, err)
	for _, mode := range []string{"off", "loopback"} {
		t.Run(mode, func(t *testing.T) {
			root, _ := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, root, "project", fmt.Sprintf("  - name: network-probe\n    stage: handoff\n    files: all\n    fail_on_output: true\n    run: [python3, gate.py, %q, %q]\n", mode, hostNetwork))
			sidecar := `checks:
  network-probe:
    profile: linux-worktree-v1
    epoch: network-probe
    sources: {mode: worktree}
`
			if mode == "loopback" {
				sidecar += "    network: loopback\n"
			}
			writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), sidecar)
			writeFile(t, filepath.Join(root, "gate.py"), `import errno
import fcntl
import os
import socket
import struct
import sys

mode, host_ns = sys.argv[1:]
assert os.readlink("/proc/self/ns/net") != host_ns
assert [name for _, name in socket.if_nameindex()] == ["lo"]
with open("/proc/self/status") as status:
    caps = dict(line.split(":", 1) for line in status)
    assert int(caps["CapEff"], 16) == int(caps["CapAmb"], 16) == 0
with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
    flags = fcntl.ioctl(sock, 0x8913, struct.pack("16s24x", b"lo"))
    assert bool(struct.unpack_from("H", flags, 16)[0] & 1) == (mode == "loopback")

for family, local, remote in [
    (socket.AF_INET, "127.0.0.1", "192.0.2.1"),
    (socket.AF_INET6, "::1", "2001:db8::1"),
]:
    with socket.socket(family, socket.SOCK_DGRAM) as sock:
        try:
            sock.connect((remote, 12345))
        except OSError as error:
            allowed = (errno.ENETUNREACH, errno.EADDRNOTAVAIL) if mode == "off" else (errno.ENETUNREACH,)
            assert error.errno in allowed, error
        else:
            raise AssertionError("non-loopback route is present: " + remote)

    if mode == "off":
        continue
    with socket.socket(family) as listener:
        listener.settimeout(2)
        listener.bind((local, 0))
        listener.listen()
        with socket.socket(family) as client:
            client.settimeout(2)
            client.connect(listener.getsockname())
            with listener.accept()[0] as peer:
                client.sendall(b"ok")
                assert peer.recv(2) == b"ok"

print("network proof: " + mode)
`)
			feedback := RunStageChecks(context.Background(), root, "worker", "handoff")
			skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
			require.Contains(t, feedback.Blocking, "non-empty formatting output")
			require.Contains(t, feedback.Blocking, "network proof: "+mode)
		})
	}
}

func skipIfHandoffSandboxUnavailable(t *testing.T, err error) {
	t.Helper()
	if err != nil && (strings.Contains(err.Error(), "isolation unavailable on this host") ||
		strings.Contains(err.Error(), "make mount propagation private: permission denied") ||
		strings.Contains(err.Error(), "make mount propagation private: operation not permitted")) {
		t.Skip(err.Error())
	}
}

func TestHandoffSandboxReportsHighExitStatuses(t *testing.T) {
	live := t.TempDir()
	writeFile(t, filepath.Join(live, "README"), "snapshot input\n")
	inputs := []string{"README"}
	proof, err := digestGateInputs(context.Background(), live, "", inputs)
	require.NoError(t, err)
	for _, code := range []string{"126", "127", "128"} {
		failure, err := executeHandoffSnapshot(context.Background(), t.TempDir(), live, proof, inputs, editCheck{
			name: "gate", cwd: gateRoot, timeout: 5 * time.Second,
			argv: []string{"/bin/sh", "-c", "echo gate-stdout; echo gate-stderr >&2; exit \"$1\"", "gate", code},
		})
		skipIfHandoffSandboxUnavailable(t, err)
		t.Run(code, func(t *testing.T) {
			require.NoError(t, err)
			require.Contains(t, failure, "gate: exit status "+code)
			require.Contains(t, failure, "\ngate-stdout\n")
			require.Contains(t, failure, "\ngate-stderr")
		})
	}
}

func TestHandoffSnapshotRetriesInterruptedExits(t *testing.T) {
	for _, code := range []int{137, 129, 130, 143} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			root, _ := postEditFixture(t)
			t.Setenv("HOME", t.TempDir())
			writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
			checkPolicy(t, root, "project", fmt.Sprintf("  - name: snapshot-check\n    stage: handoff\n    files: all\n    run: [/bin/sh, -c, 'exit %d']\n    snapshot: {profile: linux-shell-v1, inputs: [README], epoch: v1}\n", code))
			var logs bytes.Buffer
			ctx := diagnostic.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))
			for range 2 {
				invocation, _ := diagnostic.WithHookInvocation(ctx)
				feedback := RunStageChecks(invocation, root, "worker", "handoff")
				skipIfHandoffSandboxUnavailable(t, errors.New(feedback.Blocking))
				require.Contains(t, feedback.Blocking, fmt.Sprintf("check exited %d (likely killed or interrupted); not cached", code))
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

func TestHandoffSnapshotLeaseAndProof(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	writeFile(t, filepath.Join(root, "sub", "verdict"), "1\n")
	writeFile(t, filepath.Join(root, "gate.sh"), "test \"$PWD\" = /checkout/sub && test \"$1\" = /checkout && test \"$2\" = /checkout/sub && test \"$3\" = /checkout/sub/verdict || exit 99\ntest ! -e /etc/passwd && test ! -w \"$3\" && test -z \"$CLAUDE_PROJECT_DIR\" || exit 98\nsleep 0.1\nread verdict < \"$3\"\nexit \"$verdict\"\n")
	stageGit(t, root, "add", "gate.sh")
	stageGit(t, root, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "-qm", "gate")
	setPolicy := func(epoch string) {
		checkPolicy(t, root, "project", "  - name: snapshot-check\n    stage: handoff\n    files: ['sub/verdict']\n    cwd: file_dir\n    run: [/bin/sh, /checkout/gate.sh, '{root}', '{package_dir}', '{files}']\n"+
			fmt.Sprintf("    snapshot: {profile: linux-shell-v1, inputs: [gate.sh, sub/verdict], epoch: %s}\n", epoch))
	}
	setPolicy("v1")

	var logs bytes.Buffer
	ctx := diagnostic.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))
	run := func() StageFeedback {
		invocation, _ := diagnostic.WithHookInvocation(ctx)
		return RunStageChecks(invocation, root, "worker", "handoff")
	}
	var results [2]StageFeedback
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() { results[i] = run() })
	}
	wg.Wait()
	if strings.Contains(results[0].Blocking, "isolation unavailable on this host") {
		t.Skip(results[0].Blocking)
	}
	require.Equal(t, results[0], results[1])
	require.Contains(t, results[0].Blocking, "exit status 1")
	require.Equal(t, 1, strings.Count(logs.String(), "outcome=launched"))
	require.Equal(t, 1, strings.Count(logs.String(), "outcome=reused"))

	cache := filepath.Join(root, ".punt-labs", "local", "ethos", "handoff-v1")
	slots, err := filepath.Glob(filepath.Join(cache, "*", "*.json"))
	require.NoError(t, err)
	require.Len(t, slots, 1)
	saved, ok := readGateResult(slots[0])
	require.True(t, ok)
	t.Run("reclaim orphaned pending result", func(t *testing.T) {
		pending := saved
		pending.Pending = true
		data, err := json.Marshal(pending)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(slots[0], data, 0o600))
		require.Contains(t, run().Blocking, "exit status 1")
		require.Equal(t, 2, strings.Count(logs.String(), "outcome=launched"))
		require.Equal(t, 1, strings.Count(logs.String(), "outcome=reused"))
	})
	resetTerminal := func(t *testing.T) {
		t.Helper()
		data, err := json.Marshal(saved)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(slots[0], data, 0o600))
	}
	t.Run("expire old terminal result", func(t *testing.T) {
		resetTerminal(t)
		starts := strings.Count(logs.String(), "outcome=launched")
		old := time.Now().Add(-365 * 24 * time.Hour)
		require.NoError(t, os.Chtimes(slots[0], old, old))
		require.Contains(t, run().Blocking, "exit status 1")
		require.Equal(t, starts+1, strings.Count(logs.String(), "outcome=launched"))
	})
	t.Run("bound results in a stripe", func(t *testing.T) {
		resetTerminal(t)
		for i := range 10 {
			key := fmt.Sprintf("%s%061x", saved.Key[:3], i)
			_, slot := handoffGatePaths(cache, key)
			data, err := json.Marshal(gateResult{Key: key, Proof: saved.Proof})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(slot, data, 0o600))
		}
		require.Contains(t, run().Blocking, "exit status 1")
		files, err := filepath.Glob(filepath.Join(filepath.Dir(slots[0]), "*.json"))
		require.NoError(t, err)
		require.LessOrEqual(t, len(files), 8)
	})

	writeFile(t, filepath.Join(root, "sub", "verdict"), "0\n")
	require.Equal(t, StageFeedback{}, run())
	require.Equal(t, StageFeedback{}, run())
	require.Equal(t, 4, strings.Count(logs.String(), "outcome=launched"))
	setPolicy("v2")
	require.Equal(t, StageFeedback{}, run())
	require.Equal(t, 5, strings.Count(logs.String(), "outcome=launched"))

	checkPolicy(t, root, "project", "  - name: snapshot-check\n    stage: handoff\n    files: all\n    timeout: 500ms\n    run: [/bin/sh, -c, 'sleep 10']\n"+
		"    snapshot: {profile: linux-shell-v1, inputs: [gate.sh], epoch: v3}\n")
	for range 2 {
		require.Contains(t, run().Blocking, "snapshot check timed out or was interrupted")
	}
	require.Equal(t, 7, strings.Count(logs.String(), "outcome=launched"))
}

func TestHandoffReadOnlyStopTracksIgnoredSnapshotInputs(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".gitignore"), "verdict\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: snapshot-check\n    stage: handoff\n    files: all\n    run: [/bin/sh, -c, 'exit 1']\n    snapshot: {profile: linux-shell-v1, inputs: [verdict], epoch: v1}\n")
	input := filepath.Join(root, "verdict")
	writeFile(t, input, "pass\n")
	start, err := handoffState(root, "worker")
	require.NoError(t, err)
	state, err := checkoutState(root)
	require.NoError(t, err)
	writeFile(t, input, "fail\n")
	current, err := checkoutState(root)
	require.NoError(t, err)
	require.Equal(t, state, current)
	after, err := handoffState(root, "worker")
	require.NoError(t, err)
	require.NotEqual(t, start, after)
}

type handoffLogObserver func([]byte) (int, error)

func (f handoffLogObserver) Write(b []byte) (int, error) { return f(b) }

func TestHandoffDoesNotReturnStaleSnapshotVerdict(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - name: snapshot-check\n    stage: handoff\n    files: all\n    run: [/bin/sh, -c, 'read verdict < verdict; exit \"$verdict\"']\n    snapshot: {profile: linux-shell-v1, inputs: [verdict], epoch: v1}\n")
	path := filepath.Join(root, "verdict")
	writeFile(t, path, "0\n")
	var mutationErr error
	observer := handoffLogObserver(func(b []byte) (int, error) {
		if bytes.Contains(b, []byte("outcome=launched")) {
			mutationErr = os.WriteFile(path, []byte("1\n"), 0o600)
		}
		return len(b), nil
	})
	ctx := diagnostic.WithLogger(context.Background(), slog.New(slog.NewTextHandler(observer, nil)))
	ctx, _ = diagnostic.WithHookInvocation(ctx)
	got := RunStageChecks(ctx, root, "worker", "handoff")
	if strings.Contains(got.Blocking, "isolation unavailable on this host") {
		t.Skip(got.Blocking)
	}
	require.NoError(t, mutationErr)
	require.Contains(t, got.Blocking, "checkout changed during the snapshot check")
}

func TestHandoffSnapshotRejectsChangedInputs(t *testing.T) {
	root, _ := postEditFixture(t)
	inputs := []string{"README"}
	proof, err := digestGateInputs(context.Background(), root, "", inputs)
	require.NoError(t, err)
	writeFile(t, filepath.Join(root, "README"), "another version\n")

	var logs bytes.Buffer
	ctx := diagnostic.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))
	ctx, _ = diagnostic.WithHookInvocation(ctx)
	failure, err := executeHandoffSnapshot(ctx, t.TempDir(), root, proof, inputs,
		editCheck{name: "probe", argv: []string{"/bin/sh", "-c", "exit 1"}, cwd: gateRoot, timeout: 2 * time.Second})
	if err != nil && strings.Contains(err.Error(), "isolation unavailable on this host") {
		t.Skip(err)
	}
	require.ErrorContains(t, err, "could not establish snapshot isolation")
	require.Empty(t, failure)
	require.NotContains(t, logs.String(), "outcome=launched")
}
