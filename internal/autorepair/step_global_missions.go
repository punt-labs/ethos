package autorepair

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

const stepGlobalMissions = "global-missions"

// reasonImportedKept is the residual reason for a mission copied into the
// repository whose global files were left in place.
const reasonImportedKept = "imported; global copy kept"

func init() {
	Register(Step{Name: stepGlobalMissions, Scope: ScopeBoth, Run: runGlobalMissions})
}

// runGlobalMissions copies the global missions (v3.0-v3.11) this repository
// owns into its store through the redactor, each copy verified under the
// mission locks. It never deletes or changes a global file: a mission may be
// in use by another repository, and nothing in a global record says which.
// Each new copy is recorded once as a residual naming the kept global copy;
// a copy already present and equal adds no item, log line or residual; a
// copy already present that has since diverged is its own residual.
// Missions the repository does not own stay global and are not copied. A
// mission that cannot be parsed, redacted or verified is a residual; the
// step finishes. The step prints nothing: every action and warning goes to
// env.Log.
func runGlobalMissions(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		out.Deferred = true
		return out, nil
	}
	store, err := env.StoreRoot()
	if err != nil {
		return out, fmt.Errorf("importing global missions: %w", err)
	}
	checkout, err := env.CheckoutRoot()
	if err != nil {
		return out, fmt.Errorf("importing global missions: %w", err)
	}
	res, err := mission.ImportGlobalMissions(env.Global, store, checkout)
	if err != nil {
		return out, fmt.Errorf("importing global missions: %w", err)
	}
	global := filepath.Join(env.Global, "missions")
	repo := filepath.Join(store, ".punt-labs", "ethos", "missions")
	for _, w := range res.Warnings {
		env.Log.Logf(stepGlobalMissions, "scan-warning", nil, fmt.Errorf("%s", w))
	}
	for _, id := range res.Imported {
		env.Log.Logf(stepGlobalMissions, "import", []string{filepath.Join(global, id+".yaml"), filepath.Join(repo, id)}, nil)
		out.Residuals = append(out.Residuals, settledResidual(stepGlobalMissions, id, reasonImportedKept))
	}
	out.Items = len(res.Imported)
	// A copy imported by an earlier run and still equal to its global source is
	// the same settled state; re-emitting it keeps a doctor recheck, which
	// replaces this step's residuals, from dropping the record.
	for _, id := range res.Present {
		out.Residuals = append(out.Residuals, settledResidual(stepGlobalMissions, id, reasonImportedKept))
	}
	for _, id := range res.Diverged {
		env.Log.Logf(stepGlobalMissions, "diverged", []string{filepath.Join(repo, id)}, nil)
		out.Residuals = append(out.Residuals, actionableResidual(stepGlobalMissions, id, mission.ReasonRepoCopyDiverged, "clears when the global copy of "+id+" matches the repository copy or is gone"))
	}
	for _, f := range res.Failed {
		env.Log.Logf(stepGlobalMissions, "import-failed", []string{filepath.Join(global, f.ID+".yaml")}, fmt.Errorf("%s", f.Reason))
		out.Residuals = append(out.Residuals, actionableResidual(stepGlobalMissions, f.ID, f.Reason, seeLogAction))
	}
	return out, nil
}
