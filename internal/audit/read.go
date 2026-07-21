package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
)

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

// LegacyLines reads a frozen legacy file (audit.jsonl or log.jsonl) as
// ts-tagged lines, kept undeduped and untagged by session — the frozen pool
// predates the monotonic-ts discipline. Unlike a chunk, a legacy file keeps
// the tolerant rule: a torn final line is dropped and a terminated
// unparseable line is skipped, never an error. A missing file yields nil.
func LegacyLines(path string) ([]Line, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading legacy %s: %w", path, err)
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

// StableSortByTS sorts lines by ts, stably, so equal-ts legacy lines keep
// their file order.
func StableSortByTS(lines []Line) {
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].TS < lines[j].TS })
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
