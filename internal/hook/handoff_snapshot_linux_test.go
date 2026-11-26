package hook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/checkset"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/stretchr/testify/require"
)

func TestHandoffSandboxRejectsInvalidPlan(t *testing.T) {
	root := t.TempDir()
	require.ErrorContains(t, enterHandoffSandbox(root, root, gateRoot, "", "", []string{"/bin/bash"}, nil, nil), "invalid sandbox plan")
	require.ErrorContains(t, enterHandoffSandbox(root, root, "/etc", "", "", []string{"/bin/sh"}, nil, nil), "invalid sandbox plan")
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
	proof, err := digestGateInputs(context.Background(), live, "", inputs)
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
	proof, err := digestGateInputs(context.Background(), live, "", inputs)
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
	proof, err := digestGateInputs(context.Background(), live, "", inputs)
	require.NoError(t, err)
	failure, err := executeHandoffSnapshot(context.Background(), t.TempDir(), live, proof, inputs, editCheck{
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
	})
	skipIfHandoffSandboxUnavailable(t, err)
	require.NoError(t, err)
	require.Empty(t, failure)
}

func TestHandoffWorktreeSandboxMountsPrivateCheckoutScratch(t *testing.T) {
	live := t.TempDir()
	writeFile(t, filepath.Join(live, "README"), "snapshot input\n")
	writeFile(t, filepath.Join(live, ".venv", "ambient-only"), "not a snapshot input\n")
	inputs := []string{"README"}
	proof, err := digestGateInputs(context.Background(), live, "", inputs)
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
	proof, err = digestGateInputs(context.Background(), live, "", inputs)
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
