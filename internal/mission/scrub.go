package mission

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// TrackedRecordFiles returns the repo-relative, slash-separated paths git
// tracks under .punt-labs/ethos. It is the file list ScrubFiles is meant
// to be given: scrubbing only tracked files leaves the live zone and any
// in-flight mission untouched.
func TrackedRecordFiles(repoRoot string) ([]string, error) {
	out, err := gitenv.Output(repoRoot, "git", "ls-files", "-z", "--", ".punt-labs/ethos")
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
			return nil, fmt.Errorf("listing tracked files in %s: %w: %s", repoRoot, err, bytes.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("listing tracked files in %s: %w", repoRoot, err)
	}
	var rels []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			rels = append(rels, string(p))
		}
	}
	return rels, nil
}

// scrubbable reports whether rel is a mutable tracked record: a mission's
// contract.yaml, results.yaml, reflections.yaml or corrections.yaml, a delegation's
// record.yaml or prompt.md, the missions.jsonl trace log, or the plain
// output of `ethos mission export` (missions/<id>.contract.yaml and
// missions/<id>.result.yaml).
//
// Everything else is skipped. Legacy log.jsonl and audit.jsonl and the
// sealed chunks are excluded because DES-058 makes sealed chunks immutable
// and the operator ruled that history is not rewritten. Chunks sealed
// before the redactor existed were NOT redacted with the credential, email
// and foreign-home passes; they stay as they were sealed. The identity
// graph carries identity data on purpose; the machine-local zone is never
// tracked.
func scrubbable(rel string) bool {
	rel = path.Clean(rel)
	if rel == ".punt-labs/ethos/missions.jsonl" {
		return true
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 4 || strings.Join(parts[:3], "/") != ".punt-labs/ethos/missions" {
		return false
	}
	switch len(parts) {
	case 4:
		return strings.HasSuffix(parts[3], ".contract.yaml") || strings.HasSuffix(parts[3], ".result.yaml")
	case 5:
		return parts[4] == "contract.yaml" || parts[4] == "results.yaml" || parts[4] == "reflections.yaml" || parts[4] == "corrections.yaml"
	case 7:
		return parts[4] == "delegations" && (parts[6] == "record.yaml" || parts[6] == "prompt.md")
	}
	return false
}

// ScrubResult reports what ScrubFiles did. Changed holds the paths whose
// content changed (or would change, on a dry run); Skipped holds tracked
// paths that are gone from the worktree.
type ScrubResult struct {
	Changed []string
	Skipped []string
	// Busy holds the paths ScrubFilesDeferring left alone because a writer held
	// one of their locks. Interrupted is set when the context ended before every
	// path was visited.
	Busy        []string
	Interrupted bool
	// Failed counts the paths that could not be redacted, Refused those among
	// them the write guard refused, and Unwritable those the filesystem would
	// not read or write.
	Failed, Refused, Unwritable int
}

// ScrubFilesDeferring is ScrubFiles for a caller that must not wait: it takes
// each file's writer locks without blocking and reports a file whose lock is
// held as Busy, to be tried again later. It stops between files when ctx ends.
// It prints nothing; a durability warning for an installed rewrite is returned
// in the outcome. It does not run the mission writers' receipt reconciliation,
// which a lifecycle mutation needs and a redaction does not.
func ScrubFilesDeferring(ctx context.Context, p layout.Policy, globalRoot string, rels []string) (ScrubResult, MutationOutcome, error) {
	var res ScrubResult
	var out MutationOutcome
	if _, err := p.LockPath("mission-summary", "missions"); err != nil {
		return res, out, fmt.Errorf("scrubbing: %w", err)
	}
	repoRoot := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	r, err := NewPathRedactor(repoRoot)
	if err != nil {
		return res, out, fmt.Errorf("scrubbing %s: %w", repoRoot, err)
	}
	if strings.TrimSpace(globalRoot) == "" {
		return res, out, fmt.Errorf("scrubbing %s: the global store root is required for the record locks", repoRoot)
	}
	s := NewRepositoryStore(p, globalRoot)
	sorted := append([]string(nil), rels...)
	sort.Strings(sorted)
	var failed []error
	for _, rel := range sorted {
		if !scrubbable(rel) {
			continue
		}
		if ctx.Err() != nil {
			res.Interrupted = true
			break
		}
		did, err := s.scrubFileTry(r, rel, &out)
		switch {
		case errors.Is(err, ErrLockBusy):
			res.Busy = append(res.Busy, rel)
		case errors.Is(err, errScrubMissing):
			res.Skipped = append(res.Skipped, rel)
		case err != nil:
			failed = append(failed, fmt.Errorf("%s: %w", rel, err))
			res.Failed++
			if errors.Is(err, ErrGuardRefused) {
				res.Refused++
			}
			if errors.Is(err, os.ErrPermission) {
				res.Unwritable++
			}
		case did:
			res.Changed = append(res.Changed, rel)
		}
	}
	if len(failed) > 0 {
		return res, out, fmt.Errorf("%d rewritten, %d failed: %w", len(res.Changed), len(failed), errors.Join(failed...))
	}
	return res, out, nil
}

// scrubFileTry is scrubFile under the locks the file's writers take, taken
// without waiting.
func (s *Store) scrubFileTry(r PathRedactor, rel string, out *MutationOutcome) (bool, error) {
	if err := rejectSymlinkedPath(s.checkoutRoot, rel); err != nil {
		return false, err
	}
	if s.missionDirAbsent(rel) {
		return scrubFileOutcome(r, s.policy, rel, false, out)
	}
	var did bool
	err := s.withRecordLocksTry(rel, func() error {
		var err error
		did, err = scrubFileOutcome(r, s.policy, rel, false, out)
		return err
	})
	return did, err
}

// withRecordLocksTry is withRecordLocks without waiting: the same locks, in the
// same order, each taken with a try-lock, so a held lock is ErrLockBusy.
func (s *Store) withRecordLocksTry(rel string, fn func() error) error {
	rel = path.Clean(rel)
	parts := strings.Split(rel, "/")
	switch {
	case rel == ".punt-labs/ethos/missions.jsonl":
		return withPolicyLockTry(s.policy, "mission-summary", "missions", lockExclusive, fn)
	case len(parts) == 5:
		return withPolicyLockTry(s.policy, "mission-state", parts[3], lockExclusive, fn)
	case len(parts) == 7:
		return withPolicyLockTry(s.policy, "mission-delegations", parts[3], lockShared, func() error {
			return withPolicyLockTry(s.policy, "delegation", parts[5], lockExclusive, fn)
		})
	}
	return fn()
}

// ScrubFiles runs the shared redactor over already-written tracked
// records, the forward fix for files an older ethos wrote unredacted
// (operator ruling 2026-10-23: no history rewrite). rels are repo-relative
// paths, normally from TrackedRecordFiles. With dryRun it writes nothing.
//
// Every file is processed. A file that cannot be parsed is not written and
// is reported in the returned error ("N rewritten, M failed: ..."), after
// the remaining files have been scrubbed: a record that cannot be parsed
// cannot be redacted, and one bad file must not hide the others. A file
// deleted from the worktree is skipped. A file is written in its canonical
// re-encoded form (see guardWrite) whenever its bytes differ from that form,
// whether or not the redactor changed a value; a file already canonical is not
// touched. A record holding a field this ethos does not know is refused and
// left as it was. Like every record writer it fails closed when no redactor
// can be built.
//
// Unless dryRun is set, each file is read, redacted and renamed under the
// locks its writers hold (the trace lock; the per-mission lock; for a
// delegation record the shared repo mission lock, then the delegation lock),
// so a concurrent close or update is not lost to the rewrite. A dry run and a
// record whose mission directory is gone take no lock: nothing is written.
// This single-root convenience API treats repoRoot as both store and checkout.
// Linked-checkout callers must use ScrubFilesWithPolicy. globalRoot is the
// separate global fallback store, not the repository staging or trace root.
// A symlinked artifact or containing directory is refused.
func ScrubFiles(repoRoot, globalRoot string, rels []string, dryRun bool) (ScrubResult, error) {
	p, err := layout.NewRepository(repoRoot, repoRoot, layout.RecordBlame)
	if err != nil {
		return ScrubResult{}, fmt.Errorf("scrubbing %s: %w", repoRoot, err)
	}
	return ScrubFilesWithPolicy(p, globalRoot, rels, dryRun)
}

// ScrubFilesWithPolicy scrubs tracked files in the policy's checkout, staging
// every rewrite in the canonical store's local zone. It consumes a validated
// request snapshot; it does not discover roots or reread configuration. Locks
// retain their authority-owner, shared trace and delegation identities at S.
func ScrubFilesWithPolicy(p layout.Policy, globalRoot string, rels []string, dryRun bool) (ScrubResult, error) {
	var res ScrubResult
	if _, err := p.LockPath("mission-summary", "missions"); err != nil {
		return res, fmt.Errorf("scrubbing: %w", err)
	}
	repoRoot := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	r, err := NewPathRedactor(repoRoot)
	if err != nil {
		return res, fmt.Errorf("scrubbing %s: %w", repoRoot, err)
	}
	if strings.TrimSpace(globalRoot) == "" {
		return res, fmt.Errorf("scrubbing %s: the global store root is required for the record locks", repoRoot)
	}
	s := NewRepositoryStore(p, globalRoot)
	sorted := append([]string(nil), rels...)
	sort.Strings(sorted)
	var failed []error
	for _, rel := range sorted {
		if !scrubbable(rel) {
			continue
		}
		did, err := s.scrubFileLocked(r, rel, dryRun)
		switch {
		case errors.Is(err, errScrubMissing):
			res.Skipped = append(res.Skipped, rel)
		case err != nil:
			failed = append(failed, fmt.Errorf("%s: %w", rel, err))
		case did:
			res.Changed = append(res.Changed, rel)
		}
	}
	if len(failed) > 0 {
		verb := "rewritten"
		if dryRun {
			verb = "to rewrite"
		}
		return res, fmt.Errorf("%d %s, %d failed: %w", len(res.Changed), verb, len(failed), errors.Join(failed...))
	}
	return res, nil
}

// errScrubMissing marks a file that was absent when scrubFile tried to
// read it. Only the read is judged: a not-exist error from the write is a
// real failure and must not be reported as a skip.
var errScrubMissing = errors.New("file does not exist")

// scrubWrite is the write step of scrubFile; tests replace it to inject
// write-time failures that a real filesystem cannot produce on demand.
var scrubWrite = func(dir, pattern, dest string, data []byte) error {
	return writeAtomicFileWithWarning(dir, pattern, dest, data, nil, "mission scrub: "+dest+": rewrite installed")
}

// scrubFileLocked is scrubFile under the locks the file's writers take,
// after refusing a symlinked path.
func (s *Store) scrubFileLocked(r PathRedactor, rel string, dryRun bool) (bool, error) {
	if err := rejectSymlinkedPath(s.checkoutRoot, rel); err != nil {
		return false, err
	}
	// A dry run writes nothing, and a record whose mission directory is gone
	// has nothing to protect: taking a lock would create its file (and the
	// directory) for a scrub that changes nothing, as Close avoids by
	// statting the directory first.
	if dryRun || s.missionDirAbsent(rel) {
		return scrubFileWithPolicy(r, s.policy, rel, dryRun)
	}
	var did bool
	err := s.withRecordLocks(rel, func() error {
		var err error
		did, err = scrubFileWithPolicy(r, s.policy, rel, dryRun)
		return err
	})
	return did, err
}

// missionDirAbsent reports whether rel names a file under a mission directory
// that does not exist.
func (s *Store) missionDirAbsent(rel string) bool {
	parts := strings.Split(path.Clean(rel), "/")
	if len(parts) < 5 {
		return false
	}
	_, err := os.Stat(filepath.Join(s.checkoutRoot, filepath.Join(parts[:4]...)))
	return missingRepoTreeDir(err)
}

// withRecordLocks runs fn holding the lock that serializes rel's writers:
// the trace lock for missions.jsonl, the per-mission lock for a mission's
// contract, results and reflections, the shared repo mission lock and then the
// delegation lock for a delegation record. Plain export files have no writer lock.
func (s *Store) withRecordLocks(rel string, fn func() error) error {
	rel = path.Clean(rel)
	parts := strings.Split(rel, "/")
	switch {
	case rel == ".punt-labs/ethos/missions.jsonl":
		return withTraceLock(RepoStatePath(s.repoRoot), fn)
	case len(parts) == 5:
		return s.withLock(parts[3], fn)
	case len(parts) == 7:
		// The close sweep (Store.Close) runs under the exclusive repo mission
		// lock and rewrites open delegation records; scrub takes the shared
		// repo mission lock first, in the sweep's order, so it cannot write
		// back an open snapshot over a record the sweep closed.
		releaseMission, err := AcquireMissionLock(s.repoRoot, parts[3])
		if err != nil {
			return err
		}
		defer releaseMission()
		release, err := s.AcquireDelegationLock(parts[5])
		if err != nil {
			return err
		}
		defer release()
	}
	return fn()
}

// rejectSymlinkedPath refuses rel when it, or any directory above it below
// repoRoot, is a symlink: a link there would send the rewrite outside the
// tracked tree.
func rejectSymlinkedPath(repoRoot, rel string) error {
	cur := repoRoot
	for _, seg := range strings.Split(path.Clean(rel), "/") {
		cur = filepath.Join(cur, seg)
		if err := rejectSymlink(cur); err != nil {
			return err
		}
	}
	return nil
}

// artifactType is the record type a tracked YAML file decodes into, which
// supplies the redact tags the YAML redactor reads. Nil for an unknown file.
func artifactType(name string) reflect.Type {
	switch {
	case name == "contract.yaml", strings.HasSuffix(name, ".contract.yaml"):
		return reflect.TypeOf(Contract{})
	case name == "results.yaml":
		return reflect.TypeOf(resultsFile{})
	case name == "reflections.yaml":
		return reflect.TypeOf(reflectionsFile{})
	case name == "record.yaml":
		return reflect.TypeOf(Delegation{})
	case strings.HasSuffix(name, ".result.yaml"):
		return reflect.TypeOf(Result{})
	}
	return nil
}

// scrubFile redacts one file in place and reports whether it changed.
func scrubFile(r PathRedactor, repoRoot, rel string, dryRun bool) (bool, error) {
	p, err := layout.NewRepository(repoRoot, repoRoot, layout.RecordBlame)
	if err != nil {
		return false, err
	}
	return scrubFileWithPolicy(r, p, rel, dryRun)
}

func scrubFileWithPolicy(r PathRedactor, p layout.Policy, rel string, dryRun bool) (bool, error) {
	return scrubFileOutcome(r, p, rel, dryRun, nil)
}

// scrubFileOutcome is scrubFileWithPolicy. A nil mo keeps the stderr warning
// of the original API; a non-nil mo collects it instead and prints nothing.
func scrubFileOutcome(r PathRedactor, p layout.Policy, rel string, dryRun bool, mo *MutationOutcome) (bool, error) {
	if _, err := p.LockPath("mission-summary", "missions"); err != nil {
		return false, err
	}
	repoRoot := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	dest := filepath.Join(repoRoot, filepath.FromSlash(rel))
	data, err := os.ReadFile(dest)
	if errors.Is(err, fs.ErrNotExist) {
		return false, errScrubMissing
	}
	if err != nil {
		return false, err
	}
	f := r.Content
	var out []byte
	switch path.Ext(rel) {
	case ".jsonl":
		out, _, err = redactJSONLines(f, r.Path, reflect.TypeOf(TraceSummary{}), data)
	case ".yaml":
		// The record's type supplies the modes, as in the writer: path
		// fields get the path pass only and IDs and enums are kept.
		var redacted bool
		out, redacted, err = redactYAMLDoc(f, r.Path, artifactType(path.Base(rel)), data)
		if err == nil && redacted && strings.HasSuffix(rel, "contract.yaml") {
			err = checkScrubbedContract(data, out)
		}
	default:
		out = []byte(f(string(data)))
	}
	if err != nil {
		return false, err
	}
	// The file is judged by what the sink would write: its canonical form. A
	// file the passes left alone is still rewritten when its bytes differ
	// from that form, so no comment, tag, directive or other syntax that the
	// checked value does not carry stays on disk.
	canon, err := guardWrite(dest, out)
	if err != nil {
		return false, err
	}
	changed := !bytes.Equal(canon, data)
	if !changed || dryRun {
		return changed, nil
	}
	if err := layout.EnsureLocalDir(p, p.TempDir()); err != nil {
		return false, err
	}
	if mo == nil {
		err = scrubWrite(p.TempDir(), layout.TmpPattern(".scrub", ""), dest, canon)
	} else {
		err = writeAtomicFileWithWarning(p.TempDir(), layout.TmpPattern(".scrub", ""), dest, canon, mo, "mission scrub: "+dest+": rewrite installed")
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
