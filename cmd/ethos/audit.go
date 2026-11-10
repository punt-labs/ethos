package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/spf13/cobra"
)

// --- audit (bare command) ---

var auditCmd = &cobra.Command{
	Use:     "audit",
	Short:   "Inspect and migrate session audit logs",
	GroupID: "admin",
	Args:    cobra.NoArgs,
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

Unions the shared local archive, published chunks, supported legacy files and
live lines, independently of the current recording mode. Equal copies collapse;
divergent monotonic identities and unreadable history are visible errors.
Prints every entry whose delegation_id field matches --delegation.

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
	auditSealDryRun        bool
	auditSealVerbose       bool
	auditSealCommitContext bool
)

var auditSealCmd = &cobra.Command{
	Use:    "seal",
	Hidden: true,
	Short:  "Archive live audit lines locally; publish eligible full-mode copies",
	Long: `Archive live audit and mission event lines into immutable local chunks.

Retains every stream in the shared store root's .punt-labs/local/ethos/trace/.
Under record: blame (the default), performs no Git index query or mutation.
Under record: full, additionally copies and stages only entries whose full-mode
eligibility was captured when written. Missing legacy eligibility is private.
Publication failure retains the archive for a later full-mode retry.

The installed pre-commit hook passes --commit-context. Only in that context,
blame archival errors warn and permit a source commit while retaining live data.
Configuration errors and full-mode failures always block. Manual commands keep
visible errors in both modes.

Flags:
  --dry-run   print the per-session line counts it would seal, writing nothing
  --verbose   print one line per sealed session
  --commit-context  allow blame archival warnings during a source commit

Exit codes:
  0  archived, nothing pending, or a blame commit-context retained-data warning
  2  invalid policy, archival failure outside that exception, or full publication failure`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAuditSeal(cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	auditShowCmd.Flags().StringVar(&auditShowDelegation, "delegation", "",
		"Delegation id to filter on (required)")
	auditShowCmd.Flags().StringVar(&auditShowFormat, "format", "json",
		"Output format: json or text")
	_ = auditShowCmd.MarkFlagRequired("delegation")

	auditSealCmd.Flags().BoolVar(&auditSealDryRun, "dry-run", false,
		"Print the line counts it would seal without writing")
	auditSealCmd.Flags().BoolVar(&auditSealVerbose, "verbose", false,
		"Print one line per sealed session")
	auditSealCmd.Flags().BoolVar(&auditSealCommitContext, "commit-context", false,
		"Warn and allow a source commit on blame-mode archival failure; configuration and full-publication failures still block")

	auditCmd.AddCommand(auditShowCmd)
	auditCmd.AddCommand(auditSealCmd)
	rootCmd.AddCommand(auditCmd)
}

// sessionStartDateResolver returns a StartDate function for the seal that
// names a brand-new sealed directory by session start rather than wall clock
// (carried refinement (a)). It reads the roster's Started date first, then a
// purge tombstone's recorded StartDate; "" lets the seal fall back to the live
// file's first-line date, then now. Best-effort — a read error yields "".
func sessionStartDateResolver() func(string) string {
	ss := sessionStore()
	// When home cannot resolve, skip the tombstone lookup entirely rather than
	// let filepath.Join degrade to the relative ".punt-labs/ethos/sessions" —
	// a relative path would consult the current working directory, reading a
	// stray same-named tombstone from wherever the command happened to run.
	home, homeErr := os.UserHomeDir()
	return func(sessionID string) string {
		if roster, err := ss.Load(sessionID); err == nil && len(roster.Started) >= 10 {
			return roster.Started[:10]
		}
		if homeErr == nil {
			globalSessions := filepath.Join(home, ".punt-labs", "ethos", "sessions")
			if tb, err := audit.ReadTombstone(filepath.Join(globalSessions, sessionID+".purged")); err == nil {
				return tb.StartDate
			}
		}
		return ""
	}
}

// activeRepoSessions returns the ids of EVERY session whose roster is bound to
// this checkout's repo, including stale and crashed rosters — the second source
// the vacuum cross-check iterates. A roster's Repo is a git-remote identity
// (org/name), not a checkout path, so it is matched against this checkout's
// identity, not repoRoot. Stale sessions are returned deliberately: a dead
// session with unsealed live lines is exactly the loss case the vacuum must
// warn about, so filtering it out would blind the guard to the very condition
// it exists to catch. A single unreadable roster skips that session
// (best-effort, the doc-sanctioned case), but a List error empties the whole
// half of the cross-check, so it warns on stderr rather than silently
// returning nil. Each result carries the checkout its roster recorded, so the
// cross-check probes the live zone where it was written.
func activeRepoSessions(repoRoot string, errOut io.Writer) []hook.ActiveSession {
	repoID, err := audit.RepoIdentity(repoRoot)
	if err != nil {
		fmt.Fprintf(errOut, "ethos: audit seal: reading the repository identity for the vacuum cross-check: %v\n", err)
		return nil
	}
	ss := sessionStore()
	ids, err := ss.List()
	if err != nil {
		fmt.Fprintf(errOut, "ethos: audit seal: listing sessions for vacuum cross-check: %v\n", err)
		return nil
	}
	var out []hook.ActiveSession
	for _, id := range ids {
		roster, err := ss.Load(id)
		if err != nil {
			continue
		}
		if roster.RepoUnresolved != "" {
			// Its repository identity was never resolved: it cannot be
			// matched to this checkout, a no-origin one included. One that
			// recorded another checkout belongs to that checkout's commits, not
			// this one's, and is not mentioned here.
			if roster.Checkout != "" && !sameCheckout(roster.Checkout, repoRoot) {
				continue
			}
			fmt.Fprintf(errOut, "ethos: audit seal: vacuum cross-check skips session %s: its repository identity was not resolved when it started (%s)\n",
				id, roster.RepoUnresolved)
			continue
		}
		if roster.Repo == repoID {
			out = append(out, hook.ActiveSession{Session: id, Checkout: roster.Checkout})
		}
	}
	return out
}

// sameCheckout reports whether a and b are the same directory, by file
// identity (a symlinked spelling is the same checkout).
func sameCheckout(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// runAuditSeal is the audit-seal command implementation. It seals every
// repository stream locally. Full mode adds eligible publication copies. Manual
// failures exit 2; only blame commit-context archival errors may warn/proceed.
func runAuditSeal(out, errOut io.Writer) error {
	return sealRepository(out, errOut, sealFlags{
		dryRun:        auditSealDryRun,
		verbose:       auditSealVerbose,
		commitContext: auditSealCommitContext,
	})
}

// sealFlags are the options of one seal run. The audit seal command fills them
// from its flags; the pre-commit hook passes commitContext alone.
type sealFlags struct {
	dryRun, verbose, commitContext bool
}

// sealRepository seals every repository stream under the given options.
func sealRepository(out, errOut io.Writer, f sealFlags) error {
	// The seal is classed as handling its own errors, so nothing refused it
	// before: it applies the full refusal itself — the store, its
	// .punt-labs/ethos path and config, as well as the checkout. A broken store
	// blocks the commit (exit 2) rather than sealing against a guess.
	// An ETHOS_REPO_ROOT naming a repository with no store yet is not a broken
	// store: there is nothing there to seal against, and enable creates it.
	roots := currentRoots()
	if err := roots.StoreFailure(); err != nil && !errors.Is(err, resolve.ErrOverrideNoStore) {
		fmt.Fprintf(errOut, "ethos: audit seal: %v\n", err)
		return failClosed{}
	}
	repoRoot, err := requireCheckout(errOut, "audit seal")
	if err != nil {
		return err
	}
	p, err := roots.Layout()
	if err != nil {
		fmt.Fprintf(errOut, "ethos: audit seal: invalid repository policy: %v\n", err)
		return failClosed{}
	}
	opts := hook.SealOptions{
		DryRun:    f.dryRun,
		Verbose:   f.verbose,
		Out:       out,
		StartDate: sessionStartDateResolver(),
	}
	res, err := hook.SealRepoWithPolicy(p, time.Now().UTC(), opts)
	if err != nil {
		fmt.Fprintf(errOut, "ethos: audit seal: %v\n", err)
		// Captured full-consent publication is not an optional archival warning,
		// even after switching to blame or when joined with an archive error.
		var promptRetry *hook.PromptPublicationRetryError
		if f.commitContext && p.RecordMode() == layout.RecordBlame && !errors.As(err, &promptRetry) {
			fmt.Fprintln(errOut, "ethos: audit seal: retained-data warning: local archival incomplete; live data retained; allowing the source commit")
			return nil
		}
		return failClosed{}
	}
	if res.Deferred {
		return nil
	}
	// Vacuum cross-check after EVERY non-dry-run seal, not only the no-op path.
	// A flagged tombstone's warning must repeat at every commit until the
	// operator acks it (DES-058 §Seal failure policy); gating on a total no-op
	// skipped it whenever anything sealed — i.e. on nearly every real commit,
	// precisely when the repo is busiest. Warn-only; never blocks the commit.
	if !f.dryRun {
		if home, hErr := os.UserHomeDir(); hErr == nil {
			globalRoot := filepath.Join(home, ".punt-labs", "ethos")
			if vErr := hook.VacuumCrossCheckWithPolicy(p, globalRoot, activeRepoSessions(repoRoot, errOut), errOut); vErr != nil {
				fmt.Fprintf(errOut, "ethos: audit seal: vacuum cross-check: %v\n", vErr)
			}
		} else {
			fmt.Fprintf(errOut, "ethos: audit seal: skipping vacuum cross-check: cannot resolve home dir: %v\n", hErr)
		}
	}
	if f.dryRun {
		fmt.Fprintf(out, "audit seal: dry-run complete (%d line(s) pending)\n", res.LinesSealed)
		return nil
	}
	if f.verbose {
		fmt.Fprintf(out,
			"audit seal: sealed %d line(s) across %d session(s), staged %d chunk(s)\n",
			res.LinesSealed, res.SessionsSealed, res.ChunksStaged)
	}
	return nil
}

// runAuditShow is the audit-show command implementation. Resolves
// repoRoot via the standard ancestor walk and the global sessions dir
// under ~/.punt-labs/ethos/sessions, queries hook.QueryAuditByDelegation
// for matching entries, then renders them as JSONL or as a one-line
// text summary per entry.
func runAuditShow(out, errOut io.Writer) error {
	_, err := requireCheckout(errOut, "audit show")
	if err != nil {
		return err
	}
	p, err := currentRoots().Layout()
	if err != nil {
		return fmt.Errorf("audit show: %w", err)
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

	entries, err := hook.QueryAuditByDelegationWithPolicy(p, globalDir, auditShowDelegation)
	if err != nil {
		return fmt.Errorf("audit show: %w", err)
	}

	// DES-058 read-time diagnostics: quarantine gaps (lines lost to
	// corruption) and gitlink-deferred sessions (live lines past the
	// watermark not yet sealed). Flagged on stderr so the delegation-filtered
	// stream on stdout stays a valid audit-log fragment.
	if diag, dErr := hook.CollectAuditDiagnosticsWithPolicy(p); dErr == nil {
		diag.WriteDiagnostics(errOut)
	} else {
		return fmt.Errorf("audit show: diagnostics: %w", dErr)
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
