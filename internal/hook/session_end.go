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
// deleteFiles, review finding K3/J3) BEFORE removing the roster —
// there is nothing left for a hook-local duplicate to do.
//
// The retry-token guarantee lives entirely inside ss.Delete, not here:
// on a sidecar-clear failure deleteFiles returns an error WITHOUT
// removing the roster (review finding J3), so the roster survives in
// List() as the token a later Purge/PurgeTombstoned pass needs to find
// and retry this session. That ordering is what matters — this
// function's own response to the returned error is deliberately
// best-effort: log to stderr and continue, the same as every other
// non-fatal cleanup step below. Failing the hook here would not
// improve on that guarantee (the roster was already left in place by
// ss.Delete) and would only make session end itself unreliable.
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
