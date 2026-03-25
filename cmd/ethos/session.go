package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage session roster",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runSessionShow()
	},
}

// --- session create ---

var (
	sessionCreateSession        string
	sessionCreateRootID         string
	sessionCreateRootPersona    string
	sessionCreatePrimaryID      string
	sessionCreatePrimaryPersona string
)

var sessionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new session roster",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runSessionCreate()
	},
}

// --- session delete ---

var sessionDeleteSession string

var sessionDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a session roster",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runSessionDelete()
	},
}

// --- session join ---

var (
	sessionJoinAgentID   string
	sessionJoinPersona   string
	sessionJoinParent    string
	sessionJoinAgentType string
	sessionJoinSession   string
)

var sessionJoinCmd = &cobra.Command{
	Use:   "join",
	Short: "Add a participant to the session",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runSessionJoin()
	},
}

// --- session leave ---

var (
	sessionLeaveAgentID string
	sessionLeaveSession string
)

var sessionLeaveCmd = &cobra.Command{
	Use:   "leave",
	Short: "Remove a participant from the session",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runSessionLeave()
	},
}

// --- session write-current ---

var (
	sessionWriteCurrentPID     string
	sessionWriteCurrentSession string
)

var sessionWriteCurrentCmd = &cobra.Command{
	Use:    "write-current",
	Short:  "Write PID-to-session mapping",
	Args:   cobra.NoArgs,
	Hidden: true,
	Run: func(cmd *cobra.Command, args []string) {
		runSessionWriteCurrent()
	},
}

// --- session delete-current ---

var sessionDeleteCurrentPID string

var sessionDeleteCurrentCmd = &cobra.Command{
	Use:    "delete-current",
	Short:  "Delete PID-to-session mapping",
	Args:   cobra.NoArgs,
	Hidden: true,
	Run: func(cmd *cobra.Command, args []string) {
		runSessionDeleteCurrent()
	},
}

// --- session purge ---

var sessionPurgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Clean up stale sessions",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runSessionPurge()
	},
}

func init() {
	// session create flags
	sessionCreateCmd.Flags().StringVar(&sessionCreateSession, "session", "", "Session ID (required)")
	sessionCreateCmd.Flags().StringVar(&sessionCreateRootID, "root-id", "", "Root agent ID (required)")
	sessionCreateCmd.Flags().StringVar(&sessionCreateRootPersona, "root-persona", "", "Root agent persona")
	sessionCreateCmd.Flags().StringVar(&sessionCreatePrimaryID, "primary-id", "", "Primary agent ID (required)")
	sessionCreateCmd.Flags().StringVar(&sessionCreatePrimaryPersona, "primary-persona", "", "Primary agent persona")
	_ = sessionCreateCmd.MarkFlagRequired("session")
	_ = sessionCreateCmd.MarkFlagRequired("root-id")
	_ = sessionCreateCmd.MarkFlagRequired("primary-id")

	// session delete flags
	sessionDeleteCmd.Flags().StringVar(&sessionDeleteSession, "session", "", "Session ID (required)")
	_ = sessionDeleteCmd.MarkFlagRequired("session")

	// session join flags
	sessionJoinCmd.Flags().StringVar(&sessionJoinAgentID, "agent-id", "", "Agent ID (required)")
	sessionJoinCmd.Flags().StringVar(&sessionJoinPersona, "persona", "", "Persona handle")
	sessionJoinCmd.Flags().StringVar(&sessionJoinParent, "parent", "", "Parent agent ID")
	sessionJoinCmd.Flags().StringVar(&sessionJoinAgentType, "agent-type", "", "Agent type")
	sessionJoinCmd.Flags().StringVar(&sessionJoinSession, "session", "", "Session ID (auto-detected if omitted)")
	_ = sessionJoinCmd.MarkFlagRequired("agent-id")

	// session leave flags
	sessionLeaveCmd.Flags().StringVar(&sessionLeaveAgentID, "agent-id", "", "Agent ID (required)")
	sessionLeaveCmd.Flags().StringVar(&sessionLeaveSession, "session", "", "Session ID (auto-detected if omitted)")
	_ = sessionLeaveCmd.MarkFlagRequired("agent-id")

	// session write-current flags
	sessionWriteCurrentCmd.Flags().StringVar(&sessionWriteCurrentPID, "pid", "", "Claude PID (required)")
	sessionWriteCurrentCmd.Flags().StringVar(&sessionWriteCurrentSession, "session", "", "Session ID (required)")
	_ = sessionWriteCurrentCmd.MarkFlagRequired("pid")
	_ = sessionWriteCurrentCmd.MarkFlagRequired("session")

	// session delete-current flags
	sessionDeleteCurrentCmd.Flags().StringVar(&sessionDeleteCurrentPID, "pid", "", "Claude PID (required)")
	_ = sessionDeleteCurrentCmd.MarkFlagRequired("pid")

	sessionCmd.AddCommand(
		sessionCreateCmd,
		sessionDeleteCmd,
		sessionJoinCmd,
		sessionLeaveCmd,
		sessionWriteCurrentCmd,
		sessionDeleteCurrentCmd,
		sessionPurgeCmd,
	)
	rootCmd.AddCommand(sessionCmd)
}

