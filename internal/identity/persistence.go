package identity

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// StoreOption configures a store's explicit storage policy. Without options,
// NewStore retains the global/standalone layout, regardless of its pathname.
type StoreOption func(*storeConfig)

type storeConfig struct {
	repository bool
	policy     layout.Policy
}

// PublicationError reports an APPLIED change whose crash durability could not
// be confirmed. Link, rename or removal already succeeded; callers must not
// roll back, describe the target as unchanged, or blindly retry a create.
// Errors before that commit point never have this type. Path identifies the
// applied change (also when a multi-file legacy migration stopped partway).
type PublicationError struct {
	Path string
	Err  error
}

func (e *PublicationError) Error() string {
	return fmt.Sprintf("identity change applied at %s; crash durability unconfirmed: %v", e.Path, e.Err)
}

func (e *PublicationError) Unwrap() error { return e.Err }

// RepositoryPolicy selects repository-local overlays, locks and staging. The
// immutable policy is copied, not retained through a mutable caller pointer.
// root must equal p.RecordRoot(); invalid options fail on I/O, never fall back.
func RepositoryPolicy(p layout.Policy) StoreOption {
	return func(c *storeConfig) {
		c.repository = true
		c.policy = p
	}
}

func (s *Store) validatePolicy() error {
	if !s.config.repository {
		return nil
	}
	p := s.config.policy
	if p.RecordRoot() == "" || filepath.Clean(s.root) != p.RecordRoot() {
		return fmt.Errorf("identity repository policy does not match store root %q", s.root)
	}
	return nil
}

func (s *Store) lockPath(handle string) (string, error) {
	if err := s.validatePolicy(); err != nil {
		return "", err
	}
	if s.config.repository {
		return s.config.policy.LockPath("identity", filepath.Base(handle))
	}
	return s.Path(handle) + ".lock", nil
}

// withLock serializes every repository mutation for a handle, including ext
// namespace-count checks. Locks are never removed: unlinking a held lock would
// allow a second inode to create an independent critical section.
func (s *Store) withLock(handle string, fn func() error) error {
	return s.withLockUsing(handle, flock, fn)
}

// ErrLockBusy means a non-waiting lock attempt found the lock held elsewhere.
var ErrLockBusy = errors.New("identity lock is held by another process")

// withTryLock is withLock without the wait: a held lock returns ErrLockBusy.
func (s *Store) withTryLock(handle string, fn func() error) error {
	return s.withLockUsing(handle, tryFlock, fn)
}

func (s *Store) withLockUsing(handle string, lock func(*os.File) error, fn func() error) error {
	path, err := s.lockPath(handle)
	if err != nil {
		return err
	}
	if s.config.repository {
		if err := layout.EnsureLocalDir(s.config.policy, filepath.Dir(path)); err != nil {
			return err
		}
		if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("identity lock %s is not a regular file", path)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("examining identity lock: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("creating identity lock: %w", err)
	}
	defer f.Close()
	if err := lock(f); err != nil {
		return fmt.Errorf("acquiring identity lock: %w", err)
	}
	defer funlock(f)
	return fn()
}

// writeFile stages repository data locally, syncs and closes it before its
// atomic commit. EXDEV is returned unchanged through wrapping; no adjacent
// fallback is permitted. Global direct-write placement is intentionally kept.
func (s *Store) writeFile(path string, data []byte, create bool) error {
	if err := s.validatePolicy(); err != nil {
		return err
	}
	if !s.config.repository {
		return os.WriteFile(path, data, 0o600)
	}
	if err := s.checkPath(path, false); err != nil {
		return err
	}
	if err := s.ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := layout.CreateLocalTemp(s.config.policy, "identity", ".yaml")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		if s.syncFile != nil {
			err = s.syncFile(f)
		} else {
			err = f.Sync()
		}
	}
	if err := errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("persisting identity staging file: %w", err)
	}
	// Recheck after staging, immediately before the commit operation.
	if err := s.checkPath(path, false); err != nil {
		return err
	}
	if create {
		// A hard link publishes fully written bytes without clobbering an
		// existing identity. A failed link leaves that identity untouched.
		if err := os.Link(f.Name(), path); err != nil {
			return fmt.Errorf("publishing identity: %w", err)
		}
		return s.confirmPublication(path)
	}
	rename := s.rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(f.Name(), path); err != nil {
		return fmt.Errorf("renaming identity file: %w", err)
	}
	return s.confirmPublication(path)
}

// syncAncestors follows mission.syncOwnedAncestors: sync the destination's
// directory first, then each ancestor through S. This also establishes newly
// created directories, even if an earlier interrupted attempt left them behind.
// Syncing the staging file alone cannot persist these directory entries.
func (s *Store) syncAncestors(dir string) error {
	root := filepath.Dir(filepath.Dir(s.config.policy.RecordRoot()))
	if !within(root, dir) {
		return fmt.Errorf("identity durability path is outside the canonical store")
	}
	sync := s.syncDir
	if sync == nil {
		sync = syncDirectory
	}
	for {
		if err := sync(dir); err != nil {
			return fmt.Errorf("syncing identity directory %s: %w", dir, err)
		}
		if dir == root {
			return nil
		}
		dir = filepath.Dir(dir)
	}
}

func (s *Store) confirmPublication(path string) error {
	if err := s.syncAncestors(filepath.Dir(path)); err != nil {
		return &PublicationError{Path: path, Err: err}
	}
	return nil
}

func (s *Store) removeFile(path string) error {
	if err := s.checkPath(path, false); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if s.config.repository {
		return s.confirmPublication(path)
	}
	return nil
}

func (s *Store) writeReplacement(path string, data []byte) error {
	if s.config.repository {
		return s.writeFile(path, data, false)
	}
	// Keep the existing global identity update staging destination.
	tmp := layout.TmpPath(path)
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing identity: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("renaming identity: %w", err)
	}
	return nil
}
