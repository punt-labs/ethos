package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Quarantine retires a corrupt sealed chunk and recovers what it can, the
// sanctioned alternative to `git commit --no-verify` (docs/audit-seal.md
// §Seal failure policy). In order:
//
//  1. Retire the corrupt chunk: git mv <stem>.jsonl -> <stem>.jsonl.corrupt,
//     freeing the chunk name so the re-seal cannot clobber it.
//  2. Re-seal every line of the chunk's [first,last] range the live file
//     still holds into an ordinary content-named chunk.
//  3. Write a deterministic .quarantine marker: the retired chunk name, the
//     verified content-derived <last>, the unrecovered sub-range, the reason.
//  4. git add the .corrupt, the re-sealed chunk, and the marker.
//
// The marker is the last durable artifact, so a crash mid-verb resumes
// deterministically by artifact state:
//
//   - marker present & parses -> idempotent no-op (re-stage covered artifacts).
//   - marker absent/torn, chunk present -> fresh run (retire, re-seal, mark).
//   - marker absent/torn, .corrupt present -> resume (finish re-seal + mark);
//     a chunk already at the re-seal target that verifies is the completed
//     re-seal and is kept, not a collision.
//
// livePath is the session audit live file (session namespace) or the
// per-(mission, session) mission live log (mission namespace) the re-seal
// draws from. Returns the marker it wrote (or found).
func Quarantine(repoRoot, sealedDir string, cn ChunkName, livePath, reason string) (Marker, error) {
	stem := cn.Stem()
	chunkPath := filepath.Join(sealedDir, cn.ChunkFile())
	corruptPath := filepath.Join(sealedDir, stem+".jsonl.corrupt")
	markerPath := filepath.Join(sealedDir, cn.MarkerFile())

	// State: a parseable marker means the quarantine already completed — an
	// idempotent no-op, but NOT blind to fresh damage (OPT-1): content-verify
	// any chunk standing at a name the marker covers and retire fresh
	// corruption as a new event under a content-hashed name, then re-stage.
	if mk, err := ReadMarker(markerPath); err == nil {
		if err := reconcileUnderMarker(repoRoot, sealedDir, cn); err != nil {
			return Marker{}, err
		}
		if _, sErr := stageQuarantineArtifacts(repoRoot, sealedDir, cn); sErr != nil {
			return Marker{}, sErr
		}
		return mk, nil
	}

	// Ensure the corrupt bytes sit at <stem>.jsonl.corrupt — retire on a
	// fresh run, or find them already there on a resume.
	if _, err := os.Stat(chunkPath); err == nil {
		// A chunk AND an existing .corrupt with no covering marker is the
		// resume-window "fresh damage" state. The first event's .corrupt must
		// never be overwritten (docs §Seal failure policy), so the fresh chunk
		// is retired under a content-hashed .corrupt-<hash> name that cannot
		// collide with it (OPT-1), preserving both evidences; the re-seal +
		// marker below then complete the quarantine.
		if _, cErr := os.Stat(corruptPath); cErr == nil {
			if rErr := retireChunkUnderHash(repoRoot, sealedDir, cn, chunkPath); rErr != nil {
				return Marker{}, rErr
			}
		} else if rErr := renameRetire(repoRoot, chunkPath, corruptPath); rErr != nil {
			return Marker{}, rErr
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Marker{}, fmt.Errorf("stat %s: %w", chunkPath, err)
	} else if _, cErr := os.Stat(corruptPath); cErr != nil {
		return Marker{}, fmt.Errorf(
			"quarantine %s: neither the chunk nor its .corrupt exists", cn.ChunkFile())
	}

	corruptBytes, err := os.ReadFile(corruptPath)
	if err != nil {
		return Marker{}, fmt.Errorf("reading retired chunk %s: %w", corruptPath, err)
	}
	corruptMax := maxParseableTS(corruptBytes)

	// Re-seal the live lines still present in the corrupt chunk's range.
	reseal, rf, rl, err := liveLinesInRange(livePath, cn.First, cn.Last)
	if err != nil {
		return Marker{}, err
	}
	resealMax := cn.First - 1
	if len(reseal) > 0 {
		resealMax = rl
		re := ChunkName{Namespace: cn.Namespace, Session: cn.Session, First: rf, Last: rl}
		rePath := filepath.Join(sealedDir, re.ChunkFile())
		if _, statErr := os.Stat(rePath); statErr == nil {
			// Resume: an existing target that verifies is the completed
			// re-seal, kept as-is; one that fails is fresh damage.
			if _, vErr := ReadChunkVerified(rePath, rl); vErr != nil {
				return Marker{}, fmt.Errorf("quarantine resume: %w", vErr)
			}
		} else if err := WriteChunkAtomic(sealedDir, re.TempFile(), re.ChunkFile(), reseal); err != nil {
			return Marker{}, err
		}
	}

	verifiedLast := resealMax
	if corruptMax > verifiedLast {
		verifiedLast = corruptMax
	}
	var unrecFirst, unrecLast int64
	if verifiedLast > resealMax {
		unrecFirst = resealMax + 1
		if len(reseal) == 0 {
			unrecFirst = cn.First
		}
		unrecLast = verifiedLast
	}

	marker := Marker{
		Chunk:            stem,
		VerifiedLast:     verifiedLast,
		UnrecoveredFirst: unrecFirst,
		UnrecoveredLast:  unrecLast,
		Reason:           reason,
	}
	if err := writeMarkerAtomic(sealedDir, cn.MarkerFile(), marker); err != nil {
		return Marker{}, err
	}
	if _, err := stageQuarantineArtifacts(repoRoot, sealedDir, cn); err != nil {
		return Marker{}, err
	}
	return marker, nil
}

// stageQuarantineArtifacts git-adds every chunk-namespace artifact in the
// directory — the .corrupt, the marker, the re-sealed chunk — so the tree is
// clean and committable with no hand-staging.
func stageQuarantineArtifacts(repoRoot, sealedDir string, cn ChunkName) (int, error) {
	return StageUntrackedChunks(repoRoot, sealedDir, cn.Namespace, cn.Session)
}

// renameRetire moves src to dst via git (staging the rename) or a plain
// rename when git mv fails (an untracked source). The caller guarantees dst is
// absent, so the rename cannot clobber.
func renameRetire(repoRoot, src, dst string) error {
	if mvErr := GitMv(repoRoot, src, dst); mvErr != nil {
		if rErr := os.Rename(src, dst); rErr != nil {
			return fmt.Errorf("retiring %s: git mv: %v; rename: %w", src, mvErr, rErr)
		}
	}
	return nil
}

// corruptHashName returns the .corrupt-<hash> filename for a chunk stem and its
// content, using a deterministic never-overwrite sequence (-2, -3, …) so a
// second event never collides with the first .corrupt or an identical earlier
// .corrupt-<hash> (docs/audit-seal.md §Seal failure policy).
func corruptHashName(dir, stem string, content []byte) string {
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])[:12]
	base := stem + ".jsonl.corrupt-" + hash
	candidate := base
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(dir, candidate)); errors.Is(err, fs.ErrNotExist) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, n)
	}
}

