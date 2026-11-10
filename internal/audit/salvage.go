package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// SalvageResult is what SalvageChunk did to one corrupt chunk.
type SalvageResult struct {
	Replaced    bool           // the chunk was damaged and has been replaced
	Salvaged    int            // lines installed in the replacement chunk
	Lost        bool           // some line could not be recovered; a gap marker records the range
	Conflicts   []LiveConflict // recovered lines refused because surviving evidence disagrees
	Replacement string         // path of the replacement chunk, "" when no line survived
}

// SalvageChunk repairs a sealed chunk whose bytes no longer decode (a torn
// write, a hand edit that left an invalid escape). Under the stream lock held
// by the caller it keeps every line that still decodes, adds the live lines of
// the chunk's range, redacts each through guard, drops a line whose ts is
// already held by another chunk with a different payload (the surviving
// evidence wins; both digests are returned), installs the rest as a
// replacement chunk in the local archive and reads it back, writes a gap
// marker when anything was lost, writes the shared availability marker beside
// a published source, and only then deletes the damaged bytes. Nothing of the
// original is kept. A chunk that reads cleanly is left alone.
func SalvageChunk(p layout.Policy, s Stream, source string, cn ChunkName, guard LineGuard) (SalvageResult, error) {
	var res SalvageResult
	if err := s.Validate(); err != nil {
		return res, err
	}
	if _, err := auditReadFilePresent(source); err != nil {
		return res, err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return res, fmt.Errorf("reading chunk %s: %w", source, err)
	}
	if _, err := ReadChunkVerified(source, cn.Last); err == nil {
		return res, nil
	}
	r, err := recoverLines(p, s, data, cn, source, guard)
	if err != nil {
		return res, err
	}
	res.Conflicts, res.Salvaged, res.Lost = r.conflicts, len(r.keep), r.lost

	date := filepath.Base(filepath.Dir(source))
	if len(date) > len(SessionDateFormat) {
		date = date[:len(SessionDateFormat)]
	}
	dir, err := ArchiveDir(p, s, date)
	if err != nil {
		return res, err
	}
	dest, err := installRecovered(p, dir, cn, r.keep, source)
	if err != nil {
		return res, err
	}
	res.Replacement = dest
	m := Marker{Chunk: cn.Stem(), Reason: "chunk bytes were damaged; surviving lines were recovered", VerifiedLast: r.verifiedLast}
	if res.Lost {
		m.UnrecoveredFirst, m.UnrecoveredLast = r.lo, r.hi
		mb, err := MarshalMarker(m)
		if err != nil {
			return res, err
		}
		if err := ReplaceLocalFile(p, filepath.Join(dir, cn.MarkerFile()), mb); err != nil {
			return res, fmt.Errorf("writing gap marker for chunk %s: %w", source, err)
		}
	}
	if publishedSource(p, source) {
		// The published copy loses the whole named range whatever was
		// recovered locally; a reader of that copy alone sees the hole.
		shared := m
		shared.UnrecoveredFirst, shared.UnrecoveredLast = r.lo, r.hi
		if err := writeSharedMarker(p, source, cn, shared); err != nil {
			return res, fmt.Errorf("writing shared marker for chunk %s: %w", source, err)
		}
	}
	if dest != source {
		if err := removeSource(source); err != nil {
			return res, err
		}
	}
	res.Replaced = true
	return res, nil
}

// decodeSalvageLine returns the guarded form of raw when raw is a decodable
// audit line with a usable ts.
func decodeSalvageLine(raw []byte, guard LineGuard) (Line, bool) {
	if _, err := CanonicalPayload(raw); err != nil {
		return Line{}, false
	}
	var h tsHolder
	if err := json.Unmarshal(raw, &h); err != nil {
		return Line{}, false
	}
	ts, err := ParseLineTS(h.TS)
	if err != nil {
		return Line{}, false
	}
	out, _, _ := guard(raw)
	if _, err := CanonicalPayload(out); err != nil {
		return Line{}, false
	}
	return Line{TS: ts, Raw: out}, true
}

// chunkEvidence maps each ts held by the stream's other readable chunks to
// its canonical payload. A chunk that does not read is not evidence, and
// neither is the chunk being recovered (except).
func chunkEvidence(p layout.Policy, s Stream, except string) (map[int64][]byte, error) {
	dirs, err := StreamDirs(p, s)
	if err != nil {
		return nil, err
	}
	session := ""
	if s.Namespace == MissionNS {
		session = s.Session
	}
	out := map[int64][]byte{}
	for _, dir := range dirs {
		entries, err := readDirIfPresent(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			other, kind := Classify(e.Name(), s.Namespace)
			path := filepath.Join(dir, e.Name())
			if kind != KindValid || path == except || s.Namespace == MissionNS && other.Session != session {
				continue
			}
			// A symlink, directory or FIFO under a chunk's name is not
			// evidence, and opening a FIFO would block; it is left alone.
			if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
				continue
			}
			lines, err := ReadChunkVerified(path, other.Last)
			if err != nil {
				continue
			}
			for _, l := range lines {
				b, err := CanonicalPayload(l.Raw)
				if err != nil {
					return nil, err
				}
				out[l.TS] = b
			}
		}
	}
	return out, nil
}
