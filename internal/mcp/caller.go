package mcp

import (
	"fmt"
	"regexp"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/session"
)

const opencodeSessionKey = "ai.opencode/sessionID"

var callerSessionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

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

// callerParticipant requires the exact request-local key, never a root,
// primary, PID, or environment-derived participant.
func (h *Handler) callerParticipant(sid string) (*session.Participant, error) {
	if h.sessionStore == nil {
		return nil, fmt.Errorf("session store not configured")
	}
	roster, err := h.sessionStore.Load(sid)
	if err != nil {
		return nil, fmt.Errorf("loading caller roster: %w", err)
	}
	if roster.Session != sid {
		return nil, fmt.Errorf("caller roster session %q does not match %q", roster.Session, sid)
	}
	var caller *session.Participant
	for i := range roster.Participants {
		p := &roster.Participants[i]
		if p.AgentID == sid {
			if caller != nil {
				return nil, fmt.Errorf("caller participant %q is duplicated", sid)
			}
			caller = p
		}
	}
	if caller == nil {
		return nil, fmt.Errorf("caller participant %q not found in roster; run `ethos session join --session %s --agent-id %s --persona AGENT_HANDLE` with an existing identity handle", sid, sid, sid)
	}
	return caller, nil
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
