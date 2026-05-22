package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
<repo>/.ethos/sessions/<YYYY-MM-DD>-<id>/audit.jsonl, then deletes the
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
  2  must run inside a repo (no <repo>/.ethos/sessions/ destination)`,
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

Walks <repo>/.ethos/sessions/<date>-<id>/audit.jsonl and falls back
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

	auditCmd.AddCommand(auditMigrateCmd)
	auditCmd.AddCommand(auditShowCmd)
	rootCmd.AddCommand(auditCmd)
}

// runAuditMigrate is the audit-migrate command implementation.
// Resolves repoRoot via the standard ancestor walk and the global
// sessions directory under ~/.punt-labs/ethos/sessions. Surfaces
// "must run inside a repo" with exit code 2 when no repo root can be
// found.
func runAuditMigrate(out, errOut io.Writer) error {
	repoRoot := resolve.FindRepoRoot()
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
	// the same writer we print to. In non-verbose mode we discard
	// the routine "skip"/"noop" lines but still let "nothing to
	// migrate" through so the operator sees something on stdout.
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
	repoRoot := resolve.FindRepoRoot()
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
