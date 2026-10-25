//go:build linux || darwin

package mcp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allText joins every text block of a result.
func allText(t *testing.T, result *mcplib.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range result.Content {
		tc, ok := c.(mcplib.TextContent)
		require.True(t, ok, "%T", c)
		b.WriteString(tc.Text + "\n")
	}
	return b.String()
}

// A running `ethos serve` started on a broken .git refuses writes; once the
// .git is repaired, the next call writes to the repository store, with no
// restart.
func TestMCP_RepairedGitTakesEffectWithoutRestart(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}
	env := setupMCPTestEnv(t)
	dot := filepath.Join(env.repo, ".git")
	saved := filepath.Join(env.repo, "saved.git")
	require.NoError(t, os.Rename(dot, saved))
	require.NoError(t, os.WriteFile(dot, nil, 0o644)) // git refuses an empty gitfile

	c := startMCPClient(t, env)
	create := map[string]interface{}{"method": "create", "name": "River Tam", "handle": "river", "kind": "human"}

	result := callTool(t, c, "identity", create)
	require.True(t, result.IsError)
	assert.Contains(t, allText(t, result), "refusing")

	require.NoError(t, os.Remove(dot))
	require.NoError(t, os.Rename(saved, dot))

	result = callTool(t, c, "identity", create)
	assert.False(t, result.IsError, allText(t, result))
	assert.FileExists(t, filepath.Join(env.repo, ".punt-labs", "ethos", "identities", "river.yaml"),
		"the write lands in the repository store")
	result = callTool(t, c, "identity", map[string]interface{}{"method": "list"})
	assert.NotContains(t, allText(t, result), "could not be located")
}
