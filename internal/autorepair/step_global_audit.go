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

// runGlobalAudit imports the global session audit files (v3.0-v3.11) of
// sessions this repository holds into the local archive, redacted, and
// deletes each global file once its archive copy reads back equal. A session
// that cannot be imported is a residual and its file stays.
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
	done, failed, err := hook.ImportGlobalAudit(env.Policy, dir)
	if err != nil {
		return out, fmt.Errorf("importing global session audit: %w", err)
	}
	for _, d := range done {
		env.Log.Logf(stepGlobalAudit, "import-and-delete", []string{filepath.Join(dir, d.Session+".audit.jsonl")}, nil)
	}
	out.Items = len(done)
	for _, f := range failed {
		env.Log.Logf(stepGlobalAudit, "import-failed", []string{filepath.Join(dir, f.Session+".audit.jsonl")}, fmt.Errorf("%s", f.Reason))
		out.Residuals = append(out.Residuals, Residual{Step: stepGlobalAudit, ID: f.Session, Reason: f.Reason})
	}
	return out, nil
}
