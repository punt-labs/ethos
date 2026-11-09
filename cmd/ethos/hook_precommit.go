package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func init() {
	hookCmd.AddCommand(&cobra.Command{
		Use:   "pre-commit",
		Short: "git pre-commit policy: seal audit lines, verify staged provenance",
		Long: `Run the commit-time policy the installed pre-commit shim delegates to:
audit seal in commit context, then verification of the provenance snapshots
staged in the actual committing index.

Exit codes:
  0  sealed, nothing pending, or a blame-mode retained-data warning
  2  invalid policy, archival failure outside that exception, full-mode
     publication failure, or a staged snapshot that does not verify`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHookPreCommit(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	})
}

// runHookPreCommit seals pending live audit lines and then verifies staged
// provenance snapshots. Either failing blocks the commit with exit 2.
func runHookPreCommit(ctx context.Context, out, errOut io.Writer) error {
	if err := sealRepository(out, errOut, sealFlags{commitContext: true}); err != nil {
		return err
	}
	if _, err := verifyStagedIndex(ctx); err != nil {
		fmt.Fprintf(errOut, "ethos: provenance verify-index: %v\n", err)
		return failClosed{}
	}
	return nil
}
