package hook

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
)

// SealOptions configures a seal run.
type SealOptions struct {
	DryRun  bool
	Verbose bool
	// Out receives per-session dry-run/verbose reporting. Nil discards it.
	Out io.Writer
	// StartDate, when set, returns a session's start date (YYYY-MM-DD) from an
	// authoritative source — the roster, or a purge tombstone — used to name a
	// brand-new sealed directory instead of the wall clock (carried refinement
	// (a); docs/audit-seal.md §Two zones). "" falls back to the live file's
	// first-line date, then to now. An existing dated directory always wins.
	StartDate func(sessionID string) string
}

// SealResult reports what a repo-wide seal did.
type SealResult struct {
	SessionsSealed int
	LinesSealed    int
	ChunksStaged   int
	// Deferred is true when the seal was a gitlink-mounted no-op — the sealed
	// tree is unreachable, so the live lines stay in the local zone until the
	// repo is vendored (bead e29s).
	Deferred bool
}

// SealRepo seals every session's not-yet-sealed audit lines AND every
// mission's not-yet-sealed log lines in the repo, and stages every untracked
// chunk (DES-058 §Seal triggers, the pre-commit primary). It is repo-wide: any
// commit drains the repo's pending live lines into tracked chunks.
//
// In a gitlink-mounted repo the sealed tree is the wrong target, so the seal
// is a no-op exit 0 with a one-line stderr deferral notice; the live lines
// stay in .punt-labs/local/ and the clean-tree goal is already met.
func SealRepo(repoRoot string, now time.Time, opts SealOptions) (SealResult, error) {
	if audit.IsGitlinkMount(repoRoot) {
		fmt.Fprintf(os.Stderr,
			"ethos: audit seal: sealing deferred: .punt-labs/ethos is a gitlink mount, pending e29s (%s)\n",
			repoRoot)
		return SealResult{Deferred: true}, nil
	}
	var res SealResult
	if err := sealSessionsInRepo(repoRoot, now, opts, &res); err != nil {
		return res, err
	}
	if err := sealMissionsInRepo(repoRoot, now, opts, &res); err != nil {
		return res, err
	}
	return res, nil
}

// sealSessionsInRepo seals each repo session's audit tail and stages chunks.
func sealSessionsInRepo(repoRoot string, now time.Time, opts SealOptions, res *SealResult) error {
	sessions, err := listRepoSessions(repoRoot)
	if err != nil {
		return err
	}
	for _, sessionID := range sessions {
		sealedDir, err := resolveSealDir(repoRoot, sessionID, now, opts)
		if err != nil {
			return fmt.Errorf("resolving session dir for %s: %w", sessionID, err)
		}
		var out sealOutcome
		lockErr := WithLiveAuditLock(repoRoot, sessionID, func() error {
			var e error
			out, e = sealSessionLocked(repoRoot, sessionID, sealedDir, now, opts)
			return e
		})
		if lockErr != nil {
			return fmt.Errorf("sealing session %s: %w", sessionID, lockErr)
		}
		if err := tallyAndStage(repoRoot, sealedDir, audit.SessionNS, "", out, opts, res); err != nil {
			return fmt.Errorf("staging chunks for %s: %w", sessionID, err)
		}
	}
	return nil
}

// resolveSealDir resolves a session's sealed directory, dating a brand-new one
// by session start rather than wall clock (carried refinement (a)). Precedence
// per docs/audit-seal.md §Two zones: an existing dated directory (any date
// prefix) always wins; else an authoritative start date from the roster or a
// purge tombstone (opts.StartDate); else the live file's first-line date; else
// now. All three fallbacks are fixed properties of the session, so none splits
// a session across two dated directories.
func resolveSealDir(repoRoot, sessionID string, now time.Time, opts SealOptions) (string, error) {
	existing, err := audit.FindSealedSessionDir(repoRoot, sessionID)
	if err != nil {
		return "", err
	}
	if existing != "" {
		return existing, nil
	}
	date := ""
	if opts.StartDate != nil {
		date = opts.StartDate(sessionID)
	}
	if date == "" {
		date = audit.LiveFirstLineDate(liveAuditPath(repoRoot, sessionID))
	}
	if date == "" {
		date = now.UTC().Format(audit.SessionDateFormat)
	}
	return filepath.Join(sealedSessionsBase(repoRoot), date+"-"+sessionID), nil
}

// sealOutcome is the per-unit result of the locked seal steps.
type sealOutcome struct {
	wroteChunk bool
	lineCount  int
}

// tallyAndStage folds one unit's outcome into the run result and, outside the
// flock, stages every untracked chunk in the sealed dir (orphan recovery).
func tallyAndStage(repoRoot, sealedDir string, ns audit.Namespace, session string, out sealOutcome, opts SealOptions, res *SealResult) error {
	if opts.DryRun {
		res.LinesSealed += out.lineCount
		return nil
	}
	if out.wroteChunk {
		res.SessionsSealed++
		res.LinesSealed += out.lineCount
	}
	staged, err := audit.StageUntrackedChunks(repoRoot, sealedDir, ns, session)
	if err != nil {
		return err
	}
	res.ChunksStaged += staged
	return nil
}

// sealSessionLocked runs the audit seal's critical section under the caller's
// per-session live-zone flock: sweep stale temps, verify sealed chunks,
// compute the watermark, select the live tail, and write it whole to a new
// chunk via temp + rename.
func sealSessionLocked(repoRoot, sessionID, sealedDir string, now time.Time, opts SealOptions) (sealOutcome, error) {
	return sealDirLocked(sealDirParams{
		repoRoot:  repoRoot,
		ns:        audit.SessionNS,
		session:   "",
		sealedDir: sealedDir,
		livePath:  liveAuditPath(repoRoot, sessionID),
		chunkName: func(first, last int64) string { return audit.SessionChunkFile(first, last) },
		tempName:  func(first, last int64) string { return audit.SessionTempFile(first, last) },
		label:     "session " + sessionID,
	}, now, opts)
}

