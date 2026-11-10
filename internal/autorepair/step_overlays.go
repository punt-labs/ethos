package autorepair

import (
	"context"
	"errors"
	"fmt"

	"github.com/punt-labs/ethos/v4/internal/identity"
)

func init() {
	Register(Step{Name: "overlays", Scope: ScopeRepository, Run: runOverlays})
}

// runOverlays moves repository identity overlays from their pre-DES-079
// location beside the identity to the local zone. A destination that already
// exists wins and the old copy is deleted. Only paths are logged, never values.
func runOverlays(ctx context.Context, env Env) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{Deferred: true}, nil
	}
	if !env.InRepository() {
		return Outcome{}, nil
	}
	s := identity.NewStore(env.Policy.RecordRoot(), identity.RepositoryPolicy(env.Policy))
	moves, residuals, busy, err := s.RelocateLegacyOverlays()
	for _, m := range moves {
		action := "move"
		switch {
		case m.Replaced:
			action = "replace-empty"
		case m.Kept:
			action = "delete-superseded"
		}
		env.Log.Logf("overlays", action, []string{m.Legacy, m.Local}, nil)
		for _, k := range m.Dropped {
			env.Log.Logf("overlays", "drop-superseded", []string{m.Handle + "/" + m.Namespace + "/" + k}, nil)
		}
	}
	out := Outcome{Items: len(moves), Deferred: busy}
	for _, r := range residuals {
		env.Log.Logf("overlays", "residual", []string{r.ID}, errors.New(r.Reason))
		out.Residuals = append(out.Residuals, Residual{Step: "overlays", ID: r.ID, Reason: storeRel(repoStore(env), r.Reason)})
	}
	if err != nil {
		env.Log.Logf("overlays", "relocate", nil, err)
		var pub *identity.PublicationError
		if errors.As(err, &pub) {
			// The change applied; durability is the only open question.
			return out, nil
		}
		return out, fmt.Errorf("relocating identity overlays: %w", err)
	}
	return out, nil
}
