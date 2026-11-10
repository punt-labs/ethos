package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/hook"
)

const stepLiveTails = "live-tails"

func init() { Register(Step{Name: stepLiveTails, Scope: ScopeRepository, Run: runLiveTails}) }

// runLiveTails is step 6: live audit and mission-log lines written before
// redaction (before v4.20.0) are rewritten through the guard, and a line whose
// ts is already archived with another payload is dropped because the archive
// wins. Items counts lines rewritten plus lines dropped.
func runLiveTails(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	streams, err := audit.ListStreams(env.Policy)
	if err != nil {
		return out, fmt.Errorf("listing audit streams: %w", err)
	}
	for _, s := range streams {
		if ctx.Err() != nil {
			out.Deferred = true
			return out, nil
		}
		live := s.LivePath(env.Policy)
		if _, err := os.Lstat(live); errors.Is(err, os.ErrNotExist) {
			continue
		}
		var res audit.LiveRepair
		busy, err := audit.TryWithStreamLock(env.Policy, s, func() error {
			var err error
			res, err = audit.RedactLive(env.Policy, s, hook.LiveLineGuard(env.Policy, s.Namespace))
			return err
		})
		switch {
		case busy:
			out.Deferred = true
			logf(env, stepLiveTails, "defer-busy-stream", []string{live}, nil)
			continue
		case err != nil:
			logf(env, stepLiveTails, "redact-live", []string{live}, err)
			out.Residuals = append(out.Residuals, Residual{Step: stepLiveTails, ID: s.Key(), Reason: "live file could not be redacted in place"})
			continue
		}
		if res.Rewritten > 0 {
			logf(env, stepLiveTails, fmt.Sprintf("redact-live lines=%d", res.Rewritten), []string{live}, nil)
		}
		for _, d := range res.Dropped {
			logf(env, stepLiveTails, fmt.Sprintf("drop-live-line ts=%d archive-sha256=%s live-sha256=%s", d.TS, d.ArchiveDigest, d.LiveDigest), []string{live}, nil)
		}
		out.Items += res.Rewritten + len(res.Dropped)
	}
	return out, nil
}

// logf records one action when the engine supplied a logger.
func logf(env Env, step, action string, paths []string, err error) {
	if env.Log != nil {
		env.Log.Logf(step, action, paths, err)
	}
}