// sealDirParams bundles the namespace-specific inputs of a locked seal.
type sealDirParams struct {
	repoRoot  string
	ns        audit.Namespace
	session   string // mission namespace only; "" for session namespace
	sealedDir string
	livePath  string
	chunkName func(first, last int64) string
	tempName  func(first, last int64) string
	label     string
}

// sealDirLocked is the namespace-agnostic seal critical section. The caller
// holds the live-zone flock.
func sealDirLocked(p sealDirParams, now time.Time, opts SealOptions) (sealOutcome, error) {
	if !opts.DryRun {
		if err := audit.SweepStaleTemps(p.sealedDir, p.ns, p.session, now); err != nil {
			return sealOutcome{}, err
		}
	}
	// Verify every sealed chunk's content against its name; a corrupt chunk
	// fails the seal (exit 2), the escape being audit quarantine.
	sc, err := audit.ScanSealedDir(p.sealedDir, p.ns, p.session)
	if err != nil {
		return sealOutcome{}, err
	}
	for _, c := range sc.Chunks {
		if _, err := audit.ReadChunkVerified(filepath.Join(p.sealedDir, c.ChunkFile()), c.Last); err != nil {
			return sealOutcome{}, err
		}
	}
	// Tail selection uses the sealed watermark (this session's own chunks +
	// markers), never the frozen legacy max — folding legacy in would strand
	// live lines below a later-growing shared legacy log. See audit.Watermark.
	watermark, err := audit.Watermark(p.sealedDir, p.ns, p.session)
	if err != nil {
		return sealOutcome{}, err
	}
	tail, first, last, err := audit.SelectLiveTail(p.livePath, watermark)
	if err != nil {
		return sealOutcome{}, err
	}
	if len(tail) == 0 {
		return sealOutcome{}, nil
	}
	if opts.DryRun {
		return sealOutcome{lineCount: len(tail)}, nil
	}
	if err := os.MkdirAll(p.sealedDir, 0o700); err != nil {
		return sealOutcome{}, fmt.Errorf("creating sealed dir %s: %w", p.sealedDir, err)
	}
	name := p.chunkName(first, last)
	if err := audit.WriteChunkAtomic(p.sealedDir, p.tempName(first, last), name, tail); err != nil {
		return sealOutcome{}, err
	}
	reportf(opts, "sealed %d line(s) for %s into %s\n", len(tail), p.label, name)
	return sealOutcome{wroteChunk: true, lineCount: len(tail)}, nil
}

// reportf writes a verbose/dry-run line to the options' sink when present.
func reportf(opts SealOptions, format string, args ...any) {
	if opts.Out == nil || (!opts.Verbose && !opts.DryRun) {
		return
	}
	fmt.Fprintf(opts.Out, format, args...)
}

// listRepoMissions returns the union of mission ids present in the live zone
// and the tracked sealed tree. A missing tree is not an error.
func listRepoMissions(repoRoot string) ([]string, error) {
	seen := make(map[string]struct{})
	var ids []string
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, base := range []string{liveMissionsDir(repoRoot), sealedMissionsBase(repoRoot)} {
		entries, err := os.ReadDir(base)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", base, err)
		}
		for _, d := range entries {
			if d.IsDir() {
				add(d.Name())
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// listMissionSessions returns the union of session ids that have live logs or
// sealed chunks under a mission directory. A malformed (near-miss) chunk name in
// the sealed dir is a hard error: it names no session, so it would otherwise
// leave the dir unscanned and slip past the fail-closed malformed-chunk check.
func listMissionSessions(repoRoot, missionID string) ([]string, error) {
	seen := make(map[string]struct{})
	var ids []string
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	// Live logs: <id>/<session-id>.log.jsonl.
	liveDir := filepath.Join(liveMissionsDir(repoRoot), missionID)
	entries, err := os.ReadDir(liveDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", liveDir, err)
	}
	for _, f := range entries {
		if f.IsDir() {
			continue
		}
		if id, ok := stripSuffix(f.Name(), ".log.jsonl"); ok {
			add(id)
		}
	}
	// Sealed artifacts: a valid chunk, its .quarantine marker, or a retired
	// .corrupt, each carrying the sealing session in its stem. Any artifact
	// with a session names a session that needs a seal-sweep scan. Including
	// .corrupt is what makes an orphan quarantine — one whose session has no
	// live log, no valid chunk, and no marker — still get a ScanSealedDir pass
	// and fail the seal loud (exit 2), rather than passing unseen and deferring
	// the incomplete quarantine to a later union read.
	sealedDir := sealedMissionDir(repoRoot, missionID)
	chunks, err := os.ReadDir(sealedDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", sealedDir, err)
	}
	for _, f := range chunks {
		if f.IsDir() {
			continue
		}
		cn, kind := audit.Classify(f.Name(), audit.MissionNS)
		if kind == audit.KindNearMiss {
			// A near-miss (unparseable stem, empty session — this also covers a
			// .corrupt whose stem does not parse) names no session, so it would
			// add nothing and the per-session ScanSealedDir sweep would never run
			// for this dir. That skips the fail-closed malformed-chunk check when
			// a near-miss is the ONLY chunk-namespace artifact. Fail loud here at
			// discovery, matching ScanSealedDir's error, so the commit blocks
			// (exit 2) rather than deferring the malformed chunk to a later read.
			return nil, fmt.Errorf("malformed chunk name %q in %s", f.Name(), sealedDir)
		}
		if cn.Session != "" {
			add(cn.Session)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
