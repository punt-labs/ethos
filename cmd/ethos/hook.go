package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/spf13/cobra"
)

var hookCmd = &cobra.Command{
	Use:    "hook",
	Short:  "Internal hook dispatcher (not for direct use)",
	Hidden: true,
}

var hookSessionStartCmd = &cobra.Command{
	Use:   "session-start",
	Short: "SessionStart hook handler",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookSessionStart()
	},
}

var hookSessionEndCmd = &cobra.Command{
	Use:   "session-end",
	Short: "SessionEnd hook handler",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookSessionEnd()
	},
}

var hookSubagentStartCmd = &cobra.Command{
	Use:   "subagent-start",
	Short: "SubagentStart hook handler",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookSubagentStart()
	},
}

var hookSubagentStopCmd = &cobra.Command{
	Use:   "subagent-stop",
	Short: "SubagentStop hook handler",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookSubagentStop()
	},
}

var hookPreCompactCmd = &cobra.Command{
	Use:   "pre-compact",
	Short: "PreCompact hook handler",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookPreCompact()
	},
}

var hookFormatOutputCmd = &cobra.Command{
	Use:   "format-output",
	Short: "PostToolUse output formatter",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookFormatOutput()
	},
}

var hookPreToolUseCmd = &cobra.Command{
	Use:   "pre-tool-use",
	Short: "PreToolUse hook handler (verifier file allowlist)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookPreToolUse()
	},
}

var hookAuditLogCmd = &cobra.Command{
	Use:   "audit-log",
	Short: "PostToolUse audit logger",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookAuditLog()
	},
}

func init() {
	hookCmd.AddCommand(
		hookSessionStartCmd,
		hookSessionEndCmd,
		hookSubagentStartCmd,
		hookSubagentStopCmd,
		hookPreCompactCmd,
		hookPreToolUseCmd,
		hookFormatOutputCmd,
		hookAuditLogCmd,
	)
	rootCmd.AddCommand(hookCmd)
}

func runHookSessionStart() error {
	is := identityStore()
	deps := hook.SessionStartDeps{
		Store:    is,
		Sessions: sessionStore(),
		Teams:    layeredTeamStore(is),
		Roles:    layeredRoleStore(is),
	}
	if err := hook.HandleSessionStart(os.Stdin, deps); err != nil {
		return fmt.Errorf("hook session-start: %w", err)
	}
	return nil
}

func runHookSessionEnd() error {
	ss := sessionStore()
	if err := hook.HandleSessionEnd(os.Stdin, ss); err != nil {
		return fmt.Errorf("hook session-end: %w", err)
	}
	return nil
}

func runHookSubagentStart() error {
	s := identityStore()
	ss := sessionStore()
	// Phase 3.3: wire the mission store and live hash sources so the
	// verifier hash gate enforces frozen-evaluator pinning at every
	// subagent spawn. A drift between mission create and verifier
	// spawn refuses the spawn with a fatal, operator-readable error.
	ms := missionStore()
	hashSources, err := mission.NewLiveHashSources(s, layeredRoleStore(s), layeredTeamStore(s))
	if err != nil {
		return fmt.Errorf("hook subagent-start: %w", err)
	}
	deps := hook.SubagentStartDeps{
		Identities: s,
		Sessions:   ss,
		Missions:   ms,
		Hash:       hashSources,
		RepoRoot:   resolve.FindRepoRoot(),
	}
	if err := hook.HandleSubagentStartWithDeps(os.Stdin, deps); err != nil {
		return fmt.Errorf("hook subagent-start: %w", err)
	}
	return nil
}

func runHookSubagentStop() error {
	ss := sessionStore()
	if err := hook.HandleSubagentStop(os.Stdin, ss); err != nil {
		return fmt.Errorf("hook subagent-stop: %w", err)
	}
	return nil
}

func runHookPreCompact() error {
	is := identityStore()
	deps := hook.PreCompactDeps{
		Identities: is,
		Sessions:   sessionStore(),
		Teams:      layeredTeamStore(is),
		Roles:      layeredRoleStore(is),
	}
	if err := hook.HandlePreCompact(os.Stdin, deps); err != nil {
		return fmt.Errorf("hook pre-compact: %w", err)
	}
	return nil
}

func runHookPreToolUse() error {
	if err := hook.HandlePreToolUse(os.Stdin, os.Stdout); err != nil {
		return fmt.Errorf("hook pre-tool-use: %w", err)
	}
	return nil
}

func runHookFormatOutput() error {
	if err := hook.HandleFormatOutput(os.Stdin, os.Stdout); err != nil {
		return fmt.Errorf("hook format-output: %w", err)
	}
	return nil
}

