package audit

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	_, kind := Classify(filepath.Base(source), cn.Namespace)
	// A retired file is never "left alone": its bytes are deleted under a
	// covering marker even when they happen to decode.
	if _, err := ReadChunkVerified(source, cn.Last); err == nil && kind != KindCorrupt {
		return res, nil
	}
	date := filepath.Base(filepath.Dir(source))
	if len(date) > len(SessionDateFormat) {
		date = date[:len(SessionDateFormat)]
	}
	dir, err := ArchiveDir(p, s, date)
	if err != nil {
		return res, err
	}
	return salvageBytes(p, s, salvageInput{source: source, dir: dir, cn: cn, data: data, retired: kind == KindCorrupt, published: publishedSource(p, source)}, guard)
}

// salvageInput is the damaged bytes salvageBytes recovers and where they sit.
type salvageInput struct {
	source    string
	dir       string // archive directory the replacement and markers go to
	cn        ChunkName
	data      []byte
	retired   bool // a marker is written whether or not a line was lost
	published bool // a shared availability marker is written beside source
}

// salvageBytes is the recovery behind SalvageChunk and RecoverHeldQuarantine.
// Nothing of the damaged bytes is deleted until the replacement reads back and
// every marker that records a loss is durable.
func salvageBytes(p layout.Policy, s Stream, in salvageInput, guard LineGuard) (SalvageResult, error) {
	var res SalvageResult
	source, dir, cn := in.source, in.dir, in.cn
	r, err := recoverLines(p, s, in.data, cn, source, guard)
	if err != nil {
		return res, err
	}
	res.Conflicts, res.Salvaged, res.Lost = r.conflicts, len(r.keep), r.lost

	m := Marker{Chunk: cn.Stem(), Reason: "chunk bytes were damaged; surviving lines were recovered", VerifiedLast: r.verifiedLast}
	// A retired file (<stem>.jsonl.corrupt) is covered by nothing once its
	// bytes are gone; its marker is written whether or not a line was lost,
	// so the stem stays accounted for. Only a loss records a range.
	if res.Lost {
		m.UnrecoveredFirst, m.UnrecoveredLast = r.lo, r.hi
	}
	// writeMarkers makes every record of a loss durable. A replacement that
	// keeps the source's name overwrites the raw bytes, so it runs first.
	writeMarkers := func() error {
		if m.HasGap() || in.retired {
			mb, err := MarshalMarker(m)
			if err != nil {
				return err
			}
			if err := ReplaceLocalFile(p, filepath.Join(dir, cn.MarkerFile()), mb); err != nil {
				return fmt.Errorf("writing gap marker for chunk %s: %w", source, err)
			}
		}
		if in.published {
			// The published copy loses the whole named range whatever was
			// recovered locally; a reader of that copy alone sees the hole.
			shared := m
			shared.UnrecoveredFirst, shared.UnrecoveredLast = r.lo, r.hi
			if err := writeSharedMarker(p, source, cn, shared); err != nil {
				return fmt.Errorf("writing shared marker for chunk %s: %w", source, err)
			}
		}
		return nil
	}
	dest, err := installRecovered(p, dir, cn, r.keep, source, writeMarkers)
	if err != nil {
		return res, err
	}
	res.Replacement = dest
	if dest != source {
		if err := writeMarkers(); err != nil {
			return res, err
		}
		if err := removeSource(source); err != nil {
			return res, err
		}
	}
	res.Replaced = true
	return res, nil
}

// RecoverHeldQuarantine recovers raw bytes QuarantineLocal held under
// local/ethos/quarantine/<key>/ whose retirement record is gone or unusable.
// The chunk is named from the lines that parse (the sidecar's verified range,
// then the file's mtime, when none do), recovered through guard into the
// local archive under a marker that records the loss, and only then are the
// held bytes and their sidecar deleted.
func RecoverHeldQuarantine(p layout.Policy, s Stream, held string, guard LineGuard) (SalvageResult, error) {
	var res SalvageResult
	if err := s.Validate(); err != nil {
		return res, err
	}
	data, err := os.ReadFile(held)
	if err != nil {
		return res, fmt.Errorf("reading held quarantine bytes %s: %w", held, err)
	}
	var first, last int64
	for _, raw := range SplitLines(data) {
		var h tsHolder
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		ts, err := ParseLineTS(h.TS)
		if err != nil {
			continue
		}
		if first == 0 || ts < first {
			first = ts
		}
		if ts > last {
			last = ts
		}
	}
	if first == 0 {
		if side, err := os.ReadFile(held + ".json"); err == nil {
			var rec localRetirement
			if json.Unmarshal(side, &rec) == nil && rec.VerifiedLast > 0 {
				first, last = rec.VerifiedFirst, rec.VerifiedLast
				if first == 0 {
					first = last
				}
			}
		}
	}
	if first == 0 {
		info, err := os.Stat(held)
		if err != nil {
			return res, fmt.Errorf("examining held quarantine bytes %s: %w", held, err)
		}
		first = info.ModTime().UnixNano()
		last = first
	}
	cn := ChunkName{Namespace: s.Namespace, First: first, Last: last}
	if s.Namespace == MissionNS {
		cn.Session = s.Session
	}
	dir, err := ArchiveDir(p, s, timeFromUnixNanoDate(first))
	if err != nil {
		return res, err
	}
	res, err = salvageBytes(p, s, salvageInput{source: held, dir: dir, cn: cn, data: data, retired: true}, guard)
	if err != nil {
		return res, err
	}
	if err := os.Remove(held + ".json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, fmt.Errorf("deleting held quarantine sidecar %s: %w", held+".json", err)
	}
	return res, nil
}

// RetirementRecordSHA returns the digest a valid local retirement record
// names, and false when b is not one (legacy raw bytes, a truncated record).
func RetirementRecordSHA(b []byte) (string, bool) {
	var rec localRetirement
	if json.Unmarshal(b, &rec) != nil || rec.validate() != nil {
		return "", false
	}
	return rec.SHA256, true
}

// HeldQuarantineDir is where QuarantineLocal holds a stream's raw bytes.
func HeldQuarantineDir(p layout.Policy, s Stream) string {
	return filepath.Join(p.LocalRoot(), "quarantine", fmt.Sprintf("%x", sha256.Sum256([]byte(s.Key()))))
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
