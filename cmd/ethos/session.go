package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/punt-labs/ethos/internal/session"
)

// nextArg advances i and returns the flag value. Exits with an error
// if no value follows the flag.
func nextArg(args []string, i *int, flag string) string {
	*i++
	if *i >= len(args) {
		fmt.Fprintf(os.Stderr, "ethos: %s requires a value\n", flag)
		os.Exit(1)
	}
	return args[*i]
}

func runSession(args []string) {
	if len(args) == 0 {
		// Default: show current session roster.
		runSessionShow()
		return
	}

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "create":
		runSessionCreate(subArgs)
	case "delete":
		runSessionDelete(subArgs)
	case "join":
		runSessionJoin(subArgs)
	case "leave":
		runSessionLeave(subArgs)
	case "write-current":
		runSessionWriteCurrent(subArgs)
	case "delete-current":
		runSessionDeleteCurrent(subArgs)
	case "purge":
		runSessionPurge()
	default:
		fmt.Fprintf(os.Stderr, "ethos session: unknown subcommand %q\n", sub)
		printSubcommandHelp("session")
		os.Exit(1)
	}
}

func runSessionShow() {
	sessionID, _ := resolveSessionContext()
	ss := sessionStore()
	roster, err := ss.Load(sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	if jsonOutput {
		printJSON(roster)
		return
	}

	fmt.Printf("Session: %s\n", roster.Session)
	fmt.Printf("Started: %s\n", roster.Started)
	fmt.Println()
	for _, p := range roster.Participants {
		persona := p.Persona
		if persona == "" {
			persona = "(none)"
		}
		parent := p.Parent
		if parent == "" {
			parent = "(root)"
		}
		parts := []string{
			fmt.Sprintf("%-16s", p.AgentID),
			fmt.Sprintf("persona=%-16s", persona),
			fmt.Sprintf("parent=%s", parent),
		}
		if p.AgentType != "" {
			parts = append(parts, fmt.Sprintf("type=%s", p.AgentType))
		}
		fmt.Println("  " + strings.Join(parts, "  "))
	}
}

func runSessionCreate(args []string) {
	var sessionID, rootID, rootPersona, primaryID, primaryPersona string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session":
			sessionID = nextArg(args, &i, "--session")
		case "--root-id":
			rootID = nextArg(args, &i, "--root-id")
		case "--root-persona":
			rootPersona = nextArg(args, &i, "--root-persona")
		case "--primary-id":
			primaryID = nextArg(args, &i, "--primary-id")
		case "--primary-persona":
			primaryPersona = nextArg(args, &i, "--primary-persona")
		}
	}

	if sessionID == "" || rootID == "" || primaryID == "" {
		fmt.Fprintln(os.Stderr, "Usage: ethos session create --session ID --root-id X --root-persona Y --primary-id Z --primary-persona W")
		os.Exit(1)
	}

	ss := sessionStore()
	root := session.Participant{AgentID: rootID, Persona: rootPersona}
	primary := session.Participant{AgentID: primaryID, Persona: primaryPersona, Parent: rootID}
	if err := ss.Create(sessionID, root, primary); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]string{"session": sessionID})
	}
}

func runSessionDelete(args []string) {
	var sessionID string
	for i := 0; i < len(args); i++ {
		if args[i] == "--session" {
			sessionID = nextArg(args, &i, "--session")
		}
	}
	if sessionID == "" {
		fmt.Fprintln(os.Stderr, "Usage: ethos session delete --session ID")
		os.Exit(1)
	}
	ss := sessionStore()
	if err := ss.Delete(sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
}

func runSessionJoin(args []string) {
	var agentID, persona, parent, agentType, sessionID string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent-id":
			agentID = nextArg(args, &i, "--agent-id")
		case "--persona":
			persona = nextArg(args, &i, "--persona")
		case "--parent":
			parent = nextArg(args, &i, "--parent")
		case "--agent-type":
			agentType = nextArg(args, &i, "--agent-type")
		case "--session":
			sessionID = nextArg(args, &i, "--session")
		}
	}

	if agentID == "" {
		fmt.Fprintln(os.Stderr, "ethos session join: --agent-id is required")
		os.Exit(1)
	}

	if sessionID == "" {
		sessionID, _ = resolveSessionContext()
	}

	ss := sessionStore()
	p := session.Participant{
		AgentID:   agentID,
		Persona:   persona,
		AgentType: agentType,
		Parent:    parent,
	}
	if err := ss.Join(sessionID, p); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(p)
	}
}

func runSessionLeave(args []string) {
	var agentID, sessionID string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--agent-id":
			agentID = nextArg(args, &i, "--agent-id")
		case "--session":
			sessionID = nextArg(args, &i, "--session")
		}
	}

	if agentID == "" {
		fmt.Fprintln(os.Stderr, "ethos session leave: --agent-id is required")
		os.Exit(1)
	}

	if sessionID == "" {
		sessionID, _ = resolveSessionContext()
	}

	ss := sessionStore()
	if err := ss.Leave(sessionID, agentID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
}

func runSessionWriteCurrent(args []string) {
	var claudePID, sessionID string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pid":
			claudePID = nextArg(args, &i, "--pid")
		case "--session":
			sessionID = nextArg(args, &i, "--session")
		}
	}
	if claudePID == "" || sessionID == "" {
		fmt.Fprintln(os.Stderr, "Usage: ethos session write-current --pid PID --session ID")
		os.Exit(1)
	}
	ss := sessionStore()
	if err := ss.WriteCurrentSession(claudePID, sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
}

func runSessionDeleteCurrent(args []string) {
	var claudePID string
	for i := 0; i < len(args); i++ {
		if args[i] == "--pid" {
			claudePID = nextArg(args, &i, "--pid")
		}
	}
	if claudePID == "" {
		fmt.Fprintln(os.Stderr, "Usage: ethos session delete-current --pid PID")
		os.Exit(1)
	}
	ss := sessionStore()
	if err := ss.DeleteCurrentSession(claudePID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
}

func runSessionPurge() {
	ss := sessionStore()
	purged, err := ss.Purge()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		if purged == nil {
			purged = []string{}
		}
		printJSON(purged)
		return
	}
	if len(purged) == 0 {
		fmt.Println("No stale sessions found.")
		return
	}
	for _, id := range purged {
		fmt.Printf("Purged session %s\n", id)
	}
}
