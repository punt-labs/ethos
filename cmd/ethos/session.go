package main

import (
	"fmt"
	"os"
	"time"

	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/session"
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:     "session",
	Short:   "Manage session roster",
	GroupID: "session",
	Args:    cobra.NoArgs,
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
	Use:    "create",
	Short:  "Create a new session roster",
	Hidden: true,
	Args:   cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runSessionCreate()
	},
}

// --- session delete ---

var sessionDeleteSession string

var sessionDeleteCmd = &cobra.Command{
	Use:    "delete",
	Short:  "Delete a session roster",
	Hidden: true,
	Args:   cobra.NoArgs,
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

// --- session list ---

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sessions",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		runSessionList()
	},
}

// --- session show ---

var sessionShowCmd = &cobra.Command{
	Use:   "show [session-id]",
	Short: "Show session roster",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 1 {
			runSessionShowByID(args[0])
		} else {
			runSessionShow()
		}
	},
}

// --- session roster (hidden alias) ---

var sessionRosterCmd = &cobra.Command{
	Use:    "roster [session-id]",
	Short:  "Show session roster (alias for show)",
	Hidden: true,
	Args:   cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 1 {
			runSessionShowByID(args[0])
		} else {
			runSessionShow()
		}
	},
}

// --- session iam ---

var sessionIamSession string

var sessionIamCmd = &cobra.Command{
	Use:   "iam <persona>",
	Short: "Declare persona in current session",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runIam(args[0])
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

	// session iam flags
	sessionIamCmd.Flags().StringVar(&sessionIamSession, "session", "", "Session ID (full or prefix)")

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
		sessionIamCmd,
		sessionListCmd,
		sessionShowCmd,
		sessionRosterCmd,
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
	printRoster(ss, sessionID)
}

func runSessionShowByID(idOrPrefix string) {
	ss := sessionStore()
	sessionID, err := ss.MatchByPrefix(idOrPrefix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}
	printRoster(ss, sessionID)
}

func printRoster(ss *session.Store, sessionID string) {
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
	if roster.Repo != "" {
		fmt.Printf("Repo:    %s\n", roster.Repo)
	}
	if roster.Host != "" {
		fmt.Printf("Host:    %s\n", roster.Host)
	}
	fmt.Printf("Started: %s\n", formatStarted(roster.Started))
	fmt.Println()

	headers := []string{"AGENT_ID", "PERSONA", "ROLE", "PARENT", "JOINED"}
	rows := make([][]string, len(roster.Participants))
	for i, p := range roster.Participants {
		persona := p.Persona
		if persona == "" {
			persona = "-"
		}
		parent := p.Parent
		if parent == "" {
			parent = "-"
		}
		joined := formatStarted(p.Joined)
		if joined == "" {
			joined = "-"
		}
		role := inferRole(i, p.Parent)
		rows[i] = []string{p.AgentID, persona, role, parent, joined}
	}
	fmt.Println(hook.FormatTable(headers, rows))
}

// inferRole derives a display role from a participant's position and parentage.
func inferRole(index int, parent string) string {
	if index == 0 {
		return "root"
	}
	if index == 1 {
		return "primary"
	}
	if parent == "" {
		return "-"
	}
	return "teammate"
}

func runSessionCreate() {
	ss := sessionStore()
	root := session.Participant{AgentID: sessionCreateRootID, Persona: sessionCreateRootPersona}
	primary := session.Participant{AgentID: sessionCreatePrimaryID, Persona: sessionCreatePrimaryPersona, Parent: sessionCreateRootID}
	if err := ss.Create(sessionCreateSession, root, primary, "", ""); err != nil {
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
	ss := sessionStore()
	sid := sessionJoinSession
	if sid == "" {
		sid, _ = resolveSessionContext()
	} else {
		resolved, err := ss.MatchByPrefix(sid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			os.Exit(1)
		}
		sid = resolved
	}

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
	ss := sessionStore()
	sid := sessionLeaveSession
	if sid == "" {
		sid, _ = resolveSessionContext()
	} else {
		resolved, err := ss.MatchByPrefix(sid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
			os.Exit(1)
		}
		sid = resolved
	}

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

func runSessionList() {
	ss := sessionStore()
	ids, err := ss.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ethos: %v\n", err)
		os.Exit(1)
	}

	type sessionEntry struct {
		Session      string `json:"session"`
		Started      string `json:"started"`
		Repo         string `json:"repo,omitempty"`
		Host         string `json:"host,omitempty"`
		Participants int    `json:"participants"`
	}

	var entries []sessionEntry
	for _, id := range ids {
		roster, loadErr := ss.Load(id)
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "ethos: warning: session %s: %v\n", id, loadErr)
			continue
		}
		entries = append(entries, sessionEntry{
			Session:      id,
			Started:      roster.Started,
			Repo:         roster.Repo,
			Host:         roster.Host,
			Participants: len(roster.Participants),
		})
	}

	if jsonOutput {
		if entries == nil {
			entries = []sessionEntry{}
		}
		printJSON(entries)
		return
	}

	if len(entries) == 0 {
		fmt.Println("No sessions found.")
		return
	}

	headers := []string{"SESSION", "PARTICIPANTS", "REPO", "STARTED"}
	rows := make([][]string, len(entries))
	for i, e := range entries {
		repo := e.Repo
		if repo == "" {
			repo = "-"
		}
		rows[i] = []string{
			shortID(e.Session),
			fmt.Sprintf("%d", e.Participants),
			repo,
			formatStarted(e.Started),
		}
	}
	fmt.Println(hook.FormatTable(headers, rows))
}

// shortID truncates a session ID to its first 8 characters for display.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// formatStarted converts an RFC3339 timestamp to a human-readable format.
// Returns the raw string if parsing fails.
func formatStarted(raw string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.Local().Format("Mon Jan _2 15:04")
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
