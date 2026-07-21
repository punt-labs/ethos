package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
	"github.com/punt-labs/ethos/internal/hook"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/spf13/cobra"
)

// --- audit (bare command) ---

var auditCmd = &cobra.Command{
	Use:     "audit",
	Short:   "Inspect and migrate session audit logs",
	GroupID: "admin",
	Args:    cobra.NoArgs,
}

// --- audit migrate ---

var (
	auditMigrateDryRun  bool
	auditMigrateVerbose bool
)

var auditMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate legacy global audit logs into the repo-tree layout",
	Long: `Migrate legacy global audit logs into the repo-tree layout.

Scans ~/.punt-labs/ethos/sessions/*.audit.jsonl (the v3.11 layout) and
copies each file's entries into the v3.12+ repo-tree layout under
<repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<id>/audit.jsonl, then deletes the
legacy file once every entry has landed and been fsynced.

Idempotent: a second run on the same machine is a no-op. Cross-repo
safe: a legacy session whose id has no matching repo-tree directory is
left in place — it belongs to a different repo's work tree.

Flags:
  --dry-run   show what would change without writing or deleting
  --verbose   print one decision line per session to stdout

Exit codes:
  0  migration completed (including the no-op "nothing to migrate" case)
  1  one or more sessions failed mid-copy; both sources stayed in place
  2  must run inside a repo (no <repo>/.punt-labs/ethos/sessions/ destination)`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAuditMigrate(cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

// --- audit show ---

var (
	auditShowDelegation string
	auditShowFormat     string
)

var auditShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show audit entries that belong to one delegation",
	Long: `Show audit entries that belong to one delegation.

Walks <repo>/.punt-labs/ethos/sessions/<date>-<id>/audit.jsonl and falls back
to ~/.punt-labs/ethos/sessions/<id>.audit.jsonl for sessions with no
repo-tree counterpart. Prints every entry whose delegation_id field
matches --delegation.

Flags:
  --delegation <id>   delegation id to filter on (required)
  --format <fmt>      "json" (one JSONL line per entry, default) or
                      "text" (ts<TAB>tool<TAB>file_path-or-preview)

Exit codes:
  0  success (including zero matching entries)
  2  must run inside a repo`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAuditShow(cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

// --- audit seal ---

var (
	auditSealDryRun  bool
	auditSealVerbose bool
)

var auditSealCmd = &cobra.Command{
	Use:   "seal",
	Short: "Seal pending live audit lines into immutable tracked chunks",
	Long: `Seal pending live audit lines into immutable tracked chunks.

Visits every session in the repo, copies each session's not-yet-sealed
live lines (ts past the sealed watermark) into a new immutable chunk
audit-<first>-<last>.jsonl under the session's dated sealed directory,
and git-adds every untracked chunk it finds — recovering an orphan a
prior crashed seal left behind. Between seals no tracked file changes, so
the repo tree stays clean while a session is live.

Called by the pre-commit hook so sealed records land in the same commit
as the work they document, and by ethos mission close.

Flags:
  --dry-run   print the per-session line counts it would seal, writing nothing
  --verbose   print one line per sealed session

Exit codes:
  0  sealed, nothing pending, or a gitlink-mounted no-op (deferral notice on stderr)
  2  fail-closed: an I/O error, a malformed chunk name, a corrupt sealed
     chunk, or a git-add failure — blocks the commit; the escape is
     ethos audit quarantine, never git commit --no-verify`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAuditSeal(cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

// --- audit quarantine ---

var auditQuarantineReason string

var auditQuarantineCmd = &cobra.Command{
	Use:   "quarantine <chunk-path>",
	Short: "Retire a corrupt sealed chunk and recover what the live file holds",
	Long: `Retire a corrupt sealed chunk, the sanctioned alternative to
git commit --no-verify when a seal or read fails on a corrupt chunk.

Given the chunk path the seal/read error named, it retires the chunk to
<name>.jsonl.corrupt (committed as evidence), re-seals every line of the
chunk's range the live file still holds into a fresh content-named chunk,
writes a deterministic .quarantine marker recording the verified loss
point and any unrecovered sub-range, and stages all three — so the tree
is committable without --no-verify and the loss becomes a visible audit
event and a read-time gap marker, never a silent skip.

Idempotent and crash-resumable: re-running after a mid-verb crash
completes the quarantine from whichever artifacts exist.

Exit codes:
  0  quarantined (or already quarantined — idempotent no-op)
  2  must run inside a repo, or the path is not a recognizable chunk`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAuditQuarantine(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0])
	},
}

func init() {
	auditMigrateCmd.Flags().BoolVar(&auditMigrateDryRun, "dry-run", false,
		"Enumerate what would migrate without making changes")
	auditMigrateCmd.Flags().BoolVar(&auditMigrateVerbose, "verbose", false,
		"Print per-session decisions to stdout")

	auditShowCmd.Flags().StringVar(&auditShowDelegation, "delegation", "",
		"Delegation id to filter on (required)")
	auditShowCmd.Flags().StringVar(&auditShowFormat, "format", "json",
		"Output format: json or text")
	_ = auditShowCmd.MarkFlagRequired("delegation")

	auditSealCmd.Flags().BoolVar(&auditSealDryRun, "dry-run", false,
		"Print the line counts it would seal without writing")
	auditSealCmd.Flags().BoolVar(&auditSealVerbose, "verbose", false,
		"Print one line per sealed session")

	auditQuarantineCmd.Flags().StringVar(&auditQuarantineReason, "reason", "corrupt sealed chunk",
		"Corruption reason recorded in the quarantine marker")

	auditCmd.AddCommand(auditMigrateCmd)
	auditCmd.AddCommand(auditShowCmd)
	auditCmd.AddCommand(auditSealCmd)
	auditCmd.AddCommand(auditQuarantineCmd)
	rootCmd.AddCommand(auditCmd)
}

// sessionStartDateResolver returns a StartDate function for the seal that
// names a brand-new sealed directory by session start rather than wall clock
// (carried refinement (a)). It reads the roster's Started date first, then a
// purge tombstone's recorded StartDate; "" lets the seal fall back to the live
// file's first-line date, then now. Best-effort — a read error yields "".
func sessionStartDateResolver(repoRoot string) func(string) string {
	ss := sessionStore()
	home, _ := os.UserHomeDir()
	globalSessions := filepath.Join(home, ".punt-labs", "ethos", "sessions")
	return func(sessionID string) string {
		if roster, err := ss.Load(sessionID); err == nil && len(roster.Started) >= 10 {
			return roster.Started[:10]
		}
		if tb, err := audit.ReadTombstone(filepath.Join(globalSessions, sessionID+".purged")); err == nil {
			return tb.StartDate
		}
		return ""
	}
}

// activeRepoSessions returns the ids of roster-active sessions bound to
// repoRoot — the second source the vacuum cross-check iterates. Best-effort:
// a roster read error skips that session rather than failing the seal.
func activeRepoSessions(repoRoot string) []string {
	ss := sessionStore()
	ids, err := ss.List()
	if err != nil {
		return nil
	}
	var out []string
	for _, id := range ids {
		roster, err := ss.Load(id)
		if err != nil {
			continue
		}
		if roster.Repo == repoRoot {
			out = append(out, id)
		}
	}
	return out
}

// runAuditQuarantine is the audit-quarantine command implementation. It
// retires a corrupt chunk named by chunkPath and recovers what the live file
// holds, printing a one-line summary of the marker it wrote.
func runAuditQuarantine(out, errOut io.Writer, chunkPath string) error {
	repoRoot := resolve.EnvRepoRoot()
	if repoRoot == "" {
		fmt.Fprintln(errOut, "ethos: audit quarantine must run inside a repo")
		return usageError{}
	}
	// Resolve a relative path against the repo root so the operator can pass
	// the path exactly as the seal/read error printed it.
	if !filepath.IsAbs(chunkPath) {
		chunkPath = filepath.Join(repoRoot, chunkPath)
	}
	marker, err := hook.QuarantineChunk(repoRoot, chunkPath, auditQuarantineReason)
	if err != nil {
		fmt.Fprintf(errOut, "ethos: audit quarantine: %v\n", err)
		return failClosed{}
	}
	if marker.HasGap() {
		fmt.Fprintf(out,
			"quarantined %s: recovered through ts %d, lost [%d,%d] — staged, commit to record\n",
			marker.Chunk, marker.VerifiedLast, marker.UnrecoveredFirst, marker.UnrecoveredLast)
	} else {
		fmt.Fprintf(out,
			"quarantined %s: fully recovered through ts %d — staged, commit to record\n",
			marker.Chunk, marker.VerifiedLast)
	}
	return nil
}

// runAuditSeal is the audit-seal command implementation. It seals every
// repo session's pending live lines and stages the chunks. Fail-closed
// (DES-055 shape): on any seal error it prints a self-contained message to
// stderr and returns failClosed so the process exits 2, blocking the
// commit. A gitlink-mounted no-op and a nothing-to-seal run both exit 0.
func runAuditSeal(out, errOut io.Writer) error {
	repoRoot := resolve.EnvRepoRoot()
	if repoRoot == "" {
		fmt.Fprintln(errOut, "ethos: audit seal must run inside a repo")
		return usageError{}
	}
	opts := hook.SealOptions{
		DryRun:    auditSealDryRun,
		Verbose:   auditSealVerbose,
		Out:       out,
		StartDate: sessionStartDateResolver(repoRoot),
	}
	res, err := hook.SealRepo(repoRoot, time.Now().UTC(), opts)
	if err != nil {
		fmt.Fprintf(errOut, "ethos: audit seal: %v\n", err)
		return failClosed{}
	}
	if res.Deferred {
		return nil
	}
	// Vacuum cross-check on the no-op path: a seal that touched nothing must
	// still notice a session whose unsealed lines were lost (DES-058 §Seal
	// failure policy). Warnings only — never blocks the commit.
	if !auditSealDryRun && res.SessionsSealed == 0 && res.ChunksStaged == 0 {
		if home, hErr := os.UserHomeDir(); hErr == nil {
			globalRoot := filepath.Join(home, ".punt-labs", "ethos")
			if vErr := hook.VacuumCrossCheck(repoRoot, globalRoot, activeRepoSessions(repoRoot), errOut); vErr != nil {
				fmt.Fprintf(errOut, "ethos: audit seal: vacuum cross-check: %v\n", vErr)
			}
		}
	}
	if auditSealDryRun {
		fmt.Fprintf(out, "audit seal: dry-run complete (%d line(s) pending)\n", res.LinesSealed)
		return nil
	}
	if auditSealVerbose {
		fmt.Fprintf(out,
			"audit seal: sealed %d line(s) across %d session(s), staged %d chunk(s)\n",
			res.LinesSealed, res.SessionsSealed, res.ChunksStaged)
	}
	return nil
}

// runAuditMigrate is the audit-migrate command implementation.
// Resolves repoRoot via the standard ancestor walk and the global
// sessions directory under ~/.punt-labs/ethos/sessions. Surfaces
// "must run inside a repo" with exit code 2 when no repo root can be
// found.
func runAuditMigrate(out, errOut io.Writer) error {
	repoRoot := resolve.EnvRepoRoot()
	if repoRoot == "" {
		fmt.Fprintln(errOut, "ethos: audit migrate must run inside a repo")
		return usageError{}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	globalDir := filepath.Join(home, ".punt-labs", "ethos", "sessions")

	// In verbose mode the library writes per-session decisions to
	// the same writer we print to. In non-verbose mode every library
	// line is discarded; the operator sees only the final
	// "dry-run complete" or "complete" summary printed by this
	// command (Copilot on PR #328 — comment previously promised
	// "still let nothing-to-migrate through" but sink=io.Discard
	// drops that too; the summary line below covers the empty case).
	sink := out
	if !auditMigrateVerbose {
		sink = io.Discard
	}
	if err := hook.MigrateAudit(globalDir, repoRoot, auditMigrateDryRun, sink); err != nil {
		return fmt.Errorf("audit migrate: %w", err)
	}
	if !auditMigrateVerbose {
		// Quiet mode: one terminal status line so the operator knows
		// the command did something. Dry-run gets a distinct line so
		// nobody mistakes a dry-run for a real migration.
		if auditMigrateDryRun {
			fmt.Fprintln(out, "audit migrate: dry-run complete")
		} else {
			fmt.Fprintln(out, "audit migrate: complete")
		}
	}
	return nil
}

// runAuditShow is the audit-show command implementation. Resolves
// repoRoot via the standard ancestor walk and the global sessions dir
// under ~/.punt-labs/ethos/sessions, queries hook.QueryAuditByDelegation
// for matching entries, then renders them as JSONL or as a one-line
// text summary per entry.
func runAuditShow(out, errOut io.Writer) error {
	repoRoot := resolve.EnvRepoRoot()
	if repoRoot == "" {
		fmt.Fprintln(errOut, "ethos: audit show must run inside a repo")
		return usageError{}
	}
	switch auditShowFormat {
	case "json", "text":
	default:
		fmt.Fprintf(errOut, "ethos: --format must be json or text, got %q\n", auditShowFormat)
		return usageError{}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	globalDir := filepath.Join(home, ".punt-labs", "ethos", "sessions")

	entries, err := hook.QueryAuditByDelegation(repoRoot, globalDir, auditShowDelegation)
	if err != nil {
		return fmt.Errorf("audit show: %w", err)
	}

	// DES-058 read-time diagnostics: quarantine gaps (lines lost to
	// corruption) and gitlink-deferred sessions (live lines past the
	// watermark not yet sealed). Flagged on stderr so the delegation-filtered
	// stream on stdout stays a valid audit-log fragment.
	if diag, dErr := hook.CollectAuditDiagnostics(repoRoot, time.Now().UTC()); dErr == nil {
		diag.WriteDiagnostics(errOut)
	} else {
		fmt.Fprintf(errOut, "ethos: audit show: diagnostics: %v\n", dErr)
	}

	if auditShowFormat == "json" {
		return renderAuditJSONL(out, entries)
	}
	return renderAuditText(out, entries)
}

// renderAuditJSONL writes one JSON object per line, matching the
// on-disk shape so `ethos audit show` output is itself a valid audit
// log fragment.
func renderAuditJSONL(out io.Writer, entries []hook.AuditView) error {
	enc := json.NewEncoder(out)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return fmt.Errorf("encoding entry: %w", err)
		}
	}
	return nil
}

// renderAuditText writes one line per entry in the form
// "<ts>\t<tool>\t<file_path-or-preview>". The third column prefers the
// tool_input.file_path field (the common case for Read/Edit/Write);
// otherwise it falls back to tool_input_preview.
func renderAuditText(out io.Writer, entries []hook.AuditView) error {
	for _, e := range entries {
		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", e.Ts, e.Tool, e.Summary()); err != nil {
			return fmt.Errorf("writing entry: %w", err)
		}
	}
	return nil
}
