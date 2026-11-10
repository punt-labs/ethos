package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// LineGuard judges one line about to be stored: the line to store, the rule it
// broke ("" when it was clean), and whether it became a loss marker.
type LineGuard func(line []byte) (out []byte, rule string, marker bool)

// ReadLiveLines returns the complete lines of a stream's live file without
// checking them against the archive, so a caller can repair a conflict that
// ReadStream would refuse. A torn tail is ignored; a missing file is empty.
func ReadLiveLines(p layout.Policy, s Stream) ([]Line, error) {
	return readCompleteLive(s.LivePath(p), s.Session)
}

// LiveConflict records a live line dropped because its ts was already
// archived with a different payload. Digests are hex SHA-256 of the canonical
// payloads; the payloads themselves are never logged.
type LiveConflict struct {
	TS            int64
	ArchiveDigest string
	LiveDigest    string
}

// LiveRepair is what RedactLive changed in one live file.
type LiveRepair struct {
	Rewritten int            // lines replaced by their redacted form
	Dropped   []LiveConflict // lines removed because the archive wins
	// Complete is true when every complete line of the file has been judged.
	// It is false only after a chunked call (limit > 0) that stopped early.
	Complete bool
}

// Changed reports whether the live file was rewritten.
func (r LiveRepair) Changed() bool { return r.Rewritten > 0 || len(r.Dropped) > 0 }

// liveCursor records how much of a live file RedactLive has judged: the byte
// length of the prefix and its SHA-256. A later call skips the prefix only if
// the file still begins with exactly those bytes, so a replaced or truncated
// file is judged again from the start.
type liveCursor struct {
	Offset int    `json:"offset"`
	SHA256 string `json:"sha256"`
}

func liveCursorPath(p layout.Policy, s Stream) string {
	return filepath.Join(p.LocalRoot(), "cursors", fmt.Sprintf("live-%x.json", sha256.Sum256([]byte(s.Key()))))
}

// readLiveCursor returns the byte length of the prefix of b already judged, or
// 0 when there is no cursor or b no longer begins with the prefix it names.
func readLiveCursor(p layout.Policy, s Stream, b []byte) int {
	raw, err := os.ReadFile(liveCursorPath(p, s))
	if err != nil {
		return 0
	}
	var c liveCursor
	if json.Unmarshal(raw, &c) != nil || c.Offset <= 0 || c.Offset > len(b) {
		return 0
	}
	if digest(b[:c.Offset]) != c.SHA256 {
		return 0
	}
	return c.Offset
}

