package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/process"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:     "session",
	Short:   "Manage session roster",
	GroupID: "session",
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionShow(cmd)
	},
}

// --- session start ---

var sessionStartPersona string

var sessionStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start (or re-attach to) a harness-neutral session",
	Long: `Start a session from any harness (Codex, a plain terminal, or Claude Code).

Mints a session, creates the roster from your resolved identity, and prints
an eval-able export line so the rest of ethos can find it:

    eval "$(ethos session start)"

If ETHOS_SESSION already names a live session, that one is reported rather
than minting a second, so re-running it in a subshell or rc file is safe.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionStart(cmd)
	},
}

// --- session end ---

var sessionEndSession string

var sessionEndCmd = &cobra.Command{
	Use:   "end",
	Short: "End the active session (delete its roster)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionEnd(cmd)
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
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionCreate(cmd)
	},
}

// --- session delete ---

var sessionDeleteSession string

var sessionDeleteCmd = &cobra.Command{
	Use:    "delete",
	Short:  "Delete a session roster",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionDelete(cmd)
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
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionJoin(cmd)
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
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionLeave(cmd)
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
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionWriteCurrent(cmd)
	},
}

// --- session delete-current ---

var sessionDeleteCurrentPID string

var sessionDeleteCurrentCmd = &cobra.Command{
	Use:    "delete-current",
	Short:  "Delete PID-to-session mapping",
	Args:   cobra.NoArgs,
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionDeleteCurrent(cmd)
	},
}

// --- session list ---

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all sessions",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionList(cmd)
	},
}

// --- session show ---

var sessionShowCmd = &cobra.Command{
	Use:   "show [session-id]",
	Short: "Show session roster",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			return runSessionShowByID(cmd, args[0])
		}
		return runSessionShow(cmd)
	},
}

// --- session roster (hidden alias) ---

var sessionRosterCmd = &cobra.Command{
	Use:    "roster [session-id]",
	Short:  "Show session roster (alias for show)",
	Hidden: true,
	Args:   cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 {
			return runSessionShowByID(cmd, args[0])
		}
		return runSessionShow(cmd)
	},
}

// --- session iam ---

var sessionIamSession string

var sessionIamCmd = &cobra.Command{
	Use:   "iam <persona>",
	Short: "Declare persona in current session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIam(args[0])
	},
}

// --- session purge ---

var (
	sessionPurgeForce bool
	sessionPurgeAck   string
)

var sessionPurgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Clean up stale sessions",
	Long: `Clean up stale sessions.

Refuses to purge a session whose live audit file still holds unsealed
lines (commit to seal them, or re-run with --force to leave a flagged
tombstone and proceed). The seal's vacuum cross-check warns on a flagged
tombstone at every commit until it is acknowledged with
--ack <session-id>, which retires the tombstone without discarding the
loss record.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSessionPurge(cmd)
	},
}

func init() {
	// session start / end flags
	sessionStartCmd.Flags().StringVar(&sessionStartPersona, "persona", "", "Persona for the primary agent (folds the first iam)")
	sessionEndCmd.Flags().StringVar(&sessionEndSession, "session", "", "Session ID (full or prefix; auto-detected if omitted)")

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

	// session purge flags
	sessionPurgeCmd.Flags().BoolVar(&sessionPurgeForce, "force", false,
		"Purge even when a session has unsealed audit lines (leaves a flagged tombstone)")
	sessionPurgeCmd.Flags().StringVar(&sessionPurgeAck, "ack", "",
		"Acknowledge and retire the tombstone for the named session id")

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
		sessionStartCmd,
		sessionEndCmd,
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

func runSessionShow(cmd *cobra.Command) error {
	ss := sessionStore()
	sessionID, _ := resolve.SessionID(ss)
	if sessionID == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "No active session.")
		return nil
	}
	return printRoster(cmd, ss, sessionID)
}

func runSessionShowByID(cmd *cobra.Command, idOrPrefix string) error {
	ss := sessionStore()
	sessionID, err := ss.MatchByPrefix(idOrPrefix)
	if err != nil {
		return err
	}
	return printRoster(cmd, ss, sessionID)
}

