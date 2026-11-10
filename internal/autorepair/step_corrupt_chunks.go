package autorepair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/hook"
)

const stepCorruptChunks = "corrupt-chunks"

func init() { Register(Step{Name: stepCorruptChunks, Scope: ScopeRepository, Run: runCorruptChunks}) }

// orphanReason is the marker reason recorded when the step completes a
// retirement an older binary left unfinished.
const orphanReason = "interrupted quarantine completed by the storage upgrade"

// maxRepairPasses bounds the scan-repair loop per stream directory; every
// pass that makes progress removes at least one offending artifact, so a
// directory needs at most as many passes as it has artifacts.
const maxRepairPasses = 64

// runCorruptChunks is step 7. For every stream it repairs each archive
// directory until audit.ScanSealedDir reads it cleanly: a sealed chunk whose
// bytes no longer decode is replaced by a chunk of its redacted, decodable
// lines; a retirement an interrupted quarantine left without its final marker
// is completed; a legacy raw .corrupt is salvaged, covered by a marker and
// deleted; a stale .corrupt beside a valid chunk of the same stem is covered
// and deleted. A recovered line that conflicts with surviving evidence is not
// installed; both digests are logged. Items counts repairs applied. A busy
// stream lock defers; a directory still dirty after the pass budget is a
// residual.
func runCorruptChunks(ctx context.Context, env Env) (Outcome, error) {
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
		dirs, err := audit.StreamDirs(env.Policy, s)
		if err != nil {
			logf(env, stepCorruptChunks, "list-stream-dirs", nil, err)
			out.Deferred = true
			continue
		}
		dirty := false
		for _, dir := range dirs {
			d, err := dirNeedsRepair(dir, s)
			if err != nil {
				logf(env, stepCorruptChunks, "scan-chunks", []string{dir}, err)
				out.Deferred = true
				continue
			}
			dirty = dirty || d
		}
		if !dirty {
			continue
		}
		guard := hook.LiveLineGuard(env.Policy, s.Namespace)
		busy, err := audit.TryWithStreamLock(env.Policy, s, func() error {
			for _, dir := range dirs {
				repairDir(ctx, env, &out, s, dir, guard)
			}
			return nil
		})
		if err != nil {
			logf(env, stepCorruptChunks, "lock-stream", nil, err)
			out.Deferred = true
			continue
		}
		if busy {
			out.Deferred = true
			logf(env, stepCorruptChunks, "defer-busy-stream", nil, nil)
		}
	}
	return out, nil
}

// dirNeedsRepair reports whether dir has a damaged chunk or an artifact the
// scan refuses. A missing directory needs nothing.
func dirNeedsRepair(dir string, s audit.Stream) (bool, error) {
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if _, err := audit.ScanSealedDir(dir, s.Namespace, s.Session); err != nil {
		var orphan *audit.OrphanCorruptError
		if errors.As(err, &orphan) {
			return true, nil
		}
		return false, err
	}
	damaged, err := damagedChunks(dir, s)
	return len(damaged) > 0, err
}

// repairDir drives one directory to a clean scan under the stream lock.
func repairDir(ctx context.Context, env Env, out *Outcome, s audit.Stream, dir string, guard audit.LineGuard) {
	for pass := 0; pass < maxRepairPasses; pass++ {
		if ctx.Err() != nil {
			out.Deferred = true
			return
		}
		_, err := audit.ScanSealedDir(dir, s.Namespace, s.Session)
		var orphan *audit.OrphanCorruptError
		switch {
		case err == nil:
			damaged, err := damagedChunks(dir, s)
			if err != nil {
				logf(env, stepCorruptChunks, "scan-chunks", []string{dir}, err)
				out.Deferred = true
				return
			}
			if len(damaged) == 0 {
				return
			}
			for _, d := range damaged {
				if !salvage(env, out, s, d.path, d.name, guard) {
					return
				}
			}
		case errors.As(err, &orphan):
			if !repairOrphan(env, out, s, orphan, guard) {
				return
			}
		default:
			logf(env, stepCorruptChunks, "scan-chunks", []string{dir}, err)
			out.Deferred = true
			return
		}
	}
	logf(env, stepCorruptChunks, "repair-passes-exhausted", []string{dir}, nil)
	out.Residuals = append(out.Residuals, Residual{Step: stepCorruptChunks, ID: filepath.Base(dir), Reason: "archive directory still refused after repair"})
}

