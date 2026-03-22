package hook

import (
	"fmt"
	"io"
	"os"
	"time"

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

	_ = ss.Delete(sessionID)

	claudePID := fmt.Sprintf("%d", os.Getppid())
	_ = ss.DeleteCurrentSession(claudePID)

	return nil
}
