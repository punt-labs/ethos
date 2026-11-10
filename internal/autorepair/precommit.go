package autorepair

import (
	"context"
	"fmt"
	"slices"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// preCommitSteps are the registered steps the commit-time pass runs, in order.
// Every name must be in StepOrder. untrack-local is not here: it stages
// record-tree deletions, which must never ride along in the user's commit;
// UnstageUnder below already guards the index for .punt-labs/local/.
var preCommitSteps = []string{"live-tails", "hook-sections"}

// PreCommit is the cheap pass the git pre-commit hook runs before sealing. It
// removes every path under .punt-labs/local/ from the committing index (the
// files stay on disk), then runs the registered cheap steps when a store was
// located. It returns the one line for stderr, or "" when nothing was
// unstaged. A step failure is logged, never returned; only a git failure while
// unstaging is an error, because a local path left in the index would be
// committed.
func PreCommit(ctx context.Context, env Env) (string, error) {
	if env.Roots.Checkout == "" {
		return "", nil
	}
	genv, err := gitenv.ActualIndexEnv()
	if err != nil {
		return "", fmt.Errorf("selecting the committing index: %w", err)
	}
	n, err := gitenv.UnstageUnder(env.Roots.Checkout, genv, layout.LocalPrefix)
	if err != nil {
		return "", err
	}
	if n > 0 {
		logStep(env, "pre-commit", "unstaged local paths", nil, nil)
	}
	if env.InRepository() {
		for _, name := range preCommitSteps {
			runNamed(ctx, env, name)
		}
	} else {
		logStep(env, "pre-commit", "skipped repair steps: store not located", nil, nil)
	}
	if n == 0 {
		return "", nil
	}
	return fmt.Sprintf("ethos: hook pre-commit: unstaged %d path(s) under %s; they stay on disk", n, layout.LocalPrefix), nil
}

// runNamed runs the registered step called name within StepBudget. A name in
// StepOrder with no registered step is logged: it is a step not built yet, or
// a registration that went missing.
func runNamed(ctx context.Context, env Env, name string) {
	i := slices.IndexFunc(Steps, func(s Step) bool { return s.Name == name })
	if i < 0 {
		logStep(env, name, "skipped: step not registered", nil, nil)
		return
	}
	sctx, cancel := context.WithTimeout(ctx, StepBudget)
	defer cancel()
	if _, err := Steps[i].Run(sctx, env); err != nil {
		logStep(env, name, "failed", nil, err)
	}
}
