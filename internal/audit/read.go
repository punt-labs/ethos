package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Gap records a sub-range of a quarantined chunk that the live file no longer
// held, so it was unrecoverable. The read surfaces it as an explicit marker
// so a reader sees which lines were lost to corruption and when — never a
// silent hole (docs/audit-seal.md §`ethos audit show`).
type Gap struct {
	Chunk string
	First int64
	Last  int64
}

// GapMarkers returns the unrecovered sub-ranges recorded by the quarantine
// markers in a sealed directory. A marker with no gap (full recovery) yields
// nothing. In the mission namespace, session filters to one session's markers.
func GapMarkers(dir string, ns Namespace, session string) ([]Gap, error) {
	sc, err := ScanSealedDir(dir, ns, session)
	if err != nil {
		return nil, err
	}
	var gaps []Gap
	for _, m := range sc.Markers {
		mk, err := ReadMarker(filepath.Join(dir, m.MarkerFile()))
		if err != nil {
			continue // a torn marker reads as absent
		}
		if mk.HasGap() {
			gaps = append(gaps, Gap{Chunk: mk.Chunk, First: mk.UnrecoveredFirst, Last: mk.UnrecoveredLast})
		}
	}
	return gaps, nil
}

// Line is one JSONL line tagged with its parsed timestamp and (in the mission
// namespace) its session. Raw is the exact on-disk bytes so a caller decodes
// it into its own line type. Session is empty in the session namespace, where
// the per-session directory already fixes identity.
type Line struct {
	Session string
	TS      int64
	Raw     []byte
}

// DecodeChunkStrict parses every line of a sealed chunk, tolerating no torn
// tail and no unparseable line. A chunk is written whole by the seal, so any
// deviation is corruption: a missing final newline, or any line without a
// parseable ts, is an error naming the chunk.
func DecodeChunkStrict(data []byte, path string) ([]Line, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("corrupt chunk %s: missing final newline (torn write)", path)
	}
	var out []Line
	lineNo := 0
	for _, raw := range SplitLines(data) {
		lineNo++
		var h tsHolder
		if err := json.Unmarshal(raw, &h); err != nil {
			return nil, fmt.Errorf("corrupt chunk %s: line %d: %w", path, lineNo, err)
		}
		ts, err := ParseLineTS(h.TS)
		if err != nil {
			return nil, fmt.Errorf("corrupt chunk %s: line %d: %w", path, lineNo, err)
		}
		cp := make([]byte, len(raw))
		copy(cp, raw)
		out = append(out, Line{TS: ts, Raw: cp})
	}
	return out, nil
}

// ReadChunkVerified reads a sealed chunk and verifies its content matches its
// name: it must parse whole and its last line's ts must equal the <last> in
// its filename. Corruption is surfaced as an error (exit 2), the escape being
// ethos audit quarantine.
func ReadChunkVerified(path string, expectedLast int64) ([]Line, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading chunk %s: %w", path, err)
	}
	lines, err := DecodeChunkStrict(data, path)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("corrupt chunk %s: empty", path)
	}
	if lines[len(lines)-1].TS != expectedLast {
		return nil, fmt.Errorf(
			"corrupt chunk %s: last ts %d != filename <last> %d",
			path, lines[len(lines)-1].TS, expectedLast)
	}
	return lines, nil
}

// LiveLinesPastWatermark returns the live file's complete, parseable lines
// with ts strictly past the watermark, tagged with the given session. A torn
// tail is dropped and a terminated unparseable line is skipped. A missing
// file yields no lines.
func LiveLinesPastWatermark(livePath, session string, watermark int64) ([]Line, error) {
	data, err := os.ReadFile(livePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading live %s: %w", livePath, err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if cut := lastNewline(data); cut >= 0 {
			data = data[:cut+1]
		} else {
			data = nil
		}
	}
	var out []Line
	for _, raw := range SplitLines(data) {
		var h tsHolder
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		ts, perr := ParseLineTS(h.TS)
		if perr != nil {
			continue
		}
		if ts <= watermark {
			continue
		}
		cp := make([]byte, len(raw))
		copy(cp, raw)
		out = append(out, Line{Session: session, TS: ts, Raw: cp})
	}
	return out, nil
}