func printRoster(cmd *cobra.Command, ss *session.Store, sessionID string) error {
	roster, err := ss.Load(sessionID)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if jsonOutput {
		return writeJSON(out, roster)
	}

	fmt.Fprintf(out, "Session: %s\n", roster.Session)
	if roster.Repo != "" {
		fmt.Fprintf(out, "Repo:    %s\n", roster.Repo)
	}
	if roster.Host != "" {
		fmt.Fprintf(out, "Host:    %s\n", roster.Host)
	}
	fmt.Fprintf(out, "Started: %s\n", formatStarted(roster.Started))
	fmt.Fprintln(out)

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
	fmt.Fprintln(out, hook.FormatTable(headers, rows))
	return nil
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

// runSessionStart mints a session (or re-attaches to the one named by
// ETHOS_SESSION) and prints an eval-able export line. It bottoms out in the
// same Store.Create the SessionStart hook and `session create` use, and
// writes no current-pointer — outside Claude Code the discovery channel is
// ETHOS_SESSION (DES-061 R1).
func runSessionStart(cmd *cobra.Command) error {
	ss := sessionStore()

	// 0. Validate --persona up front against the identity handle charset
	//    (lowercase alphanumeric with hyphens). This rejects any value with
	//    a shell metacharacter before it can be interpolated into the
	//    eval-able export lines, and aligns with persona resolving to a real
	//    identity. Rejected values never reach stdout.
	if sessionStartPersona != "" {
		if err := attribute.ValidateSlug(sessionStartPersona); err != nil {
			return fmt.Errorf("session start: --persona %q must be a valid handle (lowercase alphanumeric with hyphens)", sessionStartPersona)
		}
	}

	// 1. Idempotency: an existing ETHOS_SESSION. A live roster re-attaches;
	//    a not-found env is stale and mints a fresh session (by design). An
	//    exists-but-unparseable roster is a crash artifact that most likely
	//    holds unsealed audit lines — do NOT silently mint over it (which
	//    would repoint the env anchor away from it), fail with a remedy.
	if envID := os.Getenv("ETHOS_SESSION"); envID != "" {
		switch roster, lerr := ss.Load(envID); {
		case lerr == nil:
			// Refuse to re-attach to a loadable roster whose ID has unsafe
			// characters — before any roster mutation — so a control-char
			// ETHOS_SESSION can neither be echoed nor mutate the roster.
			if !safeSessionID.MatchString(envID) {
				return fmt.Errorf("session start: refusing to re-attach to ETHOS_SESSION %q — contains unsafe characters", envID)
			}
			// A re-run with --persona still folds the iam — upsert-join the
			// persona so the advertised ETHOS_AGENT_ID names a real
			// participant (Join is idempotent: same persona stays one).
			// Parent is the root recorded at mint, read from the roster —
			// NOT re-resolved: a prior run may have exported
			// ETHOS_AGENT_ID=<persona>, which would make resolve.Resolve
			// return the persona itself and self-parent it, silently
			// breaking post-compaction primary-agent discovery (Parent==root).
			if sessionStartPersona != "" {
				parent := ""
				if len(roster.Participants) > 0 {
					parent = roster.Participants[0].AgentID
				}
				// Skip the join when the persona IS the root — joining it
				// would set root's Parent to itself (unique-AgentID upsert).
				if sessionStartPersona != parent {
					p := session.Participant{AgentID: sessionStartPersona, Persona: sessionStartPersona, Parent: parent}
					if jerr := ss.Join(envID, p); jerr != nil {
						return fmt.Errorf("session start: joining persona: %w", jerr)
					}
				}
			}
			return printSessionStart(cmd, envID, sessionStartPersona, false)
		case !errors.Is(lerr, os.ErrNotExist):
			return fmt.Errorf("session start: ETHOS_SESSION %q names an unreadable roster (%v); repair or clear it — see `ethos session purge` / `ethos audit quarantine`", envID, lerr)
		}
		// not-found: fall through to mint a fresh session (by design).
	}

	// 2. Mint an opaque 32-hex ID (16 bytes of crypto/rand).
	id, err := mintSessionID()
	if err != nil {
		return fmt.Errorf("session start: %w", err)
	}

	// 3. Resolve identities. Root is the human, resolved via the IDENTITY
	//    chain only (git/OS) — passing nil for the session store skips the
	//    ambient-session walk, so minting inside a live Claude session does
	//    NOT take that session's primary persona as the new root. Primary is
	//    the agent: --persona (folds the first iam and keys the primary so a
	//    matching ETHOS_AGENT_ID export reflects it), else ETHOS_AGENT_ID,
	//    else the repo default agent, else the human.
	store := identityStore()
	human, err := resolve.Resolve(store, nil)
	if err != nil {
		return fmt.Errorf("session start: resolving identity: %w", err)
	}
	primaryID := sessionStartPersona
	if primaryID == "" {
		primaryID = os.Getenv("ETHOS_AGENT_ID")
	}
	if primaryID == "" {
		agent, aerr := resolve.ResolveAgent(resolve.StoreRepoRoot())
		if aerr != nil {
			return fmt.Errorf("session start: resolving agent: %w", aerr)
		}
		primaryID = agent
	}
	if primaryID == "" {
		primaryID = human
	}
	primaryPersona := sessionStartPersona
	if primaryPersona == "" {
		primaryPersona = primaryID
	}

	// 4. Create the roster via the shared Store.Create (repo/host resolved
	//    exactly as the hook resolves them — R6 convergence).
	root := session.Participant{AgentID: human, Persona: human}
	primary := session.Participant{AgentID: primaryID, Persona: primaryPersona, Parent: human}
	if err := ss.CreateInCheckout(id, root, primary,
		hook.ResolveRepo(), resolve.EnvRepoRoot(), hook.ResolveHost()); err != nil {
		return fmt.Errorf("session start: creating roster: %w", err)
	}

	return printSessionStart(cmd, id, sessionStartPersona, true)
}

// printSessionStart writes the eval-able export line(s) to stdout and a
// human-readable confirmation to stderr, so eval "$(ethos session start)"
// captures only the exports. When persona is non-empty (--persona folded
// the first iam), a second export sets ETHOS_AGENT_ID to it so the eval'd
// shell resolves that persona — the primary participant is keyed on it. The
// export line IS the contract, so a failed --json stdout write propagates
// to a non-zero exit rather than exiting 0 having emitted nothing.
// safeSessionID admits any opaque session ID (our 32-hex mints, Claude
// Code's UUID session_ids, any sane identifier) while rejecting whitespace,
// control characters, and shell metacharacters. Echoing is gated on it so a
// user-supplied ETHOS_SESSION carrying a newline or `;`/`$()` cannot make
// the eval-able export multi-line or inject — without over-constraining the
// opaque-ID contract (DES-061 R1/F7) to the mint shape.
var safeSessionID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func printSessionStart(cmd *cobra.Command, id, persona string, created bool) error {
	if !safeSessionID.MatchString(id) {
		return fmt.Errorf("session start: refusing to echo a session id with unsafe characters: %q", id)
	}
	if jsonOutput {
		out := map[string]any{"session": id, "created": created}
		if persona != "" {
			out["agent_id"] = persona
		}
		return writeJSON(cmd.OutOrStdout(), out)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "export ETHOS_SESSION=%s\n", shellQuote(id))
	if persona != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "export ETHOS_AGENT_ID=%s\n", shellQuote(persona))
	}
	if created {
		fmt.Fprintf(cmd.ErrOrStderr(), "ethos: started session %s\n", id)
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "ethos: session %s already active\n", id)
	}
	return nil
}

