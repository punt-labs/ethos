package autorepair

import (
	"context"
	"fmt"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

func init() { Register(Step{Name: "scrub-records", Scope: ScopeRepository, Run: runScrubRecords}) }

// runScrubRecords redacts tracked mission records written before v4.20.0 in
// place, so the change appears as an ordinary diff (DES-080 decision 6). Items
// counts files changed. A record that cannot be redacted is left as it was and
// recorded as one residual; the files that could be scrubbed still are.
func runScrubRecords(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		out.Deferred = true
		return out, nil
	}
	rels, err := mission.TrackedRecordFiles(env.Roots.Checkout)
	if err != nil {
		env.Log.Logf("scrub-records", "list-tracked", nil, err)
		out.Deferred = true
		return out, nil
	}
	res, err := mission.ScrubFilesWithPolicy(env.Policy, env.Global, rels, false)
	for _, rel := range res.Changed {
		env.Log.Logf("scrub-records", "redact-in-place", []string{rel}, nil)
	}
	out.Items = len(res.Changed)
	if err != nil {
		env.Log.Logf("scrub-records", "refuse", nil, err)
		out.Residuals = append(out.Residuals, Residual{
			Step:   "scrub-records",
			ID:     "tracked-records",
			Reason: fmt.Sprintf("some tracked mission records could not be redacted and were left as they were: %v", err),
		})
	}
	return out, nil
}