// RedactLive makes a stream's live file safe to archive. Each complete line
// whose ts is archived with another payload is dropped (the archive wins);
// each remaining line the guard would change is replaced by the guard's
// output; every other byte, including a torn tail, stays. A line whose ts is
// archived with an equal payload is left alone: the archive already holds it.
// The caller holds the stream lock. The file is replaced atomically, and only
// when something changed.
//
// The guard costs milliseconds a line, so ctx is checked before each line;
// when it is done RedactLive writes nothing and returns ctx.Err(). With
// limit > 0 it judges at most limit lines past the prefix an earlier call
// recorded, rewrites that much, and records the new prefix, so a stream too
// large for one budget is finished across calls. Complete says whether any
// lines remain. limit == 0 judges the whole file and keeps no record.
func RedactLive(ctx context.Context, p layout.Policy, s Stream, guard LineGuard, limit int) (LiveRepair, error) {
	var res LiveRepair
	live, err := ReadLiveLines(p, s)
	if err != nil {
		return res, err
	}
	if len(live) == 0 {
		res.Complete = true
		return res, nil
	}
	path := s.LivePath(p)
	b, err := os.ReadFile(path)
	if err != nil {
		return LiveRepair{}, fmt.Errorf("reading live audit %s: %w", path, err)
	}
	ends := liveLineEnds(b)
	if len(ends) != len(live) {
		return LiveRepair{}, fmt.Errorf("live audit %s changed while it was read", path)
	}
	start, end := 0, len(live)
	if limit > 0 {
		skip := readLiveCursor(p, s, b)
		for start < len(live) && ends[start] <= skip {
			start++
		}
		if start+limit < end {
			end = start + limit
		}
	}
	res.Complete = end == len(live)
	if start == end {
		return res, nil
	}
	archived, err := archivedPayloads(p, s)
	if err != nil {
		return LiveRepair{}, err
	}
	replace := map[string][]byte{} // raw live line -> its redacted form; nil drops it
	for _, l := range live[start:end] {
		if err := ctx.Err(); err != nil {
			return LiveRepair{}, err
		}
		got, err := CanonicalPayload(l.Raw)
		if err != nil {
			return LiveRepair{}, err
		}
		out, _, _ := guard(l.Raw)
		after, err := CanonicalPayload(out)
		if err != nil {
			return LiveRepair{}, fmt.Errorf("guarded audit payload at ts %d is not a valid line: %w", l.TS, err)
		}
		if want, ok := archived[l.TS]; ok {
			// Compare the guarded form: a live line whose redaction is what
			// the archive holds is already archived, not a conflict.
			switch {
			case bytes.Equal(want, got):
			case bytes.Equal(want, after):
				replace[string(l.Raw)] = out
				res.Rewritten++
			default:
				res.Dropped = append(res.Dropped, LiveConflict{TS: l.TS, ArchiveDigest: digest(want), LiveDigest: digest(after)})
				replace[string(l.Raw)] = nil
			}
			continue
		}
		if !bytes.Equal(got, after) {
			replace[string(l.Raw)] = out
			res.Rewritten++
		}
	}
	out := b
	if res.Changed() {
		out = rewriteLive(b, replace)
		if err := ReplaceLocalFile(p, path, out); err != nil {
			return LiveRepair{}, fmt.Errorf("replacing live audit %s with its redacted lines: %w", path, err)
		}
	}
	if limit > 0 {
		// The prefix through the last judged line, in its stored form.
		n := len(rewriteLive(b[:ends[end-1]], replace))
		c, err := json.Marshal(liveCursor{Offset: n, SHA256: digest(out[:n])})
		if err != nil {
			return LiveRepair{}, fmt.Errorf("encoding the live audit cursor: %w", err)
		}
		if err := ReplaceLocalFile(p, liveCursorPath(p, s), c); err != nil {
			return LiveRepair{}, fmt.Errorf("recording the live audit cursor: %w", err)
		}
	}
	return res, nil
}

// liveLineEnds returns, for each complete non-blank line of b in order, the
// offset just past its newline. It mirrors readCompleteLive's choice of lines.
func liveLineEnds(b []byte) []int {
	var ends []int
	for pos := 0; pos < len(b); {
		i := bytes.IndexByte(b[pos:], '\n')
		if i < 0 {
			break
		}
		if len(bytes.TrimSpace(b[pos:pos+i])) != 0 {
			ends = append(ends, pos+i+1)
		}
		pos += i + 1
	}
	return ends
}

// rewriteLive applies replace to the complete lines of b; a nil value drops
// the line. A torn tail stays for the writer's reopen repair.
func rewriteLive(b []byte, replace map[string][]byte) []byte {
	var out bytes.Buffer
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			out.Write(b)
			break
		}
		if r, ok := replace[string(b[:i])]; !ok {
			out.Write(b[:i+1])
		} else if r != nil {
			out.Write(r)
			out.WriteByte('\n')
		}
		b = b[i+1:]
	}
	return out.Bytes()
}

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

// archivedPayloads maps each archived ts of the stream to its canonical payload.
func archivedPayloads(p layout.Policy, s Stream) (map[int64][]byte, error) {
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
		scan, err := ScanSealedDir(dir, s.Namespace, session)
		if err != nil {
			return nil, err
		}
		for _, c := range scan.Chunks {
			lines, err := ReadChunkVerified(filepath.Join(dir, c.ChunkFile()), c.Last)
			if err != nil {
				// A chunk that does not decode is not evidence; salvaging it is
				// the corrupt-chunk step's job, and a later read still reports it.
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