// shellQuote wraps s in single quotes with POSIX-safe escaping so an eval'd
// export line cannot break out of the assignment or inject a command. Each
// embedded single quote is replaced by the sequence
//
//	'\''
//
// (close-quote, escaped quote, reopen-quote). Values are already
// handle-validated, so this is defense in depth — belt and suspenders on the
// eval contract.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// mintSessionID returns 16 bytes of crypto/rand hex-encoded to a
// 32-character string — opaque, filesystem-safe, fixed-length, zero new
// dependency (DES-061).
func mintSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("minting session id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// runSessionEnd tears down the resolved session: delete the roster, then
// remove the Claude-path PID current-pointer — but only when that pointer
// actually names the session we ended. Ending a stale or other session
// (--session or a nested ETHOS_SESSION) must not sever the caller's live
// Claude session's discovery channel.
func runSessionEnd(cmd *cobra.Command) error {
	ss := sessionStore()
	// Teardown does NOT hard-verify an env-sourced ID: "already gone" is
	// success (the rm -f norm), so a re-run in a trap handler or rc cleanup
	// with a stale ETHOS_SESSION is a no-op with a note, not an error.
	sid, _, err := resolveSession(sessionEndSession, false)
	if err != nil {
		// No session context at all is the same "nothing to tear down"
		// state as an already-gone roster — end is idempotent across the
		// board (the sole teardown exception; state-writers still hard-fail).
		if errors.Is(err, errNoSession) {
			fmt.Fprintln(cmd.ErrOrStderr(), "ethos: no active session; nothing to end")
			return nil
		}
		return err
	}
	if _, lerr := ss.Load(sid); lerr != nil {
		// not-found is a clean no-op (rm -f); an exists-but-unparseable
		// roster is a crash artifact most likely holding unsealed audit
		// lines — refuse rather than os.Remove it out from under the seal.
		if errors.Is(lerr, os.ErrNotExist) {
			fmt.Fprintf(cmd.ErrOrStderr(), "ethos: session %s not found; nothing to end\n", sid)
			return nil
		}
		return fmt.Errorf("session end: session %q has an unreadable roster (%v); repair or clear it — see `ethos session purge` / `ethos audit quarantine`", sid, lerr)
	}
	if err := ss.Delete(sid); err != nil {
		return err
	}
	// Only remove the current-pointer if it points at the session we ended;
	// otherwise a stale-session teardown from inside a live Claude session
	// would silently orphan that live session.
	claudePID := process.FindClaudePID()
	switch cur, rerr := ss.ReadCurrentSession(claudePID); {
	case rerr == nil && cur == sid:
		if derr := ss.DeleteCurrentSession(claudePID); derr != nil {
			return fmt.Errorf("session end: %w", derr)
		}
	case rerr != nil && !errors.Is(rerr, os.ErrNotExist):
		// Never delete on an unverifiable read — but a dangling pointer that
		// now names a deleted session would confuse walk consumers, so say so.
		fmt.Fprintf(cmd.ErrOrStderr(), "ethos: current-pointer could not be verified and was left in place: %v\n", rerr)
	}
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), map[string]string{"ended": sid})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "ended session %s\n", sid)
	return nil
}

