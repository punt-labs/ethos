package main

import (
	"context"

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
			runPostEdit(cmd.Context(), func(ctx context.Context) {
				hook.PostEdit(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), agent)
			})
		},
	}
	cmd.Flags().String("agent", "", "Agent handle for check-set resolution")
	hookCmd.AddCommand(cmd)
}
