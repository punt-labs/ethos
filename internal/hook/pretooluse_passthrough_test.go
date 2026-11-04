package hook

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// In Claude Code a PreToolUse "allow" approves the call without asking, past
// the user's permission checks. The hook is not a permission authority outside
// a verifier spawn, so every pass-through must carry no permissionDecision.
func TestPreToolUse_PassThroughNeverDecidesPermission(t *testing.T) {
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("HOME", t.TempDir())
	for name, p := range map[string]map[string]any{
		"Bash":      {"tool_name": "Bash", "tool_input": map[string]any{"command": "touch /x"}},
		"Read":      {"tool_name": "Read", "tool_input": map[string]any{"file_path": "/x"}},
		"Write":     {"tool_name": "Write", "tool_input": map[string]any{"file_path": "/x"}},
		"Edit":      {"tool_name": "Edit", "tool_input": map[string]any{"file_path": "/x"}},
		"ethos MCP": {"tool_name": "mcp__plugin_ethos_self__identity", "tool_input": map[string]any{"method": "list"}},
		"other MCP": {"tool_name": "mcp__plugin_biff_tty__plan", "tool_input": map[string]any{}},
		"Agent":     {"tool_name": "Agent", "tool_input": map[string]any{"subagent_type": "bwk"}},
	} {
		t.Run(name, func(t *testing.T) {
			p["session_id"] = "sess-1"
			data, err := json.Marshal(p)
			require.NoError(t, err)
			var out bytes.Buffer
			require.NoError(t, HandlePreToolUse(strings.NewReader(string(data)), &out))
			assert.NotContains(t, out.String(), "permissionDecision", out.String())
			assert.NotContains(t, out.String(), `"allow"`, out.String())
		})
	}
}

// A verifier spawn with an allowlist passes calls inside it through without a
// decision too, and still denies calls outside it.
func TestPreToolUse_VerifierInsideAllowlistPassesThrough(t *testing.T) {
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "docs/")
	t.Setenv("MISSION_ID", "")
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(
		`{"tool_name":"Bash","tool_input":{"command":"ls"}}`), &out))
	assert.NotContains(t, out.String(), "permissionDecision")
}
