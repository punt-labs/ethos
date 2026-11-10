package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

func init() { Register(Step{Name: "scrub-records", Scope: ScopeRepository, Run: runScrubRecords}) }

// runScrubRecords redacts tracked mission records written before v4.20.0 in
// place, so the change appears as an ordinary diff (DES-080 decision 6). Items
// counts files changed.
//
// A record whose writer lock is held, or a budget that ends before every record
// is visited, defers the rest to the next run. A record that cannot be redacted
// is left as it was and recorded as one residual; the files that could be
// scrubbed still are. The step takes no blocking lock and prints nothing.
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
	res, mo, err := mission.ScrubFilesDeferring(ctx, env.Policy, env.Global, rels)
	for _, rel := range res.Changed {
		env.Log.Logf("scrub-records", "redact-in-place", []string{rel}, nil)
	}
	for _, w := range mo.Warnings {
		env.Log.Logf("scrub-records", "durability-unconfirmed", nil, errors.New(w))
	}
	for _, rel := range res.Busy {
		env.Log.Logf("scrub-records", "defer-busy", []string{rel}, nil)
	}
	out.Items = len(res.Changed)
	out.Deferred = len(res.Busy) > 0 || res.Interrupted
	if err != nil {
		env.Log.Logf("scrub-records", "refuse", nil, err)
		out.Residuals = append(out.Residuals, Residual{
			Step:   "scrub-records",
			ID:     "tracked-records",
			Reason: scrubReason(err),
		})
	}
	return out, nil
}

// scrubReason names why records were left as they were. It is derived from the
// kind of failure, never from a path or a value in the error text.
func scrubReason(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return "some tracked mission records are unreadable or unwritable and were left as they were"
	case strings.Contains(err.Error(), "refusing to write"):
		return "some tracked mission records were refused by the write guard and left as they were"
	}
	return fmt.Sprintf("some tracked mission records could not be redacted and were left as they were (%d failed)", failedCount(err))
}

// failedCount reads the failure count ScrubFilesDeferring puts in its error.
func failedCount(err error) int {
	var rewritten, failed int
	if _, scanErr := fmt.Sscanf(err.Error(), "%d rewritten, %d failed", &rewritten, &failed); scanErr != nil {
		return 0
	}
	return failed
}
