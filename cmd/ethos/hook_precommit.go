package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

func init() {
	hookCmd.AddCommand(&cobra.Command{
		Use:   "pre-commit",
		Short: "git pre-commit policy: seal audit lines, verify staged provenance",
		Long: `Run the commit-time policy the installed pre-commit shim delegates to:
audit seal in commit context, then verification of the provenance snapshots
staged in the actual committing index. Before sealing, every path under
.punt-labs/local/ is removed from the committing index (files stay on disk).

Exit codes:
  0  sealed, nothing pending, or a blame-mode retained-data warning
  2  invalid policy, archival failure outside that exception, full-mode
     publication failure, a staged snapshot that does not verify, or a git
     failure while unstaging local paths`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHookPreCommit(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	})
}

// localTreePrefix is the repository-relative directory of machine-local ethos
// data. None of it belongs in a commit, however it got staged.
const localTreePrefix = ".punt-labs/local/"

// unstageLocal removes every staged or HEAD-tracked path under localTreePrefix
// from the committing index, leaving the files on disk, and says how many it
// unstaged. A git failure blocks the commit with git's message.
func unstageLocal(errOut io.Writer) error {
	checkout, err := requireCheckout(errOut, "hook pre-commit")
	if err != nil {
		return err
	}
	env, err := gitenv.ActualIndexEnv()
	if err != nil {
		fmt.Fprintf(errOut, "ethos: hook pre-commit: %v\n", err)
		return failClosed{}
	}
	n, err := gitenv.UnstageUnder(checkout, env, localTreePrefix)
	if err != nil {
		fmt.Fprintf(errOut, "ethos: hook pre-commit: %v\n", err)
		return failClosed{}
	}
	if n > 0 {
		fmt.Fprintf(errOut, "ethos: hook pre-commit: unstaged %d path(s) under %s; they stay on disk\n", n, localTreePrefix)
	}
	return nil
}

// runHookPreCommit unstages local data, seals pending live audit lines and then
// verifies staged provenance snapshots. Any of them failing blocks the commit
// with exit 2.
func runHookPreCommit(ctx context.Context, out, errOut io.Writer) error {
	if err := unstageLocal(errOut); err != nil {
		return err
	}
	if err := sealRepository(out, errOut, sealFlags{commitContext: true}); err != nil {
		return err
	}
	if _, err := verifyStagedIndex(ctx); err != nil {
		fmt.Fprintf(errOut, "ethos: provenance verify-index: %v\n", err)
		return failClosed{}
	}
	return nil
}
