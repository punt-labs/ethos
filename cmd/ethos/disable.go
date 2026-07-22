package main

import (
	"fmt"

	"github.com/punt-labs/ethos/internal/enable"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/spf13/cobra"
)

var disableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable ethos in the current repo",
	Long: `Disable ethos in the current repo.

Removes the import line, deletes the enabled marker, and unchains the git
hooks. Non-destructive: the vendored guide and all config and audit data
stay on disk, dormant. Refuses when a sibling worktree is still enabled
(the git hooks are shared); pass --force to unchain anyway.`,
	GroupID:      "admin",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDisable(cmd)
	},
}

var disableForce bool

func init() {
	disableCmd.Flags().BoolVar(&disableForce, "force", false, "Unchain even when a sibling worktree is still enabled")
	rootCmd.AddCommand(disableCmd)
}

func runDisable(cmd *cobra.Command) error {
	repoRoot := resolve.FindRepoRoot()
	if repoRoot == "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "ethos: disable: not in a git repository")
		return failClosed{}
	}
	rep, err := enable.Disable(repoRoot, disableForce)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), rep)
	}
	printEnableReport(cmd.OutOrStdout(), cmd.ErrOrStderr(), rep, "disabled")
	return nil
}
