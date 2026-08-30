package hook

import (
	"fmt"
	"io"

	"github.com/punt-labs/ethos/v4/internal/mission"
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
// Only a CLAIM-origin binding produces trailers. `mission create` and
// `mission dispatch` also bind the session — that is what files the
// next spawn's delegation under the right mission (ethos-7vo3) — but
// they name a mission FOR SOMEONE ELSE. The leader goes on doing
// unrelated work in the same session, and stamping those commits with
// a mission they dispatched is ethos-jawp's false-trailer class
// arriving through a new door. `ethos mission claim` is how an
// operator says "I am working on this", and it is the only thing that
// turns trailers on.
//
// The delegation is emitted only when its binding names the same
// mission, so a binding left from an earlier dispatch under a
// different mission cannot ride along (ethos-jawp).
func WriteCommitTrailers(w io.Writer, globalRoot, sessionID string) error {
	if globalRoot == "" || sessionID == "" {
		return nil
	}
	binding, err := mission.ReadActiveMissionBinding(globalRoot, sessionID)
	if err != nil {
		return fmt.Errorf("reading active mission for session %q: %w", sessionID, err)
	}
	missionID := binding.MissionID
	if missionID == "" || binding.Origin != mission.BindOriginClaim {
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
