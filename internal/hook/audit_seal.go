package hook

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/gitenv"
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
//
// A seal that cannot finish — git did not answer, a chunk would not write —
// returns an error that blocks the commit and leaves the tree as it found it:
// see runSeal for the three phases and the rollback.
func SealRepo(repoRoot string, now time.Time, opts SealOptions) (SealResult, error) {
	gitlink, err := audit.IsGitlinkMount(repoRoot)
	if err != nil {
		return SealResult{}, SealGitError(repoRoot, "checking for a gitlink mount (git ls-files)", err, true)
	}
	if gitlink {
		fmt.Fprintf(os.Stderr,
			"ethos: audit seal: sealing deferred: .punt-labs/ethos is a gitlink mount, pending e29s (%s)\n",
			repoRoot)
		return SealResult{Deferred: true}, nil
	}
	units, err := repoSealUnits(repoRoot, now, opts)
	if err != nil {
		return SealResult{}, err
	}
	var res SealResult
	if err := runSeal(repoRoot, units, opts, &res); err != nil {
		return res, err
	}
	return res, nil
}

// repoSealUnits lists every unit a repo-wide seal covers: each session's audit
// tail, then each (mission, session) log tail. It reads only the file system.
func repoSealUnits(repoRoot string, now time.Time, opts SealOptions) ([]sealUnit, error) {
	var units []sealUnit
	sessions, err := listRepoSessions(repoRoot)
	if err != nil {
		return nil, err
	}
	for _, sessionID := range sessions {
		sealedDir, err := resolveSealDir(repoRoot, sessionID, now, opts)
		if err != nil {
			return nil, fmt.Errorf("resolving session dir for %s: %w", sessionID, err)
		}
		units = append(units, sessionSealUnit(repoRoot, sessionID, sealedDir, now, opts))
	}
	missions, err := listRepoMissions(repoRoot)
	if err != nil {
		return nil, err
	}
	for _, missionID := range missions {
		ids, err := listMissionSessions(repoRoot, missionID)
		if err != nil {
			return nil, err
		}
		for _, sessionID := range ids {
			units = append(units, missionSealUnit(repoRoot, missionID, sessionID, now, opts))
		}
	}
	return units, nil
}

// sealUnit is one (session) or (mission, session) tail a seal covers.
type sealUnit struct {
	label     string
	ns        audit.Namespace
	session   string // mission namespace only; "" for session namespace
	sealedDir string
	// write runs the locked critical section: it writes the unit's new chunk,
	// if any, and touches no git.
	write func() (sealOutcome, error)
}

func sessionSealUnit(repoRoot, sessionID, sealedDir string, now time.Time, opts SealOptions) sealUnit {
	return sealUnit{
		label: "session " + sessionID, ns: audit.SessionNS, sealedDir: sealedDir,
		write: func() (sealOutcome, error) {
			var out sealOutcome
			err := WithLiveAuditLock(repoRoot, sessionID, func() error {
				var e error
				out, e = sealSessionLocked(repoRoot, sessionID, sealedDir, now, opts)
				return e
			})
			return out, err
		},
	}
}

// runSeal seals units in three phases, so a seal that cannot finish leaves the
// tree as it found it:
//
//  1. every read-only git query — one `git status` over the chunks already on
//     disk, to find orphans a crashed seal left unstaged, and where the index
//     lives;
//  2. every chunk write, each under its unit's flock;
//  3. one `git add` of the orphans and the new chunks, after a snapshot of
//     the index.
//
// A failure in phase 1 happens before anything is written. A failure in phase
// 2 or 3 removes the chunks this run wrote (the live files are untouched, so
// the next seal rewrites them) and, after phase 3's git has exited, puts the
// snapshot back over the index: `git add` of several paths can stage some and
// then fail, and the index is part of the tree. SweepStaleTemps (removing
// leftover temp files) and MkdirAll (creating empty directories) may persist:
// neither changes tracked content, so they are not part of "unchanged". A dry
// run only counts.
func runSeal(repoRoot string, units []sealUnit, opts SealOptions, res *SealResult) error {
	if opts.DryRun {
		for _, u := range units {
			out, err := u.write()
			if err != nil {
				return fmt.Errorf("sealing %s: %w", u.label, err)
			}
			res.LinesSealed += out.lineCount
		}
		return nil
	}

	var existing []string
	for _, u := range units {
		paths, err := audit.ChunkArtifacts(u.sealedDir, u.ns, u.session)
		if err != nil {
			return fmt.Errorf("listing chunks for %s: %w", u.label, err)
		}
		existing = append(existing, paths...)
	}
	var pending []string
	if len(existing) > 0 {
		var err error
		pending, err = audit.UntrackedOrModified(repoRoot, existing)
		if err != nil {
			return SealGitError(repoRoot, "reading the status of the chunks already on disk (git status)", err, true)
		}
	}
	indexPath, err := gitIndexPath(repoRoot)
	if err != nil {
		return SealGitError(repoRoot, "locating the index (git rev-parse --git-path index)", err, true)
	}

	var wrote []string
	for _, u := range units {
		out, err := u.write()
		if err != nil {
			return rollbackSeal(wrote, nil, fmt.Errorf("sealing %s: %w", u.label, err), "writing chunks")
		}
		if out.wroteChunk {
			res.SessionsSealed++
			res.LinesSealed += out.lineCount
			wrote = append(wrote, out.chunkPath)
		}
	}

	toAdd := append([]string(nil), pending...)
	seen := make(map[string]bool, len(pending))
	for _, p := range pending {
		seen[p] = true
	}
	for _, p := range wrote {
		if !seen[p] {
			toAdd = append(toAdd, p)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}
	snap, err := snapshotIndex(indexPath)
	if err != nil {
		return rollbackSeal(wrote, nil, fmt.Errorf("snapshotting the index: %w", err), "snapshotting the index")
	}
	if err := audit.GitAdd(repoRoot, toAdd...); err != nil {
		return rollbackSeal(wrote, snap, SealGitError(repoRoot, "staging chunks (git add)", err, false), "staging chunks (git add)")
	}
	snap.discard()
	res.ChunksStaged += len(toAdd)
	return nil
}

// gitIndexPath is where git keeps the index for repoRoot: $GIT_INDEX_FILE, or
// the index of this checkout (a linked worktree has its own).
func gitIndexPath(repoRoot string) (string, error) {
	out, _, err := gitenv.Read(gitenv.Spec{
		Git: "git", Dir: repoRoot, Env: gitenv.Inherited(), Args: []string{"rev-parse", "--git-path", "index"},
	}, 0)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", errors.New("git printed no index path")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(repoRoot, p)
	}
	return p, nil
}

