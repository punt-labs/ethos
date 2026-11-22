package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/process"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

func init() {
	hookCmd.AddCommand(&cobra.Command{
		Use:   "pre-commit",
		Short: "git pre-commit policy: seal audit lines, verify staged provenance",
		Long: `Run the commit-time policy the installed pre-commit shim delegates to:
audit seal in commit context, then verification of the provenance snapshots
staged in the actual committing index, then configured commit-stage checks.
Before sealing, every path under
.punt-labs/local/ is removed from the committing index (files stay on disk).
With ~/.punt-hooks-kill, no writes or checks run, but an index containing private
local paths, or an index that cannot be inspected, still refuses the commit.

Exit codes:
  0  sealed, nothing pending, or a blame-mode retained-data warning
  2  invalid policy, archival failure outside that exception, full-mode
     publication failure, a staged snapshot that does not verify, or a git
     failure while unstaging local paths, or a blocking configured check`,
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
	storeErr := roots.StoreFailure()
	policy, layoutErr := roots.Layout()
	store := roots.Store
	action, cause := "", error(nil)
	switch {
	case storeErr != nil:
		action, cause = "store not located", storeErr
	case layoutErr != nil:
		action, cause = "repository policy not read", layoutErr
	}
	if cause != nil {
		store = ""
		roots.Store = ""
	}
	env := autorepair.Env{Roots: roots, Global: defaultGlobalRoot(), Now: time.Now().UTC()}
	if env.Global != "" {
		env.Log = autorepair.NewAppendLog(env.Global, store, checkout, version)
	}
	if cause != nil {
		logPreCommit(env, action, cause)
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
	if hook.Killed() {
		return killedPreCommit(ctx, errOut)
	}
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
	checkout, err := requireCheckout(errOut, "hook pre-commit")
	if err != nil {
		return err
	}
	feedback := hook.RunStageChecks(ctx, checkout, committingAgent(), "commit")
	if feedback.Advisory != "" {
		fmt.Fprintln(errOut, feedback.Advisory)
	}
	if feedback.Blocking != "" {
		fmt.Fprintln(errOut, feedback.Blocking)
		return failClosed{}
	}
	return nil
}

func killedPreCommit(ctx context.Context, errOut io.Writer) error {
	paths, err := privateIndexPaths(ctx)
	if err != nil {
		fmt.Fprintf(errOut, "ethos: hook pre-commit: cannot inspect committing index with kill switch: %v\n", err)
		return failClosed{}
	}
	for _, path := range paths {
		literal := shellQuote(":(top,literal)" + path)
		fmt.Fprintf(errOut, "ethos: %q is private local data; kill switch leaves the index unchanged. Remove only from the index (files stay on disk) with git rm --cached -r -- %s\n", path, literal) // msgguard: allow operator-selected kill switch forbids automatic index changes
	}
	if len(paths) > 0 {
		return failClosed{}
	}
	return nil
}

func privateIndexPaths(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	checkout, err := resolve.CheckoutOf(cwd)
	if err != nil {
		return nil, err
	}
	if checkout == "" {
		return nil, resolve.ErrNotInRepo
	}
	env, err := gitenv.ActualIndexEnv()
	if err != nil {
		return nil, err
	}
	limit := gitenv.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		limit = min(limit, time.Until(deadline))
		if limit <= 0 {
			return nil, context.DeadlineExceeded
		}
	}
	data, _, err := gitenv.Read(gitenv.Spec{Git: "git", Dir: checkout, Env: append(env, "GIT_OPTIONAL_LOCKS=0"), Args: []string{"ls-files", "--cached", "-z", "--", layout.LocalPrefix}, Limit: limit}, 0)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, path := range strings.Split(string(data), "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func committingAgent() string {
	ss := sessionStore()
	id, _, err := resolve.SessionID(ss)
	if err != nil {
		return ""
	}
	roster, err := ss.Load(id)
	if err != nil {
		return ""
	}
	agent := os.Getenv("ETHOS_AGENT_ID")
	if agent == "" {
		agent = process.FindClaudePID()
	}
	if participant := roster.FindParticipant(agent); participant != nil {
		return participant.Persona
	}
	return ""
}
