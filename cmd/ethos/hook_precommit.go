package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
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

// repairPreCommit runs the DES-084 cheap pass: it removes every path under
// .punt-labs/local/ from the committing index, leaving the files on disk, runs
// the cheap repair steps, and says how many paths it unstaged. A git failure
// while unstaging blocks the commit with git's message.
func repairPreCommit(ctx context.Context, errOut io.Writer) error {
	checkout, err := requireCheckout(errOut, "hook pre-commit")
	if err != nil {
		return err
	}
	line, err := autorepair.PreCommit(ctx, preCommitEnv(checkout))
	if err != nil {
		fmt.Fprintf(errOut, "ethos: hook pre-commit: %v\n", err)
		return failClosed{}
	}
	if line != "" {
		fmt.Fprintln(errOut, line)
	}
	return nil
}

// preCommitEnv builds the repair Env as the engine does: located roots, the
// repository policy read from them, the global root, and the repair log. A
// store that cannot be used is left empty, never replaced by the checkout, so
// the repair steps are skipped and the unstage of local paths still runs.
func preCommitEnv(checkout string) autorepair.Env {
	roots := currentRoots().Located
	roots.Checkout = checkout
	env := autorepair.Env{Roots: roots, Global: defaultGlobalRoot(), Now: time.Now().UTC()}
	if env.Global != "" {
		env.Log = autorepair.NewAppendLog(env.Global, roots.Store, checkout, version)
	}
	if err := roots.StoreFailure(); err != nil {
		env.Roots.Store = ""
		logPreCommit(env, "store not located", err)
		return env
	}
	policy, err := roots.Layout()
	if err != nil {
		env.Roots.Store = ""
		logPreCommit(env, "repository policy not read", err)
		return env
	}
	env.Policy = policy
	return env
}

func logPreCommit(env autorepair.Env, action string, err error) {
	if env.Log != nil {
		env.Log.Logf("pre-commit", action, nil, err)
	}
}

// runHookPreCommit unstages local data, seals pending live audit lines and then
// verifies staged provenance snapshots. Any of them failing blocks the commit
// with exit 2.
func runHookPreCommit(ctx context.Context, out, errOut io.Writer) error {
	if err := repairPreCommit(ctx, errOut); err != nil {
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