func runHookAuditLog() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("hook audit-log: %w", err)
	}
	sessionsDir := home + "/.punt-labs/ethos/sessions"
	// DES-054 phase 1: prefer the date-keyed repo-tree layout when
	// the hook fires inside a repo; fall back to the legacy global
	// sessions directory otherwise. FindRepoRoot returns the empty
	// string outside a git tree, which routes the audit log through
	// the legacy fallback path without further branching.
	//
	// Unified session flock — DES-054 v5 §"Storage Layout": one lock
	// covers roster + audit log, eliminating the prior two-lock
	// acquisition order. Drain stdin once into a buffer so the peek
	// for session_id and the subsequent HandleAuditLog read of the
	// same bytes do not race against a half-consumed pipe. The drain
	// reuses the hook package's deadline-aware reader so an open pipe
	// with no EOF (the real Claude Code shape) does not hang the
	// process. When the peek fails to find a session_id (malformed
	// payload, empty input, parse error), fall through to the unlocked
	// legacy path so an unparseable input still surfaces through
	// HandleAuditLog's stderr diagnostic rather than failing silently
	// here.
	data := drainAuditStdin()
	sessionID := peekSessionID(data)
	repoRoot := resolve.FindRepoRoot()
	if sessionID == "" {
		_ = hook.HandleAuditLog(bytes.NewReader(data), repoRoot, sessionsDir)
		return nil
	}
	ss := sessionStore()
	// WithSessionLock returns errors from BOTH lock acquisition AND
	// the callback fn. Distinguish them with a flag so a future
	// HandleAuditLog error path does not silently double-write the
	// audit entry (Bugbot LOW on PR #327). The flag flips only when
	// fn returns; an error from WithSessionLock with handlerRan=false
	// means lock acquisition failed and the unlocked fall-through is
	// safe.
	handlerRan := false
	if lockErr := ss.WithSessionLock(sessionID, func() error {
		err := hook.HandleAuditLog(bytes.NewReader(data), repoRoot, sessionsDir)
		handlerRan = true
		return err
	}); lockErr != nil && !handlerRan {
		// Lock acquisition failed — typically a permissions or fs
		// problem on the session lock file. Surface the failure on
		// stderr so the operator sees it, then fall through to the
		// unlocked legacy path so the audit entry still lands. A lost
		// audit entry is the worse outcome.
		fmt.Fprintf(os.Stderr,
			"ethos: audit-log: acquiring session lock %s: %v; falling through unlocked\n",
			sessionID, lockErr)
		_ = hook.HandleAuditLog(bytes.NewReader(data), repoRoot, sessionsDir)
	}
	return nil
}

// drainAuditStdin reads the hook payload bytes from os.Stdin with a
// short deadline so an open pipe without EOF (the shape Claude Code
// produces) does not block the process. Returns an empty slice on
// every failure mode — the caller routes empty input through the
// unlocked legacy HandleAuditLog path.
func drainAuditStdin() []byte {
	if err := os.Stdin.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		// Deadlines unsupported (Linux pipes that the parent did
		// not close) — race a single Read of a 64KiB buffer against
		// a 1-second timer so we do not block on an open pipe.
		//
		// Single-Read truncates payloads larger than 64KiB on this
		// platform path; a loop here would block in producers that
		// never close the writer (the subprocess test exercise this
		// shape — see TestSubprocess_AuditLog). Multi-chunk fallback
		// without a per-read deadline is a deferred follow-up
		// (Copilot on PR #327).
		ch := make(chan []byte, 1)
		go func() {
			buf := make([]byte, 65536)
			n, err := os.Stdin.Read(buf)
			if err != nil && !errors.Is(err, io.EOF) {
				fmt.Fprintf(os.Stderr,
					"ethos: audit-log: stdin read error: %v\n", err)
			}
			ch <- buf[:n]
		}()
		select {
		case b := <-ch:
			return b
		case <-time.After(time.Second):
			return nil
		}
	}
	defer os.Stdin.SetReadDeadline(time.Time{}) //nolint:errcheck
	var buf []byte
	chunk := make([]byte, 65536)
	for {
		n, err := os.Stdin.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			if sdErr := os.Stdin.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); sdErr != nil {
				break
			}
		}
		if err != nil {
			// EOF and deadline-exceeded are both normal terminators —
			// silent. The Claude Code hook protocol leaves the pipe open
			// without EOF, so the per-chunk SetReadDeadline above is the
			// expected termination signal once the producer goes idle.
			// Any other error (EBADF, EIO) is a real failure that should
			// be visible on stderr so the operator can correlate a
			// truncated audit entry with the underlying fault.
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrDeadlineExceeded) {
				fmt.Fprintf(os.Stderr,
					"ethos: audit-log: stdin read error: %v\n", err)
			}
			break
		}
	}
	return buf
}

// peekSessionID extracts session_id from a JSON-encoded audit hook
// payload without disturbing the bytes. Returns "" on parse error or a
// missing field; the caller routes to the unlocked legacy path so an
// unparseable payload still surfaces through HandleAuditLog's stderr
// diagnostic rather than failing silently here.
func peekSessionID(data []byte) string {
	var probe struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return ""
	}
	return probe.SessionID
}
