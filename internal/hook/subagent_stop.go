package hook

import (
	"io"
	"time"

	"github.com/punt-labs/ethos/internal/session"
)

// HandleSubagentStop reads the SubagentStop hook payload from stdin
// and removes the subagent from the session roster.
func HandleSubagentStop(r io.Reader, ss *session.Store) error {
	input, _ := ReadInput(r, time.Second)

	agentID, _ := input["agent_id"].(string)
	sessionID, _ := input["session_id"].(string)

	if agentID == "" || sessionID == "" {
		return nil
	}

	_ = ss.Leave(sessionID, agentID)
	return nil
}
