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
			sc.Markers = append(sc.Markers, cn)
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

// coveredByMarker reports whether some marker's named range contains the
// artifact's named range.
func coveredByMarker(artifact ChunkName, markers []ChunkName) bool {
	for _, m := range markers {
		if m.First <= artifact.First && artifact.Last <= m.Last {
			return true
		}
	}
	return false
}

// Watermark returns the max sealed timestamp for a session: the max over the
// valid chunk names' <last>, the covering markers' verified <last>, and the
// frozen legacy files' max line ts. Zero when nothing is sealed.
//
// legacyPaths are frozen files (an audit.jsonl, or a log.jsonl plus a drained
// missions/<id>.jsonl residue) scanned once for their max ts; each predates
// the monotonic-ts discipline, so the max over all lines contributes, not the
// last line.
func Watermark(dir string, ns Namespace, session string, legacyPaths ...string) (int64, error) {
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
