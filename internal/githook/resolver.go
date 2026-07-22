package githook

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HooksDir returns the hooks directory git runs for the repo at repoRoot,
// plus any advisory warnings about a core.hooksPath that lands inside the
// work tree (a tracked file the seal would dirty) or outside the repo
// (shared across every repo using that hooksPath).
//
// It asks git directly first — `git rev-parse --git-path hooks` honors
// core.hooksPath and resolves a worktree's common dir. When git is
// unavailable it falls back to reading .git and commondir by hand, so a
// worktree still lands on the common hooks dir git actually runs. This is the
// one resolver install, enable, and doctor share, so they cannot drift; the
// manual fallback is kept (not install.sh's git-required form) so enable
// resolves in a git-less environment where doctor already succeeds.
func HooksDir(repoRoot string) (string, []string) {
	if dir, warns, ok := gitHooksPath(repoRoot); ok {
		return dir, warns
	}
	return manualHooksDir(repoRoot), nil
}

// gitHooksPath returns git's own resolution of the hooks directory, warnings
// about a diverted core.hooksPath, and whether git answered. The
// work-tree-root anchor matters: without it git would walk up from a non-repo
// repoRoot and resolve an ancestor repo's hooks.
func gitHooksPath(repoRoot string) (string, []string, bool) {
	top, err := gitOut(repoRoot, "rev-parse", "--show-toplevel")
	if err != nil || !samePath(top, repoRoot) {
		return "", nil, false
	}
	hdRaw, err := gitOut(repoRoot, "rev-parse", "--git-path", "hooks")
	if err != nil || hdRaw == "" {
		return "", nil, false
	}
	hd := hdRaw
	if !filepath.IsAbs(hd) {
		hd = filepath.Join(repoRoot, hd)
	}
	return hd, classifyHooksPath(repoRoot, top, hdRaw), true
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

// samePath reports whether a and b name the same location, tolerating the
// symlinked temp roots (macOS /tmp → /private/tmp) tests run under.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}
