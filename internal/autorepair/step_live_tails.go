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
// wins. Items counts lines rewritten plus lines dropped. A stream whose
// archive the scan refuses, or whose lock is held, is deferred to the next
// Ensure; this step records no residuals.
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
		// The archive is what a live line is compared against; while any of
		// the stream's archive directories is refused by the scan, the
		// comparison cannot be trusted. Step 7 (corrupt-chunks) repairs that
		// and runs after this step in StepOrder, so the stream is deferred to
		// the next Ensure rather than recorded as a residual.
		clean, err := archiveScansClean(env, s)
		if err != nil {
			logf(env, stepLiveTails, "scan-archive", []string{live}, err)
			out.Deferred = true
			continue
		}
		if !clean {
			logf(env, stepLiveTails, "defer-dirty-scan", []string{live}, nil)
			out.Deferred = true
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
			// Anything RedactLive refuses is a condition of the archive or the
			// filesystem at this moment, not a property of the live file that
			// another run cannot change; it is retried, never left permanent.
			logf(env, stepLiveTails, "redact-live", []string{live}, err)
			out.Deferred = true
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

// archiveScansClean reports whether every archive directory of the stream
// passes audit.ScanSealedDir. A missing directory is clean.
func archiveScansClean(env Env, s audit.Stream) (bool, error) {
	dirs, err := audit.StreamDirs(env.Policy, s)
	if err != nil {
		return false, err
	}
	for _, dir := range dirs {
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := audit.ScanSealedDir(dir, s.Namespace, s.Session); err != nil {
			var orphan *audit.OrphanCorruptError
			if errors.As(err, &orphan) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}
