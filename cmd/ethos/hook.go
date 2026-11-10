package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/enable"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/spf13/cobra"
)

// hookCmd is a pure dispatcher: every hook is a subcommand and the
// group itself does nothing useful. It still carries a RunE, because a
// command with no Run is "not runnable" and cobra answers an unknown
// subcommand by printing this group's help on STDOUT and exiting 0
// (command.go: `if !c.Runnable() { return flag.ErrHelp }`, which
// precedes argument validation). A caller that shells out to `ethos
// hook <x>` against a binary too old to know <x> — the commit-msg
// trailer fallback does exactly that — then reads help text as output
// and silently produces nothing (rsc on PR #415).
//
// RunE plus NoArgs makes both misuses loud: an unknown subcommand
// fails argument validation, and a bare `ethos hook` returns the error
// below. Both exit non-zero, which is what a shell caller tests.
var hookCmd = &cobra.Command{
	Use:    "hook",
	Short:  "Internal hook dispatcher (not for direct use)",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("hook: no hook named; run `ethos hook --help` for the list")
	},
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

var hookCommitTrailersCmd = &cobra.Command{
	Use:    "commit-trailers",
	Hidden: true,
	Short:  "Mission/Delegation trailer values for the committing session",
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookCommitTrailers(cmd.OutOrStdout())
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
		hookCommitTrailersCmd,
	)
	rootCmd.AddCommand(hookCmd)
}

// refreshGitHooks adapts enable.RefreshHooksTo to the SessionStart dependency.
// The result is filled even when err is not nil, so a partial refresh is told.
func refreshGitHooks(checkout, store string) (hook.HookRefresh, error) {
	rep, err := enable.RefreshHooksTo(checkout, store)
	var r hook.HookRefresh
	if rep == nil {
		return r, err
	}
	r.Warnings = rep.Warnings
	for _, st := range rep.Steps {
		name := strings.TrimPrefix(st.Step, "hook:")
		switch st.Status {
		case "installed":
			r.Installed = append(r.Installed, name)
		case enable.StatusSkippedNonShell:
			r.NonShell = append(r.NonShell, name)
		case "chained":
			r.Chained = append(r.Chained, name)
		case enable.StatusSkippedTracked:
			r.Tracked = append(r.Tracked, name)
		case enable.StatusSkippedShared:
			r.Shared = append(r.Shared, name)
		case enable.StatusSkippedTrackedMissing:
			r.TrackedMissing = append(r.TrackedMissing, name)
		default:
			r.Refreshed = append(r.Refreshed, name)
		}
	}
	return r, err
}

