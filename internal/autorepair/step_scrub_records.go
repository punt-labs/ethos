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
	checkout, err := env.CheckoutRoot()
	if err != nil {
		return out, fmt.Errorf("scrubbing records: %w", err)
	}
	rels, err := mission.TrackedRecordFiles(checkout)
	if err != nil {
		env.Log.Logf("scrub-records", "list-tracked", nil, err)
		out.Deferred = true
		return out, nil
	}
	res, mo, err := mission.ScrubFilesDeferring(ctx, env.Policy, env.Global, skipDecidedAggregates(env, rels))
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
			Reason: scrubReason(res),
		})
	}
	return out, nil
}

// skipDecidedAggregates drops missions/<id>/reflections.yaml from rels when the
// mission's contract carries round_decisions.
//
// Step 5 (record-tree-trace) tells a legacy aggregate from a live publication by
// recomputing mission.LegacyDecisionID from the file's bytes and comparing it
// with the IDs stored in the contract. A file this step rewrote no longer hashes
// to those IDs, so step 5 would class it as a live publication and never archive
// it, leaving legacy detail tracked. Step 5 archives and redacts that file
// itself through the line guard, so skipping it here loses no redaction.
//
// A mission with no round_decisions (a fenced one) keeps its aggregate in rels:
// step 5 classes it as the mission's round authority and never archives or
// hashes it, so scrubbing it here changes nothing step 5 later reads.
func skipDecidedAggregates(env Env, rels []string) []string {
	const prefix = ".punt-labs/ethos/missions/"
	var kept []string
	for _, rel := range rels {
		parts := strings.Split(rel, "/")
		if strings.HasPrefix(rel, prefix) && len(parts) == 5 && parts[4] == "reflections.yaml" {
			b, err := os.ReadFile(repositoryContractPath(env, parts[3]))
			if err == nil {
				if c, err := mission.DecodeContract(b); err == nil && c.RoundDecisions != nil {
					continue
				}
			}
		}
		kept = append(kept, rel)
	}
	return kept
}

// scrubReason names why records were left as they were. It is derived from the
// counts of failure kinds, never from a path or a value in the error text.
func scrubReason(res mission.ScrubResult) string {
	switch {
	case res.Unwritable > 0:
		return "some tracked mission records are unreadable or unwritable and were left as they were"
	case res.Refused > 0:
		return "some tracked mission records were refused by the write guard and left as they were"
	}
	return fmt.Sprintf("some tracked mission records could not be redacted and were left as they were (%d failed)", res.Failed)
}
