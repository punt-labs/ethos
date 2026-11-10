package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/lineguard"
)

const stepLiveTails = "live-tails"

func init() { Register(Step{Name: stepLiveTails, Scope: ScopeRepository, Run: runLiveTails}) }

// runLiveTails is step 6: live audit and mission-log lines written before
// redaction (before v4.20.0) are rewritten through the guard, and a line whose
// ts is already archived with another payload is dropped because the archive
// wins. Items counts lines rewritten plus lines dropped. A stream whose
// archive the scan transiently refuses, or whose lock is held, is deferred to
// the next Ensure; a malformed chunk name or non-regular file in the archive
// is a residual, because no later run changes it.
func runLiveTails(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	streams, err := audit.ListStreamsByName(env.Policy)
	if err != nil {
		// A directory whose contents cannot be listed as streams (a file
		// named like a chunk that is not one) is a permanent condition of
		// the tree: recorded once, so the stamp can land, never retried on
		// every command.
		if reason, ok := permanentScanReason(err); ok {
			logf(env, stepLiveTails, "scan-streams", nil, err)
			out.Residuals = append(out.Residuals, Residual{Step: stepLiveTails, ID: "streams", Reason: reason})
			return out, nil
		}
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
			if reason, ok := permanentScanReason(err); ok {
				out.Residuals = append(out.Residuals, Residual{Step: stepLiveTails, ID: filepath.Base(live), Reason: reason})
			} else {
				out.Deferred = true
			}
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
			res, err = redactLiveChunks(ctx, env.Policy, s, liveGuard(env.Policy, s.Namespace))
			return err
		})
		switch {
		case ctx.Err() != nil && err != nil:
			// The budget ran out inside the stream. The chunks finished so
			// far are on disk and recorded, so the next run resumes past them.
			logf(env, stepLiveTails, "defer-budget", []string{live}, nil)
			out.Deferred = true
			return out, nil
		case busy:
			out.Deferred = true
			logf(env, stepLiveTails, "defer-busy-stream", []string{live}, nil)
			continue
		case err != nil:
			logf(env, stepLiveTails, "redact-live", []string{live}, err)
			if isLiveLineError(err) {
				// A complete line the file holds that cannot be decoded, or
				// whose ts cannot be parsed, is a property of the file no
				// later run changes: a residual naming the stream, never
				// the content.
				out.Residuals = append(out.Residuals, Residual{Step: stepLiveTails, ID: filepath.Base(live), Reason: "a live line cannot be decoded; file left in place"})
				continue
			}
			// Anything else RedactLive refuses is a condition of the archive
			// or the filesystem at this moment; it is retried.
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

// liveGuard builds a stream's guard; a test replaces it with a slow stub.
var liveGuard = liveLineGuard

// liveChunkLines is how many live lines one RedactLive call judges. The guard
// costs milliseconds a line, so a chunk is well under a step budget and the
// work a budget cuts short is small.
var liveChunkLines = 100

// redactLiveChunks redacts the stream's live file chunk by chunk until the
// file is complete or ctx is done. Each finished chunk is already on disk.
func redactLiveChunks(ctx context.Context, p layout.Policy, s audit.Stream, guard audit.LineGuard) (audit.LiveRepair, error) {
	var total audit.LiveRepair
	for {
		res, err := audit.RedactLive(ctx, p, s, guard, liveChunkLines)
		total.Rewritten += res.Rewritten
		total.Dropped = append(total.Dropped, res.Dropped...)
		if err != nil || res.Complete {
			total.Complete = res.Complete
			return total, err
		}
	}
}

// liveLineGuard is the redaction guard for lines of a stream's live file: the
// mission-event guard for a mission log, the session-audit guard otherwise.
// It mirrors hook.LiveLineGuard over the lineguard leaf so the seal and the
// storage upgrade rewrite a pre-redaction line the same way, and autorepair
// never imports internal/hook.
func liveLineGuard(p layout.Policy, ns audit.Namespace) audit.LineGuard {
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	if ns == audit.MissionNS {
		return audit.LineGuard(lineguard.MissionLineGuardFor(checkout))
	}
	return audit.LineGuard(lineguard.SessionLineGuardFor(checkout))
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

// isLiveLineError reports whether err is a complete live line the reader
// could not decode or whose ts it could not parse: readCompleteLive wraps
// both as "live audit <path> line <n>: ...".
func isLiveLineError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "live audit ") && strings.Contains(err.Error(), " line ")
}