// retireChunkUnderHash retires the chunk at chunkPath under a content-hashed
// .corrupt-<hash> name, preserving any existing .corrupt evidence.
func retireChunkUnderHash(repoRoot, sealedDir string, chunkCN ChunkName, chunkPath string) error {
	b, err := os.ReadFile(chunkPath)
	if err != nil {
		return fmt.Errorf("reading fresh-damage chunk %s: %w", chunkPath, err)
	}
	hashed := corruptHashName(sealedDir, chunkCN.Stem(), b)
	return renameRetire(repoRoot, chunkPath, filepath.Join(sealedDir, hashed))
}

// reconcileUnderMarker is the "not blind to fresh damage" half of the
// idempotent no-op (OPT-1): it content-verifies every valid chunk standing at
// a name the marker's range covers and retires a corrupt one as a new event
// under a content-hashed .corrupt-<hash> name, so fresh corruption of a
// re-sealed chunk becomes recorded evidence rather than a chunk a later
// seal/read trips over.
func reconcileUnderMarker(repoRoot, sealedDir string, cn ChunkName) error {
	entries, err := os.ReadDir(sealedDir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", sealedDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		c, kind := Classify(e.Name(), cn.Namespace)
		if kind != KindValid {
			continue
		}
		if cn.Namespace == MissionNS && c.Session != cn.Session {
			continue
		}
		// Only chunks whose range the marker covers are its concern.
		if !(cn.First <= c.First && c.Last <= cn.Last) {
			continue
		}
		p := filepath.Join(sealedDir, e.Name())
		if _, vErr := ReadChunkVerified(p, c.Last); vErr != nil {
			if rErr := retireChunkUnderHash(repoRoot, sealedDir, c, p); rErr != nil {
				return rErr
			}
		}
	}
	return nil
}

// liveLinesInRange returns the live file's complete, parseable lines whose ts
// lies within [first,last] inclusive, plus that subset's first and last ts.
// The live file is never truncated, so in the common case every line of a
// corrupt chunk's range is still here and recovery is full.
func liveLinesInRange(livePath string, first, last int64) (lines [][]byte, rf, rl int64, err error) {
	data, err := os.ReadFile(livePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, 0, nil
		}
		return nil, 0, 0, fmt.Errorf("reading live %s: %w", livePath, err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if cut := lastNewline(data); cut >= 0 {
			data = data[:cut+1]
		} else {
			data = nil
		}
	}
	for _, raw := range SplitLines(data) {
		var h tsHolder
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		ts, perr := ParseLineTS(h.TS)
		if perr != nil || ts < first || ts > last {
			continue
		}
		if len(lines) == 0 {
			rf = ts
		}
		rl = ts
		cp := make([]byte, len(raw))
		copy(cp, raw)
		lines = append(lines, cp)
	}
	return lines, rf, rl, nil
}

// maxParseableTS returns the max ts over the parseable lines of a chunk's
// (corrupt) bytes — the max ts the corruption actually reached.
func maxParseableTS(data []byte) int64 {
	var mx int64
	first := true
	for _, raw := range SplitLines(data) {
		var h tsHolder
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		ts, perr := ParseLineTS(h.TS)
		if perr != nil {
			continue
		}
		if first || ts > mx {
			mx = ts
			first = false
		}
	}
	return mx
}

// writeMarkerAtomic writes a marker via temp + fsync + rename so it never
// appears in a torn state (a torn marker reads as absent everywhere).
func writeMarkerAtomic(dir, name string, m Marker) error {
	data, err := MarshalMarker(m)
	if err != nil {
		return err
	}
	tempPath := filepath.Join(dir, "."+name+".tmp")
	f, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating temp marker %s: %w", tempPath, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("writing temp marker %s: %w", tempPath, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("syncing temp marker %s: %w", tempPath, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("closing temp marker %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("renaming temp marker: %w", err)
	}
	return nil
}
