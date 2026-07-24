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
// session-required consumer (iam, session join/leave/end, mission
// claim/release). Order (DES-061 R2):
//  1. explicit --session/arg (full or prefix match; walk bypassed — R3)
//  2. ETHOS_SESSION env      (walk bypassed — R3)
//  3. Claude process-tree walk (zero-config inside Claude Code)
//  4. errNoSession — actionable, names `ethos session start`
//
// agentID keys the participant: ETHOS_AGENT_ID if set, else the Claude PID.
func resolveHardSession(explicit string) (sessionID, agentID string, err error) {
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
			// An explicit ETHOS_SESSION must name a real roster — same
			// contract as --session (validated above). Without this a stale
			// env sails through the hard chain and stages a phantom binding
			// (mission claim/release) or a teardown of nothing.
			if source == resolve.SessionSourceEnv {
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
