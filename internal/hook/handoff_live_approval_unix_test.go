//go:build linux || darwin

package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
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
		got, err := ResolveHandoffLivePolicy(checkout, "", "project")
		require.NoError(t, err)
		return got
	}
	local := selected(root)
	linked := selected(sibling)
	description, err := local.Description()
	require.NoError(t, err)
	require.Contains(t, description, "live")
	_, err = ResolveHandoffLivePolicy(sibling, "Explore", "project")
	require.Error(t, err)
	_, err = ResolveHandoffLivePolicy(sibling, "", "other")
	require.ErrorContains(t, err, "has no valid live-verified check")
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
	revocation, err := ResolveHandoffLiveRevocation(sibling, "project")
	require.NoError(t, err)
	require.Empty(t, revocation.approval())
	require.NoError(t, linked.Revoke())
	require.Empty(t, local.approval())
	require.NoError(t, local.Approve())
	require.NotEqual(t, nonce, local.approval())
	require.NoError(t, revocation.Revoke())
	require.Empty(t, local.approval())
}

func TestHandoffLiveApprovalRejectsUnsafePins(t *testing.T) {
	root, _ := postEditFixture(t)
	t.Setenv("HOME", t.TempDir())
	policy, err := newLiveApprovalPolicy(root, "project", "policy-sha")
	require.NoError(t, err)
	require.NoError(t, policy.Revoke())
	require.NoError(t, policy.Approve())
	path, err := policy.pinPath(false)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0o644))
	require.Empty(t, policy.approval())
	require.NoError(t, policy.Approve())
	writeFile(t, path, "{invalid")
	require.Empty(t, policy.approval())
	busy, err := audit.TryLock(path+".lock", func() error {
		require.ErrorContains(t, policy.Revoke(), "live approval is being updated")
		return nil
	})
	require.NoError(t, err)
	require.False(t, busy)
	require.NoError(t, policy.Revoke())
	require.NoError(t, policy.Revoke())
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o755))
	require.ErrorContains(t, policy.Revoke(), "live approval directory is not private")
	require.ErrorContains(t, policy.Approve(), "live approval directory is not private")
	t.Setenv("HOME", "relative")
	require.ErrorContains(t, policy.Approve(), "home directory must be absolute")
	require.Empty(t, policy.approval())
	t.Setenv("HOME", t.TempDir())
	machineRoot := filepath.Join(os.Getenv("HOME"), ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(machineRoot, 0o700))
	require.NoError(t, os.Symlink(filepath.Join(machineRoot, "live-approvals"), filepath.Join(machineRoot, "live-approvals")))
	require.Error(t, policy.Approve(), "an approval directory must not follow a symlink loop")

	for _, slot := range []string{".", "../escape"} {
		_, err := newLiveApprovalPolicy(root, slot, "policy-sha")
		require.Error(t, err)
	}
	t.Setenv("HOME", root)
	require.ErrorContains(t, policy.Approve(), "live approval directory must be outside the checkout")
	require.ErrorContains(t, (liveApprovalPolicy{}).Approve(), "no resolved live policy revision")
}
