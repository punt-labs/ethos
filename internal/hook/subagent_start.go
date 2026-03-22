package hook

import (
	"io"
	"time"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/session"
)

// HandleSubagentStart reads the SubagentStart hook payload from stdin
// and joins the subagent to the session roster.
func HandleSubagentStart(r io.Reader, store *identity.Store, ss *session.Store) error {
	input, _ := ReadInput(r, time.Second)

	agentID, _ := input["agent_id"].(string)
	agentType, _ := input["agent_type"].(string)
	sessionID, _ := input["session_id"].(string)

	if agentID == "" || sessionID == "" {
		return nil
	}

	// Resolve persona: if an identity exists with the same handle as
	// agent_type, use it as the persona.
	persona := ""
	if agentType != "" {
		if _, err := store.Load(agentType, identity.Reference(true)); err == nil {
			persona = agentType
		}
	}

	p := session.Participant{
		AgentID:   agentID,
		Persona:   persona,
		Parent:    "", // Parent discovery happens at a higher level.
		AgentType: agentType,
	}

	_ = ss.Join(sessionID, p)
	return nil
}