// tsSessionHolder decodes the ts and session fields of a residue line. The
// superseded shared-live mission log tagged each line with its session so a
// single shared file stayed attributable; the current per-session live logs
// carry identity in the path, not the line. Lenient by default — unknown
// fields are ignored — so a residue line's session is readable even though the
// strict event decoder rejects the field.
type tsSessionHolder struct {
	TS      string `json:"ts"`
	Session string `json:"session"`
}

// MaxLastBySession returns each session's greatest chunk <last> across chunks —
// the per-session sealed watermark the legacy residue drain filters against
// (docs/audit-seal.md §Migration). A session absent from the map has no sealed
// chunk.
func MaxLastBySession(chunks []ChunkName) map[string]int64 {
	m := make(map[string]int64, len(chunks))
	for _, c := range chunks {
		if cur, ok := m[c.Session]; !ok || c.Last > cur {
			m[c.Session] = c.Last
		}
	}
	return m
}

// ResidueLinesFiltered reads the superseded shared-live design's per-checkout
// missions/<id>.jsonl residue, draining it once with a PER-LINE filter: a line
// is kept iff its ts is strictly past the max <last> of the sealed chunks
// carrying THAT LINE'S OWN session id (docs/audit-seal.md §Migration). That
// design's seal already copied some lines into log-<session>-<..> chunks, so a
// whole read would double-count every already-sealed line.
//
// "Sealed" is a per-session property, so the threshold is per session, never
// cross-session: a line whose session has no sealed chunk keeps everything, and
// a line with no readable session attribution is kept. Over-retention is
// bounded and visible in the read; a cross-session threshold would silently
// drop a lagging session's never-sealed lines whose range sits below another
// session's. Surviving lines carry no session tag — the residue enters the
// pre-discipline legacy pool undeduped and is never re-attributed.
//
// Tolerant like the live-tail read: a torn final line is dropped and a
// terminated unparseable line is skipped, never an error. A missing file
// yields nil.
func ResidueLinesFiltered(path string, sessionSealedLast map[string]int64) ([]Line, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading residue %s: %w", path, err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if cut := lastNewline(data); cut >= 0 {
			data = data[:cut+1]
		} else {
			data = nil
		}
	}
	var out []Line
	for _, raw := range SplitLines(data) {
		var h tsSessionHolder
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		ts, perr := ParseLineTS(h.TS)
		if perr != nil {
			continue
		}
		if h.Session != "" {
			if last, ok := sessionSealedLast[h.Session]; ok && ts <= last {
				continue // already copied into that session's sealed chunk
			}
		}
		cp := make([]byte, len(raw))
		copy(cp, raw)
		out = append(out, Line{TS: ts, Raw: cp})
	}
	return out, nil
}

// DedupByIdentity collapses lines sharing an identity to the first seen.
// Post-discipline lines (chunks + live tail) dedup on (session, ts) —
// loss-free, since equal identity means a byte-identical line from the same
// append-only live file. Input order is preserved for survivors.
func DedupByIdentity(in []Line) []Line {
	type key struct {
		s  string
		ts int64
	}
	seen := make(map[key]struct{}, len(in))
	out := in[:0:0]
	for _, l := range in {
		k := key{l.Session, l.TS}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, l)
	}
	return out
}

// SortChunks returns chunks ordered by First so a concatenated read is in
// time order before the stable sort.
func SortChunks(chunks []ChunkName) []ChunkName {
	out := make([]ChunkName, len(chunks))
	copy(out, chunks)
	sort.Slice(out, func(i, j int) bool {
		if out[i].First != out[j].First {
			return out[i].First < out[j].First
		}
		return out[i].Session < out[j].Session
	})
	return out
}
