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
	assert.Equal(t, 1, code, stderr)
	assert.Contains(t, stderr, "git submodule update --init")
	current, err := os.ReadFile(agent)
	require.NoError(t, err)
	assert.Equal(t, original, string(current))
}
