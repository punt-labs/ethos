package hook

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/process"
	"github.com/punt-labs/ethos/v4/internal/session"
)

// HandleSessionEnd reads the SessionEnd hook payload from stdin,
// deletes the session roster, cleans up the PID-keyed current file, and
// clears the session's mission bindings.
//
// Review finding C6 (m-2026-09-08-004 round 2): `claude --resume`
// reuses the session ID, so without this a claim or pending dispatch
// left over from before the session ended survived into the resumed
// session and could capture an entirely unrelated later spawn or
// commit — the same class of stale-binding risk this whole ADR exists
// to close, triggered by session resumption instead of back-to-back
// dispatch. A session ending is treated the same as an explicit
// `ethos mission release`: every claim and pending dispatch for this
// session ID is cleared, so a resumed session starts with no
// inherited attribution. Best-effort and non-fatal, matching every
// other cleanup step here — a failure here must not stop the roster
// deletion that follows.
func HandleSessionEnd(r io.Reader, ss *session.Store) error {
	input, err := ReadInput(r, time.Second)
	if err != nil {
		return fmt.Errorf("session-end: %w", err)
	}

	sessionID, _ := input["session_id"].(string)
	if sessionID == "" {
		return nil
	}

	clearSessionMissionBindings(sessionID)

	if err := ss.Delete(sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: failed to delete session %s: %v\n", sessionID, err)
	}

	claudePID := process.FindClaudePID()
	if err := ss.DeleteCurrentSession(claudePID); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "ethos: failed to delete current session file: %v\n", err)
	}

	return nil
}

// clearSessionMissionBindings clears sessionID's claim, its
// delegation-binding sidecar, and every pending dispatch — the same
// three-way scope `ethos mission release` clears (cmd/ethos/mission.go's
// runMissionRelease) — so a resumed session (C6) never inherits
// attribution from before it ended. Review finding H2 (full-branch
// review, m-2026-09-08-004 round 3): this function's comment already
// claimed release parity, but the delegation-binding clear itself was
// missing, so a stale binding survived session end and could tag a
// later, unrelated session's commits (the same ethos-jawp class
// ClearDelegationBinding's own doc comment names). Advisory: errors are
// reported to stderr, never returned, matching HandleSessionEnd's own
// non-fatal cleanup discipline.
func clearSessionMissionBindings(sessionID string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-end: user home dir: %v\n", err)
		return
	}
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	if err := mission.ClearActiveMission(globalRoot, sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-end: clearing active mission for %q: %v\n", sessionID, err)
	}
	if err := mission.ClearDelegationBinding(globalRoot, sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-end: clearing delegation binding for %q: %v\n", sessionID, err)
	}
	if err := mission.ClearDispatchPending(globalRoot, sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: session-end: clearing dispatch-pending for %q: %v\n", sessionID, err)
	}
}
