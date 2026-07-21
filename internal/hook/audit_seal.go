package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// SealOptions configures a seal run.
type SealOptions struct {
	DryRun  bool
	Verbose bool
	// Out receives per-session dry-run/verbose reporting. Nil discards it.
	Out io.Writer
}

// SealResult reports what a repo-wide seal did.
type SealResult struct {
	SessionsSealed int
	LinesSealed    int
	ChunksStaged   int
	// Deferred is true when the seal was a gitlink-mounted no-op — the
	// sealed tree is unreachable, so the live lines stay in the local zone
	// until the repo is vendored (bead e29s).
	Deferred bool
}

// SealRepo seals every session's not-yet-sealed audit lines in the repo
// and stages every untracked chunk (DES-058 §Seal triggers, the pre-commit
// primary). It is repo-wide: any commit drains the repo's pending audit
// lines into tracked chunks, so an orphaned or crashed session's lines land
// at the next commit with no liveness probe.
//
// In a gitlink-mounted repo the sealed tree is the wrong target, so the
// seal is a deliberate no-op exit 0 with a one-line stderr deferral notice;
// the live lines stay in .punt-labs/local/ and the clean-tree goal is
// already met because that zone is gitignored.
func SealRepo(repoRoot string, now time.Time, opts SealOptions) (SealResult, error) {
	if isGitlinkMount(repoRoot) {
		fmt.Fprintf(os.Stderr,
			"ethos: audit seal: sealing deferred: .punt-labs/ethos is a gitlink mount, pending e29s (%s)\n",
			repoRoot)
		return SealResult{Deferred: true}, nil
	}

	sessions, err := listRepoSessions(repoRoot)
	if err != nil {
		return SealResult{}, err
	}

	var res SealResult
	for _, sessionID := range sessions {
		sealedDir, err := resolveRepoSessionDir(repoRoot, sessionID, now)
		if err != nil {
			return res, fmt.Errorf("resolving session dir for %s: %w", sessionID, err)
		}
		var sealed sessionSealOutcome
		lockErr := WithLiveAuditLock(repoRoot, sessionID, func() error {
			var e error
			sealed, e = sealSessionLocked(repoRoot, sessionID, sealedDir, now, opts)
			return e
		})
		if lockErr != nil {
			return res, fmt.Errorf("sealing session %s: %w", sessionID, lockErr)
		}
		if opts.DryRun {
			res.LinesSealed += sealed.lineCount
			if sealed.lineCount > 0 {
				reportf(opts, "would seal %d line(s) for session %s\n", sealed.lineCount, sessionID)
			}
			continue
		}
		if sealed.wroteChunk {
			res.SessionsSealed++
			res.LinesSealed += sealed.lineCount
		}
		// git add runs outside the flock and stages EVERY untracked chunk
		// in the dir, whether this run wrote one or a prior crashed seal
		// left it — orphan recovery (§Write atomicity).
		staged, err := stageUntrackedChunks(repoRoot, sealedDir, sessionNamespace, "")
		if err != nil {
			return res, fmt.Errorf("staging chunks for %s: %w", sessionID, err)
		}
		res.ChunksStaged += staged
	}
	return res, nil
}

// sessionSealOutcome is the per-session result of the locked seal steps.
type sessionSealOutcome struct {
	wroteChunk bool
	lineCount  int
}

// sealSessionLocked runs the seal's critical section under the caller's
// per-session live-zone flock: sweep stale temps, verify sealed chunks,
// compute the watermark, select the live tail, and write it whole to a new
// chunk via temp + rename. The git add is the caller's job, outside the
// lock.
func sealSessionLocked(repoRoot, sessionID, sealedDir string, now time.Time, opts SealOptions) (sessionSealOutcome, error) {
	legacyPath := filepath.Join(sealedDir, "audit.jsonl")
	livePath := liveAuditPath(repoRoot, sessionID)

	// A dry run must not mutate the tree: report the count, touch nothing.
	if !opts.DryRun {
		if err := sweepStaleTemps(sealedDir, sessionNamespace); err != nil {
			return sessionSealOutcome{}, err
		}
	}

	// Verify every sealed chunk's content against its name; a corrupt
	// chunk fails the seal (exit 2), the escape being audit quarantine.
	sc, err := scanSealedDir(sealedDir, sessionNamespace, "")
	if err != nil {
		return sessionSealOutcome{}, err
	}
	for _, c := range sc.chunks {
		name := sessionChunkFile(c.First, c.Last)
		if _, err := readChunkVerified(filepath.Join(sealedDir, name), c.Last); err != nil {
			return sessionSealOutcome{}, err
		}
	}

	watermark, err := sessionWatermark(sealedDir, sessionNamespace, "", legacyPath)
	if err != nil {
		return sessionSealOutcome{}, err
	}

	tail, first, last, err := selectLiveTail(livePath, watermark)
	if err != nil {
		return sessionSealOutcome{}, err
	}
	if len(tail) == 0 {
		return sessionSealOutcome{}, nil
	}
	if opts.DryRun {
		return sessionSealOutcome{lineCount: len(tail)}, nil
	}
	if err := os.MkdirAll(sealedDir, 0o700); err != nil {
		return sessionSealOutcome{}, fmt.Errorf("creating sealed dir %s: %w", sealedDir, err)
	}
	chunkName := sessionChunkFile(first, last)
	tempName := sessionTempFile(first, last)
	if err := writeChunkAtomic(sealedDir, tempName, chunkName, tail); err != nil {
		return sessionSealOutcome{}, err
	}
	reportf(opts, "sealed %d line(s) for session %s into %s\n", len(tail), sessionID, chunkName)
	return sessionSealOutcome{wroteChunk: true, lineCount: len(tail)}, nil
}

