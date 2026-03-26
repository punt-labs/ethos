package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
)

// SubagentStartResult is the JSON output of the subagent-start hook.
type SubagentStartResult struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext,omitempty"`
	} `json:"hookSpecificOutput"`
}

// HandleSubagentStart reads the SubagentStart hook payload from stdin,
// joins the subagent to the session roster, and emits persona context
// if the subagent matches an ethos identity.
func HandleSubagentStart(r io.Reader, store *identity.Store, ss *session.Store) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("subagent-start: %w", err)
	}

	agentID, _ := input["agent_id"].(string)
	agentType, _ := input["agent_type"].(string)
	sessionID, _ := input["session_id"].(string)

	if agentID == "" || sessionID == "" {
		return nil
	}

	// Resolve persona: if an identity exists with the same handle as
	// agent_type, use it as the persona.
	persona := ""
	if agentType != "" && store.Exists(agentType) {
		persona = agentType
	}

	p := session.Participant{
		AgentID:   agentID,
		Persona:   persona,
		Parent:    process.FindClaudePID(),
		AgentType: agentType,
	}

	if joinErr := ss.Join(sessionID, p); joinErr != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to join session %s: %v\n", sessionID, joinErr)
	}

	// If no persona matched, nothing more to do.
	if persona == "" {
		return nil
	}

	// Load identity with full attribute content for persona injection.
	id, err := store.Load(persona)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: identity %q exists but attribute resolution failed: %v\n", persona, err)
		return nil
	}
	for _, w := range id.Warnings {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: identity %q: %s\n", persona, w)
	}

	block := BuildPersonaBlock(id)
	if block == "" {
		return nil
	}

	// Prepend parent context if we can resolve the parent from the roster.
	parentLine := resolveParentLine(ss, sessionID, p.Parent, store)
	if parentLine != "" {
		block = insertAfterFirstLine(block, parentLine)
	}

	result := SubagentStartResult{}
	result.HookSpecificOutput.HookEventName = "SubagentStart"
	result.HookSpecificOutput.AdditionalContext = block
	return json.NewEncoder(os.Stdout).Encode(result)
}

// resolveParentLine finds the primary Claude agent in the session roster
// and returns a "You report to Name (handle)." line. The primary agent
// is identified as the participant whose AgentID matches the subagent's
// Parent field (the Claude PID). Returns "" if the parent cannot be resolved.
func resolveParentLine(ss *session.Store, sessionID, parentID string, store *identity.Store) string {
	if parentID == "" {
		return ""
	}
	roster, err := ss.Load(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: resolveParentLine: session load failed: %v\n", err)
		return ""
	}
	// Find the participant whose AgentID matches the subagent's parent.
	var parentHandle string
	for _, p := range roster.Participants {
		if p.AgentID == parentID && p.Persona != "" {
			parentHandle = p.Persona
			break
		}
	}
	if parentHandle == "" {
		return ""
	}
	parentIdentity, err := store.Load(parentHandle, identity.Reference(true))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: subagent-start: resolveParentLine: load identity %q: %v\n", parentHandle, err)
		return ""
	}
	return fmt.Sprintf("You report to %s (%s).", parentIdentity.Name, parentIdentity.Handle)
}

// insertAfterFirstLine inserts extra after the first line of text.
func insertAfterFirstLine(text, extra string) string {
	for i, c := range text {
		if c == '\n' {
			return text[:i] + "\n" + extra + text[i:]
		}
	}
	return text + "\n" + extra
}
