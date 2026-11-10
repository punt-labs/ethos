package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

type localRetirement struct {
	Withheld      string `json:"withheld"`
	SHA256        string `json:"sha256"`
	Size          int    `json:"size"`
	VerifiedFirst int64  `json:"verified_first,omitempty"`
	VerifiedLast  int64  `json:"verified_last"`
}

const localRetirementWithheld = "raw corrupt bytes retained locally"

func (r localRetirement) validate() error {
	if r.Withheld != localRetirementWithheld {
		return errors.New("invalid local retirement record kind")
	}
	if digest, err := hex.DecodeString(r.SHA256); err != nil || len(digest) != sha256.Size || r.Size < 0 {
		return errors.New("invalid local retirement digest or size")
	}
	return nil
}

// QuarantineLocal retires only the selected chunk. The caller holds its stream
// lock and supplies a redacted reason and recovered-line guard. Recovered lines
// are redacted by the guard, and a line that contradicts a surviving chunk is
// not installed. The damaged bytes are held in S only while recovery runs and
// are deleted when it completes. For a published source a conservative availability
// marker is installed at that location BEFORE removal, without touching Git's
// index. Local recovery never proves that the shared snapshot was repaired.
func QuarantineLocal(p layout.Policy, s Stream, source, dir string, cn ChunkName, reason string, guard func([]byte) ([]byte, string, bool)) (Marker, error) {
	if err := s.Validate(); err != nil {
		return Marker{}, err
	}
	if guard == nil {
		return Marker{}, errors.New("quarantine requires a recovered-line guard")
	}
	// Missing sources can resume a prior retirement. A symlink or special file
	// is not missing or damaged evidence: refuse before preparing any recovery.
	if _, err := auditReadFilePresent(source); err != nil {
		return Marker{}, err
	}
	if err := layout.EnsureLocalDir(p, dir); err != nil {
		return Marker{}, err
	}
	markerPath := filepath.Join(dir, cn.MarkerFile())
	retiredPath := filepath.Join(dir, CorruptName(cn.Stem()))
	// A missing or malformed marker permits recovery, but an unreadable one
	// may hold higher bounds. Read both markers before replacing any evidence
	// or taking the completed-recovery shortcut, matching ScanSealedDir's
	// distinction between filesystem failures and malformed marker contents.
	prior, markerErr := ReadMarker(markerPath)
	var pathErr *os.PathError
	if !errors.Is(markerErr, os.ErrNotExist) && errors.As(markerErr, &pathErr) {
		return Marker{}, markerErr
	}
	if markerErr == nil && prior.Chunk != cn.Stem() {
		return Marker{}, errors.New("quarantine marker covers a different chunk")
	}
	var previous []Marker
	if markerErr == nil {
		previous = append(previous, prior)
	}
	rel, err := filepath.Rel(ArchiveRoot(p), source)
	published := err != nil || !filepath.IsLocal(rel)
	sharedPath := filepath.Join(filepath.Dir(source), cn.MarkerFile())
	if published {
		shared, err := ReadMarker(sharedPath)
		if !errors.Is(err, os.ErrNotExist) && errors.As(err, &pathErr) {
			return Marker{}, err
		}
		if err == nil {
			if shared.Chunk != cn.Stem() {
				return Marker{}, errors.New("published quarantine marker covers a different chunk")
			}
			previous = append(previous, shared)
		}
	}
	current, readErr := os.ReadFile(source)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return Marker{}, readErr
	}
	damaged := false
	if readErr == nil {
		_, err := ReadChunkVerified(source, cn.Last)
		damaged = err != nil
	}
	var rec localRetirement
	if damaged {
		rec = localRetirement{Withheld: localRetirementWithheld, SHA256: fmt.Sprintf("%x", sha256.Sum256(current)), Size: len(current)}
		for _, raw := range SplitLines(current) {
			var h tsHolder
			if json.Unmarshal(raw, &h) != nil {
				continue
			}
			ts, err := ParseLineTS(h.TS)
			if err != nil {
				continue
			}
			if rec.VerifiedFirst == 0 || ts < rec.VerifiedFirst {
				rec.VerifiedFirst = ts
			}
			if ts > rec.VerifiedLast {
				rec.VerifiedLast = ts
			}
		}
	} else {
		b, err := os.ReadFile(retiredPath)
		if err != nil {
			return Marker{}, fmt.Errorf("reading quarantine retirement: %w", err)
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			return Marker{}, fmt.Errorf("invalid local retirement record %s: %w", retiredPath, err)
		}
	}
	if err := rec.validate(); err != nil {
		return Marker{}, fmt.Errorf("local retirement %s: %w", retiredPath, err)
	}
	// Every repeated retirement invalidates its old completion before removing
	// the source. Only the final recovery write restores the matching digest.
	if !damaged && markerErr == nil && prior.RetiredSHA256 == rec.SHA256 {
		return prior, syncAuditParents(p, markerPath)
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(s.Key())))
	rawPath := filepath.Join(p.LocalRoot(), "quarantine", key, rec.SHA256+".corrupt")
	if damaged {
		if err := WriteLocalAtomic(p, rawPath, current); err != nil {
			return Marker{}, err
		}
	}
	// The raw bytes are kept only until recovery completes, so a retry that
	// finds them gone has nothing left to compare against.
	raw, err := os.ReadFile(rawPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Marker{}, fmt.Errorf("reading quarantine original: %w", err)
	}
	if err == nil && (fmt.Sprintf("%x", sha256.Sum256(raw)) != rec.SHA256 || len(raw) != rec.Size) {
		return Marker{}, errors.New("quarantine original does not match retirement metadata")
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return Marker{}, err
	}
	// Both evidence and its metadata survive repeated damage. The conventional
	// .corrupt diagnostic is only the latest recovery pointer, never raw bytes.
	if err := WriteLocalAtomic(p, rawPath+".json", append(body, '\n')); err != nil {
		return Marker{}, err
	}
	if err := ReplaceLocalFile(p, retiredPath, append(body, '\n')); err != nil {
		return Marker{}, err
	}
	lo, hi := cn.First, cn.Last
	verifiedLast := rec.VerifiedLast
	if rec.VerifiedFirst != 0 && rec.VerifiedFirst < lo {
		lo = rec.VerifiedFirst
	}
	if rec.VerifiedLast > hi {
		hi = rec.VerifiedLast
	}
	for _, m := range previous {
		if m.UnrecoveredFirst != 0 && m.UnrecoveredFirst < lo {
			lo = m.UnrecoveredFirst
		}
		if m.UnrecoveredLast > hi {
			hi = m.UnrecoveredLast
		}
		if m.VerifiedLast > hi {
			hi = m.VerifiedLast
		}
		if m.VerifiedLast > verifiedLast {
			verifiedLast = m.VerifiedLast
		}
	}
	if published {
		if err := writeSharedMarker(p, source, cn, Marker{Chunk: cn.Stem(), VerifiedLast: verifiedLast, UnrecoveredFirst: lo, UnrecoveredLast: hi, Reason: reason}); err != nil {
			return Marker{}, err
		}
	}
	if damaged {
		if markerErr == nil {
			// Content addressing identifies evidence, not a retirement attempt.
			// Even identical damaged bytes must not reuse the completion of a
			// previous recovery after the recovered archive is removed again.
			pending := prior
			pending.RetiredSHA256 = ""
			pending.VerifiedLast = verifiedLast
			body, err := MarshalMarker(pending)
			if err != nil {
				return Marker{}, err
			}
			if err := ReplaceLocalFile(p, markerPath, body); err != nil {
				return Marker{}, err
			}
		}
		if err := os.Remove(source); err != nil {
			return Marker{}, err
		}
		if err := syncAuditDirectory(filepath.Dir(source)); err != nil {
			return Marker{}, err
		}
	}
	// Recover from the damaged bytes themselves (when they are still held) and
	// the live lines of the range, the same way SalvageChunk does. With the raw
	// bytes gone, recovery can only draw on the live file.
	r, err := recoverLines(p, s, raw, ChunkName{Namespace: cn.Namespace, Session: cn.Session, First: lo, Last: hi}, source, guard)
	if err != nil {
		return Marker{}, err
	}
	lo, hi = r.lo, r.hi
	if r.verifiedLast > verifiedLast {
		verifiedLast = r.verifiedLast
	}
	lines := r.keep
	m := Marker{Chunk: cn.Stem(), VerifiedLast: verifiedLast, Reason: reason, RetiredSHA256: rec.SHA256}
	// With the raw bytes already deleted, the retired content can be vouched
	// for only by a replacement an earlier run installed for this range; the
	// live file cannot say which interior lines the damaged bytes held.
	installedBefore := false
	if raw == nil && rec.Size > 0 {
		installedBefore, err = replacementInstalled(dir, cn, lo, hi)
		if err != nil {
			return Marker{}, err
		}
	}
	if _, err := installRecovered(p, dir, cn, lines, source); err != nil {
		return Marker{}, err
	}
	// Loss is recorded whenever the recovered lines do not account for the
	// retired range (a recovered line was refused, a bound is held nowhere,
	// or the raw bytes are gone with no earlier replacement to vouch for
	// them), or a prior marker already recorded a gap. A clean completion is
	// never written over unaccounted content.
	if r.lost || raw == nil && rec.Size > 0 && !installedBefore || markerErr == nil && prior.HasGap() {
		// One interval cannot encode disjoint losses. Include content-derived
		// endpoints, rather than declaring recovery based on an untrusted name.
		m.UnrecoveredFirst, m.UnrecoveredLast = lo, hi
	}
	body, err = json.Marshal(m)
	if err != nil {
		return Marker{}, err
	}
	if err := ReplaceLocalFile(p, markerPath, append(body, '\n')); err != nil {
		return Marker{}, err
	}
	// Recovery is complete: the damaged bytes are no longer needed, and may hold
	// text redaction never saw, so they are deleted rather than kept.
	for _, path := range []string{rawPath, rawPath + ".json"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return Marker{}, fmt.Errorf("deleting damaged chunk bytes %s: %w", path, err)
		}
	}
	return m, nil
}

// replacementInstalled reports whether dir holds a readable chunk whose range
// lies within [lo, hi]: the replacement an earlier recovery of this retirement
// installed. The damaged source itself never reads, so it never qualifies,
// even under the same name.
func replacementInstalled(dir string, cn ChunkName, lo, hi int64) (bool, error) {
	entries, err := readDirIfPresent(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		other, kind := Classify(e.Name(), cn.Namespace)
		if kind != KindValid || other.Session != cn.Session || other.First < lo || other.Last > hi {
			continue
		}
		if info, err := os.Lstat(filepath.Join(dir, e.Name())); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if _, err := ReadChunkVerified(filepath.Join(dir, e.Name()), other.Last); err == nil {
			return true, nil
		}
	}
	return false, nil
}
