package githook

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

// Lookup is the answer to "where does git run this repo's hooks".
//
// Warnings holds every advisory line for the user: a core.hooksPath that lands
// inside the work tree or outside the repo, and the notice of a degraded
// lookup. Degraded says the directory was not fully verified against git;
// Reason says why. The directory is not exported: a caller that acts on it
// goes through Verified, which refuses a lookup it cannot trust, and one that
// only shows it uses Display.
type Lookup struct {
	dir      string
	root     string // the work tree the lookup was made for
	Warnings []string
	Degraded bool
	Reason   string
	cause    error // why the lookup is degraded; Remedy classifies it
}

// Display returns the hooks directory for a message or a read-only scan. Do not
// create or write into it: it may be a guess.
func (l Lookup) Display() string { return l.dir }

// Explain returns Reason, followed by the advice for its cause when there is
// one.
func (l Lookup) Explain() string {
	if r := l.Remedy(); r != "" {
		return l.Reason + "; " + r
	}
	return l.Reason
}

// Remedy returns advice matched to why the lookup is degraded, or "" when the
// cause is not one it recognizes: telling a user to install git when git is
// installed sends them the wrong way.
func (l Lookup) Remedy() string {
	var mismatch *workTreeError
	msg := ""
	if l.cause != nil {
		msg = l.cause.Error()
	}
	switch {
	case errors.Is(l.cause, exec.ErrNotFound):
		return "install git"
	case strings.Contains(msg, "dubious ownership") || strings.Contains(msg, "safe.directory"):
		return "fix safe.directory"
	case errors.As(l.cause, &mismatch):
		return "run from, or point ETHOS_REPO_ROOT at, the repository's top level"
	}
	return ""
}

// workTreeError says git reports a work tree other than the one asked about.
type workTreeError struct{ top, root string }

func (e *workTreeError) Error() string {
	return fmt.Sprintf("git reports the work tree at %s, not %s; %s is a subdirectory of another repository or has no .git of its own", e.top, e.root, e.root)
}

// Verified returns the hooks directory only when git itself named it. A
// degraded lookup ignores core.hooksPath, so acting on its directory may touch
// hooks git never runs, or miss the ones it does.
func (l Lookup) Verified() (string, error) {
	if l.Degraded {
		return "", fmt.Errorf("hooks directory not verified: %s", l.Reason)
	}
	return l.dir, nil
}

// HooksDir returns the hooks directory git runs for the repo at repoRoot.
//
// It asks git directly first — `git rev-parse --git-path hooks` honors
// core.hooksPath and resolves a worktree's common dir. When git cannot answer
// (missing, failing, or reporting a work tree that is not repoRoot) it falls
// back to reading .git and commondir by hand and marks the Lookup degraded.
// That fallback cannot see core.hooksPath. This is the one resolver install,
// enable, disable, and doctor share, so they cannot drift. The manual fallback
// exists so doctor and disable can report where hooks would be without git;
// enable requires a verified lookup and refuses a degraded one.
//
// repoRoot is made absolute first; ETHOS_REPO_ROOT may be relative.
func HooksDir(repoRoot string) Lookup {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return degradedLookup(repoRoot, manualHooksDir(repoRoot), fmt.Errorf("making %s absolute: %w", repoRoot, err))
	}
	l, err := gitHooksPath(root)
	if err != nil {
		return degradedLookup(root, manualHooksDir(root), err)
	}
	return l
}

// degradedLookup builds the Lookup for a hooks dir found without git's help.
func degradedLookup(root, dir string, cause error) Lookup {
	return Lookup{
		dir:      dir,
		root:     root,
		Degraded: true,
		Reason:   cause.Error(),
		cause:    cause,
		Warnings: []string{fmt.Sprintf(
			"git did not resolve the hooks directory (%s); core.hooksPath is ignored and %s is assumed", cause, dir)},
	}
}

