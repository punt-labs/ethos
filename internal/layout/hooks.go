package layout

import (
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
// directory rather than from the static Table.
func HookEntries(repoRoot, hooksDir string) []Entry {
	rel, err := filepath.Rel(evalPath(repoRoot), evalPath(hooksDir))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	rel = filepath.ToSlash(rel)
	// .git is not tracked; .punt-labs is ethos's own zones, whose classes a
	// source hook entry would overlap.
	for _, z := range []string{".git", ".punt-labs"} {
		if rel == z || strings.HasPrefix(rel, z+"/") {
			return nil
		}
	}
	out := make([]Entry, 0, len(hookNames))
	for _, n := range hookNames {
		out = append(out, Entry{Pattern: escape(path.Join(rel, n)), Class: Source})
	}
	return out
}

// TableFor returns Table with the hook entries for the resolved hooks
// directory placed before the fallback entries, which must stay last.
func TableFor(repoRoot, hooksDir string) []Entry {
	var out, fallback []Entry
	for _, e := range Table {
		if e.Fallback {
			fallback = append(fallback, e)
		} else {
			out = append(out, e)
		}
	}
	out = append(out, HookEntries(repoRoot, hooksDir)...)
	return append(out, fallback...)
}

// evalPath resolves symlinks in the longest existing prefix of p and appends
// the rest, so a directory not yet created resolves the way the repo root does.
func evalPath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
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
