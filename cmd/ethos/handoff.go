package main

import (
	"encoding/json"
	"fmt"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/spf13/cobra"
)

func init() {
	handoff := &cobra.Command{Use: "handoff", Short: "Manage handoff gate dependencies", GroupID: "admin"}
	var agent string
	fetch := &cobra.Command{
		Use:   "fetch-runtime",
		Short: "Install the current platform's pinned handoff runtime archives",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := requireCheckout(cmd.ErrOrStderr(), "handoff fetch-runtime"); err != nil {
				return err
			}
			installed, err := hook.FetchHandoffRuntimes(cmd.Context(), currentRoots().Located, agent)
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
	handoff.AddCommand(fetch)
	rootCmd.AddCommand(handoff)
}
