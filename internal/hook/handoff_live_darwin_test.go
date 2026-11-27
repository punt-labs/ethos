package hook

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/stretchr/testify/require"
)

func TestDarwinHandoffLiveDefersUnkeyedSymlink(t *testing.T) {
	root := approvedDarwinLiveGate(t, `[/bin/sh, -c, 'test "$(cat gate-input)" = pass']`, "")
	target := filepath.Join(root, "ignored-scratch", "value")
	writeFile(t, target, "pass")
	require.NoError(t, os.Symlink("ignored-scratch/value", filepath.Join(root, "gate-input")))
	var logs bytes.Buffer
	ctx, _ := diagnostic.WithHookInvocation(diagnostic.WithLogger(t.Context(), slog.New(slog.NewTextHandler(&logs, nil))))
	result := RunStageChecks(ctx, root, "worker", "handoff")
	require.Contains(t, result.Advisory, "symlink target is outside the keyed inputs", "%+v", result)
	require.Contains(t, logs.String(), "deferral_reason=")
	require.Contains(t, logs.String(), "symlink target is outside the keyed inputs")
	require.NotContains(t, logs.String(), "outcome=launched")
}

func TestDarwinHandoffLiveReportsClosingKeyError(t *testing.T) {
	root := approvedDarwinLiveGate(t, "[/bin/sh, -c, 'touch ignored-scratch/launched']", "")
	scratch := filepath.Join(root, "ignored-scratch")
	require.NoError(t, os.Mkdir(scratch, 0o700))
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+
		"if [ \"$1\" = --no-optional-locks ] && [ \"$2\" = status ] && [ -f ignored-scratch/launched ]; then exit 42; fi\n"+
		"exec "+audit.ShellQuote(realGit)+" \"$@\"\n"), 0o700))
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	var logs bytes.Buffer
	ctx, _ := diagnostic.WithHookInvocation(diagnostic.WithLogger(t.Context(), slog.New(slog.NewTextHandler(&logs, nil))))
	result := RunStageChecks(ctx, root, "worker", "handoff")
	require.Empty(t, result.Blocking)
	require.Contains(t, result.Advisory, "cannot verify checkout key during the live-verified check")
	require.Contains(t, result.Advisory, "git status: exit status 42")
	require.Contains(t, logs.String(), "outcome=launched")
	require.Contains(t, logs.String(), "outcome=deferred")
	require.Contains(t, logs.String(), "deferral_reason=")
	require.Contains(t, logs.String(), "git status: exit status 42")
	require.Contains(t, logs.String(), "key_verification_ms=")
	require.NotContains(t, logs.String(), "outcome=completed")
	require.NotContains(t, logs.String(), "outcome=reused")
}

func TestDarwinHandoffLiveNormalizesKeyedSymlink(t *testing.T) {
	root := approvedDarwinLiveGate(t, `[/bin/sh, -c, 'test "$(cat gate-input)" = pass']`, "")
	writeFile(t, filepath.Join(root, "config.txt"), "pass")
	require.NoError(t, os.Symlink("./config.txt", filepath.Join(root, "gate-input")))
	stageGit(t, root, "add", "config.txt", "gate-input")
	ctx, _ := diagnostic.WithHookInvocation(t.Context())
	require.Equal(t, StageFeedback{}, RunStageChecks(ctx, root, "worker", "handoff"))
	writeFile(t, filepath.Join(root, "config.txt"), "fail")
	ctx, _ = diagnostic.WithHookInvocation(t.Context())
	require.Contains(t, RunStageChecks(ctx, root, "worker", "handoff").Blocking, "exit status 1")
}

func TestDarwinHandoffLiveApprovalHintUsesValidSelector(t *testing.T) {
	root := approvedDarwinLiveGate(t, "[/usr/bin/true]", "")
	policy, err := ResolveHandoffLiveRevocation(root, "project")
	require.NoError(t, err)
	require.NoError(t, policy.Revoke())
	ctx, _ := diagnostic.WithHookInvocation(t.Context())
	result := RunStageChecks(ctx, root, "Explore", "handoff")
	require.Contains(t, result.Advisory, "run: ethos handoff approve-live project)")
	_, err = ResolveHandoffLivePolicy(root, "", "project")
	require.NoError(t, err)
}

func TestDarwinHandoffLiveExit127Blocks(t *testing.T) {
	root := approvedDarwinLiveGate(t, "[/bin/sh, -c, missing-ethos-test-command]", "")
	ctx, _ := diagnostic.WithHookInvocation(t.Context())
	result := RunStageChecks(ctx, root, "worker", "handoff")
	require.Contains(t, result.Blocking, "exit status 127", "%+v", result)
	require.Empty(t, result.Advisory)
}

func TestDarwinHandoffLiveCheckoutScratchDefersBeforeWatching(t *testing.T) {
	root := approvedDarwinLiveGate(t, "[/usr/bin/true]", "    scratch: {checkout: [ignored-scratch]}\n")
	ctx, _ := diagnostic.WithHookInvocation(t.Context())
	result := RunStageChecks(ctx, root, "worker", "handoff")
	require.Contains(t, result.Advisory, "scratch.checkout is unavailable with live-verified")
	require.NoDirExists(t, filepath.Join(root, "ignored-scratch"))
	require.NoDirExists(t, filepath.Join(root, ".git", "ethos-live"))
}

func approvedDarwinLiveGate(t *testing.T, run, extra string) string {
	t.Helper()
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".gitignore"), ".punt-labs/local/\nignored-scratch/\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - {name: live, stage: handoff, files: all, run: "+run+"}\n")
	writeFile(t, filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml"), "checks:\n  live:\n    profile: linux-worktree-v1\n    isolation: live-verified\n    epoch: proof\n    sources: {mode: worktree}\n"+extra)
	policy, err := ResolveHandoffLivePolicy(root, "", "project")
	require.NoError(t, err)
	require.NoError(t, policy.Approve())
	return root
}
