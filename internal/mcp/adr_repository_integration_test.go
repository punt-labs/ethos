//go:build linux || darwin

package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/adr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCP_ADRRepositoryAndConfigurationRecovery(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "storage acceptance requires the real CLI binary")
	env := setupMCPTestEnv(t)
	legacyDir := filepath.Join(env.home, ".punt-labs", "ethos", "adrs")
	legacy := adr.NewStore(legacyDir)
	old := &adr.ADR{Title: "Legacy decision", Context: "Earlier context", Decision: "Keep the old record"}
	require.NoError(t, legacy.Create(old))
	oldBytes, err := os.ReadFile(filepath.Join(legacyDir, old.ID+".yaml"))
	require.NoError(t, err)
	c := startMCPClient(t, env)

	show := callTool(t, c, "adr", map[string]interface{}{"method": "show", "id": old.ID})
	require.False(t, show.IsError, resultText(t, show))
	assert.Contains(t, resultText(t, show), old.Title)
	create := callTool(t, c, "adr", map[string]interface{}{
		"method": "create", "title": "Repository decision", "decision": "Keep the record in Git",
		"context_text": "Work in " + env.repo + "/source; notify someone@example.com",
	})
	require.False(t, create.IsError, resultText(t, create))
	var created adr.ADR
	require.NoError(t, json.Unmarshal([]byte(resultText(t, create)), &created))
	assert.NotEqual(t, old.ID, created.ID)
	repoPath := filepath.Join(env.repo, ".punt-labs", "ethos", "adrs", created.ID+".yaml")
	stored, err := os.ReadFile(repoPath)
	require.NoError(t, err)
	assert.NotContains(t, string(stored), env.repo)
	assert.NotContains(t, string(stored), "someone@example.com")
	_, err = os.Stat(filepath.Join(legacyDir, created.ID+".yaml"))
	require.ErrorIs(t, err, os.ErrNotExist)
	stillOld, err := os.ReadFile(filepath.Join(legacyDir, old.ID+".yaml"))
	require.NoError(t, err)
	assert.Equal(t, oldBytes, stillOld)

	// The same process must refuse an invalid policy, including reads, and
	// recover on the next request without retaining a stale global-only store.
	configPath := filepath.Join(env.repo, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("record: invalid\n"), 0o600))
	for _, method := range []string{"list", "create"} {
		result := callTool(t, c, "adr", map[string]interface{}{
			"method": method, "title": "Must refuse", "decision": "Never written",
		})
		require.True(t, result.IsError, resultText(t, result))
		assert.Contains(t, resultText(t, result), "record")
	}
	missions := callTool(t, c, "mission", map[string]interface{}{"method": "list"})
	require.True(t, missions.IsError, "invalid recording policy must not answer from a global fallback: %s", resultText(t, missions))
	assert.Contains(t, resultText(t, missions), "record")
	require.NoError(t, os.WriteFile(configPath, []byte("record: full\nagent: test-agent\n"), 0o600))
	show = callTool(t, c, "adr", map[string]interface{}{"method": "show", "id": created.ID})
	require.False(t, show.IsError, resultText(t, show))
	assert.Contains(t, resultText(t, show), created.Title)

	// A malformed repository record must not disappear from a successful list.
	require.NoError(t, os.WriteFile(repoPath, []byte("title: [invalid"), 0o600))
	list := callTool(t, c, "adr", map[string]interface{}{"method": "list"})
	require.True(t, list.IsError, resultText(t, list))
	assert.Contains(t, resultText(t, list), created.ID)
}