// indexSnapshot is a copy of the index taken before `git add`, in a sibling
// file (same directory, so a restore is a rename).
type indexSnapshot struct {
	path, snap string
	existed    bool // false: there was no index, and restoring removes the one git made
}

// snapshotIndex copies the index at path to path+".ethos-tmp". A missing index
// is recorded, not an error: a repository with no commits has none.
func snapshotIndex(path string) (*indexSnapshot, error) {
	s := &indexSnapshot{path: path, snap: path + ".ethos-tmp"}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	s.existed = true
	if err := os.WriteFile(s.snap, data, 0o600); err != nil {
		return nil, err
	}
	return s, nil
}

// restore puts the snapshot back over the index (git has exited), or removes
// the index when there was none.
func (s *indexSnapshot) restore() error {
	if !s.existed {
		if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return os.Rename(s.snap, s.path)
}

// discard removes the snapshot after a successful add.
func (s *indexSnapshot) discard() {
	if s.existed {
		_ = os.Remove(s.snap)
	}
}

// rollbackSeal undoes a failed seal: it restores the index from snap (nil
// before the add), then removes the chunks this run wrote (the live files hold
// every line). It returns the failure naming the phase, saying the tree is
// unchanged only when every step succeeded and listing what is left when one
// did not.
func rollbackSeal(wrote []string, snap *indexSnapshot, cause error, phase string) error {
	var left []string
	if snap != nil {
		if err := snap.restore(); err != nil {
			left = append(left, fmt.Sprintf("restoring the index from %s: %v", snap.snap, err))
		}
	}
	for _, p := range wrote {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			left = append(left, fmt.Sprintf("removing chunk %s: %v", p, err))
		}
	}
	return sealStageFailure("", phase, cause, left)
}

// sealStageFailure is the error for a seal that failed after it began
// writing: cause, with the phase named. left is what the rollback could not
// undo; with none, the tree is unchanged and the message says so.
func sealStageFailure(repoRoot, phase string, cause error, left []string) error {
	if len(left) == 0 {
		return fmt.Errorf("%w; nothing was sealed or staged, the commit is blocked and the tree is unchanged", cause)
	}
	return fmt.Errorf("%w; the commit is blocked, but the rollback of %s did not finish: %s; "+
		"the next seal stages the chunks", cause, phase, strings.Join(left, "; "))
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
	chunkPath  string // the chunk written, when wroteChunk
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
	return sealOutcome{wroteChunk: true, lineCount: len(tail), chunkPath: filepath.Join(p.sealedDir, name)}, nil
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

// SealGitError says why a git step of the seal failed, naming the phase. A git
// that did not answer says so and where. unchanged says whether the tree is
// known to be as it was (a read-only phase, or nothing written yet): the
// message claims "the tree is unchanged" only then. A failure after chunks were
// written passes false and is finished by rollbackSeal, which makes the claim
// only once the rollback has actually succeeded.
func SealGitError(repoRoot, phase string, err error, unchanged bool) error {
	var e error
	if err != nil && errors.Is(err, gitenv.ErrTimeout) {
		e = fmt.Errorf("the seal could not run because git did not answer in %s while %s (%w)", repoRoot, phase, err)
	} else {
		e = fmt.Errorf("%s: %w", phase, err)
	}
	if unchanged {
		return fmt.Errorf("%w; nothing was sealed or staged, the commit is blocked and the tree is unchanged", e)
	}
	return e
}