// selectLiveTail returns the raw JSON lines of the live file with ts
// strictly past the watermark, plus the first and last ts of that tail.
// Complete lines only: a torn trailing line is dropped and a terminated
// unparseable line is skipped with a stderr count (§`ethos audit show`).
// The returned lines are the exact on-disk bytes so the seal is a
// transformation-free byte copy.
func selectLiveTail(livePath string, watermark int64) (lines [][]byte, first, last int64, err error) {
	data, err := os.ReadFile(livePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, 0, nil
		}
		return nil, 0, 0, fmt.Errorf("reading live %s: %w", livePath, err)
	}
	// Drop a torn (non-newline-terminated) tail before selecting.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if cut := lastNewline(data); cut >= 0 {
			data = data[:cut+1]
		} else {
			data = nil
		}
	}
	skipped := 0
	for _, raw := range splitLines(data) {
		var e auditEntry
		if jerr := json.Unmarshal(raw, &e); jerr != nil {
			skipped++
			continue
		}
		ts, perr := parseLineTS(e.Ts)
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
		// Copy the line: splitLines returns slices into data.
		line := make([]byte, len(raw))
		copy(line, raw)
		lines = append(lines, line)
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr,
			"ethos: audit seal: %s: skipped %d unparseable line(s)\n", livePath, skipped)
	}
	return lines, first, last, nil
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

// writeChunkAtomic writes lines to a temp file in dir, fsyncs it, and
// renames it to the final chunk name — so the tracked chunk never exists
// in a partial state (I11-chunk). A fresh run refuses if the target chunk
// already exists (defense in depth; the quarantine resume path is the sole
// exception, handled elsewhere).
func writeChunkAtomic(dir, tempName, chunkName string, lines [][]byte) error {
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

// sweepStaleTemps deletes stale chunk temp files under dir before the seal
// writes its own. A crash before a rename leaves a temp; a later seal's
// tail is wider, so it writes a different temp name and the stale one is
// never overwritten naturally. In a session directory the sweep is scoped
// to that session's own .audit-*.jsonl.tmp; in a mission directory it is
// namespace-wide (.log-*-*.jsonl.tmp), because several sessions seal into
// one shared directory (§Write atomicity).
func sweepStaleTemps(dir string, ns chunkNamespace) error {
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
		name := e.Name()
		_, kind := classifyChunk(name, ns)
		if kind != chunkTemp {
			continue
		}
		// The sweep is namespace-wide in a mission directory and
		// session-scoped by construction in a session directory (only one
		// session's temps ever land there), so no session filter is
		// applied to the temp names.
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing stale temp %s: %w", name, err)
		}
	}
	return nil
}

// stageUntrackedChunks git-adds every chunk-namespace artifact in dir — a
// valid chunk, a quarantine marker, or a .corrupt artifact — recovering an
// orphan a prior crashed seal left untracked (§Write atomicity). A missing
// dir stages nothing.
func stageUntrackedChunks(repoRoot, dir string, ns chunkNamespace, session string) (int, error) {
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
		name := e.Name()
		cn, kind := classifyChunk(name, ns)
		if ns == missionNamespace && session != "" &&
			(kind == chunkValid || kind == chunkQuarantine || kind == chunkCorrupt) &&
			cn.Session != session {
			continue
		}
		switch kind {
		case chunkValid, chunkQuarantine, chunkCorrupt:
			paths = append(paths, filepath.Join(dir, name))
		case chunkOther, chunkNearMiss, chunkTemp:
			// Siblings, near-misses, and temps are never staged here.
		}
	}
	if len(paths) == 0 {
		return 0, nil
	}
	// Restrict to chunks git does not already have clean-tracked, so the
	// staged count reflects real work (an already-committed chunk that a
	// re-seal re-adds is a no-op, not a staging). git add is still
	// unconditional over the survivors — orphan recovery stages every
	// untracked chunk (§Write atomicity).
	pending, err := untrackedOrModified(repoRoot, paths)
	if err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}
	if err := gitAdd(repoRoot, pending...); err != nil {
		return 0, err
	}
	return len(pending), nil
}

// untrackedOrModified returns the subset of paths git reports as untracked
// or modified relative to the index. A clean-tracked chunk is dropped so a
// re-seal does not report it as newly staged.
func untrackedOrModified(repoRoot string, paths []string) ([]string, error) {
	args := append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, paths...)
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	dirty := make(map[string]struct{})
	for _, line := range splitLines(out) {
		// Porcelain v1: "XY <path>", path starts at column 3.
		if len(line) < 4 {
			continue
		}
		dirty[string(line[3:])] = struct{}{}
	}
	if len(dirty) == 0 {
		return nil, nil
	}
	var pending []string
	for _, p := range paths {
		rel, err := filepath.Rel(repoRoot, p)
		if err != nil {
			rel = p
		}
		if _, ok := dirty[rel]; ok {
			pending = append(pending, p)
		}
	}
	return pending, nil
}

// reportf writes a verbose/dry-run line to the options' sink when present.
func reportf(opts SealOptions, format string, args ...any) {
	if opts.Out == nil {
		return
	}
	if !opts.Verbose && !opts.DryRun {
		return
	}
	fmt.Fprintf(opts.Out, format, args...)
}
