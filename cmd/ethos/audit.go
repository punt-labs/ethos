package main

import (
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

func init() {
	auditMigrateCmd.Flags().BoolVar(&auditMigrateDryRun, "dry-run", false,
		"Enumerate what would migrate without making changes")
	auditMigrateCmd.Flags().BoolVar(&auditMigrateVerbose, "verbose", false,
		"Print per-session decisions to stdout")

	auditCmd.AddCommand(auditMigrateCmd)
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
