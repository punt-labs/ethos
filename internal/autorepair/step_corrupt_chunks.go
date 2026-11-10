package autorepair

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/hook"
)

const stepCorruptChunks = "corrupt-chunks"

func init() { Register(Step{Name: stepCorruptChunks, Scope: ScopeRepository, Run: runCorruptChunks}) }

// runCorruptChunks is step 7: a sealed chunk whose bytes no longer decode
// (torn write, hand redaction that left an invalid escape) is replaced by a
// chunk of its redacted, decodable lines. A recovered line that conflicts with
// surviving evidence is not installed; both digests are logged. The damaged
// bytes are deleted. Items counts chunks replaced.
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
			continue
		}
		damaged, orphans, err := damagedChunks(dirs, s)
		if err != nil {
			logf(env, stepCorruptChunks, "scan-chunks", nil, err)
			continue
		}
		if len(damaged) == 0 && len(orphans) == 0 {
			continue
		}
		busy, err := audit.TryWithStreamLock(env.Policy, s, func() error {
			for _, o := range orphans {
				_, err := audit.QuarantineLocal(env.Policy, s, o.path, filepath.Dir(o.path), o.name, orphanReason, hook.LiveLineGuard(env.Policy, s.Namespace))
				if err != nil {
					logf(env, stepCorruptChunks, "complete-orphan-corrupt", []string{o.path}, err)
					out.Residuals = append(out.Residuals, Residual{Step: stepCorruptChunks, ID: filepath.Base(o.path), Reason: "interrupted quarantine could not be completed"})
					continue
				}
				logf(env, stepCorruptChunks, "complete-orphan-corrupt", []string{o.path}, nil)
				out.Items++
			}
			for _, d := range damaged {
				res, err := audit.SalvageChunk(env.Policy, s, d.path, d.name, hook.LiveLineGuard(env.Policy, s.Namespace))
				if err != nil {
					logf(env, stepCorruptChunks, "salvage-chunk", []string{d.path}, err)
					out.Residuals = append(out.Residuals, Residual{Step: stepCorruptChunks, ID: filepath.Base(d.path), Reason: "chunk could not be salvaged"})
					continue
				}
				for _, c := range res.Conflicts {
					logf(env, stepCorruptChunks, fmt.Sprintf("refuse-conflicting-line ts=%d surviving-sha256=%s recovered-sha256=%s", c.TS, c.ArchiveDigest, c.LiveDigest), []string{d.path}, nil)
				}
				if res.Replaced {
					logf(env, stepCorruptChunks, fmt.Sprintf("replace-chunk salvaged=%d gap=%t", res.Salvaged, res.Lost), []string{d.path, res.Replacement}, nil)
					out.Items++
				}
			}
			return nil
		})
		if err != nil {
			logf(env, stepCorruptChunks, "lock-stream", nil, err)
			continue
		}
		if busy {
			out.Deferred = true
			logf(env, stepCorruptChunks, "defer-busy-stream", nil, nil)
		}
	}
	return out, nil
}

type damagedChunk struct {
	path string
	name audit.ChunkName
}

// orphanReason is the marker reason recorded when the step completes a
// quarantine an older binary left unfinished.
const orphanReason = "interrupted quarantine completed by the storage upgrade"

// damagedChunks lists the valid-named chunks in dirs whose bytes do not decode,
// and the orphan .corrupt retirements (a .corrupt with no covering marker, left
// by an interrupted quarantine) whose source chunk is gone. A chunk that cannot
// be read at all (permissions, I/O) is not damaged bytes and is left alone.
func damagedChunks(dirs []string, s audit.Stream) (damaged, orphans []damagedChunk, err error) {
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		var corrupts []audit.ChunkName
		var markers []audit.ChunkName
		for _, e := range entries {
			cn, kind := audit.Classify(e.Name(), s.Namespace)
			if e.IsDir() || s.Namespace == audit.MissionNS && (kind == audit.KindValid || kind == audit.KindCorrupt || kind == audit.KindQuarantine) && cn.Session != s.Session {
				continue
			}
			switch kind {
			case audit.KindValid:
				path := filepath.Join(dir, e.Name())
				_, err := audit.ReadChunkVerified(path, cn.Last)
				var readErr *audit.ChunkReadError
				if err != nil && !errors.As(err, &readErr) {
					damaged = append(damaged, damagedChunk{path, cn})
				}
			case audit.KindCorrupt:
				corrupts = append(corrupts, cn)
			case audit.KindQuarantine:
				if _, err := audit.ReadMarker(filepath.Join(dir, cn.MarkerFile())); err == nil {
					markers = append(markers, cn)
				}
			}
		}
		for _, cn := range corrupts {
			if coveredBy(cn, markers) {
				continue
			}
			if _, err := os.Lstat(filepath.Join(dir, cn.ChunkFile())); err == nil {
				continue // the source still exists; the KindValid branch owns it
			}
			orphans = append(orphans, damagedChunk{filepath.Join(dir, cn.ChunkFile()), cn})
		}
	}
	return damaged, orphans, nil
}

// coveredBy reports whether a marker of the same session names cn's stem.
func coveredBy(cn audit.ChunkName, markers []audit.ChunkName) bool {
	for _, m := range markers {
		if m.Stem() == cn.Stem() {
			return true
		}
	}
	return false
}
