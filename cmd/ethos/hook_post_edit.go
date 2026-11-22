package main

import (
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{
		Use:    "post-edit",
		Short:  "Advisory checks for an edited file",
		Hidden: true,
		Run: func(cmd *cobra.Command, _ []string) {
			agent, _ := cmd.Flags().GetString("agent")
			hook.PostEdit(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), agent)
		},
	}
	cmd.Flags().String("agent", "", "Agent handle for check-set resolution")
	hookCmd.AddCommand(cmd)
}
