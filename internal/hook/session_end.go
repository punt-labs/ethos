package hook

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
)

// HandleSessionEnd reads the SessionEnd hook payload from stdin,
// deletes the session roster, and cleans up the PID-keyed current file.
func HandleSessionEnd(r io.Reader, ss *session.Store) error {
	input, _ := ReadInput(r, time.Second)

	sessionID, _ := input["session_id"].(string)
	if sessionID == "" {
		return nil
	}

	if err := ss.Delete(sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to delete session %s: %v\n", sessionID, err)
	}

	claudePID := process.FindClaudePID()
	if err := ss.DeleteCurrentSession(claudePID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to delete current session file: %v\n", err)
	}

	return nil
}
