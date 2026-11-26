package hook

import (
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/diagnostic"
	"github.com/stretchr/testify/require"
)

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
	policy, err := ResolveHandoffLivePolicy(root, "project")
	require.NoError(t, err)
	require.NoError(t, policy.Approve())
	return root
}
