package hook

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/punt-labs/ethos/v4/internal/process"
	"github.com/punt-labs/ethos/v4/internal/session"
)

// HandleSessionEnd reads the SessionEnd hook payload from stdin,
// clears the session's mission bindings, deletes the session roster,
// and cleans up the PID-keyed current file.
//
// Review finding C6 (m-2026-09-08-004 round 2): `claude --resume`
// reuses the session ID, so without this a claim or pending dispatch
// left over from before the session ended survived into the resumed
// session and could capture an entirely unrelated later spawn or
// commit — the same class of stale-binding risk this whole ADR exists
// to close, triggered by session resumption instead of back-to-back
// dispatch. A session ending is treated the same as an explicit
// `ethos mission release`: every claim, delegation-binding sidecar, and
// pending dispatch for this session ID is cleared, so a resumed session
// starts with no inherited attribution.
//
// Review finding J5 (full-branch review, m-2026-09-08-004 round 3):
// this used to hand-maintain its OWN copy of the same three-clear list
// session.Store.deleteFiles also maintains (a fourth sidecar type added
// to one and not the other would have drifted silently). ss.Delete
// alone now does both jobs — it clears the same three sidecars (via
// deleteFiles, review finding K3/J3) BEFORE removing the roster, and
// propagates a clear failure as an error rather than swallowing it —
// there is nothing left for a hook-local duplicate to do. A clear
// failure here is reported to stderr, matching every other
// non-fatal cleanup step in this function.
func HandleSessionEnd(r io.Reader, ss *session.Store) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("session-end: %w", err)
	}

	sessionID, _ := input["session_id"].(string)
	if sessionID == "" {
		return nil
	}

	if err := ss.Delete(sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to delete session %s: %v\n", sessionID, err)
	}

	claudePID := process.FindClaudePID()
	if err := ss.DeleteCurrentSession(claudePID); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "ethos: failed to delete current session file: %v\n", err)
	}

	return nil
}
