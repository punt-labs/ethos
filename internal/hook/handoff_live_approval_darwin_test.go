package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandoffLiveApprovalRevisionAndLinkedWorktree(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	writeFile(t, filepath.Join(root, ".punt-labs/ethos.yaml"), "checks: project\n")
	checkPolicy(t, root, "project", "  - {name: live, stage: handoff, files: all, run: [/bin/true]}\n")
	sidecar := filepath.Join(".punt-labs/ethos/checks/snapshots", "project.yaml")
	const profile = "checks:\n  live:\n    profile: linux-worktree-v1\n    isolation: live-verified\n    epoch: proof\n    sources: {mode: worktree}\n"
	writeFile(t, filepath.Join(root, sidecar), profile)
	stageGit(t, root, "add", ".")
	stageGit(t, root, "-c", "user.name=T", "-c", "user.email=t@t", "commit", "-qm", "policy")
	sibling := filepath.Join(t.TempDir(), "linked")
	stageGit(t, root, "worktree", "add", "--detach", sibling)
	selected := func(checkout string) liveApprovalPolicy {
		t.Helper()
		got, err := ResolveHandoffLivePolicy(checkout, "project")
		require.NoError(t, err)
		return got
	}
	local := selected(root)
	linked := selected(sibling)
	require.Equal(t, local.Repo, linked.Repo)
	require.Equal(t, local.Revision, linked.Revision)
	require.Empty(t, local.approval())
	require.NoError(t, local.Approve())
	nonce := linked.approval()
	require.NotEmpty(t, nonce)
	writeFile(t, filepath.Join(sibling, sidecar), profile+"# edit makes a new revision\n")
	require.Empty(t, selected(sibling).approval())
	writeFile(t, filepath.Join(sibling, sidecar), profile)
	policyPath := filepath.Join(sibling, ".punt-labs/ethos/checks/project.yaml")
	policyYAML, err := os.ReadFile(policyPath)
	require.NoError(t, err)
	writeFile(t, policyPath, string(policyYAML)+"# edit makes a new revision\n")
	require.Empty(t, selected(sibling).approval())
	require.Equal(t, nonce, selected(root).approval())
	require.NoError(t, linked.Revoke())
	require.Empty(t, local.approval())
	require.NoError(t, local.Approve())
	require.NotEqual(t, nonce, local.approval())
}
