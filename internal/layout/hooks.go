package layout

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// hookNames are the git hooks `ethos enable` writes.
var hookNames = []string{"pre-commit", "commit-msg"}

// HookEntries returns class-1 entries for the hook files `ethos enable` writes
// into hooksDir, when core.hooksPath places that directory inside the working
// tree at repoRoot. It returns nil when hooksDir is outside the tree (the
// default .git/hooks included) or inside .punt-labs/, because those files are
// not the repository's or belong to ethos's own classes.
// The directory is the user's choice, so the entries come from the resolved
// directory rather than from the static Table. A path that cannot be resolved
// for a reason other than not existing is an error.
func HookEntries(repoRoot, hooksDir string) ([]Entry, error) {
	rel, ok, err := relInTree(repoRoot, hooksDir)
	if err != nil || !ok {
		return nil, err
	}
	// .git is not tracked; .punt-labs is ethos's own zones, whose classes a
	// source hook entry would overlap.
	for _, z := range []string{".git", ".punt-labs"} {
		if underOrEqual(rel, z) {
			return nil, nil
		}
	}
	out := make([]Entry, 0, len(hookNames))
	for _, n := range hookNames {
		out = append(out, Entry{Pattern: escape(path.Join(rel, n)), Class: Source})
	}
	return out, nil
}

// PuntLabsHookPaths returns the repoRoot-relative paths of the hook files
// `ethos enable` writes into hooksDir, when hooksDir is .punt-labs/ or inside
// it. No table entry claims those files, so a caller can report them.
func PuntLabsHookPaths(repoRoot, hooksDir string) ([]string, error) {
	rel, ok, err := relInTree(repoRoot, hooksDir)
	if err != nil || !ok || !underOrEqual(rel, ".punt-labs") {
		return nil, err
	}
	out := make([]string, 0, len(hookNames))
	for _, n := range hookNames {
		out = append(out, path.Join(rel, n))
	}
	return out, nil
}

// TableFor returns Table with the hook entries for the resolved hooks
// directory placed before the fallback entries, which must stay last.
func TableFor(repoRoot, hooksDir string) ([]Entry, error) {
	hooks, err := HookEntries(repoRoot, hooksDir)
	if err != nil {
		return nil, err
	}
	var out, fallback []Entry
	for _, e := range Table {
		if e.Fallback {
			fallback = append(fallback, e)
		} else {
			out = append(out, e)
		}
	}
	out = append(out, hooks...)
	return append(out, fallback...), nil
}

// relInTree returns hooksDir as a slash path relative to repoRoot, and whether
// it lies inside the tree.
func relInTree(repoRoot, hooksDir string) (string, bool, error) {
	root, err := evalPath(repoRoot)
	if err != nil {
		return "", false, err
	}
	dir, err := evalPath(hooksDir)
	if err != nil {
		return "", false, err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false, nil
	}
	return filepath.ToSlash(rel), true, nil
}

// underOrEqual reports whether rel is dir or a path beneath it.
func underOrEqual(rel, dir string) bool {
	return rel == dir || strings.HasPrefix(rel, dir+"/")
}

// evalPath resolves symlinks in the longest existing prefix of p and appends
// the rest, so a directory not yet created resolves the way the repo root does.
// Only a missing path is walked past; any other failure is returned.
func evalPath(p string) (string, error) {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(r, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolving %s: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// escape quotes the glob metacharacters in a literal path.
func escape(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '*', '?', '[', '\\':
			b.WriteByte('\\')
		}
		b.WriteByte(p[i])
	}
	return b.String()
}
