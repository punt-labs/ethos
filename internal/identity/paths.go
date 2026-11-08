package identity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// repositoryPathError is a hard boundary refusal, not an absent or malformed
// optional extension that Load may omit with a warning.
type repositoryPathError struct {
	path   string
	reason string
}

func (e *repositoryPathError) Error() string {
	return fmt.Sprintf("identity path %s: %s", e.path, e.reason)
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// checkPath refuses symlinks (even ones pointing inside the store) before any
// repository data is read, created, replaced or removed. It also checks local
// reads, which must not follow a redirected overlay into the source tree.
// Missing paths are allowed here; the subsequent operation reports absence.
// Like layout.EnsureLocalDir this is a placement check, not a sandbox against
// an adversary replacing ancestors concurrently. Cooperating writers use the
// handle lock and atomic publication. The resolver supplies physical roots.
func (s *Store) checkPath(path string, directory bool) error {
	if err := s.validatePolicy(); err != nil {
		return err
	}
	if !s.config.repository {
		return nil
	}
	p := s.config.policy
	if !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		!within(p.RecordRoot(), path) && !within(p.LocalRoot(), path) {
		return &repositoryPathError{path, "outside the repository identity store"}
	}
	var ancestors []string
	for dir := path; ; dir = filepath.Dir(dir) {
		ancestors = append(ancestors, dir)
		if filepath.Dir(dir) == dir {
			break
		}
	}
	for i := len(ancestors) - 1; i >= 0; i-- {
		name := ancestors[i]
		info, err := os.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("examining identity path %s: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return &repositoryPathError{name, "symlink; refusing redirected identity storage"}
		}
		if i > 0 || directory {
			if !info.IsDir() {
				return &repositoryPathError{name, "not a directory"}
			}
		} else if !info.Mode().IsRegular() {
			return &repositoryPathError{name, "not a regular file"}
		}
	}
	return nil
}

func (s *Store) readFile(path string) ([]byte, error) {
	if err := s.checkPath(path, false); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (s *Store) readDir(path string) ([]os.DirEntry, error) {
	if err := s.checkPath(path, true); err != nil {
		return nil, err
	}
	return os.ReadDir(path)
}

func (s *Store) ensureDir(path string) error {
	if err := s.checkPath(path, true); err != nil {
		return err
	}
	if s.config.repository && within(s.config.policy.LocalRoot(), path) {
		// Retain the foundation's local-directory creation checks.
		return layout.EnsureLocalDir(s.config.policy, path)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("creating identity directory: %w", err)
	}
	return s.checkPath(path, true)
}
