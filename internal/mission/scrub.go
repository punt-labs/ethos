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
func ScrubFiles(repoRoot string, rels []string, dryRun bool) (ScrubResult, error) {
	var res ScrubResult
	r, err := NewPathRedactor(repoRoot)
	if err != nil {
		return res, fmt.Errorf("scrubbing %s: %w", repoRoot, err)
	}
	sorted := append([]string(nil), rels...)
	sort.Strings(sorted)
	var failed []error
	for _, rel := range sorted {
		if !scrubbable(rel) {
			continue
		}
		did, err := scrubFile(r, repoRoot, rel, dryRun)
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
		// A contract's enforcement fields get the path pass only, as in
		// the writer; see the redact:"path" tags on Contract.
		if strings.HasSuffix(rel, "contract.yaml") {
			out, changed, err = redactYAMLDocPaths(f, r.Path, data)
		} else {
			out, changed, err = redactYAMLDoc(f, data)
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
