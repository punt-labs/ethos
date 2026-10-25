package layout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// HookNames returns the git hooks `ethos enable` writes. It is the one list
// `ethos enable` and the path-class table both read, so a hook added to enable
// is classified by the same edit.
func HookNames() []string {
	return []string{HookPreCommit, HookCommitMsg, HookPreMergeCommit}
}

// The git hooks `ethos enable` writes, named once for enable and doctor.
const (
	HookPreCommit = "pre-commit"
	HookCommitMsg = "commit-msg"
	// HookPreMergeCommit runs the seal for `git merge --no-ff`, which runs it
	// and commit-msg but not pre-commit.
	HookPreMergeCommit = "pre-merge-commit"
)

// HookEntries returns class-1 entries for the hook files `ethos enable` writes
// into hooksDir, when core.hooksPath places that directory inside the working
// tree at repoRoot. It returns nil when hooksDir is outside the tree (the
// default .git/hooks included) or inside .punt-labs/, because those files are
// not the repository's or belong to ethos's own classes.
// The directory is the user's choice, so the entries come from the resolved
// directory rather than from the static Table. A path that cannot be resolved
// for a reason other than not existing is an error.
func HookEntries(repoRoot, hooksDir string) ([]Entry, error) {
	h, ok, err := locate(repoRoot, hooksDir)
	if err != nil || !ok {
		return nil, err
	}
	// .git is not tracked; .punt-labs is ethos's own zones, whose classes a
	// source hook entry would overlap.
	for _, z := range []string{".git", ".punt-labs"} {
		in, err := h.under(z)
		if err != nil || in {
			return nil, err
		}
	}
	names := HookNames()
	out := make([]Entry, 0, len(names))
	for _, n := range names {
		out = append(out, Entry{Pattern: escape(path.Join(h.rel, n)), Class: Source})
	}
	return out, nil
}

// PuntLabsHookPaths returns the repoRoot-relative paths of the hook files
// `ethos enable` writes into hooksDir, when hooksDir is .punt-labs/ or inside
// it. layout derives no hook entries for this directory; a zone entry may still
// match the files, so a caller classifies each one before reporting it.
func PuntLabsHookPaths(repoRoot, hooksDir string) ([]string, error) {
	h, ok, err := locate(repoRoot, hooksDir)
	if err != nil || !ok {
		return nil, err
	}
	in, err := h.under(".punt-labs")
	if err != nil || !in {
		return nil, err
	}
	names := HookNames()
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, path.Join(h.rel, n))
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

// hooksLoc is a hooks directory that lies inside the working tree: root and
// dir are symlink-resolved, rel is dir as a slash path relative to root.
type hooksLoc struct{ root, dir, rel string }

// locate returns where hooksDir lies relative to repoRoot, and whether it is
// inside the tree. A path that cannot be related to the root is an error, not
// a directory outside the tree.
func locate(repoRoot, hooksDir string) (hooksLoc, bool, error) {
	root, err := evalPath(repoRoot)
	if err != nil {
		return hooksLoc{}, false, err
	}
	dir, err := evalPath(hooksDir)
	if err != nil {
		return hooksLoc{}, false, err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return hooksLoc{}, false, fmt.Errorf("relating hooks directory %s to repository root %s: %w", dir, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return hooksLoc{}, false, nil
	}
	return hooksLoc{root: root, dir: dir, rel: filepath.ToSlash(rel)}, true, nil
}

// under reports whether the hooks directory is the top-level directory zone of
// the working tree, or beneath it. It compares by name and by file identity: on
// a case-insensitive filesystem .Punt-Labs is .punt-labs, and a spelling
// comparison would miss it.
func (h hooksLoc) under(zone string) (bool, error) {
	if underOrEqual(h.rel, zone) {
		return true, nil
	}
	return sameOrUnder(h.root, h.dir, zone)
}

// sameOrUnder reports whether dir, or an ancestor of dir below root, is the
// same file as root/zone. A zone that does not exist has no identity to
// match; the caller's name comparison is all there is.
func sameOrUnder(root, dir, zone string) (bool, error) {
	zi, err := os.Stat(filepath.Join(root, zone))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking %s: %w", filepath.Join(root, zone), err)
	}
	for cur := dir; cur != root; cur = filepath.Dir(cur) {
		fi, err := os.Stat(cur)
		switch {
		case err == nil:
			if os.SameFile(fi, zi) {
				return true, nil
			}
		case !errors.Is(err, fs.ErrNotExist):
			return false, fmt.Errorf("checking %s: %w", cur, err)
		}
		if filepath.Dir(cur) == cur {
			break
		}
	}
	return false, nil
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
