package main

import (
	"fmt"
	"os"

	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
)

func runIam(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: ethos iam <persona>")
		os.Exit(1)
	}
	persona := args[0]

	sessionID, agentID := resolveSessionContext()
	ss := sessionStore()
	if err := ss.Join(sessionID, session.Participant{
		AgentID: agentID,
		Persona: persona,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
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
}

// resolveSessionContext determines the session ID and agent ID from
// the environment. Session ID comes from ETHOS_SESSION or the PID-keyed
// current file. Agent ID comes from ETHOS_AGENT_ID or Claude PID walk.
// Exits the process if session ID cannot be determined.
func resolveSessionContext() (sessionID, agentID string) {
	sessionID = os.Getenv("ETHOS_SESSION")
	agentID = os.Getenv("ETHOS_AGENT_ID")

	if sessionID == "" {
		claudePID := process.FindClaudePID()
		ss := sessionStore()
		sid, err := ss.ReadCurrentSession(claudePID)
		if err == nil {
			sessionID = sid
		}
	}

	if sessionID == "" {
		fmt.Fprintln(os.Stderr, "ethos: cannot determine session ID — set ETHOS_SESSION or start a Claude session")
		os.Exit(1)
	}

	if agentID == "" {
		agentID = process.FindClaudePID()
	}

	return sessionID, agentID
}