// gitHooksPath returns git's own resolution of the hooks directory. A non-nil
// error says why git did not answer. The work-tree-root anchor matters:
// without it git would walk up from a non-repo repoRoot and resolve an
// ancestor repo's hooks.
func gitHooksPath(repoRoot string) (Lookup, error) {
	top, err := gitOut(repoRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return Lookup{}, fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	if !sameFile(top, repoRoot) {
		return Lookup{}, &workTreeError{top: top, root: repoRoot}
	}
	hdRaw, err := gitOut(repoRoot, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return Lookup{}, fmt.Errorf("git rev-parse --git-path hooks: %w", err)
	}
	if hdRaw == "" {
		return Lookup{}, errors.New("git rev-parse --git-path hooks printed nothing")
	}
	hd := hdRaw
	if !filepath.IsAbs(hd) {
		hd = filepath.Join(repoRoot, hd)
	}
	warns, err := classifyHooksPath(repoRoot, top, hdRaw)
	l := Lookup{dir: hd, root: repoRoot, Warnings: warns}
	if err != nil {
		l.Degraded = true
		l.Reason = err.Error()
		l.cause = err
		l.Warnings = append(l.Warnings, "hooks directory classification incomplete: "+l.Reason)
	}
	return l, nil
}

// sameFile reports whether a and b name the same directory, by file identity.
// Text comparison would call `~/coding/x` and `~/Coding/x` different on a
// case-insensitive file system, where git reports one spelling.
func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// classifyHooksPath warns when the resolved hooks dir is not under the git
// dir: inside the work tree (a tracked file) or outside the repo (shared).
// Comparison is done on symlink-resolved paths so a symlinked temp root
// (macOS /tmp → /private/tmp) does not defeat the prefix test. When git cannot
// name the common dir it classifies against <top>/.git, the default location,
// and returns the failure so the caller can report the lookup unverified.
func classifyHooksPath(repoRoot, top, hdRaw string) ([]string, error) {
	hd := hdRaw
	if !filepath.IsAbs(hd) {
		hd = filepath.Join(repoRoot, hd)
	}
	common, err := gitOut(repoRoot, "rev-parse", "--git-common-dir")
	if err == nil && common == "" {
		err = errors.New("git rev-parse --git-common-dir printed nothing")
	} else if err != nil {
		err = fmt.Errorf("git rev-parse --git-common-dir: %w", err)
	}
	if err != nil {
		common = filepath.Join(top, ".git")
	} else if !filepath.IsAbs(common) {
		common = filepath.Join(repoRoot, common)
	}
	switch {
	case isUnder(hd, common):
		return nil, err // under the git dir — private, not tracked
	case isUnder(hd, top):
		return []string{fmt.Sprintf(
			"core.hooksPath places hooks at %s inside the work tree — the seal will be written into a tracked file", hd)}, err
	default:
		return []string{fmt.Sprintf(
			"core.hooksPath places hooks outside the repo at %s — shared across every repo using this hooksPath", hd)}, err
	}
}

// manualGitDir resolves repoRoot's git directory without git: ".git" itself, or
// the target of its "gitdir:" line. The result is not checked to exist.
func manualGitDir(repoRoot string) string {
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
	return gd
}

// manualHooksDir resolves the hooks dir without git by reading the git
// directory and its "commondir", so a worktree still lands on the common
// ".git/hooks" git actually runs.
func manualHooksDir(repoRoot string) string {
	gd := manualGitDir(repoRoot)
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

// gitOut runs git in repoRoot and returns its trimmed stdout. A failing git's
// stderr is part of the error: "not a git repository" and "dubious ownership"
// are what the user needs to see, not "exit status 128". git runs in the C
// locale, so Remedy can match those words, and without an exported GIT_DIR,
// so it answers for repoRoot (gitenv).
func gitOut(repoRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
	cmd.Env = gitenv.Parsed()
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
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
