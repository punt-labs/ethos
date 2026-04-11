package main

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
)

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

// resolveSessionContext determines the session ID and agent ID from
// the environment and flags. Resolution order:
//  1. --session flag (full or prefix match)
//  2. ETHOS_SESSION env var
//  3. PID tree lookup via FindClaudePID
//
// Returns an error if session ID cannot be determined.
func resolveSessionContext() (sessionID, agentID string, err error) {
	agentID = os.Getenv("ETHOS_AGENT_ID")
	ss := sessionStore()

	// 1. --session flag from the iam subcommand.
	if sessionIamSession != "" {
		sid, err := ss.MatchByPrefix(sessionIamSession)
		if err != nil {
			return "", "", err
		}
		sessionID = sid
	}

	// 2. ETHOS_SESSION env var.
	if sessionID == "" {
		sessionID = os.Getenv("ETHOS_SESSION")
	}

	// 3. PID tree lookup.
	if sessionID == "" {
		claudePID := process.FindClaudePID()
		sid, err := ss.ReadCurrentSession(claudePID)
		if err != nil {
			return "", "", fmt.Errorf("no session found in process tree. Use --session to specify one")
		}
		sessionID = sid
	}

	if agentID == "" {
		agentID = process.FindClaudePID()
	}

	return sessionID, agentID, nil
}