// repairOrphan resolves one OrphanCorruptError by what is on disk. It returns
// false when the step must stop on this directory (the condition is recorded).
func repairOrphan(env Env, out *Outcome, s audit.Stream, e *audit.OrphanCorruptError, guard audit.LineGuard) bool {
	cn, kind := audit.Classify(filepath.Base(e.Chunk), s.Namespace)
	if kind != audit.KindValid {
		return residual(env, out, e.Chunk, "orphan .corrupt names no chunk", nil)
	}
	corrupt := filepath.Join(e.Dir, audit.CorruptName(cn.Stem()))
	sourceInfo, sourceErr := os.Lstat(e.Chunk)
	sourcePresent := sourceErr == nil
	if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
		return residual(env, out, e.Chunk, "source chunk cannot be examined", sourceErr)
	}
	if sourcePresent && !sourceInfo.Mode().IsRegular() {
		return residual(env, out, e.Chunk, "source chunk is not a regular file", nil)
	}
	if sourcePresent {
		if _, err := audit.ReadChunkVerified(e.Chunk, cn.Last); err != nil {
			// Damaged source beside a retirement artifact: recover the source;
			// the artifact is handled on the next pass.
			return salvage(env, out, s, e.Chunk, cn, guard)
		}
		// A valid source is the authority for its stem; the stale .corrupt is
		// covered with a no-gap marker and deleted.
		return coverStaleCorrupt(env, out, s, corrupt, cn)
	}
	b, err := os.ReadFile(corrupt)
	if err != nil {
		return residual(env, out, corrupt, "retired bytes cannot be read", err)
	}
	if isRetirementRecord(b) {
		// An interrupted QuarantineLocal (pending or missing marker): resume it.
		_, err := audit.QuarantineLocal(env.Policy, s, e.Chunk, e.Dir, cn, orphanReason, guard)
		if err != nil {
			return residual(env, out, corrupt, "interrupted quarantine could not be completed", err)
		}
		logf(env, stepCorruptChunks, "complete-orphan-corrupt", []string{corrupt}, nil)
		out.Items++
		return true
	}
	// Legacy raw .corrupt from the old rename-based quarantine: salvage its
	// bytes into the local archive under a covering marker and delete it.
	return salvage(env, out, s, corrupt, cn, guard)
}

// salvage runs SalvageChunk on source and records the outcome. It returns
// false when the step must stop on this directory.
func salvage(env Env, out *Outcome, s audit.Stream, source string, cn audit.ChunkName, guard audit.LineGuard) bool {
	res, err := audit.SalvageChunk(env.Policy, s, source, cn, guard)
	if err != nil {
		return residual(env, out, source, "chunk could not be salvaged", err)
	}
	for _, c := range res.Conflicts {
		logf(env, stepCorruptChunks, fmt.Sprintf("refuse-conflicting-line ts=%d surviving-sha256=%s recovered-sha256=%s", c.TS, c.ArchiveDigest, c.LiveDigest), []string{source}, nil)
	}
	if res.Replaced {
		logf(env, stepCorruptChunks, fmt.Sprintf("replace-chunk salvaged=%d gap=%t", res.Salvaged, res.Lost), []string{source, res.Replacement}, nil)
		out.Items++
	}
	return true
}

// coverStaleCorrupt writes a no-gap marker for a .corrupt whose valid source is
// present, then deletes the stale bytes.
func coverStaleCorrupt(env Env, out *Outcome, s audit.Stream, corrupt string, cn audit.ChunkName) bool {
	m := audit.Marker{Chunk: cn.Stem(), VerifiedLast: cn.Last, Reason: "stale retirement artifact beside its valid chunk"}
	b, err := audit.MarshalMarker(m)
	if err != nil {
		return residual(env, out, corrupt, "marker could not be encoded", err)
	}
	if err := audit.ReplaceLocalFile(env.Policy, filepath.Join(filepath.Dir(corrupt), cn.MarkerFile()), b); err != nil {
		return residual(env, out, corrupt, "covering marker could not be written", err)
	}
	if err := os.Remove(corrupt); err != nil && !errors.Is(err, os.ErrNotExist) {
		return residual(env, out, corrupt, "stale .corrupt could not be deleted", err)
	}
	logf(env, stepCorruptChunks, "cover-stale-corrupt", []string{corrupt}, nil)
	out.Items++
	return true
}

// isRetirementRecord reports whether b is the JSON record QuarantineLocal
// writes in place of retired bytes, as opposed to legacy raw bytes.
func isRetirementRecord(b []byte) bool {
	var rec struct {
		Withheld string `json:"withheld"`
	}
	return json.Unmarshal(b, &rec) == nil && rec.Withheld != ""
}

// residual records a permanent condition and stops work on its directory.
func residual(env Env, out *Outcome, path, reason string, err error) bool {
	logf(env, stepCorruptChunks, "residual", []string{path}, err)
	out.Residuals = append(out.Residuals, Residual{Step: stepCorruptChunks, ID: filepath.Base(path), Reason: reason})
	return false
}

type damagedChunk struct {
	path string
	name audit.ChunkName
}

// damagedChunks lists the valid-named regular-file chunks in dir whose bytes
// do not decode. A chunk that cannot be read at all (permissions, I/O) is not
// damaged bytes and is left alone.
func damagedChunks(dir string, s audit.Stream) ([]damagedChunk, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []damagedChunk
	for _, e := range entries {
		cn, kind := audit.Classify(e.Name(), s.Namespace)
		if kind != audit.KindValid || !e.Type().IsRegular() || s.Namespace == audit.MissionNS && cn.Session != s.Session {
			continue
		}
		path := filepath.Join(dir, e.Name())
		_, err := audit.ReadChunkVerified(path, cn.Last)
		var readErr *audit.ChunkReadError
		if err != nil && !errors.As(err, &readErr) {
			out = append(out, damagedChunk{path, cn})
		}
	}
	return out, nil
}
