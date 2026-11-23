package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	// A chunked or deadline-limited call can leave a remainder.
	Complete bool
}

// Changed reports whether the live file was rewritten.
func (r LiveRepair) Changed() bool { return r.Rewritten > 0 || len(r.Dropped) > 0 }

// liveCursor records how much of a live file RedactLive has judged: the byte
// length of the prefix and its SHA-256. A later call skips the prefix only if
// the file still begins with exactly those bytes, so a replaced or truncated
// file is judged again from the start.
type liveCursor struct {
	Offset       int    `json:"offset"`
	SHA256       string `json:"sha256"`
	ArchiveStamp string `json:"archive_stamp,omitempty"`
	ArchiveMaxTS int64  `json:"archive_max_ts,omitempty"`
}

func liveCursorPath(p layout.Policy, s Stream) string {
	return filepath.Join(p.LocalRoot(), "cursors", fmt.Sprintf("live-%x.json", sha256.Sum256([]byte(s.Key()))))
}

// readLiveCursor returns the checked prefix only while both its live bytes and
// the archive evidence used to judge it are unchanged.
func readLiveCursor(path string, b []byte, archiveStamp string) liveCursor {
	raw, err := os.ReadFile(path)
	if err != nil {
		return liveCursor{}
	}
	var c liveCursor
	if json.Unmarshal(raw, &c) != nil || c.Offset <= 0 || c.Offset > len(b) || b[c.Offset-1] != '\n' {
		return liveCursor{}
	}
	if c.ArchiveStamp != archiveStamp || digest(b[:c.Offset]) != c.SHA256 {
		return liveCursor{}
	}
	return c
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
	cursor := ""
	if limit > 0 {
		cursor = liveCursorPath(p, s)
	}
	return redactLive(ctx, p, s, guard, limit, cursor, false)
}

// RedactLiveUntil saves the judged prefix on deadline so another commit can
// resume. The separate cursor belongs to this checkout and guard configuration.
// A caller must hold the stream lock and still validate pending archival and
// publication payloads before writing them out.
func RedactLiveUntil(ctx context.Context, p layout.Policy, s Stream, guard LineGuard, guardScope string) (LiveRepair, error) {
	return redactLive(ctx, p, s, guard, 0, sealLiveCursorPath(p, s, guardScope), true)
}

// ResetLiveSealCursor makes the next bounded seal reconcile from the start.
// The caller holds the stream lock.
func ResetLiveSealCursor(p layout.Policy, s Stream, guardScope string) error {
	path := sealLiveCursorPath(p, s, guardScope)
	if present, err := auditReadFilePresent(path); err != nil || !present {
		return err
	}
	return ReplaceLocalFile(p, path, []byte("{}"))
}

func sealLiveCursorPath(p layout.Policy, s Stream, guardScope string) string {
	return filepath.Join(p.LocalRoot(), "cursors", fmt.Sprintf("seal-%x.json", sha256.Sum256([]byte(s.Key()+"\x00"+p.CheckoutRecordRoot()+"\x00"+guardScope))))
}

