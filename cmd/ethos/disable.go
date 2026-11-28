package main

import (
	"github.com/punt-labs/ethos/v4/internal/enable"
	"github.com/spf13/cobra"
)

var disableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable ethos in the current repo",
	Long: `Disable ethos in the current checkout (the default).

Use --scope project to remove the shared integration and unchain git hooks.
Add --opt-out to record that the project must stay off.
The vendored guide, configuration, and audit data stay on disk.
Project disable refuses to unchain hooks while sibling worktrees use them
unless --force is specified. Without an explicit --scope, --force uses
project scope.`,
	GroupID:      "admin",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDisable(cmd)
	},
}

var disableForce bool

func init() {
	disableCmd.Flags().String("scope", "local", "Enablement scope: project or local")
	disableCmd.Flags().Bool("opt-out", false, "Record a committed project opt-out")
	disableCmd.Flags().BoolVar(&disableForce, "force", false, "Unchain even when a sibling worktree is still enabled")
	rootCmd.AddCommand(disableCmd)
}

func runDisable(cmd *cobra.Command) error {
	repoRoot := locateForCommand(cmd, "disable")
	if repoRoot == "" {
		return failClosed{}
	}
	scope, err := enableScope(cmd)
	if err != nil {
		return err
	}
	if disableForce && !cmd.Flags().Changed("scope") {
		scope = "project"
	}
	optOut, err := projectEnableFlag(cmd, scope, "opt-out")
	if err != nil {
		return err
	}
	if scope == "local" {
		rep, err := enable.DisableLocal(repoRoot)
		if err != nil {
			emitPartialReport(cmd, rep)
			return err
		}
		return finishEnable(cmd, rep, "disabled")
	}
	storeRoot, err := enableStoreRoot(repoRoot, func() (string, error) {
		r := currentRoots()
		if r.StoreErr != nil {
			return "", r.StoreErr
		}
		return r.Store, r.EthosErr
	})
	if err != nil {
		return err
	}
	rep, err := enable.DisableTo(repoRoot, storeRoot, disableForce, optOut)
	if err != nil {
		emitPartialReport(cmd, rep)
		return err
	}
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), rep)
	}
	printEnableReport(cmd.OutOrStdout(), cmd.ErrOrStderr(), rep, "disabled")
	return nil
}
