package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"

	"github.com/punt-labs/ethos/v4/internal/process"
)

// ethosMCPTool matches ethos's own MCP tools, installed as the plugin
// "ethos" or "ethos-dev" with server "self".
var ethosMCPTool = regexp.MustCompile(`^mcp__plugin_ethos(-dev)?_self__`)

// callerArg is the reserved argument the MCP server reads to learn its caller
// (internal/mcp.callerArg). Claude Code gives an MCP server no session or
// agent identity of its own, so the hook declares it per call (DES-074,
// amendment 2026-11-03).
const callerArg = "_caller"

// handleEthosMCPCall answers PreToolUse for a call to one of ethos's own MCP
// tools. The ordinary gates run first, and a denial stands. Otherwise the call
// proceeds with the caller declared in its arguments, and with no permission
// decision, so the user's permission rules apply as they would without a hook.
func handleEthosMCPCall(input map[string]any, w io.Writer) error {
	var buf bytes.Buffer
	if err := routePreToolUse(input, &buf); err != nil {
		return err
	}
	var gate PreToolUseResult
	if err := json.Unmarshal(buf.Bytes(), &gate); err != nil {
		return fmt.Errorf("pre-tool-use: decoding the gate result: %w", err)
	}
	caller := declaredCaller(input)
	if gate.HookSpecificOutput.PermissionDecision == "deny" || caller == nil {
		_, err := w.Write(buf.Bytes())
		return err
	}
	toolInput, _ := input["tool_input"].(map[string]any)
	updated := make(map[string]any, len(toolInput)+1)
	for k, v := range toolInput {
		updated[k] = v
	}
	// Always overwrite: a model-supplied value must never name the caller.
	updated[callerArg] = caller
	return json.NewEncoder(w).Encode(PreToolUseResult{HookSpecificOutput: preToolUseOutput{
		HookEventName: "PreToolUse",
		UpdatedInput:  updated,
	}})
}

// declaredCaller is the {session_id, agent_id} of the call's author, or nil
// when either cannot be determined. session_id is the payload's. agent_id is
// the payload's when a subagent is calling, else this hook's own corroborated
// CLAUDE_PID, which names the leader (DES-061 R4's key order).
func declaredCaller(input map[string]any) map[string]any {
	sessionID, _ := input["session_id"].(string)
	if sessionID == "" {
		return nil
	}
	agentID, _ := input["agent_id"].(string)
	if agentID == "" {
		pid, ok := process.ClaudePIDFromEnvCorroborated()
		if !ok {
			return nil
		}
		agentID = pid
	}
	return map[string]any{"session_id": sessionID, "agent_id": agentID}
}
