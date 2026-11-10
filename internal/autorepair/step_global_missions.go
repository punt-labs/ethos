package autorepair

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

const stepGlobalMissions = "global-missions"

func init() {
	Register(Step{Name: stepGlobalMissions, Scope: ScopeBoth, Run: runGlobalMissions})
}

// runGlobalMissions imports the global missions (v3.0-v3.11) this repository
// owns into its store through the redactor and deletes the global copies.
// Missions the repository does not own stay global. A mission that cannot be
// parsed or redacted is a residual and stays where it is; the step finishes.
func runGlobalMissions(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		out.Deferred = true
		return out, nil
	}
	res, err := mission.ImportGlobalMissions(env.Global, env.Roots.Store, env.Roots.Checkout)
	if err != nil {
		return out, fmt.Errorf("importing global missions: %w", err)
	}
	global := filepath.Join(env.Global, "missions")
	repo := filepath.Join(env.Roots.Store, ".punt-labs", "ethos", "missions")
	for _, id := range res.Imported {
		env.Log.Logf(stepGlobalMissions, "import", []string{filepath.Join(global, id+".yaml"), filepath.Join(repo, id)}, nil)
	}
	for _, id := range res.Cleaned {
		env.Log.Logf(stepGlobalMissions, "delete-stale", []string{filepath.Join(global, id+".yaml")}, nil)
	}
	out.Items = len(res.Imported)
	for _, f := range res.Failed {
		env.Log.Logf(stepGlobalMissions, "import-failed", []string{filepath.Join(global, f.ID+".yaml")}, fmt.Errorf("%s", f.Reason))
		out.Residuals = append(out.Residuals, Residual{Step: stepGlobalMissions, ID: f.ID, Reason: f.Reason})
	}
	return out, nil
}
