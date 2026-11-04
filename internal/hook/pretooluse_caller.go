package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

// rawEthosMCPTool finds the name of an ethos MCP tool in a payload that did not
// parse, or "" when there is none. A truncated payload usually still holds its
// tool_name.
var rawEthosMCPTool = regexp.MustCompile(`"tool_name"\s*:\s*"(mcp__plugin_ethos(?:-dev)?_self__[A-Za-z0-9_]*)"`)

// refuseUnreadable answers a payload that could not be read or parsed. Claude
// Code runs the hook for every tool call; for one of ethos's own MCP tools a
// hook that fails with no output lets the call through undeclared, so the call
// is denied, naming the cause. For any other payload the cause is returned as
// the hook's error, as it always was.
func refuseUnreadable(raw []byte, w io.Writer, cause error) error {
	m := rawEthosMCPTool.FindSubmatch(raw)
	if m == nil {
		return cause
	}
	return denyEthosCall(w, string(m[1]), cause)
}

// denyEthosCall denies a call to an ethos MCP tool that the hook could not
// process, with the cause in the reason and on stderr (hook-errors.log).
func denyEthosCall(w io.Writer, toolName string, cause error) error {
	fmt.Fprintf(os.Stderr, "ethos: %v; denied %s\n", cause, toolName)
	reason := fmt.Sprintf("ethos pre-tool-use: cannot declare the caller of %s: %v", toolName, cause)
	return json.NewEncoder(w).Encode(preToolUseDeny(reason))
}

// handleEthosMCPCall answers PreToolUse for a call to one of ethos's own MCP
// tools. The ordinary gates run first, and a denial stands. Otherwise the call
// proceeds with the caller declared in its arguments, and with no permission
// decision, so the user's permission rules apply as they would without a hook.
// A failure of any step denies the call (refuseUnreadable says why): the server
// would otherwise be called with a caller nobody declared.
func handleEthosMCPCall(input map[string]any, w io.Writer) error {
	toolName, _ := input["tool_name"].(string)
	var buf bytes.Buffer
	if err := routePreToolUse(input, &buf); err != nil {
		return denyEthosCall(w, toolName, fmt.Errorf("pre-tool-use: %w", err))
	}
	var gate PreToolUseResult
	if err := json.Unmarshal(buf.Bytes(), &gate); err != nil {
		return denyEthosCall(w, toolName, fmt.Errorf("pre-tool-use: decoding the gate result: %w", err))
	}
	if gate.HookSpecificOutput.PermissionDecision == decisionDeny {
		_, err := w.Write(buf.Bytes())
		return err
	}
	toolInput, _ := input["tool_input"].(map[string]any)
	caller, cause := declaredCaller(input)
	_, spoofed := toolInput[callerArg]
	if caller == nil {
		// The server refuses a Claude Code call without a caller, so this is
		// loud there; here the cause goes to hook-errors.log.
		fmt.Fprintf(os.Stderr, "ethos: pre-tool-use: no caller declared for %s: %v\n", toolName, cause)
		if !spoofed {
			_, err := w.Write(buf.Bytes())
			return err
		}
	}
	updated := make(map[string]any, len(toolInput)+1)
	for k, v := range toolInput {
		updated[k] = v
	}
	// Always overwrite, or with no caller to declare remove: a model-supplied
	// value must never name the caller.
	if caller != nil {
		updated[callerArg] = caller
	} else {
		delete(updated, callerArg)
	}
	return json.NewEncoder(w).Encode(PreToolUseResult{HookSpecificOutput: preToolUseOutput{
		HookEventName: "PreToolUse",
		UpdatedInput:  updated,
	}})
}

// declaredCaller is the {session_id, agent_id} of the call's author, or nil and
// the reason when either cannot be determined. session_id is the payload's.
// agent_id is the payload's when a subagent is calling, else this hook's own
// corroborated CLAUDE_PID, which names the leader (DES-061 R4's key order). A
// payload that names an agent_type but no agent_id is not the leader, and
// CLAUDE_PID would name it as one, so no caller is declared.
func declaredCaller(input map[string]any) (map[string]any, error) {
	sessionID, _ := input["session_id"].(string)
	if sessionID == "" {
		return nil, errors.New("the payload has no session_id")
	}
	agentID, _ := input["agent_id"].(string)
	if agentID == "" {
		if agentType, _ := input["agent_type"].(string); agentType != "" {
			return nil, fmt.Errorf("the payload names agent_type %q but no agent_id, so the caller is not the leader CLAUDE_PID names", agentType)
		}
		pid, ok := process.ClaudePIDFromEnvCorroborated()
		if !ok {
			return nil, errors.New("CLAUDE_PID is absent or not a live ancestor of the hook")
		}
		agentID = pid
	}
	return map[string]any{"session_id": sessionID, "agent_id": agentID}, nil
}
