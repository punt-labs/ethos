package audit

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

// gitRun runs git in repoRoot, bounded by gitenv's deadline and in the
// caller's environment: the seal runs inside a pre-commit hook, where
// GIT_DIR and GIT_INDEX_FILE must reach git. A failing git's stderr is part
// of the error; a git that does not answer is gitenv.ErrTimeout.
func gitRun(repoRoot, what string, args ...string) ([]byte, error) {
	return gitRunIn(repoRoot, what, nil, args...)
}

// gitRunIn is gitRun with stdin for git to read.
func gitRunIn(repoRoot, what string, stdin io.Reader, args ...string) ([]byte, error) {
	out, err := gitenv.Run(gitenv.Spec{Git: "git", Dir: repoRoot, Args: args, Stdin: stdin})
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("%s: %w: %s", what, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return out, nil
}

// relPathspec converts an absolute path to a repo-relative, slash-separated git
// pathspec. Git matches a pathspec against the work-tree prefix; an absolute
// path under a symlinked root (macOS /tmp and TMPDIR resolve through /private)
// fails that prefix match, so a chunk that exists on disk reads as unmatched —
// the seal then fails closed or silently skips staging. Relativizing against
// repoRoot and running git with cmd.Dir=repoRoot sidesteps git's absolute-path
// resolution entirely. A path already relative is only slash-normalized.
func relPathspec(repoRoot, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return filepath.ToSlash(path), nil
	}
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return "", fmt.Errorf("relativizing %s against %s: %w", path, repoRoot, err)
	}
	return filepath.ToSlash(rel), nil
}

// relPathspecs relativizes each path against repoRoot.
func relPathspecs(repoRoot string, paths []string) ([]string, error) {
	out := make([]string, len(paths))
	for i, p := range paths {
		rel, err := relPathspec(repoRoot, p)
		if err != nil {
			return nil, err
		}
		out[i] = rel
	}
	return out, nil
}

// GitAdd stages one or more paths from repoRoot and returns the ones it
// staged, as the caller passed them. It skips any path the repo ignores: a repo
// that gitignores its session audit has chosen not to track it, and `git add`
// on an ignored path exits 1, which would block every commit. An ignored path
// is not an error and is not in the result; one note per process names the
// skip. A failure of the ignore check, or of the add itself, is returned:
// staging a new or orphan chunk is fail-closed (exit 2) per §Seal failure
// policy.
func GitAdd(repoRoot string, paths ...string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	specs, err := relPathspecs(repoRoot, paths)
	if err != nil {
		return nil, err
	}
	kept, err := dropIgnored(repoRoot, specs)
	if err != nil {
		return nil, err
	}
	if len(kept) == 0 {
		return nil, nil
	}
	args := append([]string{"add", "--"}, kept...)
	if _, err = gitRun(repoRoot, "git add", args...); err != nil {
		return nil, err
	}
	keep := make(map[string]bool, len(kept))
	for _, s := range kept {
		keep[s] = true
	}
	var staged []string
	for i, p := range paths {
		if keep[specs[i]] {
			staged = append(staged, p)
		}
	}
	return staged, nil
}

// ignoreNoteOut receives the one-per-process note that a path was skipped
// because the repo ignores it; ignoreNote guards it. Tests replace both.
var (
	ignoreNoteOut io.Writer = os.Stderr
	ignoreNote    sync.Once
)

// ignoredSet returns the specs git ignores. Tracked paths are never reported
// ignored by check-ignore, so a tracked chunk is not in the set.
func ignoredSet(repoRoot string, specs []string) (map[string]bool, error) {
	// Paths go on stdin, NUL-separated: -z is only valid with --stdin, and it
	// keeps git from quoting odd names in its answer.
	in := strings.NewReader(strings.Join(specs, "\x00") + "\x00")
	out, err := gitRunIn(repoRoot, "git check-ignore", in, "check-ignore", "-z", "--stdin")
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil, nil // exit 1: no path is ignored
		}
		return nil, err
	}
	ignored := make(map[string]bool)
	for _, t := range bytes.Split(out, []byte{0}) {
		if len(t) > 0 {
			ignored[string(t)] = true
		}
	}
	return ignored, nil
}

// IsIgnored reports whether git ignores path, which need not exist. A failure
// of the check is returned, never read as "not ignored".
func IsIgnored(repoRoot, path string) (bool, error) {
	spec, err := relPathspec(repoRoot, path)
	if err != nil {
		return false, err
	}
	ignored, err := ignoredSet(repoRoot, []string{spec})
	if err != nil {
		return false, err
	}
	return ignored[spec], nil
}

