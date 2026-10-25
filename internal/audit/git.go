package audit

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

// gitRun runs git in repoRoot, bounded by gitenv's deadline and in the
// caller's environment: the seal runs inside a pre-commit hook, where
// GIT_DIR and GIT_INDEX_FILE must reach git. A failing git's stderr is part
// of the error; a git that does not answer is gitenv.ErrTimeout.
func gitRun(repoRoot, what string, args ...string) ([]byte, error) {
	out, err := gitenv.OutputEnv(repoRoot, "git", nil, args...)
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

// GitAdd stages one or more paths from repoRoot. A git-add failure staging a
// new or orphan chunk is fail-closed (exit 2) per §Seal failure policy, so
// the error is returned, never swallowed.
func GitAdd(repoRoot string, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	specs, err := relPathspecs(repoRoot, paths)
	if err != nil {
		return err
	}
	args := append([]string{"add", "--"}, specs...)
	_, err = gitRun(repoRoot, "git add", args...)
	return err
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
// git mode 160000 is a gitlink. A git that fails (not a repository) answers
// false; a git that does not answer is an error, since seal or enable must
// not proceed on a guess.
func IsGitlinkMount(repoRoot string) (bool, error) {
	rel := filepath.Join(".punt-labs", "ethos")
	out, err := gitRun(repoRoot, "git ls-files", "ls-files", "--stage", "--", rel)
	if errors.Is(err, gitenv.ErrTimeout) {
		return false, err
	}
	if err != nil {
		return false, nil
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
	args := append([]string{"status", "--porcelain", "--untracked-files=all", "--"}, specs...)
	out, err := gitRun(repoRoot, "git status", args...)
	if err != nil {
		return nil, err
	}
	dirty := make(map[string]struct{})
	for _, line := range SplitLines(out) {
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