func runSessionCreate(cmd *cobra.Command) error {
	ss := sessionStore()
	root := session.Participant{AgentID: sessionCreateRootID, Persona: sessionCreateRootPersona}
	primary := session.Participant{AgentID: sessionCreatePrimaryID, Persona: sessionCreatePrimaryPersona, Parent: sessionCreateRootID}
	if err := ss.Create(sessionCreateSession, root, primary, "", ""); err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), map[string]string{"session": sessionCreateSession})
	}
	return nil
}

func runSessionDelete(cmd *cobra.Command) error {
	ss := sessionStore()
	return ss.Delete(sessionDeleteSession)
}

func runSessionJoin(cmd *cobra.Command) error {
	ss := sessionStore()
	sid := sessionJoinSession
	if sid == "" {
		var resolveErr error
		sid, _, resolveErr = resolveSessionContext()
		if resolveErr != nil {
			return resolveErr
		}
	} else {
		resolved, err := ss.MatchByPrefix(sid)
		if err != nil {
			return err
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
		return err
	}
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), p)
	}
	return nil
}

func runSessionLeave(cmd *cobra.Command) error {
	ss := sessionStore()
	sid := sessionLeaveSession
	if sid == "" {
		var resolveErr error
		sid, _, resolveErr = resolveSessionContext()
		if resolveErr != nil {
			return resolveErr
		}
	} else {
		resolved, err := ss.MatchByPrefix(sid)
		if err != nil {
			return err
		}
		sid = resolved
	}

	return ss.Leave(sid, sessionLeaveAgentID)
}

