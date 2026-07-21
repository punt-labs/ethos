package hook

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The seal watermark for a session is the maximum line timestamp already
// sealed. It is computed from the sealed directory listing — chunk names,
// a frozen legacy file scanned once, and covering quarantine markers —
// without opening any post-discipline chunk for its timestamps. The live
// writer seeds its per-session monotonic floor from this same set so no
// timestamp it mints ever sits below the watermark (docs/audit-seal.md
// §Watermark, §timestamp).

// scannedDir is the classified content of one sealed directory: the valid
// chunks (filtered to a session in the mission namespace) plus the
// covering quarantine markers. A near-miss name or an orphan .corrupt
// (a .corrupt with no covering marker) is a hard error — the seal and the
// read must fail loud rather than drop a name from the watermark.
type scannedDir struct {
	chunks  []chunkName // valid chunks, session-filtered in mission ns
	markers []chunkName // quarantine markers
}

// scanSealedDir lists dir and classifies every entry. In the mission
// namespace, session filters chunks and markers to one session id; in the
// session namespace, session is ignored (the directory is per-session).
//
// A missing directory is not an error — a session with no sealed chunks
// yet returns an empty scan. A near-miss chunk name, or a .corrupt with no
// covering marker, returns an error naming the offending file.
func scanSealedDir(dir string, ns chunkNamespace, session string) (scannedDir, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return scannedDir{}, nil
		}
		return scannedDir{}, fmt.Errorf("reading %s: %w", dir, err)
	}

	var sc scannedDir
	var corrupts []chunkName
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		cn, kind := classifyChunk(name, ns)
		if ns == missionNamespace && (kind == chunkValid || kind == chunkQuarantine || kind == chunkCorrupt) {
			if cn.Session != session {
				continue
			}
		}
		switch kind {
		case chunkValid:
			sc.chunks = append(sc.chunks, cn)
		case chunkQuarantine:
			sc.markers = append(sc.markers, cn)
		case chunkCorrupt:
			corrupts = append(corrupts, cn)
		case chunkNearMiss:
			return scannedDir{}, fmt.Errorf("malformed chunk name %q in %s", name, dir)
		case chunkTemp, chunkOther:
			// Ignored: stale temps are swept elsewhere; siblings are
			// never the seal's concern.
		}
	}

	// Every .corrupt must sit under a covering marker; a lone .corrupt is
	// a quarantine that crashed mid-verb and must fail loud so a resume
	// runs, not be passed over in silence.
	for _, c := range corrupts {
		if !coveredByMarker(c, sc.markers) {
			return scannedDir{}, fmt.Errorf(
				"orphan .corrupt for range [%d,%d] in %s: quarantine incomplete",
				c.First, c.Last, dir)
		}
	}
	return sc, nil
}

// coveredByMarker reports whether some marker's named range contains the
// artifact's named range.
func coveredByMarker(artifact chunkName, markers []chunkName) bool {
	for _, m := range markers {
		if m.First <= artifact.First && artifact.Last <= m.Last {
			return true
		}
	}
	return false
}

// sessionWatermark returns the max sealed timestamp for a session. It is
// the max over the valid chunk names' <last>, the covering markers'
// verified <last>, and a frozen legacy file's max line ts. Zero when the
// session has nothing sealed.
//
// legacyPath may be empty when the namespace has no frozen legacy file to
// scan for this session.
func sessionWatermark(dir string, ns chunkNamespace, session, legacyPath string) (int64, error) {
	sc, err := scanSealedDir(dir, ns, session)
	if err != nil {
		return 0, err
	}
	var wm int64
	for _, c := range sc.chunks {
		if c.Last > wm {
			wm = c.Last
		}
	}
	for _, m := range sc.markers {
		last, err := markerVerifiedLast(dir, ns, m)
		if err != nil {
			return 0, err
		}
		if last > wm {
			wm = last
		}
	}
	if legacyPath != "" {
		legacyMax, err := maxLegacyTS(legacyPath)
		if err != nil {
			return 0, err
		}
		if legacyMax > wm {
			wm = legacyMax
		}
	}
	return wm, nil
}

// maxLegacyTS returns the maximum ts over every line of a frozen legacy
// file. Unlike a chunk, the legacy file has no ts in its name and predates
// the monotonic-ts discipline, so its last line is not necessarily its max
// (a coarse or NTP-stepped clock may leave it low). A missing file is not
// an error and contributes zero.
func maxLegacyTS(path string) (int64, error) {
	entries, err := readAuditEntries(path)
	if err != nil {
		return 0, fmt.Errorf("scanning legacy file %s: %w", path, err)
	}
	var mx int64
	for _, e := range entries {
		ns, err := parseLineTS(e.Ts)
		if err != nil {
			// A legacy line with an unparseable ts contributes nothing;
			// it predates the discipline and readAuditEntries already
			// warned on it.
			continue
		}
		if ns > mx {
			mx = ns
		}
	}
	return mx, nil
}

// markerVerifiedLast reads a quarantine marker's verified <last>. The
// marker records the max ts the corrupt bytes actually reached — never the
// filename <last> on faith, which an inflated name could use to suppress
// every later seal. A marker that fails to parse is treated as absent and
// contributes nothing (the resume state machine will rewrite it).
func markerVerifiedLast(dir string, _ chunkNamespace, m chunkName) (int64, error) {
	// Marker file name mirrors the chunk stem with a .quarantine suffix.
	var name string
	if m.Namespace == missionNamespace {
		name = "log-" + m.Session + "-" + tsToChunkField(m.First) + "-" + tsToChunkField(m.Last) + ".quarantine"
	} else {
		name = "audit-" + tsToChunkField(m.First) + "-" + tsToChunkField(m.Last) + ".quarantine"
	}
	mk, err := readQuarantineMarker(filepath.Join(dir, name))
	if err != nil {
		// Unparseable/absent marker contributes nothing (§Seal failure
		// policy: a torn marker reads as absent everywhere).
		return 0, nil
	}
	return mk.VerifiedLast, nil
}
