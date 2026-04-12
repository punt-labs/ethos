package mission

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// WalkWriteSet resolves static write_set paths to concrete files on
// disk. Each entry is interpreted relative to repoRoot: if the entry
// names a file, it is included directly; if it names a directory, the
// directory is walked recursively and every regular file underneath is
// included. Entries that do not exist on disk are silently skipped.
//
// The returned paths are relative to repoRoot, sorted
// lexicographically, and deduplicated. A nil writeSet or empty
// repoRoot returns nil.
func WalkWriteSet(repoRoot string, writeSet []string) ([]string, error) {
	if repoRoot == "" || len(writeSet) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{})
	for _, entry := range writeSet {
		abs := filepath.Join(repoRoot, entry)
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}

		if !info.IsDir() {
			rel, err := filepath.Rel(repoRoot, abs)
			if err != nil {
				return nil, err
			}
			seen[rel] = struct{}{}
			continue
		}

		err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			seen[rel] = struct{}{}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	if len(seen) == 0 {
		return nil, nil
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}
