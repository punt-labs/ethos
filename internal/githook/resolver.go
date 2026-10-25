package githook

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/textscan"
)

// HooksDir returns the hooks directory git runs for the repo at repoRoot,
// plus any advisory warnings about a core.hooksPath that lands inside the
// work tree (a tracked file the seal would dirty) or outside the repo
// (shared across every repo using that hooksPath).
//
// It asks git directly first — `git rev-parse --git-path hooks` honors
// core.hooksPath and resolves a worktree's common dir. When git cannot answer
// it falls back to reading .git and commondir by hand, so a worktree still
// lands on the common hooks dir git actually runs. That fallback cannot see
// core.hooksPath, so it adds a warning saying the lookup was degraded and why.
// This is the one resolver install, enable, and doctor share, so they cannot
// drift; the manual fallback is kept (not install.sh's git-required form) so
// enable resolves in a git-less environment where doctor already succeeds.
//
// repoRoot is made absolute first; ETHOS_REPO_ROOT may be relative.
func HooksDir(repoRoot string) (string, []string) {
	if abs, err := filepath.Abs(repoRoot); err == nil {
		repoRoot = abs
	}
	dir, warns, err := gitHooksPath(repoRoot)
	switch {
	case err == nil:
		return dir, warns
	case errors.Is(err, errNotWorkTreeRoot):
		// git answered for some other work tree, so this root has no git
		// configuration to ignore; the manual lookup is the whole answer.
		return manualHooksDir(repoRoot), nil
	}
	dir = manualHooksDir(repoRoot)
	return dir, []string{fmt.Sprintf(
		"git did not resolve the hooks directory (%v); core.hooksPath is ignored and %s is assumed", err, dir)}
}

// errNotWorkTreeRoot says git ran and reported a work tree other than repoRoot.
var errNotWorkTreeRoot = errors.New("not the root of a git work tree")

// gitHooksPath returns git's own resolution of the hooks directory and
// warnings about a diverted core.hooksPath. A non-nil error says why git did
// not answer. The work-tree-root anchor matters: without it git would walk up
// from a non-repo repoRoot and resolve an ancestor repo's hooks.
func gitHooksPath(repoRoot string) (string, []string, error) {
	top, err := gitOut(repoRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil, fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	if !textscan.SamePath(top, repoRoot) {
		return "", nil, fmt.Errorf("git reports the work tree at %s, not %s: %w", top, repoRoot, errNotWorkTreeRoot)
	}
	hdRaw, err := gitOut(repoRoot, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", nil, fmt.Errorf("git rev-parse --git-path hooks: %w", err)
	}
	if hdRaw == "" {
		return "", nil, fmt.Errorf("git rev-parse --git-path hooks printed nothing")
	}
	hd := hdRaw
	if !filepath.IsAbs(hd) {
		hd = filepath.Join(repoRoot, hd)
	}
	return hd, classifyHooksPath(repoRoot, top, hdRaw), nil
}

// classifyHooksPath warns when the resolved hooks dir is not under the git
// dir: inside the work tree (a tracked file) or outside the repo (shared).
// Comparison is done on symlink-resolved paths so a symlinked temp root
// (macOS /tmp → /private/tmp) does not defeat the prefix test.
func classifyHooksPath(repoRoot, top, hdRaw string) []string {
	common, err := gitOut(repoRoot, "rev-parse", "--git-common-dir")
	if err != nil || common == "" {
		return nil
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(repoRoot, common)
	}
	hd := hdRaw
	if !filepath.IsAbs(hd) {
		hd = filepath.Join(repoRoot, hd)
	}
	switch {
	case isUnder(hd, common):
		return nil // under the git dir — private, not tracked
	case isUnder(hd, top):
		return []string{fmt.Sprintf(
			"core.hooksPath places hooks at %s inside the work tree — the seal will be written into a tracked file", hd)}
	default:
		return []string{fmt.Sprintf(
			"core.hooksPath places hooks outside the repo at %s — shared across every repo using this hooksPath", hd)}
	}
}

// manualHooksDir resolves the hooks dir without git by reading the ".git"
// gitdir file and its "commondir", so a worktree still lands on the common
// ".git/hooks" git actually runs.
func manualHooksDir(repoRoot string) string {
	dot := filepath.Join(repoRoot, ".git")
	gd := dot
	if info, err := os.Stat(dot); err != nil || !info.IsDir() {
		if data, err := os.ReadFile(dot); err == nil {
			line := strings.TrimSpace(string(data))
			if target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:")); target != line && target != "" {
				gd = target
				if !filepath.IsAbs(gd) {
					gd = filepath.Join(repoRoot, gd)
				}
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(gd, "commondir")); err == nil {
		if common := strings.TrimSpace(string(data)); common != "" {
			if !filepath.IsAbs(common) {
				common = filepath.Join(gd, common)
			}
			gd = common
		}
	}
	return filepath.Join(gd, "hooks")
}

func gitOut(repoRoot string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", repoRoot}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// isUnder reports whether path is dir itself or a descendant of dir, on
// symlink-resolved paths.
func isUnder(path, dir string) bool {
	p := evalPath(path)
	d := evalPath(dir)
	if p == d {
		return true
	}
	return strings.HasPrefix(p, d+string(os.PathSeparator))
}

// evalPath resolves symlinks when it can, else returns the cleaned path.
func evalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
