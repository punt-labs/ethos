package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/session"
)

// errNoSession is the step-4 failure of the session resolution chain: no
// explicit session, no ETHOS_SESSION, and no Claude process-tree pointer.
// It names the remedy so a plain-terminal or Codex user knows what to run.
var errNoSession = errors.New("no active session; run `ethos session start` (or pass --session)")

func runIam(persona string) error {
	sessionID, agentID, err := resolveSessionContext()
	if err != nil {
		return err
	}
	ss := sessionStore()
	if err := ss.Join(sessionID, session.Participant{
		AgentID: agentID,
		Persona: persona,
	}); err != nil {
		return err
	}

	if jsonOutput {
		printJSON(map[string]string{
			"session":  sessionID,
			"agent_id": agentID,
			"persona":  persona,
		})
	} else {
		fmt.Printf("Set persona %q for %s in session %s\n", persona, agentID, sessionID)
	}
	return nil
}

// resolveSessionContext resolves the session for the iam subcommand,
// keying off its --session flag. See resolveHardSession for the chain.
func resolveSessionContext() (sessionID, agentID string, err error) {
	return resolveHardSession(sessionIamSession)
}

// resolveHardSession applies the session resolution chain for a
// state-writing consumer (iam, mission claim/release) where operating on a
// session that does not exist causes real harm — so an env-sourced ID is
// verified. See resolveSession.
func resolveHardSession(explicit string) (sessionID, agentID string, err error) {
	return resolveSession(explicit, true)
}

// resolveSession applies the session resolution chain (DES-061 R2):
//  1. explicit --session/arg (full or prefix match; walk bypassed — R3)
//  2. ETHOS_SESSION env      (walk bypassed — R3)
//  3. Claude process-tree walk (zero-config inside Claude Code)
//  4. errNoSession — actionable, names `ethos session start`
//
// When verifyEnv is true, an env-sourced ID must name a real roster (parity
// with --session's MatchByPrefix) — a stale env would otherwise stage a
// phantom mission binding. Teardown (session end) passes false: "already
// gone" is success there, handled by the caller.
//
// agentID keys the participant: ETHOS_AGENT_ID if set, else the Claude PID.
func resolveSession(explicit string, verifyEnv bool) (sessionID, agentID string, err error) {
	agentID = os.Getenv("ETHOS_AGENT_ID")
	ss := sessionStore()

	if explicit != "" {
		sid, err := ss.MatchByPrefix(explicit)
		if err != nil {
			return "", "", err
		}
		sessionID = sid
	}

	if sessionID == "" {
		sid, source := resolve.SessionID(ss)
		if sid != "" {
			if verifyEnv && source == resolve.SessionSourceEnv {
				if _, lerr := ss.Load(sid); lerr != nil {
					return "", "", fmt.Errorf("ETHOS_SESSION %q: %w", sid, lerr)
				}
			}
			sessionID = sid
		}
	}

	if sessionID == "" {
		return "", "", errNoSession
	}

	if agentID == "" {
		agentID = process.FindClaudePID()
	}

	return sessionID, agentID, nil
}