// TracksAnyUnder reports whether the index lists, or has a staged change to,
// any path under dir: a tracked file, a staged addition or rename, a staged
// removal. A failure of git is returned, and a caller that must not guess
// treats it as "tracked".
func TracksAnyUnder(repoRoot, dir string) (bool, error) {
	spec, err := relPathspec(repoRoot, dir)
	if err != nil {
		return false, err
	}
	for _, args := range [][]string{
		{"ls-files", "-z", "--", spec},
		{"diff", "--cached", "--name-only", "-z", "--", spec}, // a staged removal is not in ls-files
	} {
		out, err := gitRun(repoRoot, "git "+args[0], args...)
		if err != nil {
			return false, err
		}
		if len(out) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// LocalOnly reports whether path, in directory dir, can never reach the tracked
// tree: the repo ignores it and the index holds nothing under dir. Either git
// question failing is an error; the caller does not guess.
func LocalOnly(repoRoot, dir, path string) (bool, error) {
	ignored, err := IsIgnored(repoRoot, path)
	if err != nil {
		return false, fmt.Errorf("could not tell whether the repo ignores %s: %w", path, err)
	}
	if !ignored {
		return false, nil
	}
	tracked, err := TracksAnyUnder(repoRoot, dir)
	if err != nil {
		return false, fmt.Errorf("could not tell whether the index holds anything under %s: %w", dir, err)
	}
	return !tracked, nil
}

// dropIgnored returns the specs git does not ignore, in order.
func dropIgnored(repoRoot string, specs []string) ([]string, error) {
	ignored, err := ignoredSet(repoRoot, specs)
	if err != nil {
		return nil, err
	}
	kept := make([]string, 0, len(specs))
	skipped := ""
	for _, s := range specs {
		if ignored[s] {
			skipped = s
			continue
		}
		kept = append(kept, s)
	}
	if skipped != "" {
		ignoreNote.Do(func() {
			fmt.Fprintf(ignoreNoteOut, "ethos: not staging %s: this repo gitignores it, so its audit stays local\n", skipped)
		})
	}
	return kept, nil
}

// GitMv renames src to dst via git so the move is staged. Used by quarantine
// to retire a corrupt chunk to its .corrupt name.
func GitMv(repoRoot, src, dst string) error {
	relSrc, err := relPathspec(repoRoot, src)
	if err != nil {
		return err
	}
	relDst, err := relPathspec(repoRoot, dst)
	if err != nil {
		return err
	}
	_, err = gitRun(repoRoot, "git mv", "mv", "--", relSrc, relDst)
	return err
}

// IsGitlinkMount reports whether <repoRoot>/.punt-labs/ethos is a gitlink
// (submodule) rather than regular tracked files. In that configuration the
// sealed-chunk target tree is unreachable and the seal defers. A file with
// git mode 160000 is a gitlink. Outside a repository there is none (false).
// Any other git failure — a timeout, dubious ownership, git not installed —
// is an error: seal and enable must not proceed on a guess.
func IsGitlinkMount(repoRoot string) (bool, error) {
	rel := filepath.Join(".punt-labs", "ethos")
	out, absent, err := gitenv.Read(gitenv.Spec{
		Git: "git", Dir: repoRoot, Env: gitenv.Inherited(),
		Args: []string{"ls-files", "--stage", "--", rel},
	}, gitenv.AbsentNotRepo)
	if err != nil {
		return false, fmt.Errorf("git ls-files in %s: %w", repoRoot, err)
	}
	if absent {
		return false, nil // not a repository: there is no gitlink
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "160000") {
			return true, nil
		}
	}
	return false, nil
}

// UntrackedOrModified returns the subset of paths git reports as untracked or
// modified relative to the index, so a re-seal does not report an
// already-committed chunk as newly staged.
func UntrackedOrModified(repoRoot string, paths []string) ([]string, error) {
	specs, err := relPathspecs(repoRoot, paths)
	if err != nil {
		return nil, err
	}
	args := append([]string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--"}, specs...)
	out, err := gitRun(repoRoot, "git status", args...)
	if err != nil {
		return nil, err
	}
	// -z entries are "XY path", and a rename or copy is followed by one more
	// entry, the old path, which is skipped: the new path is the one that
	// matches.
	dirty := make(map[string]struct{})
	toks := bytes.Split(out, []byte{0})
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if len(t) < 4 {
			continue
		}
		dirty[string(t[3:])] = struct{}{}
		if t[0] == 'R' || t[0] == 'C' || t[1] == 'R' || t[1] == 'C' {
			i++
		}
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

// IsTracked reports whether path is in the index.
func IsTracked(repoRoot, path string) (bool, error) {
	spec, err := relPathspec(repoRoot, path)
	if err != nil {
		return false, err
	}
	out, err := gitRun(repoRoot, "git ls-files", "ls-files", "--", spec)
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}

// GitRmCached stages the removal of path from the index and leaves the file.
// It forces: a path staged and then modified has index content that matches
// neither the file nor HEAD, which git otherwise refuses to drop.
func GitRmCached(repoRoot, path string) error {
	spec, err := relPathspec(repoRoot, path)
	if err != nil {
		return err
	}
	_, err = gitRun(repoRoot, "git rm --cached", "rm", "--cached", "-f", "-q", "--", spec)
	return err
}

// IndexBlob returns the content the index holds at path, and false when the
// index has no entry for it.
func IndexBlob(repoRoot, path string) ([]byte, bool, error) {
	tracked, err := IsTracked(repoRoot, path)
	if err != nil || !tracked {
		return nil, false, err
	}
	spec, err := relPathspec(repoRoot, path)
	if err != nil {
		return nil, false, err
	}
	out, err := gitRun(repoRoot, "git show :"+spec, "show", ":"+spec)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
