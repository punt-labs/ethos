package hook

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
)

// HandleSubagentStart reads the SubagentStart hook payload from stdin
// and joins the subagent to the session roster.
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
	if agentType != "" {
		if _, err := store.Load(agentType, identity.Reference(true)); err == nil {
			persona = agentType
		}
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
	return nil
}
