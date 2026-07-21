package audit

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
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
	cmd := exec.Command("git", "mv", "--", relSrc, relDst)
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git mv: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// IsGitlinkMount reports whether <repoRoot>/.punt-labs/ethos is a gitlink
// (submodule) rather than regular tracked files. In that configuration the
// sealed-chunk target tree is unreachable and the seal defers. A file with
// git mode 160000 is a gitlink.
func IsGitlinkMount(repoRoot string) bool {
	rel := filepath.Join(".punt-labs", "ethos")
	cmd := exec.Command("git", "ls-files", "--stage", "--", rel)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "160000") {
			return true
		}
	}
	return false
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
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
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