func runSessionShow() {
	sessionID := os.Getenv("ETHOS_SESSION")
	ss := sessionStore()
	if sessionID == "" {
		claudePID := process.FindClaudePID()
		sid, err := ss.ReadCurrentSession(claudePID)
		if err == nil {
			sessionID = sid
		}
	}
	if sessionID == "" {
		fmt.Println("No active session.")
		return
	}
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

func runSessionCreate() {
	ss := sessionStore()
	root := session.Participant{AgentID: sessionCreateRootID, Persona: sessionCreateRootPersona}
	primary := session.Participant{AgentID: sessionCreatePrimaryID, Persona: sessionCreatePrimaryPersona, Parent: sessionCreateRootID}
	if err := ss.Create(sessionCreateSession, root, primary); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(map[string]string{"session": sessionCreateSession})
	}
}

func runSessionDelete() {
	ss := sessionStore()
	if err := ss.Delete(sessionDeleteSession); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
}

func runSessionJoin() {
	sid := sessionJoinSession
	if sid == "" {
		sid, _ = resolveSessionContext()
	}

	ss := sessionStore()
	p := session.Participant{
		AgentID:   sessionJoinAgentID,
		Persona:   sessionJoinPersona,
		AgentType: sessionJoinAgentType,
		Parent:    sessionJoinParent,
	}
	if err := ss.Join(sid, p); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		printJSON(p)
	}
}

func runSessionLeave() {
	sid := sessionLeaveSession
	if sid == "" {
		sid, _ = resolveSessionContext()
	}

	ss := sessionStore()
	if err := ss.Leave(sid, sessionLeaveAgentID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
}

func runSessionWriteCurrent() {
	ss := sessionStore()
	if err := ss.WriteCurrentSession(sessionWriteCurrentPID, sessionWriteCurrentSession); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
}

func runSessionDeleteCurrent() {
	ss := sessionStore()
	if err := ss.DeleteCurrentSession(sessionDeleteCurrentPID); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
}

func runSessionPurge() {
	ss := sessionStore()

	// Purge stale PID files first (independent of roster purge).
	pidPurged, pidErr := ss.PurgeCurrent()
	if pidErr != nil {
		fmt.Fprintf(os.Stderr, "ethos: purging PID files: %v\n", pidErr)
	}

	purged, err := ss.Purge()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	if jsonOutput {
		if purged == nil {
			purged = []string{}
		}
		if pidPurged == nil {
			pidPurged = []string{}
		}
		printJSON(map[string][]string{
			"sessions":  purged,
			"pid_files": pidPurged,
		})
		if pidErr != nil {
			os.Exit(1)
		}
		return
	}
	for _, id := range purged {
		fmt.Printf("Purged session %s\n", id)
	}
	for _, pid := range pidPurged {
		fmt.Printf("Purged PID file %s\n", pid)
	}
	if pidErr != nil {
		os.Exit(1)
	}
	if len(purged) == 0 && len(pidPurged) == 0 {
		fmt.Println("No stale sessions found.")
	}
}
