package hook

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/punt-labs/ethos/internal/session"
)

// HandleSubagentStop reads the SubagentStop hook payload from stdin
// and removes the subagent from the session roster.
func HandleSubagentStop(r io.Reader, ss *session.Store) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("subagent-stop: %w", err)
	}

	agentID, _ := input["agent_id"].(string)
	sessionID, _ := input["session_id"].(string)

	if agentID == "" || sessionID == "" {
		return nil
	}

	if leaveErr := ss.Leave(sessionID, agentID); leaveErr != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to leave session %s: %v\n", sessionID, leaveErr)
	}
	return nil
}
