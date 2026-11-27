package main

import (
	"fmt"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(&cobra.Command{
		Use:          "status",
		Short:        "Show whether ethos is enabled in the current checkout",
		GroupID:      "admin",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         runStatus,
	})
}

func runStatus(cmd *cobra.Command, _ []string) error {
	checkout := locateForCommand(cmd, "status")
	if checkout == "" {
		return failClosed{}
	}
	state, err := resolve.EffectiveEnablement(checkout)
	if err != nil {
		return fmt.Errorf("determining enablement: %w", err)
	}
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), state)
	}
	if state.DecidingFile == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "ethos is off: no enablement marker")
	} else if state.Enabled {
		fmt.Fprintf(cmd.OutOrStdout(), "ethos is on: %s\n", state.DecidingFile)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "ethos is off: %s\n", state.DecidingFile)
	}
	return nil
}
