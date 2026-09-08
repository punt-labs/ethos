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

// fsyncFile calls f.Sync(). A package var so a test can inject a
// simulated fsync failure — the same technique
// internal/mission/syncdir_unix.go's syncDir uses for the identical
// reason: a real fsync failure (ENOSPC mid-flush, an unmounted device)
// is not something a portable test can engineer directly.
var fsyncFile = func(f *os.File) error { return f.Sync() }

// writeFile calls f.Write(b). A package var for the same reason fsyncFile
// is: a test cannot portably force a real Write failure (a full disk, a
// device yanked mid-write) so this lets a test inject one deterministically.
var writeFile = func(f *os.File, b []byte) (int, error) { return f.Write(b) }

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
//
// Leader review of PR #509 (m-2026-09-08-004 tail round): a caller that
// reports THIS call's error as "nothing new persisted" — as
// internal/mission/store.go's Store.DisclaimDelegation does, rolling
// back its own delegation-record write on an event-append failure —
// depends on that being true. Before this fix it was not, for TWO
// failure modes this function could produce: a short or partial Write,
// and (the leader's specific finding) f.Sync failing AFTER a fully
// successful Write. In both cases the line's bytes could already be
// sitting in the file (fully, for the sync case; partially, for the
// write case) with no rollback, so the caller's own "the log shows
// nothing" assumption was false — the log could show exactly the event
// the caller believes never happened, and a retry after such a failure
// appends a SECOND line for what looks like the same logical event.
// internal/mission/log.go's own legacy (single-tree) appendEventLocked
// already truncates back to the pre-write length on a write failure —
// this mirrors that discipline here, plus extends it to cover a
// write-succeeded-but-sync-failed outcome, which the legacy path did
// not need to handle because it never calls Sync at all.
//
// Leader review of PR #509 (I1, second tail round, and its write-failure
// sibling found in the round after): a bare Truncate rollback is not itself
// durable. Both rollback sites below — a failed or short Write, and a failed
// Sync after a successful Write — go through rollbackTruncate, which fsyncs
// the truncate too and reports a second failure distinctly; see that
// function for the rationale.
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
	fullLine := append(body, '\n')

	// end is the pre-write file length: the rollback target for either
	// failure mode below. Seek(SeekEnd) returns the new (and, since
	// nothing has written yet, current) offset directly.
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, fmt.Errorf("seeking %s: %w", livePath, err)
	}

	// The io.Writer contract says a short write (n < len(fullLine)) must
	// be accompanied by a non-nil error, but defensive code should not
	// trust implementations to honor the contract — the same reasoning
	// internal/mission/log.go's appendEventLocked already applies to its
	// own Write call.
	n, writeErr := writeFile(f, fullLine)
	if writeErr != nil || n != len(fullLine) {
		rbErr := rollbackTruncate(f, end)
		switch {
		case writeErr != nil && rbErr != nil:
			return 0, fmt.Errorf("writing %s: %w; %w", livePath, writeErr, rbErr)
		case writeErr != nil:
			return 0, fmt.Errorf("writing %s: %w", livePath, writeErr)
		case rbErr != nil:
			return 0, fmt.Errorf("writing %s: short write %d of %d bytes; %w", livePath, n, len(fullLine), rbErr)
		default:
			return 0, fmt.Errorf("writing %s: short write %d of %d bytes", livePath, n, len(fullLine))
		}
	}

	// The write fully succeeded — the bytes are on disk (or in the page
	// cache) whether or not Sync below succeeds. A Sync failure does not
	// undo that write; only Truncate does. Without rolling back here, a
	// caller treating this function's error as "nothing new persisted"
	// would be wrong: the line is genuinely readable by anyone opening
	// the file, even though the caller believes the append never
	// happened and may act on that belief (e.g. rolling back a sibling
	// mutation it made contingent on this append succeeding).
	if err := fsyncFile(f); err != nil {
		if rbErr := rollbackTruncate(f, end); rbErr != nil {
			return 0, fmt.Errorf("syncing %s: %w; %w", livePath, err, rbErr)
		}
		return 0, fmt.Errorf("syncing %s: %w", livePath, err)
	}
	return ts, nil
}

