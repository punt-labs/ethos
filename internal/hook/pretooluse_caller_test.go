package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

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
	noCaller := func(t *testing.T, out map[string]any) {
		t.Helper()
		if ui, ok := out["updatedInput"].(map[string]any); ok {
			assert.NotContains(t, ui, callerArg, "no _caller may survive")
		}
	}
	t.Run("no session_id", func(t *testing.T) {
		p := ethosCall(nil)
		delete(p, "session_id")
		out := runPreToolUse(t, p)["hookSpecificOutput"]
		assert.NotContains(t, out, "updatedInput")
		assert.NotContains(t, out, "permissionDecision")
		noCaller(t, out)
	})
	t.Run("no agent key", func(t *testing.T) {
		t.Setenv("CLAUDE_PID", "")
		out := runPreToolUse(t, ethosCall(nil))["hookSpecificOutput"]
		assert.NotContains(t, out, "updatedInput")
		assert.NotContains(t, out, "permissionDecision")
		noCaller(t, out)
	})
	t.Run("uncorroborated CLAUDE_PID", func(t *testing.T) {
		t.Setenv("CLAUDE_PID", strconv.Itoa(os.Getpid())) // live, but not an ancestor
		out := runPreToolUse(t, ethosCall(nil))["hookSpecificOutput"]
		assert.NotContains(t, out, "updatedInput")
		noCaller(t, out)
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

// When the hook cannot declare a caller it must still not let the model name
// one: a supplied _caller is stripped, the rest of the input kept, and no
// permission decision is made.
func TestPreToolUse_NoCallerStripsSpoofedCaller(t *testing.T) {
	withCorroboratedPID(t)
	spoof := map[string]any{"session_id": "victim", "agent_id": "victim-agent"}
	for name, setup := range map[string]func(t *testing.T, p map[string]any){
		"no session_id": func(t *testing.T, p map[string]any) { delete(p, "session_id") },
		"no agent key":  func(t *testing.T, p map[string]any) { t.Setenv("CLAUDE_PID", "") },
		"uncorroborated CLAUDE_PID": func(t *testing.T, p map[string]any) {
			t.Setenv("CLAUDE_PID", strconv.Itoa(os.Getpid())) // live, but not an ancestor
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := ethosCall(nil)
			p["tool_input"] = map[string]any{"method": "whoami", "reference": true, callerArg: spoof}
			setup(t, p)
			out := runPreToolUse(t, p)["hookSpecificOutput"]
			assert.NotContains(t, out, "permissionDecision")
			assert.Equal(t, map[string]any{"method": "whoami", "reference": true}, out["updatedInput"])
		})
	}
	t.Run("a deny still stands", func(t *testing.T) {
		t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "docs/")
		p := ethosCall(nil)
		delete(p, "session_id")
		p["tool_input"] = map[string]any{"method": "create", callerArg: spoof}
		out := runPreToolUse(t, p)["hookSpecificOutput"]
		assert.Equal(t, "deny", out["permissionDecision"])
		assert.NotContains(t, out, "updatedInput")
	})
}

// claude --agent X puts agent_type in every main-thread payload; agent_id
// appears only inside a subagent. So agent_id is the subagent signal, and a
// payload with agent_type but no agent_id is the main thread, keyed on the
// corroborated CLAUDE_PID like any leader.
func TestPreToolUse_AgentFlagMainThreadDeclaresLeader(t *testing.T) {
	pid := withCorroboratedPID(t)
	p := ethosCall(map[string]any{"agent_type": "bwk"})
	caller := runPreToolUse(t, p)["hookSpecificOutput"]["updatedInput"].(map[string]any)[callerArg]
	assert.Equal(t, map[string]any{"session_id": "sess-1", "agent_id": pid}, caller)

	// Inside a subagent the same agent_type is keyed on the agent_id.
	p = ethosCall(map[string]any{"agent_type": "bwk", "agent_id": "agent-sub1"})
	caller = runPreToolUse(t, p)["hookSpecificOutput"]["updatedInput"].(map[string]any)[callerArg]
	assert.Equal(t, map[string]any{"session_id": "sess-1", "agent_id": "agent-sub1"}, caller)
}

