package autorepair

import (
	"context"
	"fmt"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/enable"
)

const stepHookSections = "hook-sections"

func init() { Register(Step{Name: stepHookSections, Scope: ScopeRepository, Run: runHookSections}) }

// runHookSections refreshes stale ethos sections in the repository's git
// hooks, including sections written before the DES-083 shim that call retired
// hook subcommands. A hook that lives in a shared or tracked core.hooksPath
// cannot be rewritten without touching files other repositories or commits
// own; it is recorded as a residual. Items counts hook sections written.
func runHookSections(ctx context.Context, env Env) (Outcome, error) {
	return refreshHookSections(ctx, env, stepHookSections)
}

// refreshHookSections is the hook refresh shared by steps 10 and 16; step
// names the caller in the log and in residuals.
func refreshHookSections(ctx context.Context, env Env, step string) (Outcome, error) {
	var out Outcome
	if err := ctx.Err(); err != nil {
		out.Deferred = true
		return out, nil
	}
	checkout, err := env.CheckoutRoot()
	if err != nil {
		return out, fmt.Errorf("locating the checkout for the hook refresh: %w", err)
	}
	store, err := env.StoreRoot()
	if err != nil {
		return out, fmt.Errorf("locating the store for the hook refresh: %w", err)
	}
	rep, err := enable.RefreshHooksTo(checkout, store)
	if rep != nil {
		for _, st := range rep.Steps {
			switch st.Status {
			case "refreshed", "chained", "installed":
				out.Items++
				env.Log.Logf(step, "refresh-hook", []string{st.Detail}, nil)
			case enable.StatusSkippedNonShell, enable.StatusSkippedTracked,
				enable.StatusSkippedShared, enable.StatusSkippedTrackedMissing:
				out.Residuals = append(out.Residuals, Residual{
					Step:   step,
					ID:     st.Step + ": " + relTo(checkout, st.Detail),
					Reason: "hook cannot be rewritten: " + strings.TrimPrefix(st.Status, "skipped-"),
				})
			}
		}
	}
	if err != nil {
		env.Log.Logf(step, "refresh-hook", nil, err)
		return out, fmt.Errorf("refreshing git hook sections: %w", err)
	}
	return out, nil
}
