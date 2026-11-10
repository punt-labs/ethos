package autorepair

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/punt-labs/ethos/v4/internal/auditimport"
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
// neither signal (every roster before v4.13.0 names no checkout) is imported
// here too, so the repository that upgrades first gains the history it may
// own; its global file is kept byte-identical and the session recorded as a
// residual. Nothing is deleted on a weak signal and no owner is guessed. A
// session that cannot be imported is a residual and its file stays. The
// step prints nothing: every action goes to env.Log.
// importHeadroom is how far ahead of the step's deadline the import stops, so
// the step's outcome reaches the engine before it abandons the step. The
// guard costs milliseconds a line; one line is well inside this.
const importHeadroom = 500 * time.Millisecond

func runGlobalAudit(ctx context.Context, env Env) (Outcome, error) {
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
		return out, fmt.Errorf("importing global session audit: %w", err)
	}
	checkout, err := env.CheckoutRoot()
	if err != nil {
		return out, fmt.Errorf("importing global session audit: %w", err)
	}
	dir := filepath.Join(env.Global, "sessions")
	// The importer stops at its context; the step must return its outcome
	// before the engine's deadline or the residual and the deferral are lost
	// with the abandoned goroutine. The import gets the budget less the
	// headroom one line of the guard needs.
	ictx := ctx
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ictx, cancel = context.WithDeadline(ctx, deadline.Add(-importHeadroom))
		defer cancel()
	}
	res, err := auditimport.ImportGlobalAudit(ictx, env.Policy, dir, checkout, store)
	if err != nil {
		return out, fmt.Errorf("importing global session audit: %w", err)
	}
	file := func(id string) string { return filepath.Join(dir, id+".audit.jsonl") }
	for _, d := range res.Done {
		if d.Noop {
			// A kept global file is compared again on every run and found
			// already archived: nothing changed, nothing to count or record.
			continue
		}
		out.Items++
		// Every successful import retires the pre-redaction global file: the
		// redacted archive is the retained copy (operator ruling, DES-084).
		env.Log.Logf(stepGlobalAudit, "import-and-delete", []string{file(d.Session)}, nil)
	}
	for _, id := range res.Unowned {
		env.Log.Logf(stepGlobalAudit, "other-checkout", []string{file(id)}, nil)
	}
	for _, f := range res.Failed {
		reason := f.Reason
		if errors.Is(f.Err, context.DeadlineExceeded) || errors.Is(f.Err, context.Canceled) {
			// The file's own import outlived the step budget. It is not
			// retried on every command: the pre-redaction global copy stays
			// where it is, and the stamp records it (operator ruling: a
			// logged loss never blocks a command).
			reason = "import outlived the step budget; global copy kept"
		}
		env.Log.Logf(stepGlobalAudit, "import-failed", []string{file(f.Session)}, fmt.Errorf("%s", reason))
		out.Residuals = append(out.Residuals, Residual{Step: stepGlobalAudit, ID: f.Session, Reason: reason})
	}
	if res.Remaining > 0 {
		// The budget ran out; the files not attempted are the next run's.
		logf(env, stepGlobalAudit, fmt.Sprintf("defer-remaining files=%d", res.Remaining), nil, nil)
		out.Deferred = true
	}
	return out, nil
}