func redactLive(ctx context.Context, p layout.Policy, s Stream, guard LineGuard, limit int, cursor string, checkpoint bool) (LiveRepair, error) {
	var res LiveRepair
	path := s.LivePath(p)
	if present, err := auditReadFilePresent(path); err != nil || !present {
		res.Complete = err == nil
		return res, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		res.Complete = true
		return res, nil
	}
	if err != nil {
		return LiveRepair{}, fmt.Errorf("reading live audit %s: %w", path, err)
	}
	tail := bytes.LastIndexByte(b, '\n') + 1
	if len(bytes.TrimSpace(b[:tail])) == 0 {
		res.Complete = true
		return res, nil
	}
	chunks, err := archivedChunks(p, s)
	if err != nil {
		return LiveRepair{}, err
	}
	var archiveStamp string
	if checkpoint {
		archiveStamp, err = archiveInventoryStamp(chunks)
		if err != nil {
			return LiveRepair{}, err
		}
	}
	var saved liveCursor
	if cursor != "" {
		saved = readLiveCursor(cursor, b[:tail], archiveStamp)
	}
	next := saved.Offset
	lineNo := bytes.Count(b[:next], []byte{'\n'})
	var archived map[int64][]byte
	archiveMaxTS := saved.ArchiveMaxTS
	if !checkpoint {
		archived, archiveMaxTS, err = archivedPayloads(chunks, true)
		if err != nil {
			return res, err
		}
	} else if saved.Offset == 0 {
		archiveMaxTS = math.MinInt64
		if len(chunks) > 0 {
			archiveMaxTS = math.MaxInt64
		}
	}
	replace := map[string][]byte{} // raw live line -> its redacted form; nil drops it
	judged := 0
	for next < tail {
		end := next + bytes.IndexByte(b[next:tail], '\n') + 1
		raw := b[next : end-1]
		lineNo++
		if len(bytes.TrimSpace(raw)) == 0 {
			next = end
			continue
		}
		if limit > 0 && judged == limit {
			break
		}
		if err := ctx.Err(); err != nil {
			if !checkpoint || err != context.DeadlineExceeded {
				return LiveRepair{}, err
			}
			break
		}
		got, err := CanonicalPayload(raw)
		if err != nil {
			return LiveRepair{}, fmt.Errorf("live audit %s line %d: %w", path, lineNo, err)
		}
		var header tsHolder
		if err := json.Unmarshal(raw, &header); err != nil {
			return LiveRepair{}, fmt.Errorf("live audit %s line %d: %w", path, lineNo, err)
		}
		ts, err := ParseLineTS(header.TS)
		if err != nil {
			return LiveRepair{}, fmt.Errorf("live audit %s line %d: %w", path, lineNo, err)
		}
		next, judged = end, judged+1
		if archived == nil && ts <= archiveMaxTS {
			archived, archiveMaxTS, err = archivedPayloads(chunks, false)
			if err != nil {
				return res, err
			}
		}
		var want []byte
		if prior, ok := archived[ts]; ok {
			want = prior
			if checkpoint {
				if bytes.Equal(prior, raw) {
					continue
				}
				want, err = CanonicalPayload(prior)
				if err != nil {
					return LiveRepair{}, err
				}
			}
			if bytes.Equal(want, got) {
				continue
			}
		}
		out, _, _ := guard(raw)
		after, err := CanonicalPayload(out)
		if err != nil {
			return LiveRepair{}, fmt.Errorf("guarded audit payload at ts %d is not a valid line: %w", ts, err)
		}
		if want != nil {
			// Compare the guarded form: a live line whose redaction is what
			// the archive holds is already archived, not a conflict.
			switch {
			case bytes.Equal(want, after):
				replace[string(raw)] = out
				res.Rewritten++
			default:
				res.Dropped = append(res.Dropped, LiveConflict{TS: ts, ArchiveDigest: digest(want), LiveDigest: digest(after)})
				replace[string(raw)] = nil
			}
			continue
		}
		if !bytes.Equal(got, after) {
			replace[string(raw)] = out
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
	if cursor != "" && judged > 0 {
		// The prefix through the last judged line, in its stored form.
		n := next
		if res.Changed() {
			n = len(rewriteLive(b[:next], replace))
		}
		if !checkpoint {
			archiveMaxTS = 0
		}
		c, err := json.Marshal(liveCursor{Offset: n, SHA256: digest(out[:n]), ArchiveStamp: archiveStamp, ArchiveMaxTS: archiveMaxTS})
		if err != nil {
			return LiveRepair{}, fmt.Errorf("encoding the live audit cursor: %w", err)
		}
		if err := ReplaceLocalFile(p, cursor, c); err != nil {
			return LiveRepair{}, fmt.Errorf("recording the live audit cursor: %w", err)
		}
	}
	res.Complete = next == tail
	return res, nil
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

type archivedChunk struct {
	path string
	last int64
}

func archivedChunks(p layout.Policy, s Stream) ([]archivedChunk, error) {
	dirs, err := StreamDirs(p, s)
	if err != nil {
		return nil, err
	}
	session := ""
	if s.Namespace == MissionNS {
		session = s.Session
	}
	var out []archivedChunk
	for _, dir := range dirs {
		scan, err := ScanSealedDir(dir, s.Namespace, session)
		if err != nil {
			return nil, err
		}
		for _, c := range scan.Chunks {
			out = append(out, archivedChunk{path: filepath.Join(dir, c.ChunkFile()), last: c.Last})
		}
	}
	return out, nil
}

// The inventory of immutable chunks is a checkpoint hint, not evidence for
// sealing. ReadStream still compares payloads before anything can be archived.
func archiveInventoryStamp(chunks []archivedChunk) (string, error) {
	h := sha256.New()
	for _, c := range chunks {
		info, err := os.Lstat(c.path)
		if err != nil {
			return "", fmt.Errorf("examining archived audit %s: %w", c.path, err)
		}
		if !info.Mode().IsRegular() {
			return "", &NotRegularFileError{Path: c.path}
		}
		fmt.Fprintf(h, "%q:%d:%d\n", c.path, info.Size(), info.ModTime().UnixNano())
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// archivedPayloads records the maximum decoded ts rather than trusting the
// chunk name: ReadChunkVerified checks the last row, not the order of rows.
func archivedPayloads(chunks []archivedChunk, canonical bool) (map[int64][]byte, int64, error) {
	out := map[int64][]byte{}
	maxTS := int64(math.MinInt64)
	for _, c := range chunks {
		lines, err := ReadChunkVerified(c.path, c.last)
		if err != nil {
			// A chunk that does not decode is not evidence; salvaging it is
			// the corrupt-chunk step's job, and a later read still reports it.
			continue
		}
		for _, l := range lines {
			b := l.Raw
			if canonical {
				b, err = CanonicalPayload(b)
				if err != nil {
					return nil, 0, err
				}
			}
			maxTS = max(maxTS, l.TS)
			out[l.TS] = b
		}
	}
	return out, maxTS, nil
}