// rollbackTruncate undoes a failed or partial append by truncating f back to
// end (the pre-write length) and fsyncing that truncate, best-effort. It
// returns nil when both succeed, and otherwise an error describing which step
// failed — a Truncate failure, or a Truncate that succeeded but whose own
// fsync then failed.
//
// The fsync matters because Truncate alone only shortens the file's
// in-memory length: without flushing that truncate, a crash between the
// Truncate call and the filesystem's own flush can leave the old, longer
// length on disk — the very bytes AppendMonotonic's caller is about to be
// told never persisted (PR #509, findings I1 and the write-failure sibling
// of I1). Both of AppendMonotonic's rollback sites — a failed or short
// Write, and a failed Sync after a successful Write — share this exact
// hazard, so they share this one implementation rather than each carrying
// its own copy of the rationale.
//
// We do not retry the second fsync: a device that just failed two syncs in a
// row is not a transient condition this call can wait out.
func rollbackTruncate(f *os.File, end int64) error {
	if tErr := f.Truncate(end); tErr != nil {
		return fmt.Errorf("truncating to %d failed: %w", end, tErr)
	}
	if sErr := fsyncFile(f); sErr != nil {
		return fmt.Errorf("rollback truncate to %d succeeded but its own fsync failed: %w (rollback not guaranteed durable across a crash)", end, sErr)
	}
	return nil
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
		// Unlike AppendMonotonic's own rollbackTruncate sites, this
		// truncate does not need its own fsync. It is not undoing a
		// write this call made — it is repairing garbage left by some
		// PRIOR crash, and it runs unconditionally on every open. If
		// this call goes on to append and fsync, that fsync flushes
		// this truncate too (same fd, one flush covers everything
		// buffered on it). If this call instead fails before ever
		// reaching that fsync, the truncate may never reach disk — but
		// no new line was reported as persisted either way, so the
		// caller's "nothing new persisted" assumption still holds, and
		// the next open simply re-detects and re-truncates the same
		// torn tail. Idempotent cleanup does not need durability; a
		// value the caller is relying on does.
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

// staleTempAge bounds how long a foreign session's chunk temp may linger
// before a concurrent seal treats it as a crash orphan. WriteChunkAtomic
// creates a temp, writes a handful of small lines, fsyncs, and renames — a
// sub-second window even under load — so a temp older than this is a hung or
// crashed writer, never an in-flight one. Five minutes is far above any
// plausible single-chunk write yet prompt enough to keep orphans from
// accumulating across commits.
const staleTempAge = 5 * time.Minute

// SweepStaleTemps deletes stale chunk temp files under dir before the seal
// writes its own. In a session directory (single-session by construction)
// every temp is this session's, so all are swept. In a mission directory
// several sessions seal into one shared dir under distinct per-(mission,
// session) flocks, so the sweep must not delete another session's in-flight
// temp: it removes its OWN session's temps unconditionally (a leftover is its
// own prior crash orphan — its flock serializes its seals) and a foreign
// session's temp only once older than staleTempAge. A temp whose session is
// unparseable is treated as foreign and swept only when stale. now is the seal
// clock, passed so tests are deterministic.
func SweepStaleTemps(dir string, ns Namespace, session string, now time.Time) error {
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
		cn, kind := Classify(e.Name(), ns)
		if kind != KindTemp {
			continue
		}
		if !sweepable(e, ns, session, cn.Session, now) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing stale temp %s: %w", e.Name(), err)
		}
	}
	return nil
}

// sweepable reports whether SweepStaleTemps may remove a temp entry. A session
// dir sweeps all temps; a mission dir sweeps its own session's temps always and
// a foreign (or unattributable) session's temp only once its mtime is older
// than staleTempAge. An unreadable mtime is treated as not-yet-stale so a live
// temp is never removed on a transient stat error.
func sweepable(e os.DirEntry, ns Namespace, ownSession, tempSession string, now time.Time) bool {
	if ns != MissionNS || tempSession == ownSession {
		return true
	}
	info, err := e.Info()
	if err != nil {
		return false
	}
	return now.Sub(info.ModTime()) > staleTempAge
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
