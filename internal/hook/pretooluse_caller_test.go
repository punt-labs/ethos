package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runPreToolUse runs the hook on payload and returns the raw JSON object it
// wrote, so a test can see which keys are absent, not only which are empty.
func runPreToolUse(t *testing.T, payload map[string]any) map[string]map[string]any {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(string(data)), &out))
	var got map[string]map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
	return got
}

// withCorroboratedPID makes CLAUDE_PID name a live ancestor of the test
// process, as it does in a hook Claude Code spawns. The package's TestMain
// strips it.
func withCorroboratedPID(t *testing.T) string {
	t.Helper()
	pid := strconv.Itoa(os.Getppid())
	t.Setenv("CLAUDE_PID", pid)
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	return pid
}

func ethosCall(extra map[string]any) map[string]any {
	p := map[string]any{
		"tool_name":  "mcp__plugin_ethos_self__identity",
		"tool_input": map[string]any{"method": "whoami", "reference": true},
		"session_id": "sess-1",
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func TestPreToolUse_DeclaresLeaderCaller(t *testing.T) {
	pid := withCorroboratedPID(t)
	got := runPreToolUse(t, ethosCall(nil))
	out := got["hookSpecificOutput"]
	assert.Equal(t, "PreToolUse", out["hookEventName"])
	assert.NotContains(t, out, "permissionDecision", "the hook must not decide permission")
	assert.Equal(t, map[string]any{
		"method": "whoami", "reference": true,
		callerArg: map[string]any{"session_id": "sess-1", "agent_id": pid},
	}, out["updatedInput"], "the original arguments plus _caller")
}

func TestPreToolUse_SubagentCallerKeyedOnAgentID(t *testing.T) {
	pid := withCorroboratedPID(t)
	got := runPreToolUse(t, ethosCall(map[string]any{"agent_id": "agent-sub1", "agent_type": "bwk"}))
	caller := got["hookSpecificOutput"]["updatedInput"].(map[string]any)[callerArg]
	assert.Equal(t, map[string]any{"session_id": "sess-1", "agent_id": "agent-sub1"}, caller)
	assert.NotEqual(t, pid, caller.(map[string]any)["agent_id"], "a subagent is not its leader")
}

func TestPreToolUse_OverwritesModelSuppliedCaller(t *testing.T) {
	pid := withCorroboratedPID(t)
	spoof := map[string]any{"session_id": "victim", "agent_id": "victim-agent"}
	for _, extra := range []map[string]any{nil, {"agent_id": "agent-sub1"}} {
		p := ethosCall(extra)
		p["tool_input"] = map[string]any{"method": "whoami", callerArg: spoof}
		caller := runPreToolUse(t, p)["hookSpecificOutput"]["updatedInput"].(map[string]any)[callerArg].(map[string]any)
		assert.Equal(t, "sess-1", caller["session_id"])
		assert.NotEqual(t, "victim-agent", caller["agent_id"])
		if extra == nil {
			assert.Equal(t, pid, caller["agent_id"])
		}
	}
}

func TestPreToolUse_NoCallerNoInjection(t *testing.T) {
	withCorroboratedPID(t)
	t.Run("no session_id", func(t *testing.T) {
		p := ethosCall(nil)
		delete(p, "session_id")
		out := runPreToolUse(t, p)["hookSpecificOutput"]
		assert.NotContains(t, out, "updatedInput")
		assert.NotContains(t, out, "permissionDecision")
	})
	t.Run("no agent key", func(t *testing.T) {
		t.Setenv("CLAUDE_PID", "")
		out := runPreToolUse(t, ethosCall(nil))["hookSpecificOutput"]
		assert.NotContains(t, out, "updatedInput")
		assert.NotContains(t, out, "permissionDecision")
	})
	t.Run("uncorroborated CLAUDE_PID", func(t *testing.T) {
		t.Setenv("CLAUDE_PID", strconv.Itoa(os.Getpid())) // live, but not an ancestor
		out := runPreToolUse(t, ethosCall(nil))["hookSpecificOutput"]
		assert.NotContains(t, out, "updatedInput")
	})
}

func TestPreToolUse_OnlyEthosMCPToolsAreDeclared(t *testing.T) {
	withCorroboratedPID(t)
	for name, tool := range map[string]string{
		"dev prefix":      "mcp__plugin_ethos-dev_self__session",
		"mission tool":    "mcp__plugin_ethos_self__mission",
		"doctor":          "mcp__plugin_ethos_self__doctor",
		"other plugin":    "mcp__plugin_biff_tty__plan",
		"lookalike":       "mcp__plugin_ethosx_self__identity",
		"other ethos srv": "mcp__plugin_ethos_other__identity",
		"builtin":         "Bash",
	} {
		t.Run(name, func(t *testing.T) {
			p := ethosCall(map[string]any{"tool_name": tool})
			out := runPreToolUse(t, p)["hookSpecificOutput"]
			declared := name == "dev prefix" || name == "mission tool" || name == "doctor"
			if declared {
				assert.Contains(t, out, "updatedInput")
				assert.NotContains(t, out, "permissionDecision")
			} else {
				assert.NotContains(t, out, "updatedInput")
				assert.NotContains(t, out, "permissionDecision")
			}
		})
	}
}

// A gate that denies the call stands: the caller is not declared on a call the
// hook refuses.
func TestPreToolUse_DenyStandsOverInjection(t *testing.T) {
	withCorroboratedPID(t)
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "docs/")
	p := ethosCall(map[string]any{"tool_input": map[string]any{"method": "create"}})
	out := runPreToolUse(t, p)["hookSpecificOutput"]
	assert.Equal(t, "deny", out["permissionDecision"])
	assert.NotContains(t, out, "updatedInput")
}

// The ordinary gates still run for an ethos tool: a verifier's read-only call
// passes them and is then given its caller.
func TestPreToolUse_VerifierReadOnlyCallIsDeclared(t *testing.T) {
	withCorroboratedPID(t)
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "docs/")
	out := runPreToolUse(t, ethosCall(nil))["hookSpecificOutput"]
	assert.Contains(t, out, "updatedInput")
	assert.NotContains(t, out, "permissionDecision")
}

// Agent, Write and Edit keep their existing output, including the permission
// decision.
func TestPreToolUse_NonEthosToolsUnchanged(t *testing.T) {
	withCorroboratedPID(t)
	for _, tool := range []string{"Write", "Edit", "Read"} {
		p := map[string]any{
			"tool_name":  tool,
			"tool_input": map[string]any{"file_path": "/x/y.go"},
			"session_id": "sess-1",
		}
		out := runPreToolUse(t, p)["hookSpecificOutput"]
		assert.NotContains(t, out, "permissionDecision")
		assert.NotContains(t, out, "updatedInput", tool)
	}
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "docs/")
	p := map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": "/elsewhere/y.go"}, "session_id": "sess-1"}
	assert.Equal(t, "deny", runPreToolUse(t, p)["hookSpecificOutput"]["permissionDecision"])
}
