package mission

import (
	"bytes"
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
)

// TrackedRecordFiles returns the repo-relative, slash-separated paths git
// tracks under .punt-labs/ethos. It is the file list ScrubFiles is meant
// to be given: scrubbing only tracked files leaves the live zone and any
// in-flight mission untouched.
func TrackedRecordFiles(repoRoot string) ([]string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "-z", "--", ".punt-labs/ethos").Output()
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
// contract.yaml, results.yaml or reflections.yaml, a delegation's
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
		return parts[4] == "contract.yaml" || parts[4] == "results.yaml" || parts[4] == "reflections.yaml"
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
// deleted from the worktree is skipped. A file the redactor leaves
// unchanged keeps its bytes, and so does every unaffected line of a JSONL
// file. Like every record writer it fails closed when no redactor can be
// built.
//
// Each file is read, redacted and renamed under the lock its writers hold
// (the trace lock, the per-mission lock, the delegation lock), so a
// concurrent close or update is not lost to the rewrite. globalRoot is the
// shared store root those locks live under. A symlinked artifact or
// containing directory is refused, as every loader here refuses it.
func ScrubFiles(repoRoot, globalRoot string, rels []string, dryRun bool) (ScrubResult, error) {
	var res ScrubResult
	r, err := NewPathRedactor(repoRoot)
	if err != nil {
		return res, fmt.Errorf("scrubbing %s: %w", repoRoot, err)
	}
	if strings.TrimSpace(globalRoot) == "" {
		return res, fmt.Errorf("scrubbing %s: the global store root is required for the record locks", repoRoot)
	}
	s := NewStoreWithRoots(repoRoot, globalRoot)
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
var scrubWrite = writeAtomicFile

// scrubFileLocked is scrubFile under the locks the file's writers take,
// after refusing a symlinked path.
func (s *Store) scrubFileLocked(r PathRedactor, rel string, dryRun bool) (bool, error) {
	if err := rejectSymlinkedPath(s.repoRoot, rel); err != nil {
		return false, err
	}
	var did bool
	err := s.withRecordLocks(rel, func() error {
		var err error
		did, err = scrubFile(r, s.repoRoot, rel, dryRun)
		return err
	})
	return did, err
}

// withRecordLocks runs fn holding the lock that serializes rel's writers:
// the trace lock for missions.jsonl, the per-mission lock for a mission's
// contract, results and reflections, the delegation lock for a delegation
// record. Plain export files have no writer lock.
func (s *Store) withRecordLocks(rel string, fn func() error) error {
	rel = path.Clean(rel)
	parts := strings.Split(rel, "/")
	switch {
	case rel == ".punt-labs/ethos/missions.jsonl":
		return withTraceLock(RepoStatePath(s.repoRoot), fn)
	case len(parts) == 5:
		return s.withLock(parts[3], fn)
	case len(parts) == 7:
		release, err := AcquireDelegationLock(s.root, parts[5])
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
	var changed bool
	switch path.Ext(rel) {
	case ".jsonl":
		out, changed, err = redactJSONLines(f, data)
	case ".yaml":
		// The record's type supplies the modes, as in the writer: path
		// fields get the path pass only and IDs and enums are kept.
		out, changed, err = redactYAMLDoc(f, r.Path, artifactType(path.Base(rel)), data)
		if err == nil && changed && strings.HasSuffix(rel, "contract.yaml") {
			err = checkScrubbedContract(data, out)
		}
	default:
		s := f(string(data))
		out, changed = []byte(s), s != string(data)
	}
	if err != nil {
		return false, err
	}
	if !changed || dryRun {
		return changed, nil
	}
	if err := scrubWrite(filepath.Dir(dest), ".scrub-*.tmp", dest, out); err != nil {
		return false, err
	}
	return true, nil
}
