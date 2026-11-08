package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/punt-labs/ethos/v4/internal/hook"
)

func init() {
	hookCmd.AddCommand(&cobra.Command{
		Use:   "commit-message <message-file>",
		Short: "Atomically add commit trailers using canonical local staging",
		Args:  cobra.ExactArgs(1),
		RunE:  runHookCommitMessage,
	})
}

func runHookCommitMessage(_ *cobra.Command, args []string) error {
	// Consume the established request snapshot once. In particular, an enabled
	// repository with broken configuration must never get a global fallback.
	r := currentRoots()
	p, err := r.Located.Layout()
	if err != nil {
		return fmt.Errorf("commit-message: locating repository layout: %w", err)
	}
	if err := hook.ApplyCommitMessage(p, args[0], os.Getenv("MISSION_ID"), os.Getenv("DELEGATION_ID")); err != nil {
		return fmt.Errorf("commit-message: %w", err)
	}
	return nil
}
