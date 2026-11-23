package main

import (
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/spf13/cobra"
)

func init() {
	hookCmd.AddCommand(&cobra.Command{
		Use:    "pipe-input",
		Short:  "Forward a bounded hook payload into a pipe",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return hook.PipeInput(cmd.InOrStdin(), cmd.OutOrStdout())
		},
	})
}
