package hook

import (
	"fmt"
	"io"

	"github.com/punt-labs/ethos/internal/mission"
)

// Environment-variable names the commit-msg hook reads back from
// WriteCommitTrailers. They match the names the PreToolUse dispatch
// exports, so a commit made with the env already set and a commit
// that falls back to the sidecars produce the same trailers.
const (
	commitTrailerMissionKey    = "MISSION_ID"
	commitTrailerDelegationKey = "DELEGATION_ID"
)

// WriteCommitTrailers writes the Mission/Delegation trailer values for
// one session as `KEY=value` lines, in the order the commit-msg hook
// reads them. Nothing is written when the session has no active
// mission — the hook adds no trailer in that case.
//
// The session is the COMMITTING one, resolved by the caller. It used
// to be picked by the hook itself: glob every session directory, sort
// by name, take the newest one holding an active-mission sidecar.
// With two concurrent sessions each holding a mission, a commit from
// the older session was stamped with the newer session's mission and
// delegation (ethos-pobi). Reading exactly one session's sidecars is
// the fix; an unresolved session writes nothing rather than guessing.
//
// The delegation is emitted only when its binding names the same
// mission, so a binding left from an earlier dispatch under a
// different mission cannot ride along (ethos-jawp).
func WriteCommitTrailers(w io.Writer, globalRoot, sessionID string) error {
	if globalRoot == "" || sessionID == "" {
		return nil
	}
	missionID, err := mission.ReadActiveMission(globalRoot, sessionID)
	if err != nil {
		return fmt.Errorf("reading active mission for session %q: %w", sessionID, err)
	}
	if missionID == "" {
		return nil
	}
	if _, err := fmt.Fprintf(w, "%s=%s\n", commitTrailerMissionKey, missionID); err != nil {
		return fmt.Errorf("writing mission trailer: %w", err)
	}

	b, err := mission.ReadDelegationBinding(globalRoot, sessionID)
	if err != nil {
		return fmt.Errorf("reading delegation binding for session %q: %w", sessionID, err)
	}
	if b.DelegationID == "" || b.MissionID != missionID {
		return nil
	}
	if _, err := fmt.Fprintf(w, "%s=%s\n", commitTrailerDelegationKey, b.DelegationID); err != nil {
		return fmt.Errorf("writing delegation trailer: %w", err)
	}
	return nil
}
