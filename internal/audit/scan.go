package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ScannedDir is the classified content of one sealed directory: the valid
// chunks (session-filtered in the mission namespace) plus the covering
// quarantine markers. A near-miss name or an orphan .corrupt (one with no
// covering marker) is a hard error — the seal and read must fail loud rather
// than drop a name from the watermark.
type ScannedDir struct {
	Chunks  []ChunkName
	Markers []ChunkName
}

// ScanSealedDir lists dir and classifies every entry. In the mission
// namespace, session filters chunks and markers to one session id; in the
// session namespace, session is ignored (the directory is per-session).
//
// A missing directory is not an error. A near-miss chunk name, or a .corrupt
// with no covering marker, returns an error naming the offender.
func ScanSealedDir(dir string, ns Namespace, session string) (ScannedDir, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ScannedDir{}, nil
		}
		return ScannedDir{}, fmt.Errorf("reading %s: %w", dir, err)
	}
	var sc ScannedDir
	var corrupts []ChunkName
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		cn, kind := Classify(e.Name(), ns)
		// In the mission namespace a non-empty session filters to one
		// session's artifacts; an empty session means "every session"
		// (the read unions all).
		if ns == MissionNS && session != "" &&
			(kind == KindValid || kind == KindQuarantine || kind == KindCorrupt) {
			if cn.Session != session {
				continue
			}
		}
		switch kind {
		case KindValid:
			sc.Chunks = append(sc.Chunks, cn)
		case KindQuarantine:
			// A marker covers its .corrupt only if it PARSES: a torn or
			// garbage-content marker with a valid name is treated as absent by
			// every consumer (docs/audit-seal.md §Watermark, §read, resume
			// state machine), so its .corrupt stays an uncovered orphan and the
			// corrupt-coverage check below fails loud (exit 2) rather than
			// silently dropping the marker's verified_last and gap.
			if _, mErr := ReadMarker(filepath.Join(dir, cn.MarkerFile())); mErr == nil {
				sc.Markers = append(sc.Markers, cn)
			}
		case KindCorrupt:
			corrupts = append(corrupts, cn)
		case KindNearMiss:
			return ScannedDir{}, fmt.Errorf("malformed chunk name %q in %s", e.Name(), dir)
		case KindTemp, KindOther:
			// Stale temps are swept elsewhere; siblings are never the seal's concern.
		}
	}
	for _, c := range corrupts {
		if !coveredByMarker(c, sc.Markers) {
			return ScannedDir{}, fmt.Errorf(
				"orphan .corrupt for range [%d,%d] in %s: quarantine incomplete",
				c.First, c.Last, dir)
		}
	}
	return sc, nil
}

// coveredByMarker reports whether some marker of the SAME session contains the
// artifact's named range. Range nesting alone is not enough: a mission union
// read scans with an empty session filter, so both a marker and a .corrupt from
// different sessions can share a directory. Requiring m.Session == artifact.Session
// keeps one session's marker from covering another session's orphan .corrupt,
// which would silence the fail-loud incomplete-quarantine check. In the session
// namespace both Session fields are empty and the directory is single-session,
// so the equality holds trivially and current behavior stands.
func coveredByMarker(artifact ChunkName, markers []ChunkName) bool {
	for _, m := range markers {
		if m.Session == artifact.Session &&
			m.First <= artifact.First && artifact.Last <= m.Last {
			return true
		}
	}
	return false
}

// Watermark returns the sealed watermark for a session: the max ts already
// captured in immutable chunks — the max over the valid chunk names' <last> and
// the covering markers' verified <last>. Zero when nothing is sealed.
//
// This is the tail-selection boundary. A live line seals (and reads) exactly
// when its ts is strictly past this watermark. It deliberately EXCLUDES the
// frozen legacy files: those are read directly as the oldest pool and their
// lines never live in the per-session live file, so folding their max ts in
// here would strand live lines whose ts sits below a later-growing legacy max
// (a shared mission log.jsonl a sessionless writer extends) — SelectLiveTail
// and LiveLinesPastWatermark would skip them forever. The legacy max belongs
// only to the monotonic append floor; see MonotonicFloor.
func Watermark(dir string, ns Namespace, session string) (int64, error) {
	sc, err := ScanSealedDir(dir, ns, session)
	if err != nil {
		return 0, err
	}
	var wm int64
	for _, c := range sc.Chunks {
		if c.Last > wm {
			wm = c.Last
		}
	}
	for _, m := range sc.Markers {
		mk, err := ReadMarker(filepath.Join(dir, m.MarkerFile()))
		if err != nil {
			// A torn/absent marker contributes nothing.
			continue
		}
		if mk.VerifiedLast > wm {
			wm = mk.VerifiedLast
		}
	}
	return wm, nil
}

// MonotonicFloor returns the floor for minting a new strictly-monotonic live
// timestamp: the max of the sealed Watermark and every frozen legacy file's max
// line ts. A new line's ts is allocated strictly above this floor so it sorts
// after both already-sealed lines and frozen pre-discipline history.
//
// Unlike Watermark this is NOT a tail-selection boundary — it only seeds
// AppendMonotonic. Keeping the legacy max out of Watermark and in here is the
// point: a shared legacy log a sessionless writer later extends must raise the
// append floor (so new lines still sort last) without retroactively hiding
// another session's already-written live lines from the seal.
//
// legacyPaths are frozen files (an audit.jsonl, or a log.jsonl plus a drained
// missions/<id>.jsonl residue) scanned once for their max ts; each predates the
// monotonic-ts discipline, so the max over all lines contributes, not the last.
func MonotonicFloor(dir string, ns Namespace, session string, legacyPaths ...string) (int64, error) {
	wm, err := Watermark(dir, ns, session)
	if err != nil {
		return 0, err
	}
	for _, lp := range legacyPaths {
		if lp == "" {
			continue
		}
		mx, err := MaxLegacyTS(lp)
		if err != nil {
			return 0, err
		}
		if mx > wm {
			wm = mx
		}
	}
	return wm, nil
}

// MaxLegacyTS returns the maximum ts over every line of a frozen legacy file.
// A missing file is not an error and contributes zero. An unparseable line
// contributes nothing.
func MaxLegacyTS(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("scanning legacy file %s: %w", path, err)
	}
	var mx int64
	for _, raw := range SplitLines(data) {
		var h tsHolder
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		ns, err := ParseLineTS(h.TS)
		if err != nil {
			continue
		}
		if ns > mx {
			mx = ns
		}
	}
	return mx, nil
}

// SplitLines splits data on newline, dropping the empty run after a trailing
// terminator. Every returned slice is a non-empty line.
func SplitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