func runHookSessionStart() error {
	loc := currentRoots().Located
	is := identityStore()
	deps := hook.SessionStartDeps{
		Located:      &loc,
		Store:        is,
		Sessions:     sessionStore(),
		Teams:        layeredTeamStore(is),
		Roles:        layeredRoleStore(is),
		Issue:        currentRoots().configIssue(),
		RefreshHooks: refreshGitHooks,
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
	deps := hook.SubagentStartDeps{
		Identities: s,
		Sessions:   sessionStore(),
		Issue:      currentRoots().hookIssue(),
	}
	// RepoRoot is the work-tree root for the write-set walk (files under
	// review live in this checkout); StoreRoot is the mission store's repo
	// for the hash-refusal skeleton close (CR#3). When either cannot be
	// located, the mission store would be the global one — the wrong
	// store for the gate — so none is wired and the gate refuses instead.
	repoRoot, storeRoot, err := locateHookRoots()
	if err != nil {
		deps.StoreErr = err
	} else {
		// Phase 3.3: wire the mission store and live hash sources so the
		// verifier hash gate enforces frozen-evaluator pinning at every
		// subagent spawn. A drift between mission create and verifier
		// spawn refuses the spawn with a fatal, operator-readable error.
		hashSources, err := mission.NewLiveHashSources(s, layeredRoleStore(s), layeredTeamStore(s))
		if err != nil {
			return fmt.Errorf("hook subagent-start: %w", err)
		}
		deps.Missions = missionStore()
		deps.Hash = hashSources
		deps.RepoRoot = repoRoot
		deps.StoreRoot = storeRoot
	}
	if err := hook.HandleSubagentStartWithDeps(os.Stdin, deps); err != nil {
		return fmt.Errorf("hook subagent-start: %w", err)
	}
	return nil
}

// locateHookRoots returns the checkout and the store root. Both are empty
// outside a repository; any other failure to locate either is the error.
func locateHookRoots() (repoRoot, storeRoot string, err error) {
	r := currentRoots()
	if err := firstError(r.CheckoutErr, r.StoreFailure()); err != nil {
		return "", "", err
	}
	return r.Checkout, r.Store, nil
}

func runHookSubagentStop() error {
	ss := sessionStore()
	if err := hook.HandleSubagentStop(os.Stdin, ss); err != nil {
		return fmt.Errorf("hook subagent-stop: %w", err)
	}
	return nil
}

func runHookPreCompact() error {
	loc := currentRoots().Located
	is := identityStore()
	deps := hook.PreCompactDeps{
		Located:    &loc,
		Identities: is,
		Sessions:   sessionStore(),
		Teams:      layeredTeamStore(is),
		Roles:      layeredRoleStore(is),
		Issue:      currentRoots().hookIssue(),
	}
	if err := hook.HandlePreCompact(os.Stdin, deps); err != nil {
		return fmt.Errorf("hook pre-compact: %w", err)
	}
	return nil
}

func runHookPreToolUse() error {
	loc := currentRoots().Located
	if err := hook.HandlePreToolUseWithLocated(os.Stdin, os.Stdout, loc); err != nil {
		return fmt.Errorf("hook pre-tool-use: %w", err)
	}
	return nil
}

// runHookCommitTrailers prints the Mission/Delegation trailer values
// for the session that is committing, for the commit-msg hook to read
// back. Resolution is the harness-neutral chain the rest of the CLI
// uses — ETHOS_SESSION, then the Claude process-tree walk — so a hook
// running under a Bash tool call inside a session finds that session
// and no other.
//
// This hook must never block a commit, so it always exits 0 and never
// emits a trailer it cannot vouch for (ethos-pobi). It does distinguish,
// on stderr, WHY no trailer was added: a commit from a plain terminal or
// a non-Claude harness (no session was ever expected) stays fully
// silent — noisy stderr on every ordinary commit outside Claude Code
// would defeat the point. A commit running under Claude Code whose
// session could not be identified DOES print a note: per this hook's own
// rationale, a silently missing trailer is exactly the failure class it
// exists to prevent, so that case must not go unnoticed the same way.
func runHookCommitTrailers(out io.Writer) error {
	sessionID, _, err := resolve.SessionID(sessionStore())
	if err != nil {
		switch {
		case errors.Is(err, resolve.ErrNotUnderClaudeCode):
		case errors.Is(err, resolve.ErrNoSession):
			fmt.Fprintln(os.Stderr, "ethos: commit-trailers: no trailer added: no Claude Code session is bound to this process")
		default:
			fmt.Fprintf(os.Stderr, "ethos: commit-trailers: no trailer added: %v\n", err)
		}
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("hook commit-trailers: %w", err)
	}
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	if err := hook.WriteCommitTrailers(out, globalRoot, sessionID); err != nil {
		return fmt.Errorf("hook commit-trailers: %w", err)
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
	// A dropped or unlocked entry never blocks the tool call, but it is put
	// in front of the session (systemMessage and additionalContext), not
	// left on the hook's stderr, which goes to a log file nobody reads.
	if notice := writeAuditLog(home + "/.punt-labs/ethos/sessions"); notice != nil {
		if err := hook.WriteAuditNotice(os.Stdout, notice); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: audit-log: %v\n", err)
		}
	}
	return nil
}

// writeAuditLog records the PostToolUse payload on stdin. It returns the
// error wrapping hook.ErrAuditDropped when the entry was not written, and
// an error naming the lock when the entry was written without it; either
// is shown in the session. nil means the entry was written under its lock.
//
// Repository writes use the captured shared-store policy; only an operation
// captured outside a repository uses the legacy global sessions directory.
//
// Unified session flock — DES-054 v5 §"Storage Layout": one lock covers
// roster + audit log, eliminating the prior two-lock acquisition order.
// Drain stdin once into a buffer so the peek for session_id and the
// subsequent HandleAuditLog read of the same bytes do not race against a
// half-consumed pipe. The drain reuses the hook package's deadline-aware
// reader so an open pipe with no EOF (the real Claude Code shape) does not
// hang the process. When the peek fails to find a session_id (malformed
// payload, empty input, parse error), fall through to the unlocked legacy
// path so an unparseable input still surfaces through HandleAuditLog's
// diagnostic rather than failing silently here.
func writeAuditLog(sessionsDir string) error {
	// Capture before stdin can block. A linked checkout is the redaction base,
	// not the owner of live files, archive records, or stream locks.
	loc := currentRoots().Located
	if err := loc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: audit-log: %v; audit entry not written rather than written to the global store\n", err)
		return fmt.Errorf("%w: %w; not written rather than written to the global store", hook.ErrAuditDropped, err)
	}
	if loc.Store != "" {
		policy, err := loc.Layout()
		if err != nil {
			return fmt.Errorf("%w: %w", hook.ErrAuditDropped, err)
		}
		data, err := drainAuditStdin()
		if err != nil {
			return auditInputDropped(err)
		}
		// The policy-aware handler owns the shared stream lock. An outer
		// WithLiveAuditLock would both rediscover policy and take it twice.
		return hook.HandleAuditLogWithPolicy(bytes.NewReader(data), policy, sessionsDir)
	}
	data, err := drainAuditStdin()
	if err != nil {
		return auditInputDropped(err)
	}
	sessionID := peekSessionID(data)
	record := func() error { return hook.HandleAuditLog(bytes.NewReader(data), "", sessionsDir) }
	if sessionID == "" {
		return record()
	}
	// WithSessionLock returns errors from BOTH lock
	// acquisition AND the callback. handlerRan tells them apart, so a lost
	// entry is not written twice: an error with handlerRan=false means the
	// lock was not acquired and the unlocked fall-through is safe (a lost
	// audit entry is the worse outcome).
	handlerRan := false
	var dropErr error
	run := func() error {
		dropErr = record()
		handlerRan = true
		return dropErr
	}
	// Outside a repo there is no local zone. The legacy global session
	// lock covers the single-tree fallback write.
	lockName := "session lock"
	lockErr := sessionStore().WithSessionLock(sessionID, run)
	if lockErr != nil && !handlerRan {
		fmt.Fprintf(os.Stderr,
			"ethos: audit-log: acquiring %s %s: %v; falling through unlocked\n",
			lockName, sessionID, lockErr)
		if err := record(); err != nil {
			return fmt.Errorf("%w (tried without the %s: %v)", err, lockName, lockErr)
		}
		return fmt.Errorf("audit entry written without the %s (%v); a concurrent write may interleave with it",
			lockName, lockErr)
	}
	return dropErr
}

// drainAuditStdin reads the hook payload bytes from os.Stdin with the hook
// package's input timeout, so an open pipe without EOF (the shape Claude Code
// produces) does not block the process. It delegates to the hook package's
// reader, which accumulates reads until the buffer holds one complete JSON
// object, EOF arrives, or the timeout fires, so a payload larger than one 64KiB
// read (every Edit of a large file) is read in full rather than truncated.
// The timeout is the reader's own: Claude Code can pause longer than a second
// between the pieces of a large payload. On error the partial bytes are
// discarded; a payload that did not arrive whole is not parsed.
func drainAuditStdin() ([]byte, error) {
	data, err := hook.ReadFromFile(os.Stdin, hook.InputTimeout)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// auditInputDropped reports a stdin read failure on stderr and returns the
// error wrapping hook.ErrAuditDropped, so the session notice names the cause
// (too large, timed out) instead of an invalid-JSON parse of partial bytes.
func auditInputDropped(err error) error {
	fmt.Fprintf(os.Stderr, "ethos: audit-log: reading input: %v\n", err)
	return fmt.Errorf("%w: reading input: %w", hook.ErrAuditDropped, err)
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
