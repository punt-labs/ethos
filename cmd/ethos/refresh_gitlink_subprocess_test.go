//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/agentmark"
)

func TestCLI_EnableRefreshPreservesAgentsWhenGitlinkConfigIsMissing(t *testing.T) {
	se := freshCLIEnv(t, "refresh-human")
	require.NoError(t, os.MkdirAll(filepath.Join(se.repo, ".punt-labs", "ethos"), 0o755))
	cmd := exec.Command("git", "update-index", "--add", "--cacheinfo",
		"160000,0000000000000000000000000000000000000001,.punt-labs/ethos")
	cmd.Dir, cmd.Env = se.repo, se.env
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)

	agent := filepath.Join(se.repo, ".claude", "agents", "worker.md")
	original := agentmark.Add("---\nname: worker\n---\n")
	require.NoError(t, os.MkdirAll(filepath.Dir(agent), 0o755))
	require.NoError(t, os.WriteFile(agent, []byte(original), 0o644))
	_, stderr, code := runCLI(t, se, "enable", "--refresh")
	assert.Zero(t, code, stderr)
	assert.Contains(t, stderr, "git submodule update --init")
	assert.Contains(t, stderr, "agent definitions left unchanged")
	current, err := os.ReadFile(agent)
	require.NoError(t, err)
	assert.Equal(t, original, string(current))
}

func TestCLI_DoctorAcceptsRefreshedLegacyStub(t *testing.T) {
	se := freshCLIEnv(t, "refresh-human")
	store := filepath.Join(se.repo, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(filepath.Join(store, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(store, "agents", "reviewer.md"), []byte("# Legacy reviewer\n"), 0o644))
	gitRunEnv(t, store, se.env, "init", "-q")
	gitRunEnv(t, store, se.env, "add", "agents/reviewer.md")
	gitRunEnv(t, store, se.env, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "Add reviewer")
	gitRunEnv(t, se.repo, se.env, "add", ".punt-labs/ethos")

	out, stderr, code := runCLI(t, se, "enable", "--refresh")
	require.Zero(t, code, stderr)
	require.Contains(t, out, "refreshed Claude agent definitions")
	stub := filepath.Join(se.repo, ".claude", "agents", "reviewer.md")
	content, err := os.ReadFile(stub)
	require.NoError(t, err)
	require.True(t, agentmark.Has(string(content)))

	out, stderr, code = runCLI(t, se, "doctor")
	assert.Equal(t, 1, code, "%s\n%s", out, stderr)
	assert.Contains(t, out, ".punt-labs/ethos is a separate git working tree")
	assert.Contains(t, out, "generated and installed agent bytes match their sources")

	// Doctor rejects separate Git worktrees independently of agent currency.
	require.NoError(t, os.Rename(filepath.Join(store, ".git"), filepath.Join(se.home, "legacy-git")))
	gitRunEnv(t, se.repo, se.env, "add", ".punt-labs/ethos")
	out, stderr, code = runCLI(t, se, "doctor")
	require.Zero(t, code, "%s\n%s", out, stderr)
	assert.Contains(t, out, "generated and installed agent bytes match their sources")

	require.NoError(t, os.WriteFile(stub, append(content, []byte("Edited body\n")...), 0o644))
	out, stderr, code = runCLI(t, se, "doctor")
	assert.Equal(t, 1, code, "%s\n%s", out, stderr)
	assert.Contains(t, out, "agent drift (missing or different): reviewer.md")
}
