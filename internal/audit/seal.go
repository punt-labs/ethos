package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// AppendMonotonic appends one line to a live file under a strictly-monotonic
// per-writer timestamp. The caller must already hold the live-zone flock. It
// reopens the file, truncates a non-newline-terminated tail, recovers the max
// ts over complete lines, seeds the floor from that and the caller-supplied
// watermark, allocates ts = max(now, floor+1ns), calls line(ts) to render the
// finished JSONL line, appends it, and fsyncs. Returns the allocated ts.
//
// line receives the allocated ts and returns the complete line bytes (no
// trailing newline); this lets a caller inject its own line type with the ts
// field set to the allocated value.
func AppendMonotonic(livePath string, watermark int64, now time.Time, line func(ts int64) ([]byte, error)) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(livePath), 0o700); err != nil {
		return 0, fmt.Errorf("creating live dir: %w", err)
	}
	f, err := os.OpenFile(livePath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, fmt.Errorf("opening %s: %w", livePath, err)
	}
	defer f.Close()

	lastTS, err := truncateTornTailAndRecover(f, livePath)
	if err != nil {
		return 0, err
	}
	floor := lastTS
	if watermark > floor {
		floor = watermark
	}
	ts := now.UnixNano()
	if ts <= floor {
		ts = floor + 1
	}
	body, err := line(ts)
	if err != nil {
		return 0, err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return 0, fmt.Errorf("seeking %s: %w", livePath, err)
	}
	if _, err := f.Write(append(body, '\n')); err != nil {
		return 0, fmt.Errorf("writing %s: %w", livePath, err)
	}
	if err := f.Sync(); err != nil {
		return 0, fmt.Errorf("syncing %s: %w", livePath, err)
	}
	return ts, nil
}

// truncateTornTailAndRecover truncates a non-newline-terminated tail on the
// open live file and returns the max ts over its complete, parseable lines. A
// terminated line that still fails to parse is skipped and counted on stderr.
func truncateTornTailAndRecover(f *os.File, path string) (int64, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seeking %s: %w", path, err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(b) == 0 {
		return 0, nil
	}
	if b[len(b)-1] != '\n' {
		cut := lastNewline(b) + 1
		if err := f.Truncate(int64(cut)); err != nil {
			return 0, fmt.Errorf("truncating torn tail of %s: %w", path, err)
		}
		if len(trimSpace(b[cut:])) != 0 {
			fmt.Fprintf(os.Stderr, "ethos: audit: %s: truncated torn trailing line on reopen\n", path)
		}
		b = b[:cut]
	}
	var maxTS int64
	skipped := 0
	for _, raw := range SplitLines(b) {
		var h tsHolder
		if json.Unmarshal(raw, &h) != nil {
			skipped++
			continue
		}
		ts, perr := ParseLineTS(h.TS)
		if perr != nil {
			skipped++
			continue
		}
		if ts > maxTS {
			maxTS = ts
		}
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "ethos: audit: %s: skipped %d unparseable line(s) on reopen\n", path, skipped)
	}
	return maxTS, nil
}

// SelectLiveTail returns the raw JSON lines of the live file with ts strictly
// past the watermark, plus the first and last ts of that tail. Complete lines
// only: a torn trailing line is dropped and a terminated unparseable line is
// skipped with a stderr count. The lines are the exact on-disk bytes, so the
// seal is a transformation-free byte copy.
func SelectLiveTail(livePath string, watermark int64) (lines [][]byte, first, last int64, err error) {
	data, err := os.ReadFile(livePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, 0, nil
		}
		return nil, 0, 0, fmt.Errorf("reading live %s: %w", livePath, err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if cut := lastNewline(data); cut >= 0 {
			data = data[:cut+1]
		} else {
			data = nil
		}
	}
	skipped := 0
	for _, raw := range SplitLines(data) {
		var h tsHolder
		if json.Unmarshal(raw, &h) != nil {
			skipped++
			continue
		}
		ts, perr := ParseLineTS(h.TS)
		if perr != nil {
			skipped++
			continue
		}
		if ts <= watermark {
			continue
		}
		if len(lines) == 0 {
			first = ts
		}
		last = ts
		cp := make([]byte, len(raw))
		copy(cp, raw)
		lines = append(lines, cp)
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "ethos: audit seal: %s: skipped %d unparseable line(s)\n", livePath, skipped)
	}
	return lines, first, last, nil
}

// WriteChunkAtomic writes lines to a temp file in dir, fsyncs it, and renames
// it to the final chunk name — so the tracked chunk never exists in a partial
// state (I11-chunk). A fresh run refuses if the target chunk already exists.
func WriteChunkAtomic(dir, tempName, chunkName string, lines [][]byte) error {
	finalPath := filepath.Join(dir, chunkName)
	if _, err := os.Stat(finalPath); err == nil {
		return fmt.Errorf("chunk %s already exists: refusing to overwrite", finalPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", finalPath, err)
	}
	tempPath := filepath.Join(dir, tempName)
	f, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating temp chunk %s: %w", tempPath, err)
	}
	for _, line := range lines {
		if _, err := f.Write(append(line, '\n')); err != nil {
			f.Close()
			_ = os.Remove(tempPath)
			return fmt.Errorf("writing temp chunk %s: %w", tempPath, err)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("syncing temp chunk %s: %w", tempPath, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("closing temp chunk %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("renaming temp chunk %s -> %s: %w", tempPath, finalPath, err)
	}
	return nil
}

// SweepStaleTemps deletes stale chunk temp files under dir before the seal
// writes its own. In a session directory the sweep is scoped to that
// session's own temps by construction; in a mission directory it is
// namespace-wide, because several sessions seal into one shared directory.
func SweepStaleTemps(dir string, ns Namespace) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("reading %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, kind := Classify(e.Name(), ns); kind != KindTemp {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing stale temp %s: %w", e.Name(), err)
		}
	}
	return nil
}

// StageUntrackedChunks git-adds every chunk-namespace artifact in dir — a
// valid chunk, a quarantine marker, or a .corrupt artifact — recovering an
// orphan a prior crashed seal left untracked. In the mission namespace,
// session filters to one session's chunks. Returns the count actually staged
// (already-clean-tracked chunks are not counted).
func StageUntrackedChunks(repoRoot, dir string, ns Namespace, session string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		cn, kind := Classify(e.Name(), ns)
		if ns == MissionNS && session != "" &&
			(kind == KindValid || kind == KindQuarantine || kind == KindCorrupt) &&
			cn.Session != session {
			continue
		}
		switch kind {
		case KindValid, KindQuarantine, KindCorrupt:
			paths = append(paths, filepath.Join(dir, e.Name()))
		case KindOther, KindNearMiss, KindTemp:
		}
	}
	if len(paths) == 0 {
		return 0, nil
	}
	pending, err := UntrackedOrModified(repoRoot, paths)
	if err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}
	if err := GitAdd(repoRoot, pending...); err != nil {
		return 0, err
	}
	return len(pending), nil
}

// lastNewline returns the index of the last '\n' in b, or -1.
func lastNewline(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return i
		}
	}
	return -1
}

// trimSpace reports the non-whitespace remainder of b (used only to decide
// whether a truncated fragment was non-empty).
func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpace(b[start]) {
		start++
	}
	for end > start && isSpace(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
