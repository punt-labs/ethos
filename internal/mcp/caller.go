package mcp

import (
	"fmt"
	"regexp"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/punt-labs/ethos/v4/internal/doctor"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/session"
)

const opencodeSessionKey = "ai.opencode/sessionID"

// callerArg is the reserved tool argument through which ethos's own PreToolUse
// hook declares the caller to Claude Code's MCP server, which receives no
// session or agent identity of its own (DES-074, amendment 2026-11-03).
const callerArg = "_caller"

// callerOption declares callerArg in a tool schema so the argument passes
// through. Models are told only not to supply it: the hook overwrites whatever
// they send.
func callerOption() mcplib.ToolOption {
	return mcplib.WithObject(callerArg, mcplib.Description("Set by the ethos hook; do not supply."))
}

var callerSessionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// callerRef names the exact participant a request declares as its caller: the
// session roster to read and the agent_id to find in it.
type callerRef struct {
	sessionID string
	agentID   string
}

// metadataSessionID distinguishes an absent caller from an invalid explicit
// caller. Never normalize an ID: doing so could select a different session.
func (h *Handler) metadataSessionID(req mcplib.CallToolRequest) (string, bool, error) {
	if !h.opencodeSessionMetadata || req.Params.Meta == nil {
		return "", false, nil
	}
	v, present := req.Params.Meta.AdditionalFields[opencodeSessionKey]
	if !present {
		return "", false, nil
	}
	sid, ok := v.(string)
	if !ok || !callerSessionPattern.MatchString(sid) {
		return "", true, fmt.Errorf("_meta %s must be a nonempty safe session ID (letters, digits, underscores, hyphens)", opencodeSessionKey)
	}
	return sid, true, nil
}

// requestCaller reports the caller a request declares, from the opt-in
// OpenCode _meta key (session and agent are the same ID) or from callerArg. It
// distinguishes an absent caller from an invalid one: an invalid caller is
// present with an error and never falls back to another source.
func (h *Handler) requestCaller(req mcplib.CallToolRequest) (callerRef, bool, error) {
	sid, metaPresent, metaErr := h.metadataSessionID(req)
	raw, argPresent := req.GetArguments()[callerArg]
	switch {
	case metaPresent && argPresent:
		return callerRef{}, true, fmt.Errorf("%s and _meta %s both declare a caller", callerArg, opencodeSessionKey)
	case metaPresent:
		if metaErr != nil {
			return callerRef{}, true, metaErr
		}
		return callerRef{sessionID: sid, agentID: sid}, true, nil
	case argPresent:
		ref, err := parseCallerArg(raw)
		return ref, true, err
	}
	return callerRef{}, false, nil
}

// parseCallerArg validates a callerArg value with the _meta path's safe-ID
// rule. Unknown keys are refused, not ignored.
func parseCallerArg(raw any) (callerRef, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return callerRef{}, fmt.Errorf("%s must be an object with session_id and agent_id", callerArg)
	}
	var ref callerRef
	for k, v := range m {
		s, ok := v.(string)
		if !ok || !callerSessionPattern.MatchString(s) {
			return callerRef{}, fmt.Errorf("%s.%s must be a nonempty safe ID (letters, digits, underscores, hyphens)", callerArg, k)
		}
		switch k {
		case "session_id":
			ref.sessionID = s
		case "agent_id":
			ref.agentID = s
		default:
			return callerRef{}, fmt.Errorf("%s has unknown key %q", callerArg, k)
		}
	}
	if ref.sessionID == "" || ref.agentID == "" {
		return callerRef{}, fmt.Errorf("%s must carry both session_id and agent_id", callerArg)
	}
	return ref, nil
}

// callerParticipant requires the exact declared key, never a root,
// primary, PID, or environment-derived participant.
func (h *Handler) callerParticipant(ref callerRef) (*session.Participant, error) {
	if h.sessionStore == nil {
		return nil, fmt.Errorf("session store not configured")
	}
	roster, err := h.sessionStore.Load(ref.sessionID)
	if err != nil {
		return nil, fmt.Errorf("loading caller roster: %w", err)
	}
	if roster.Session != ref.sessionID {
		return nil, fmt.Errorf("caller roster session %q does not match %q", roster.Session, ref.sessionID)
	}
	var caller *session.Participant
	for i := range roster.Participants {
		p := &roster.Participants[i]
		if p.AgentID == ref.agentID {
			if caller != nil {
				return nil, fmt.Errorf("caller participant %q is duplicated", ref.agentID)
			}
			caller = p
		}
	}
	if caller == nil {
		return nil, fmt.Errorf("caller participant %q not found in roster; run `ethos session join --session %s --agent-id %s --persona AGENT_HANDLE` with an existing identity handle", ref.agentID, ref.sessionID, ref.agentID)
	}
	return caller, nil
}

// noIdentity describes a caller whose participant has no persona (DES-007,
// "Null persona handling"): a built-in subagent such as general-purpose. It
// names the agent_type as the display name, or the agent_id when the roster
// recorded none. The caller has no identity to load, and none is borrowed.
func noIdentity(p *session.Participant) string {
	label := p.AgentType
	if label == "" {
		label = p.AgentID
	}
	return fmt.Sprintf("%s has no ethos identity", label)
}

// declaredCallerResult is doctor's human-identity row for a declared caller.
// refErr is the error requestCaller returned with it, if any.
func (h *Handler) declaredCallerResult(ref callerRef, refErr error) doctor.Result {
	const name = doctor.HumanIdentityRow
	if refErr != nil {
		return doctor.Result{Name: name, Status: "FAIL", Detail: refErr.Error()}
	}
	p, err := h.callerParticipant(ref)
	if err != nil {
		return doctor.Result{Name: name, Status: "FAIL", Detail: err.Error()}
	}
	if p.Persona == "" {
		return doctor.Result{Name: name, Status: "PASS", Detail: noIdentity(p)}
	}
	id, err := h.loadCallerIdentity(p.Persona, identity.Reference(true))
	if err != nil {
		return doctor.Result{Name: name, Status: "FAIL", Detail: err.Error()}
	}
	return doctor.Result{Name: name, Status: "PASS", Detail: fmt.Sprintf("%s (%s)", id.Name, id.Handle)}
}

func (h *Handler) loadCallerIdentity(persona string, opts ...identity.LoadOption) (*identity.Identity, error) {
	// Validate before Load, whose filesystem path intentionally uses Base.
	if err := (&identity.Identity{Name: "Caller", Handle: persona, Kind: "agent"}).Validate(); err != nil {
		return nil, fmt.Errorf("invalid caller persona %q: %w", persona, err)
	}
	id, err := h.store.Load(persona, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading caller persona %q: %w", persona, err)
	}
	if err := id.Validate(); err != nil {
		return nil, fmt.Errorf("invalid caller identity %q: %w", persona, err)
	}
	if id.Handle != persona {
		return nil, fmt.Errorf("caller identity handle %q does not match persona %q", id.Handle, persona)
	}
	return id, nil
}
