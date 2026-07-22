package main

import (
	"fmt"
	"io"

	"github.com/punt-labs/ethos/internal/enable"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/spf13/cobra"
)

var enableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable ethos in the current repo",
	Long: `Enable ethos in the current repo.

Deposits the vendored agent guide and its manifest, writes the enabled
marker, adds the @.punt-labs/ethos/CLAUDE.md import line to the repo
CLAUDE.md, and chains the audit-seal and trailer git hooks. Idempotent —
re-running is the upgrade path. Run 'ethos setup' separately to configure
identities.`,
	GroupID:      "admin",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEnable(cmd)
	},
}

func init() {
	rootCmd.AddCommand(enableCmd)
}

func runEnable(cmd *cobra.Command) error {
	repoRoot := resolve.FindRepoRoot()
	if repoRoot == "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "ethos: enable: not in a git repository")
		return failClosed{}
	}
	rep, err := enable.Enable(repoRoot)
	if err != nil {
		emitPartialReport(cmd, rep)
		return err
	}
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), rep)
	}
	printEnableReport(cmd.OutOrStdout(), cmd.ErrOrStderr(), rep, "enabled")
	return nil
}

// emitPartialReport surfaces the completed steps and warnings to stderr on the
// error path, so a warning already produced (e.g. deposit's grandfather
// overwrite) is not lost when a later step fails. In --json mode it writes the
// partial report as JSON to stderr so machine consumers keep it too.
func emitPartialReport(cmd *cobra.Command, rep *enable.Report) {
	if rep == nil {
		return
	}
	errOut := cmd.ErrOrStderr()
	if jsonOutput {
		_ = writeJSON(errOut, rep)
		return
	}
	for _, s := range rep.Steps {
		fmt.Fprintf(errOut, "  %-14s %-9s %s\n", s.Step, s.Status, s.Detail)
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(errOut, "ethos: %s\n", w)
	}
}

// printEnableReport writes the per-step outcome to out and any warnings to
// errOut. The verb is "enabled" or "disabled".
func printEnableReport(out, errOut io.Writer, rep *enable.Report, verb string) {
	fmt.Fprintf(out, "ethos %s in %s\n", verb, rep.RepoRoot)
	for _, s := range rep.Steps {
		if s.Detail != "" {
			fmt.Fprintf(out, "  %-14s %-9s %s\n", s.Step, s.Status, s.Detail)
		} else {
			fmt.Fprintf(out, "  %-14s %s\n", s.Step, s.Status)
		}
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(errOut, "ethos: %s\n", w)
	}
	if rep.Hint != "" {
		fmt.Fprintf(out, "\nnext: %s\n", rep.Hint)
	}
}
