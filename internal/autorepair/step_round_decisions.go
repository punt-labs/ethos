package autorepair

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

func init() { Register(Step{Name: "round-decisions", Scope: ScopeRepository, Run: runRoundDecisions}) }

// runRoundDecisions gives each repository mission written before v4.21.0 its
// shared round decisions and corrections authority, imported from its own
// complete legacy evidence (DES-079). Items counts missions imported.
//
// A mission whose crossed-round history is incomplete, or whose evidence
// redaction refuses, is untouched and recorded as a residual: it stays fenced
// from lifecycle writes and nothing is fabricated for it. The residual is
// derived from observed state, so a second run reports the same one.
func runRoundDecisions(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	ids, err := repositoryMissionIDs(env)
	if err != nil {
		return out, fmt.Errorf("listing missions: %w", err)
	}
	store := mission.NewRepositoryStore(env.Policy, env.Global)
	for _, id := range ids {
		if ctx.Err() != nil {
			out.Deferred = true
			return out, nil
		}
		var upgraded bool
		var outcome mission.MutationOutcome
		err := mission.WithRecordLockTry(env.Policy, id, func() error {
			var err error
			upgraded, outcome, err = store.UpgradeLegacyAuthority(id)
			return err
		})
		for _, w := range outcome.Warnings {
			env.Log.Logf("round-decisions", "durability-unconfirmed", []string{missionPath(env, id)}, errors.New(w))
		}
		var history *mission.LegacyHistoryError
		switch {
		case errors.Is(err, mission.ErrLockBusy):
			env.Log.Logf("round-decisions", "defer-busy", []string{missionPath(env, id)}, nil)
			out.Deferred = true
		case errors.As(err, &history):
			env.Log.Logf("round-decisions", "fence", []string{missionPath(env, id)}, err)
			out.Residuals = append(out.Residuals, Residual{Step: "round-decisions", ID: id, Reason: history.Kind.String() + ", mission fenced from lifecycle writes"})
		case err != nil:
			env.Log.Logf("round-decisions", "refuse", []string{missionPath(env, id)}, err)
			out.Residuals = append(out.Residuals, Residual{Step: "round-decisions", ID: id, Reason: "mission authority cannot be written, mission fenced from lifecycle writes"})
		case upgraded:
			env.Log.Logf("round-decisions", "import-authority", []string{
				repositoryContractPath(env, id),
				filepath.Join(filepath.Dir(repositoryContractPath(env, id)), "corrections.yaml"),
			}, nil)
			out.Items++
		}
	}
	return out, nil
}

func missionPath(env Env, id string) string {
	return filepath.Dir(repositoryContractPath(env, id))
}
