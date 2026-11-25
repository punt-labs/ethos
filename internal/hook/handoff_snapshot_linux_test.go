package hook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
