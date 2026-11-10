package autorepair

import (
	"context"
	"fmt"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// preCommitSteps are the registered steps the commit-time pass runs, in order.
var preCommitSteps = []string{"live-tails", "untrack-local", "hook-sections"}

// PreCommit is the cheap pass the git pre-commit hook runs before sealing. It
// removes every path under .punt-labs/local/ from the committing index (the
// files stay on disk), then runs the registered cheap steps. It returns the one
// line for stderr, or "" when nothing was unstaged. A step failure is logged,
// never returned; only a git failure while unstaging is an error, because a
// local path left in the index would be committed.
func PreCommit(ctx context.Context, env Env) (string, error) {
	if !env.InRepository() || env.Roots.Checkout == "" {
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
	for _, name := range preCommitSteps {
		runNamed(ctx, env, name)
	}
	if n == 0 {
		return "", nil
	}
	return fmt.Sprintf("ethos: hook pre-commit: unstaged %d path(s) under %s; they stay on disk", n, layout.LocalPrefix), nil
}

// runNamed runs the registered step called name, if any, within StepBudget.
func runNamed(ctx context.Context, env Env, name string) {
	for _, s := range Steps {
		if s.Name != name {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, StepBudget)
		defer cancel()
		if _, err := s.Run(sctx, env); err != nil {
			logStep(env, name, "failed", nil, err)
		}
		return
	}
}
