package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

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
// replacement chunk in the local archive, writes a gap marker when anything
// was lost, and deletes the damaged bytes. Nothing of the original is kept.
// A chunk that reads cleanly is left alone.
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
	evidence, err := chunkEvidence(p, s, source)
	if err != nil {
		return res, err
	}
	lo, hi := cn.First, cn.Last
	byTS := map[int64]Line{}
	lines := 0
	for _, raw := range SplitLines(data) {
		lines++
		l, ok := decodeSalvageLine(raw, guard)
		if !ok {
			continue
		}
		if l.TS < lo {
			lo = l.TS
		}
		if l.TS > hi {
			hi = l.TS
		}
		byTS[l.TS] = l
	}
	salvagedFromChunk := len(byTS)
	live, err := ReadLiveLines(p, s)
	if err != nil {
		return res, err
	}
	for _, raw := range live {
		if raw.TS < lo || raw.TS > hi {
			continue
		}
		l, ok := decodeSalvageLine(raw.Raw, guard)
		if !ok {
			continue
		}
		if _, held := byTS[l.TS]; !held {
			byTS[l.TS] = l
		}
	}
	var keep []Line
	for _, l := range byTS {
		got, err := CanonicalPayload(l.Raw)
		if err != nil {
			return res, err
		}
		if want, ok := evidence[l.TS]; ok {
			if !bytes.Equal(want, got) {
				res.Conflicts = append(res.Conflicts, LiveConflict{TS: l.TS, ArchiveDigest: digest(want), LiveDigest: digest(got)})
			}
			continue
		}
		keep = append(keep, l)
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].TS < keep[j].TS })
	sort.Slice(res.Conflicts, func(i, j int) bool { return res.Conflicts[i].TS < res.Conflicts[j].TS })
	res.Salvaged = len(keep)
	res.Lost = lines != salvagedFromChunk || len(res.Conflicts) > 0 || len(keep) == 0 ||
		keep[0].TS > lo || keep[len(keep)-1].TS < hi

	date := filepath.Base(filepath.Dir(source))
	if len(date) > len(SessionDateFormat) {
		date = date[:len(SessionDateFormat)]
	}
	dir, err := ArchiveDir(p, s, date)
	if err != nil {
		return res, err
	}
	var body bytes.Buffer
	for _, l := range keep {
		body.Write(l.Raw)
		body.WriteByte('\n')
	}
	var dest string
	if len(keep) > 0 {
		re := ChunkName{Namespace: cn.Namespace, Session: cn.Session, First: keep[0].TS, Last: keep[len(keep)-1].TS}
		dest = filepath.Join(dir, re.ChunkFile())
		res.Replacement = dest
		if dest == source {
			err = ReplaceLocalFile(p, dest, body.Bytes())
		} else {
			err = WriteLocalAtomic(p, dest, body.Bytes())
		}
		if err != nil {
			return res, fmt.Errorf("installing replacement for chunk %s: %w", source, err)
		}
	}
	if res.Lost {
		m := Marker{Chunk: cn.Stem(), Reason: "chunk bytes were damaged; surviving lines were recovered", UnrecoveredFirst: lo, UnrecoveredLast: hi}
		if len(keep) > 0 {
			m.VerifiedLast = keep[len(keep)-1].TS
		}
		mb, err := MarshalMarker(m)
		if err != nil {
			return res, err
		}
		if err := ReplaceLocalFile(p, filepath.Join(dir, cn.MarkerFile()), mb); err != nil {
			return res, fmt.Errorf("writing gap marker for chunk %s: %w", source, err)
		}
	}
	if dest != source {
		if err := os.Remove(source); err != nil {
			return res, fmt.Errorf("removing damaged chunk %s: %w", source, err)
		}
		if err := syncAuditDirectory(filepath.Dir(source)); err != nil {
			return res, fmt.Errorf("confirming removal of %s: %w", source, err)
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
// its canonical payload. A chunk that does not read is not evidence.
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
			cn, kind := Classify(e.Name(), s.Namespace)
			path := filepath.Join(dir, e.Name())
			if kind != KindValid || path == except || s.Namespace == MissionNS && cn.Session != session {
				continue
			}
			lines, err := ReadChunkVerified(path, cn.Last)
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
