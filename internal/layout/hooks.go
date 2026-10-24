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
// default .git/hooks included), because those files are not the repository's.
// The directory is the user's choice, so the entries come from the resolved
// directory rather than from the static Table.
func HookEntries(repoRoot, hooksDir string) []Entry {
	rel, err := filepath.Rel(evalPath(repoRoot), evalPath(hooksDir))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}
	rel = filepath.ToSlash(rel)
	if rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return nil
	}
	out := make([]Entry, 0, len(hookNames))
	for _, n := range hookNames {
		out = append(out, Entry{Pattern: escape(path.Join(rel, n)), Class: Source})
	}
	return out
}

// TableFor returns Table followed by the hook entries for the resolved hooks
// directory.
func TableFor(repoRoot, hooksDir string) []Entry {
	return append(append([]Entry{}, Table...), HookEntries(repoRoot, hooksDir)...)
}

func evalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
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
