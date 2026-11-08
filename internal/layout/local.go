package layout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// EnsureLocalDir creates dir (0700) only within p.LocalRoot. It refuses every
// symlink ancestor, including the store root and its ancestors, rather than
// following a local directory redirected into source or a disposable checkout.
// Supply the physical store root returned by the resolver. Existing directories
// keep their permissions; the store root must already exist.
//
// This is a placement check, not a sandbox against an adversary concurrently
// replacing ancestors. Writers must retain their locking and atomic commit
// discipline, and must not use the returned pathname after untrusted mutation.
func EnsureLocalDir(p Policy, dir string) error {
	if err := p.validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(dir) || dir != filepath.Clean(dir) {
		return &ValidationError{"local directory", "must be a clean absolute path"}
	}
	rel, err := filepath.Rel(p.LocalRoot(), dir)
	if err != nil {
		return fmt.Errorf("locating local directory: %w", err)
	}
	if rel != "." && !filepath.IsLocal(rel) {
		return &ValidationError{"local directory", "must be within the policy's local root"}
	}
	var ancestors []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		ancestors = append(ancestors, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	// Inspect from the filesystem root down, before any creation. Mkdir (not
	// MkdirAll) and the second Lstat also handle cooperating concurrent creators.
	create := false
	for i := len(ancestors) - 1; i >= 0; i-- {
		d := ancestors[i]
		info, err := os.Lstat(d)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("creating local directory %s: %w", d, err)
			}
			info, err = os.Lstat(d)
		}
		if err != nil {
			return fmt.Errorf("examining local ancestor %s: %w", d, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("local ancestor %s is a symlink; refusing redirected local storage", d)
		}
		if !info.IsDir() {
			return fmt.Errorf("local ancestor %s is not a directory", d)
		}
		if d == p.storeRoot {
			create = true
		}
	}
	return nil
}
