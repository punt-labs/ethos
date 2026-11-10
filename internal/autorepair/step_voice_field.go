package autorepair

import (
	"context"
	"errors"
	"fmt"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

func init() {
	Register(Step{Name: "voice-field", Scope: ScopeBoth, Run: runVoiceField})
}

// runVoiceField moves a top-level voice: field (identities written before
// v2.0) in the global and repository stores into the vox extension. Extensions
// live in the global store, or in the repository under resolution: repo-only.
func runVoiceField(ctx context.Context, env Env) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{Deferred: true}, nil
	}
	var out Outcome
	global := identity.NewStore(env.Global)
	if err := voiceStore(env, &out, global, global, env.Global); err != nil {
		return out, err
	}
	if !env.InRepository() {
		return out, nil
	}
	repo := identity.NewStore(env.Policy.RecordRoot(), identity.RepositoryPolicy(env.Policy))
	ext := global
	if env.Roots.ConfigErr == nil && env.Roots.Config != nil && env.Roots.Config.Resolution == resolve.ResolutionRepoOnly {
		ext = repo
	}
	if err := voiceStore(env, &out, repo, ext, repoStore(env)); err != nil {
		return out, err
	}
	return out, nil
}

// voiceStore migrates one store and folds the result into out. where is the
// root residual paths are reported relative to: the global root or the
// repository store root.
func voiceStore(env Env, out *Outcome, s, ext *identity.Store, where string) error {
	moves, residuals, busy, err := s.MigrateLegacyVoices(ext)
	for _, m := range moves {
		env.Log.Logf("voice-field", "rewrite", []string{m.Path}, nil)
		for _, k := range m.Dropped {
			env.Log.Logf("voice-field", "drop-superseded", []string{m.Handle + "/" + k}, nil)
		}
	}
	for _, r := range residuals {
		env.Log.Logf("voice-field", "residual", []string{where, r.Handle}, errors.New(r.Reason))
		out.Residuals = append(out.Residuals, Residual{Step: "voice-field", ID: storeRel(where, r.Path), Reason: storeRel(where, r.Reason)})
	}
	out.Items += len(moves)
	out.Deferred = out.Deferred || busy
	if err == nil {
		return nil
	}
	env.Log.Logf("voice-field", "migrate", []string{where}, err)
	var pub *identity.PublicationError
	if errors.As(err, &pub) {
		// The change applied; only crash durability is unconfirmed.
		return nil
	}
	return fmt.Errorf("migrating legacy voice fields under %s: %w", where, err)
}
