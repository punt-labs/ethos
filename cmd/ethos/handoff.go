package main

import (
	"encoding/json"
	"fmt"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/spf13/cobra"
)

func init() {
	handoff := &cobra.Command{Use: "handoff", Short: "Manage handoff gate dependencies", GroupID: "admin"}
	var agent, localFile string
	fetch := &cobra.Command{
		Use:   "fetch-runtime",
		Short: "Install the current platform's pinned handoff runtime archives",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := requireCheckout(cmd.ErrOrStderr(), "handoff fetch-runtime"); err != nil {
				return err
			}
			loc := currentRoots().Located
			var installed []string
			var err error
			if cmd.Flags().Changed("file") {
				installed, err = hook.InstallHandoffRuntimeFile(cmd.Context(), loc, agent, localFile)
			} else {
				installed, err = hook.FetchHandoffRuntimes(cmd.Context(), loc, agent)
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				return json.NewEncoder(out).Encode(installed)
			}
			for _, archive := range installed {
				if _, err := fmt.Fprintln(out, archive); err != nil {
					return err
				}
			}
			return nil
		},
	}
	fetch.Flags().StringVar(&agent, "agent", "", "Select the handoff policy for this agent instead of the project default")
	fetch.Flags().StringVar(&localFile, "file", "", "Install this local archive without downloading")
	var dataAgent, dataWorktree string
	refresh := &cobra.Command{
		Use:   "refresh-data",
		Short: "Refresh the handoff policy's read-only data snapshots",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := requireCheckout(cmd.ErrOrStderr(), "handoff refresh-data"); err != nil {
				return err
			}
			loc, err := hook.HandoffDataCheckout(currentRoots().Located, dataWorktree)
			if err != nil {
				return err
			}
			refreshed, err := hook.RefreshHandoffData(cmd.Context(), loc, dataAgent)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOutput {
				return json.NewEncoder(out).Encode(refreshed)
			}
			for _, name := range refreshed {
				if _, err := fmt.Fprintln(out, name); err != nil {
					return err
				}
			}
			return nil
		},
	}
	refresh.Flags().StringVar(&dataAgent, "agent", "", "Select the handoff policy for this agent instead of the project default")
	refresh.Flags().StringVar(&dataWorktree, "worktree", "", "Select primary or linked:<Git worktree ID> in the current repository")
	handoff.AddCommand(fetch, refresh)
	rootCmd.AddCommand(handoff)
}
