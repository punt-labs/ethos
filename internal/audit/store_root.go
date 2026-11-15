package audit

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
)

// StoreRootOf returns the canonical store root S of the checkout at root: a
// checkout whose git dir is the common dir is its own store; a linked worktree
// of a non-bare repository uses the work tree holding the common .git; a
// directory outside a repository is its own store. internal/resolve holds the
// authoritative rule but imports this package, so the rule is repeated here for
// callers (the purge) that must find another checkout's store.
func StoreRootOf(root string) (string, error) {
	out, absent, err := gitenv.Read(gitenv.Spec{
		Git: "git", Dir: root, Env: gitenv.Parsed(),
		Args: []string{"rev-parse", "--absolute-git-dir", "--git-common-dir"},
	}, gitenv.AbsentNotRepo)
	if err != nil {
		return "", fmt.Errorf("finding the store of %s: %w", root, err)
	}
	if absent {
		return root, nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return "", fmt.Errorf("finding the store of %s: git answered %q, not two paths", root, out)
	}
	gitDir, common := lines[0], lines[1]
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	gi, gerr := os.Stat(gitDir)
	ci, cerr := os.Stat(common)
	if gerr == nil && cerr == nil && os.SameFile(gi, ci) {
		return root, nil
	}
	common = filepath.Clean(common)
	// A bare repository has no work tree to hold the store: the linked
	// worktree is its own.
	bare, err := commonIsBare(common)
	if err != nil {
		return "", fmt.Errorf("finding the store of %s: %w", root, err)
	}
	if bare || filepath.Base(common) != ".git" {
		return root, nil
	}
	return filepath.Dir(common), nil
}

// commonIsBare reports whether the repository at common sets core.bare.
// `--file` reads exactly that one config; exit 1 with a clean stderr is "unset".
func commonIsBare(common string) (bool, error) {
	out, _, err := gitenv.ReadNeutralConfig("git", 0, "--file", filepath.Join(common, "config"), "--bool", "core.bare")
	var ee *exec.ExitError
	switch {
	case err == nil:
		return strings.TrimSpace(string(out)) == "true", nil
	case errors.As(err, &ee) && ee.ExitCode() == 1 && gitenv.WithoutTrace(string(ee.Stderr)) == "":
		return false, nil
	default:
		return false, fmt.Errorf("reading core.bare of %s: %w", common, err)
	}
}
