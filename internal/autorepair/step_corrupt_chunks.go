package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

const stepCorruptChunks = "corrupt-chunks"

func init() { Register(Step{Name: stepCorruptChunks, Scope: ScopeRepository, Run: runCorruptChunks}) }

// orphanReason is the marker reason recorded when the step completes a
// retirement an older binary left unfinished.
const orphanReason = "interrupted quarantine completed by the storage upgrade"

// maxRepairPasses bounds the scan-repair loop per stream directory; every
// pass that makes progress removes at least one offending artifact, so a
// directory needs at most as many passes as it has artifacts. A variable so a
// test can lower it.
var maxRepairPasses = 64

// runCorruptChunks is step 7. For every stream it repairs each archive
// directory until audit.ScanSealedDir reads it cleanly: a sealed chunk whose
// bytes no longer decode is replaced by a chunk of its redacted, decodable
// lines; a retirement an interrupted quarantine left without its final marker
// is completed; a legacy raw .corrupt is recovered through the guard (a valid
// chunk of the same stem is evidence for it), covered by a marker and deleted;
// raw bytes held under quarantine/<key>/ with no usable retirement record are
// recovered the same way. A recovered line that conflicts with surviving
// evidence is not installed; both digests are logged. Items counts repairs
// applied. A busy stream lock, a dirty scan and an unfinished pass budget
// defer; a malformed name, a non-regular file and a pass that changes nothing
// are residuals.
func runCorruptChunks(ctx context.Context, env Env) (Outcome, error) {
	var out Outcome
	if !env.InRepository() {
		return out, nil
	}
	streams, err := audit.ListStreamsByName(env.Policy)
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
				if reason, ok := permanentScanReason(err); ok {
					out.Residuals = append(out.Residuals, Residual{Step: stepCorruptChunks, ID: filepath.Base(dir), Reason: reason})
				} else {
					out.Deferred = true
				}
				continue
			}
			dirty = dirty || d
		}
		held, err := heldOrphans(env, s, dirs)
		if err != nil {
			logf(env, stepCorruptChunks, "scan-held-quarantine", nil, err)
			out.Deferred = true
			continue
		}
		if !dirty && len(held) == 0 {
			continue
		}
		guard := liveLineGuard(env.Policy, s.Namespace)
		busy, err := audit.TryWithStreamLock(env.Policy, s, func() error {
			for _, dir := range dirs {
				repairDir(ctx, env, &out, s, dir, guard)
			}
			recoverHeld(env, &out, s, dirs, guard)
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
	if err := markerFault(dir, s); err != nil {
		return false, err
	}
	if _, err := audit.ScanSealedDir(dir, s.Namespace, s.Session); err != nil {
		var orphan *audit.OrphanCorruptError
		if errors.As(err, &orphan) {
			return true, nil
		}
		return false, err
	}
	damaged, err := damagedChunks(dir, s)
	if err != nil || len(damaged) > 0 {
		return len(damaged) > 0, err
	}
	legacy, err := legacyCorrupts(dir, s)
	return len(legacy) > 0, err
}

// repairDir drives one directory to a clean scan under the stream lock. A pass
// that repairs nothing is permanent (a residual); running out of passes while
// each made progress is transient (deferred).
func repairDir(ctx context.Context, env Env, out *Outcome, s audit.Stream, dir string, guard audit.LineGuard) {
	for pass := 0; pass < maxRepairPasses; pass++ {
		if ctx.Err() != nil {
			out.Deferred = true
			return
		}
		before := out.Items
		if err := markerFault(dir, s); err != nil {
			logf(env, stepCorruptChunks, "scan-markers", []string{dir}, err)
			if reason, ok := permanentScanReason(err); ok {
				out.Residuals = append(out.Residuals, Residual{Step: stepCorruptChunks, ID: filepath.Base(dir), Reason: reason})
			} else {
				out.Deferred = true
			}
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
			legacy, err := legacyCorrupts(dir, s)
			if err != nil {
				logf(env, stepCorruptChunks, "scan-chunks", []string{dir}, err)
				out.Deferred = true
				return
			}
			damaged = append(damaged, legacy...)
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
			if reason, ok := permanentScanReason(err); ok {
				out.Residuals = append(out.Residuals, Residual{Step: stepCorruptChunks, ID: filepath.Base(dir), Reason: reason})
			} else {
				out.Deferred = true
			}
			return
		}
		if out.Items == before {
			logf(env, stepCorruptChunks, "repair-pass-no-progress", []string{dir}, nil)
			out.Residuals = append(out.Residuals, Residual{Step: stepCorruptChunks, ID: filepath.Base(dir), Reason: "archive directory still refused after a repair pass that changed nothing"})
			return
		}
	}
	logf(env, stepCorruptChunks, "repair-passes-exhausted", []string{dir}, nil)
	out.Deferred = true
}

// permanentScanReason classifies a scan error no later run can change: a
// malformed chunk name, a non-regular file where an artifact belongs, or a
// marker that is unreadable or covers another chunk. Any
// other error (a permission or I/O failure) may pass, and is left transient.
// The reason names the class, never a path.
func permanentScanReason(err error) (string, bool) {
	var name *audit.MalformedChunkNameError
	var file *audit.NotRegularFileError
	switch {
	case errors.As(err, &name):
		return "archive directory holds a malformed chunk name", true
	case errors.As(err, &file):
		return "archive directory holds an artifact that is not a regular file", true
	case errors.Is(err, audit.ErrTornMarker):
		return "a marker at this name is unreadable; chunk left in place", true
	case errors.Is(err, audit.ErrMarkerCoversOtherChunk):
		return "a marker at this name covers another chunk", true
	}
	return "", false
}

// heldOrphans lists the raw bytes QuarantineLocal held for s that no valid
// retirement record in dirs references: their retirement record is gone or
// unusable, and nothing else will ever recover them.
func heldOrphans(env Env, s audit.Stream, dirs []string) ([]string, error) {
	heldDir := audit.HeldQuarantineDir(env.Policy, s)
	entries, err := os.ReadDir(heldDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	referenced := map[string]bool{}
	for _, dir := range dirs {
		des, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range des {
			if _, kind := audit.Classify(e.Name(), s.Namespace); kind != audit.KindCorrupt || !e.Type().IsRegular() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			if sha, ok := audit.RetirementRecordSHA(b); ok {
				referenced[sha] = true
			}
		}
	}
	var out []string
	for _, e := range entries {
		sha, ok := strings.CutSuffix(e.Name(), ".corrupt")
		if ok && e.Type().IsRegular() && !referenced[sha] {
			out = append(out, filepath.Join(heldDir, e.Name()))
		}
	}
	return out, nil
}

// recoverHeld recovers every orphaned held raw file of s through the guard and
// deletes it only once its redacted replacement and marker exist.
func recoverHeld(env Env, out *Outcome, s audit.Stream, dirs []string, guard audit.LineGuard) {
	held, err := heldOrphans(env, s, dirs)
	if err != nil {
		logf(env, stepCorruptChunks, "scan-held-quarantine", nil, err)
		out.Deferred = true
		return
	}
	for _, h := range held {
		res, err := audit.RecoverHeldQuarantine(env.Policy, s, h, guard)
		if err != nil {
			residual(env, out, h, "held quarantine bytes could not be recovered", err)
			continue
		}
		logf(env, stepCorruptChunks, fmt.Sprintf("recover-held-quarantine salvaged=%d gap=%t", res.Salvaged, res.Lost), []string{h, res.Replacement}, nil)
		out.Items++
	}
}

// repairOrphan resolves one OrphanCorruptError by what is on disk. A valid
// retirement record always resumes QuarantineLocal, whatever the source is
// (absent, a same-name replacement, damaged): that is the only path that
// knows the raw bytes it holds. Anything else is legacy raw bytes, recovered
// through the guard with the surviving chunks as evidence. It returns false
// when the step must stop on this directory (the condition is recorded).
func repairOrphan(env Env, out *Outcome, s audit.Stream, e *audit.OrphanCorruptError, guard audit.LineGuard) bool {
	cn, kind := audit.Classify(filepath.Base(e.Chunk), s.Namespace)
	if kind != audit.KindValid {
		return residual(env, out, e.Chunk, "orphan .corrupt names no chunk", nil)
	}
	corrupt := filepath.Join(e.Dir, audit.CorruptName(cn.Stem()))
	sourceInfo, sourceErr := os.Lstat(e.Chunk)
	sourcePresent := sourceErr == nil
	if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
		return failed(env, out, e.Chunk, "source chunk cannot be examined", sourceErr)
	}
	if sourcePresent && !sourceInfo.Mode().IsRegular() {
		return residual(env, out, e.Chunk, "source chunk is not a regular file", nil)
	}
	b, err := os.ReadFile(corrupt)
	if err != nil {
		return failed(env, out, corrupt, "retired bytes cannot be read", err)
	}
	if _, ok := audit.RetirementRecordSHA(b); ok {
		// An interrupted QuarantineLocal (pending or missing marker): resume it.
		_, err := audit.QuarantineLocal(env.Policy, s, e.Chunk, e.Dir, cn, orphanReason, guard)
		if err != nil {
			return failed(env, out, corrupt, "interrupted quarantine could not be completed", err)
		}
		logf(env, stepCorruptChunks, "complete-orphan-corrupt", []string{corrupt}, nil)
		out.Items++
		return true
	}
	if sourcePresent {
		if _, err := audit.ReadChunkVerified(e.Chunk, cn.Last); err != nil {
			// Damaged source beside legacy bytes: recover the source; the
			// legacy bytes are handled on the next pass.
			return salvage(env, out, s, e.Chunk, cn, guard)
		}
	}
	// Legacy raw .corrupt (the old rename-based quarantine, or a retirement
	// record that does not parse): recover its decodable lines into the local
	// archive, record what is lost, and delete it. A valid same-stem chunk is
	// evidence, not a reason to skip the recovery.
	return salvage(env, out, s, corrupt, cn, guard)
}

// salvage runs SalvageChunk on source and records the outcome. It returns
// false when the step must stop on this directory.
func salvage(env Env, out *Outcome, s audit.Stream, source string, cn audit.ChunkName, guard audit.LineGuard) bool {
	res, err := audit.SalvageChunk(env.Policy, s, source, cn, guard)
	if err != nil {
		return failed(env, out, source, "chunk could not be salvaged", err)
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

// failed records a repair error by its class. A malformed name or a
// non-regular file no later run can change: a residual. Anything else
// (permission, ENOSPC, EXDEV, EIO, a conflict a later pass resolves) may pass:
// deferred. It stops work on the directory either way.
func failed(env Env, out *Outcome, path, reason string, err error) bool {
	class, ok := permanentScanReason(err)
	if !ok {
		logf(env, stepCorruptChunks, "defer-transient", []string{path}, err)
		out.Deferred = true
		return false
	}
	if errors.Is(err, audit.ErrTornMarker) || errors.Is(err, audit.ErrMarkerCoversOtherChunk) {
		reason = class
	}
	return residual(env, out, path, reason, err)
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

// markerFault returns audit.ErrTornMarker for a marker in dir that does not
// decode, and audit.ErrMarkerCoversOtherChunk for one that names another stem.
// ScanSealedDir reads such a marker as absent, so it never reports them; the
// step must not call a directory clean, or touch it, while one exists.
func markerFault(dir string, s audit.Stream) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		cn, kind := audit.Classify(e.Name(), s.Namespace)
		if kind != audit.KindQuarantine || !e.Type().IsRegular() || s.Namespace == audit.MissionNS && cn.Session != s.Session {
			continue
		}
		m, err := audit.ReadMarker(filepath.Join(dir, e.Name()))
		var pathErr *os.PathError
		switch {
		case err == nil:
			if m.Chunk != cn.Stem() {
				return fmt.Errorf("marker %s names chunk %s: %w", e.Name(), m.Chunk, audit.ErrMarkerCoversOtherChunk)
			}
		case errors.Is(err, os.ErrNotExist):
		case errors.As(err, &pathErr):
			return err
		default:
			return fmt.Errorf("marker %s: %w: %v", e.Name(), audit.ErrTornMarker, err)
		}
	}
	return nil
}

// legacyCorrupts lists the regular-file .corrupt files in dir whose bytes are
// not a retirement or withheld record: raw bytes an older binary left behind,
// which a covering marker does not make safe to keep.
func legacyCorrupts(dir string, s audit.Stream) ([]damagedChunk, error) {
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
		if kind != audit.KindCorrupt || !e.Type().IsRegular() || s.Namespace == audit.MissionNS && cn.Session != s.Session {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if _, ok := audit.RetirementRecordSHA(b); !ok && !audit.IsWithheldRecord(b) {
			out = append(out, damagedChunk{path, cn})
		}
	}
	return out, nil
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
