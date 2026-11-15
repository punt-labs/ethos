package audit

import (
	"fmt"
	"os"
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
	if filepath.Base(common) != ".git" {
		return root, nil
	}
	return filepath.Dir(common), nil
}