// Every reason no caller can be declared is logged, not only the spoofed case.
func TestPreToolUse_NoCallerLogsTheCause(t *testing.T) {
	withCorroboratedPID(t)
	for name, tc := range map[string]struct {
		setup func(p map[string]any)
		want  string
	}{
		"no session_id": {func(p map[string]any) { delete(p, "session_id") }, "no session_id"},
		"no agent key":  {func(p map[string]any) { t.Setenv("CLAUDE_PID", "") }, "CLAUDE_PID is absent or not a live ancestor"},
	} {
		t.Run(name, func(t *testing.T) {
			p := ethosCall(nil)
			tc.setup(p)
			stderr := captureStderr(t, func() { runPreToolUse(t, p) })
			assert.Contains(t, stderr, "no caller declared for mcp__plugin_ethos_self__identity")
			assert.Contains(t, stderr, tc.want)
		})
	}
}

// A payload that cannot be read or parsed, for a call to an ethos MCP tool, is
// denied with the cause named, never an error with no output: the call would
// reach the server with no caller declared. Before this the hook returned the
// error and wrote nothing.
func TestPreToolUse_UnreadablePayloadForEthosToolDenies(t *testing.T) {
	boom := errors.New("boom")
	whole := `{"tool_name":"mcp__plugin_ethos_self__session","session_id":"s","tool_input":{"method":"roster"}}`
	for name, tc := range map[string]struct {
		in   io.Reader
		tool string
		want string
	}{
		"truncated":        {strings.NewReader(whole[:len(whole)-9]), "mcp__plugin_ethos_self__session", "invalid JSON input"},
		"trailing garbage": {strings.NewReader(whole + "x"), "mcp__plugin_ethos_self__session", "invalid JSON input"},
		"read error": {io.MultiReader(strings.NewReader(whole[:len(whole)-9]), iotest.ErrReader(boom)),
			"mcp__plugin_ethos_self__session", "boom"},
		"dev prefix": {strings.NewReader(`{"tool_name": "mcp__plugin_ethos-dev_self__doctor", "tool_in`),
			"mcp__plugin_ethos-dev_self__doctor", "invalid JSON input"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			var err error
			stderr := captureStderr(t, func() { err = HandlePreToolUse(tc.in, &out) })
			require.NoError(t, err, "the hook answers; it does not fail with no output")
			var got PreToolUseResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &got), out.String())
			assert.Equal(t, "deny", got.HookSpecificOutput.Decision())
			assert.Contains(t, got.HookSpecificOutput.PermissionDecisionReason, tc.tool)
			assert.Contains(t, got.HookSpecificOutput.PermissionDecisionReason, tc.want)
			assert.Contains(t, stderr, tc.want, "the cause is logged for hook-errors.log")
			assert.Empty(t, got.HookSpecificOutput.UpdatedInput)
		})
	}

	t.Run("a payload that is not for an ethos tool keeps the error", func(t *testing.T) {
		for _, in := range []string{
			`{"tool_name":"Bash","tool_input":{"command":"ls"`,
			`{"tool_name":"mcp__plugin_ethosx_self__identity","tool_in`,
			`{"tool_input":{"x":"mcp__plugin_ethos_self__identity"`, // not its tool_name
			`not json`,
		} {
			var out bytes.Buffer
			err := HandlePreToolUse(strings.NewReader(in), &out)
			require.Error(t, err, in)
			assert.Empty(t, out.String(), in)
		}
	})
}

// The output type cannot say "allow": a decoded permissionDecision is deny or
// nothing, so a handler cannot return the word that approves a call past the
// user's permission rules.
func TestPermissionDecisionCannotExpressAllow(t *testing.T) {
	assert.Equal(t, reflect.Bool, reflect.TypeOf(preToolUseOutput{}.PermissionDecision).Kind())

	deny, err := json.Marshal(preToolUseDeny("why"))
	require.NoError(t, err)
	assert.Contains(t, string(deny), `"permissionDecision":"deny"`)
	pass, err := json.Marshal(preToolUsePass())
	require.NoError(t, err)
	assert.NotContains(t, string(pass), "permissionDecision")

	for _, word := range []string{"allow", "ask", "Deny", "true"} {
		var o preToolUseOutput
		err := json.Unmarshal([]byte(`{"permissionDecision":"`+word+`"}`), &o)
		require.Error(t, err, word)
		assert.Contains(t, err.Error(), "not expressible")
	}
	var o preToolUseOutput
	require.NoError(t, json.Unmarshal([]byte(`{"permissionDecision":"deny"}`), &o))
	assert.Equal(t, "deny", o.Decision())
}
