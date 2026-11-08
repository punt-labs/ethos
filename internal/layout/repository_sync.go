package layout

import (
	"fmt"
	"path/filepath"
)

// InstallDurabilityError means the installed state at Path has unconfirmed
// directory durability. It can follow rename/link or a no-write retry that
// still cannot confirm an earlier install. The operation's result says whether
// this call wrote; this error is not permission to roll back. Pre-install write
// failures never have this type.
type InstallDurabilityError struct {
	Path string
	Err  error
}

var _ error = (*InstallDurabilityError)(nil)

func (e *InstallDurabilityError) Error() string {
	return fmt.Sprintf("change applied at %s; crash durability unconfirmed: %v", e.Path, e.Err)
}

func (e *InstallDurabilityError) Unwrap() error { return e.Err }

// SyncRepositoryDestination confirms an installed repository staging file's
// directory entry and its ancestors through S or C. Syncing the entire owned
// chain also covers directories left by an interrupted earlier attempt. For a
// symlink-resolved host or external hooks directory, sync through the filesystem
// root instead. It does not create directories or install files. Callers must
// sync and close the payload before rename/link and classify a failure after
// install as applied, not rolled back. No-write retries also need confirmation:
// matching bytes alone do not prove an earlier directory sync completed.
// Windows uses the established documented no-op.
func SyncRepositoryDestination(p Policy, path string) error {
	return syncRepositoryDestination(p, path, syncRepositoryDirectory)
}

func syncRepositoryDestination(p Policy, path string, syncDir func(string) error) error {
	if err := p.validate(); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving installed path: %w", err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return fmt.Errorf("resolving installed directory: %w", err)
	}
	stop := ""
	for _, root := range []string{p.storeRoot, p.checkoutRoot} {
		rel, err := filepath.Rel(root, dir)
		if err == nil && (rel == "." || filepath.IsLocal(rel)) {
			stop = root
			break
		}
	}
	for {
		if err := syncDir(dir); err != nil {
			return fmt.Errorf("syncing repository destination directory %s: %w", dir, err)
		}
		if dir == stop || filepath.Dir(dir) == dir {
			return nil
		}
		dir = filepath.Dir(dir)
	}
}