func runSessionWriteCurrent(cmd *cobra.Command) error {
	ss := sessionStore()
	return ss.WriteCurrentSession(sessionWriteCurrentPID, sessionWriteCurrentSession)
}

func runSessionDeleteCurrent(cmd *cobra.Command) error {
	ss := sessionStore()
	return ss.DeleteCurrentSession(sessionDeleteCurrentPID)
}

func runSessionList(cmd *cobra.Command) error {
	ss := sessionStore()
	ids, err := ss.List()
	if err != nil {
		return err
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
			fmt.Fprintf(cmd.ErrOrStderr(), "ethos: warning: session %s: %v\n", id, loadErr)
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

	out := cmd.OutOrStdout()
	if jsonOutput {
		if entries == nil {
			entries = []sessionEntry{}
		}
		return writeJSON(out, entries)
	}

	if len(entries) == 0 {
		fmt.Fprintln(out, "No sessions found.")
		return nil
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
	fmt.Fprintln(out, hook.FormatTable(headers, rows))
	return nil
}

// shortID truncates a session ID to its first 8 characters for display.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// formatStarted is a thin wrapper around hook.FormatLocalTime so
// existing callers in this package keep the same short local name.
// The implementation lives in internal/hook so session, mission, and
// any future command share one time-formatting convention.
func formatStarted(raw string) string {
	return hook.FormatLocalTime(raw)
}

func runSessionPurge(cmd *cobra.Command) error {
	ss := sessionStore()
	out := cmd.OutOrStdout()

	// --ack retires a session's tombstone so the seal's vacuum cross-check
	// stops warning on it, without discarding the loss record (DES-058).
	if sessionPurgeAck != "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolving home directory: %w", err)
		}
		retired, err := audit.AckTombstone(filepath.Join(home, ".punt-labs", "ethos", "sessions"), sessionPurgeAck)
		if err != nil {
			return err
		}
		if jsonOutput {
			return writeJSON(out, map[string]string{
				"acked":   sessionPurgeAck,
				"retired": retired,
			})
		}
		fmt.Fprintf(out, "Acknowledged tombstone for %s (retired to %s)\n", sessionPurgeAck, retired)
		return nil
	}

	// Purge stale PID files first (independent of roster purge).
	pidPurged, pidErr := ss.PurgeCurrent()
	if pidErr != nil {
		pidErr = fmt.Errorf("purging PID files: %w", pidErr)
	}

	// DES-058: PurgeTombstoned refuses to strand a session's unsealed audit
	// lines unless --force, and leaves a flagged tombstone when it proceeds. It
	// probes the live audit zone under this checkout (repoRoot) and only for
	// sessions whose recorded identity matches this checkout's (repoID); a purge
	// run outside any repo leaves every repo-bound session for its owning
	// checkout.
	repoRoot := resolve.EnvRepoRoot()
	repoID := audit.RepoIdentity(repoRoot)
	purged, refused, err := ss.PurgeTombstoned(repoRoot, repoID, sessionPurgeForce)
	if err != nil {
		return err
	}
	if jsonOutput {
		if purged == nil {
			purged = []string{}
		}
		if pidPurged == nil {
			pidPurged = []string{}
		}
		if refused == nil {
			refused = []string{}
		}
		if werr := writeJSON(out, map[string][]string{
			"sessions":  purged,
			"pid_files": pidPurged,
			"refused":   refused,
		}); werr != nil {
			return werr
		}
		if pidErr != nil {
			return pidErr
		}
		return nil
	}
	for _, id := range refused {
		fmt.Fprintf(out, "Refused to purge %s (see stderr for cause; --force overrides)\n", id)
	}
	for _, id := range purged {
		fmt.Fprintf(out, "Purged session %s\n", id)
	}
	for _, pid := range pidPurged {
		fmt.Fprintf(out, "Purged PID file %s\n", pid)
	}
	if pidErr != nil {
		return pidErr
	}
	switch {
	case len(purged) == 0 && len(pidPurged) == 0 && len(refused) == 0:
		fmt.Fprintln(out, "No stale sessions found.")
	case len(purged) == 0 && len(pidPurged) == 0:
		fmt.Fprintf(out, "Nothing purged; %d session(s) refused (see above).\n", len(refused))
	}
	return nil
}
