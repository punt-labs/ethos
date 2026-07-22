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
		return err
	}
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), rep)
	}
	printEnableReport(cmd.OutOrStdout(), cmd.ErrOrStderr(), rep, "enabled")
	return nil
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
