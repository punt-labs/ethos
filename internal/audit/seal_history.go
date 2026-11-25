package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

type verifiedHistoryLine struct {
	TS      int64  `json:"ts"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Payload []byte `json:"payload_sha256"`
}

type verifiedHistory struct {
	Content string                `json:"content_sha256"`
	Rows    []verifiedHistoryLine `json:"rows"`
}

// ReadStreamForSeal checks the same union as ReadStream, but checkpoints the
// canonical identities of immutable chunk rows. The chunk bytes, not metadata
// or timestamp ranges, determine whether those identities can be reused.
// The caller holds the stream lock. The returned lines exclude those already
// present in the destination archive directory.
func ReadStreamForSeal(ctx context.Context, p layout.Policy, s Stream, dir string, persist bool) (StreamData, error) {
	return readStreamForSeal(ctx, p, s, dir, persist, nil)
}

// ReadStreamForPublication verifies the union under the stream lock and returns
// the verified lines by destination archive chunk, before union deduplication.
func ReadStreamForPublication(ctx context.Context, p layout.Policy, s Stream, dir string) ([][]Line, error) {
	var chunks [][]Line
	if _, err := readStreamForSeal(ctx, p, s, dir, false, &chunks); err != nil {
		return nil, err
	}
	return chunks, nil
}

func readStreamForSeal(ctx context.Context, p layout.Policy, s Stream, dir string, persist bool, publication *[][]Line) (StreamData, error) {
	history := map[string][]verifiedHistoryLine{}
	checkedBytes := map[[sha256.Size]byte]verifiedHistoryLine{}
	archived := map[int64]bool{}
	load := func(path string, last int64) ([]Line, error) {
		cache := filepath.Join(p.LocalRoot(), "cursors", "history-v1-"+digest([]byte(s.Key()+"\x00"+path))+".json")
		lines, rows, err := verifyHistoryChunk(ctx, p, path, last, cache, persist)
		if err != nil {
			return nil, err
		}
		history[path] = rows
		for i, l := range lines {
			checkedBytes[sha256.Sum256(l.Raw)] = rows[i]
		}
		if filepath.Dir(path) == dir {
			if publication != nil {
				*publication = append(*publication, lines)
			}
			for _, l := range lines {
				archived[l.TS] = true
			}
		}
		return lines, nil
	}
	loadLive := func(p layout.Policy, s Stream) ([]Line, error) {
		path := s.LivePath(p)
		if present, err := auditReadDirPresent(filepath.Dir(path)); err != nil || !present {
			return nil, err
		}
		return readCompleteLiveWith(path, s.Session, func(raw []byte) (int64, error) {
			row, ok := checkedBytes[sha256.Sum256(raw)]
			if !ok {
				ts, err := chunkLineTimestamp(raw)
				if err != nil {
					return 0, err
				}
				b, err := CanonicalPayload(raw)
				if err != nil {
					return 0, err
				}
				h := sha256.Sum256(b)
				row = verifiedHistoryLine{TS: ts, Payload: h[:]}
			}
			history[path] = append(history[path], row)
			return row.TS, nil
		})
	}
	merge := func(lines []Line) ([]Line, error) {
		return mergeLines(lines, func(l Line) ([]byte, error) {
			rows := history[l.Source]
			history[l.Source] = rows[1:]
			return rows[0].Payload, nil
		})
	}
	data, err := readStream(p, s, load, loadLive, merge)
	if err != nil {
		return StreamData{}, err
	}
	pending := data.Lines[:0]
	for _, l := range data.Lines {
		if !archived[l.TS] {
			pending = append(pending, l)
		}
	}
	data.Lines = pending
	return data, nil
}

func verifyHistoryChunk(ctx context.Context, p layout.Policy, path string, last int64, cache string, persist bool) ([]Line, []verifiedHistoryLine, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, &ChunkReadError{Path: path, Err: err}
	}
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("corrupt chunk %s: empty", path)
	}
	if data[len(data)-1] != '\n' {
		return nil, nil, fmt.Errorf("corrupt chunk %s: missing final newline (torn write)", path)
	}
	content := digest(data)
	checked := readHistoryCheckpoint(cache, data, content)
	offset := 0
	if len(checked.Rows) != 0 {
		offset = checked.Rows[len(checked.Rows)-1].End + 1
	}
	previous := len(checked.Rows)
	for offset < len(data) {
		end := offset + bytes.IndexByte(data[offset:], '\n')
		if end == offset {
			offset++
			continue
		}
		if err := ctx.Err(); err != nil {
			if persist && len(checked.Rows) > previous {
				err = errors.Join(err, writeHistoryCheckpoint(p, cache, checked))
			}
			return nil, nil, err
		}
		raw := data[offset:end]
		ts, err := chunkLineTimestamp(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("corrupt chunk %s: line %d: %w", path, len(checked.Rows)+1, err)
		}
		payload, err := CanonicalPayload(raw)
		if err != nil {
			return nil, nil, err
		}
		h := sha256.Sum256(payload)
		checked.Rows = append(checked.Rows, verifiedHistoryLine{TS: ts, Start: offset, End: end, Payload: h[:]})
		offset = end + 1
	}
	if len(checked.Rows) == 0 {
		return nil, nil, fmt.Errorf("corrupt chunk %s: empty", path)
	}
	if ts := checked.Rows[len(checked.Rows)-1].TS; ts != last {
		return nil, nil, fmt.Errorf("corrupt chunk %s: last ts %d != filename <last> %d", path, ts, last)
	}
	if persist && len(checked.Rows) > previous {
		if err := writeHistoryCheckpoint(p, cache, checked); err != nil {
			return nil, nil, err
		}
	}
	lines := make([]Line, 0, len(checked.Rows))
	for _, row := range checked.Rows {
		lines = append(lines, Line{TS: row.TS, Raw: data[row.Start:row.End]})
	}
	return lines, checked.Rows, nil
}

func readHistoryCheckpoint(path string, data []byte, content string) verifiedHistory {
	empty := verifiedHistory{Content: content}
	raw, err := os.ReadFile(path)
	if err != nil {
		return empty
	}
	checksum, body, ok := bytes.Cut(raw, []byte{'\n'})
	var checked verifiedHistory
	if !ok || string(checksum) != digest(body) || json.Unmarshal(body, &checked) != nil || checked.Content != content {
		return empty
	}
	offset := 0
	for _, row := range checked.Rows {
		for offset < len(data) && data[offset] == '\n' {
			offset++
		}
		if row.Start != offset || row.End <= offset || row.End >= len(data) || data[row.End] != '\n' ||
			bytes.IndexByte(data[offset:row.End], '\n') >= 0 || len(row.Payload) != sha256.Size {
			return empty
		}
		offset = row.End + 1
	}
	return checked
}

func writeHistoryCheckpoint(p layout.Policy, path string, checked verifiedHistory) error {
	b, err := json.Marshal(checked)
	if err != nil {
		return err
	}
	raw := append(append([]byte(digest(b)), '\n'), b...)
	if err := ReplaceLocalFile(p, path, raw); err != nil {
		return fmt.Errorf("recording verified audit history: %w", err)
	}
	return nil
}
