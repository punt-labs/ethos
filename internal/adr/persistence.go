package adr

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

// DurabilityError reports a committed ADR whose directory metadata could not be
// synced. ID names the installed record. The destination contains complete new
// bytes; callers must not restore the old record or retry Create as if it failed
// before publication. Errors from link/rename are never DurabilityErrors.
type DurabilityError struct {
	ID  string
	Err error
}

func (e *DurabilityError) Error() string {
	return fmt.Sprintf("ADR %s installed; crash durability unconfirmed: %v", e.ID, e.Err)
}

func (e *DurabilityError) Unwrap() error { return e.Err }

func installed(err error) bool {
	var durability *DurabilityError
	return err == nil || errors.As(err, &durability)
}

// syncParents confirms the destination entry and all possibly new directory
// entries through the canonical store root, not the invoking checkout. Syncing
// the whole owned chain also covers ancestors created by locks or a prior
// interrupted attempt. Standalone stores retain their historical behavior.
func (s *Store) syncParents(dest string) error {
	if !s.repository {
		return nil
	}
	root := filepath.Dir(filepath.Dir(s.policy.RecordRoot()))
	rel, err := filepath.Rel(root, dest)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return fmt.Errorf("ADR durability path is outside the canonical store")
	}
	sync := s.syncDir
	if sync == nil {
		sync = syncDirectory
	}
	for dir := filepath.Dir(dest); ; dir = filepath.Dir(dir) {
		if err := sync(dir); err != nil {
			return fmt.Errorf("syncing ADR directory: %w", err)
		}
		if dir == root {
			return nil
		}
	}
}

func (s *Store) confirmDurability(dest string) error {
	if err := s.syncParents(dest); err != nil {
		id := filepath.Base(dest)
		id = id[:len(id)-len(filepath.Ext(id))]
		return &DurabilityError{ID: id, Err: err}
	}
	return nil
}

// withMutationLock takes the standalone legacy lock first, then the repository
// lock. It freezes fallback IDs and source bytes through publication even when
// another process uses NewStore to mutate the global store. This compatibility
// lock belongs to that global store; repository locks/staging remain local to
// the policy. No global ADR is written. Never reverse this acquisition order.
// IDs created globally in the future are not a cross-repository ID registry.
func (s *Store) withMutationLock(fn func() error) error {
	if s.legacy != nil {
		return s.legacy.withLock(func() error { return s.withLock(fn) })
	}
	return s.withLock(fn)
}

// withLock serializes allocation, creation, copy-on-write and read/modify/write
// across processes and linked checkouts. The lock inode is stable: never unlink
// it. Repository locks are independent of mode and checkout.
func (s *Store) withLock(fn func() error) (err error) {
	path := filepath.Join(s.dir, ".adr.lock")
	if s.repository {
		var err error
		path, err = s.policy.LockPath("adr", "store")
		if err != nil {
			return err
		}
		if err := layout.EnsureLocalDir(s.policy, filepath.Dir(path)); err != nil {
			return fmt.Errorf("creating ADR lock directory: %w", err)
		}
	} else if err := s.recordDir(true); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("ADR lock is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("examining ADR lock: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening ADR lock: %w", err)
	}
	// Closing also releases the platform lock, including on callback failure.
	// A close failure is returned, joined to the callback's own error: the
	// caller decides where it is reported, never this package's stderr.
	defer func() {
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing ADR lock: %w", cerr))
		}
	}()
	if err := lockFile(f); err != nil {
		return fmt.Errorf("locking ADR store: %w", err)
	}
	return fn()
}

// publish never falls back to a source-tree temporary on EXDEV. The destination
// is changed only after complete bytes have been synced and closed. Creation
// uses a hard link rather than a check-then-rename that could clobber a file.
func (s *Store) publish(dest string, data []byte, create bool) (err error) {
	var f *os.File
	if s.repository {
		f, err = layout.CreateLocalTemp(s.policy, "adr", ".yaml")
	} else {
		// Retain standalone staging beside its destination, outside repository
		// policy, but use a unique name rather than a shared fixed temporary.
		f, err = os.CreateTemp(s.dir, layout.TmpPattern("adr", ".yaml"))
	}
	if err != nil {
		return fmt.Errorf("creating ADR staging file: %w", err)
	}
	committed := false
	defer func() {
		rerr := os.Remove(f.Name())
		if rerr == nil || errors.Is(rerr, os.ErrNotExist) {
			return
		}
		rerr = fmt.Errorf("cleaning ADR staging file: %w", rerr)
		// Publication is the commit point. A cleanup failure after it is
		// reported as a durability problem on an installed record, never as
		// an ordinary failed write; before it, it joins the write's error.
		if committed && err == nil {
			err = &DurabilityError{ID: strings.TrimSuffix(filepath.Base(dest), ".yaml"), Err: rerr}
			return
		}
		err = errors.Join(err, rerr)
	}()
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("writing ADR staging file: %w", errors.Join(err, f.Close()))
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing ADR staging file: %w", errors.Join(err, f.Close()))
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing ADR staging file: %w", err)
	}
	commit := s.rename
	if commit == nil {
		commit = os.Rename
	}
	if create {
		commit = s.link
		if commit == nil {
			commit = os.Link
		}
	}
	if err := commit(f.Name(), dest); err != nil {
		return fmt.Errorf("publishing ADR file: %w", err)
	}
	committed = true
	// Link/rename is the commit point: a later sync failure must not remove or
	// restore the destination, or masquerade as an ordinary failed mutation.
	return s.confirmDurability(dest)
}
