package autorepair

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/hook"
)

const stepGlobalAudit = "global-audit"

func init() {
	Register(Step{Name: stepGlobalAudit, Scope: ScopeBoth, Run: runGlobalAudit})
}

// runGlobalAudit imports the global session audit files (v3.0-v3.11) that
// belong to this repository into the local archive, redacted. A session
// belongs here when its global roster names this checkout (every roster
// since v4.13.0 records one) or when the repository holds a stream for it.
// The global file is deleted only on the roster signal, once the archive
// reads back equal: a stream alone does not prove no other repository holds
// one, so that copy is kept and recorded as a residual. A session with
// neither signal cannot be placed and is a residual too; nothing is guessed.
// A session that cannot be imported is a residual and its file stays. The
// step prints nothing: every action goes to env.Log.
func runGlobalAudit(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		out.Deferred = true
		return out, nil
	}
	dir := filepath.Join(env.Global, "sessions")
	res, err := hook.ImportGlobalAudit(env.Policy, dir, env.Roots.Checkout, env.Roots.Store)
	if err != nil {
		return out, fmt.Errorf("importing global session audit: %w", err)
	}
	file := func(id string) string { return filepath.Join(dir, id+".audit.jsonl") }
	for _, d := range res.Done {
		out.Items++
		if d.Deleted {
			env.Log.Logf(stepGlobalAudit, "import-and-delete", []string{file(d.Session)}, nil)
			continue
		}
		env.Log.Logf(stepGlobalAudit, "import-keep-global", []string{file(d.Session)}, nil)
		out.Residuals = append(out.Residuals, Residual{Step: stepGlobalAudit, ID: d.Session, Reason: reasonSharedOwnership})
	}
	for _, id := range res.Unknown {
		env.Log.Logf(stepGlobalAudit, "no-owner", []string{file(id)}, nil)
		out.Residuals = append(out.Residuals, Residual{Step: stepGlobalAudit, ID: id, Reason: hook.ReasonNoAuditOwner})
	}
	for _, f := range res.Failed {
		env.Log.Logf(stepGlobalAudit, "import-failed", []string{file(f.Session)}, fmt.Errorf("%s", f.Reason))
		out.Residuals = append(out.Residuals, Residual{Step: stepGlobalAudit, ID: f.Session, Reason: f.Reason})
	}
	return out, nil
}
