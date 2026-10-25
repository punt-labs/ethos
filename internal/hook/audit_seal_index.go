package hook

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// errIndexChanged says the index is not what our `git add` left, so the
// restore did not put the snapshot over another writer's work.
var errIndexChanged = errors.New("the index was changed by another process after our git add")

// lockSlack is how far before our add started a lock's mtime may be and still
// be taken as ours: file systems with coarse timestamps round it down.
const lockSlack = time.Second

// indexSnapshot is a copy of the index taken before `git add`, in a sibling
// file (same directory, so a restore is a rename), and in memory.
type indexSnapshot struct {
	path, snap string
	existed    bool // false: there was no index, and restoring removes the one git made
	mode       fs.FileMode
	data       []byte

	addStart time.Time
	after    [sha256.Size]byte // the index our add left, as sha256 ...
	afterOK  bool              // ... and whether there was one
}

// snapshotIndex copies the index at path to path+".ethos-tmp", with the
// index's mode. A missing index is recorded, not an error: a repository with
// no commits has none.
func snapshotIndex(path string) (*indexSnapshot, error) {
	s := &indexSnapshot{path: path, snap: path + ".ethos-tmp"}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s.existed, s.mode, s.data = true, info.Mode().Perm(), data
	if err := writeFileMode(s.snap, data, s.mode); err != nil {
		return nil, err
	}
	return s, nil
}

// writeFileMode writes data to a new or truncated path with exactly mode: the
// umask does not narrow it.
func writeFileMode(path string, data []byte, mode fs.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// addFinished records the index our `git add` left, once git has exited, and
// when the add started: the restore puts the snapshot back only while the
// index is still that, and removes a leftover index.lock only if it is newer
// than the add.
func (s *indexSnapshot) addFinished(start time.Time) {
	s.addStart = start
	s.after, s.afterOK = fingerprint(s.path)
}

func fingerprint(path string) ([sha256.Size]byte, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256(data), true
}

// restore puts the snapshot back over the index (git has exited), or removes
// the index when there was none. It writes as git does: it takes
// <index>.lock with O_CREATE|O_EXCL, and renames the new index over the old
// one, so a git that is writing the index is never raced. If the index is no
// longer what our add left, another process wrote it: it is left, and
// errIndexChanged says so.
func (s *indexSnapshot) restore() error {
	lock := s.path + ".lock"
	f, err := s.takeLock(lock)
	if err != nil {
		return err
	}
	release := func() { _ = os.Remove(lock) }
	if got, ok := fingerprint(s.path); got != s.after || ok != s.afterOK {
		_ = f.Close()
		release()
		return errIndexChanged
	}
	if !s.existed {
		_ = f.Close()
		if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			release()
			return err
		}
		release()
		return nil
	}
	if _, err := f.Write(s.data); err != nil {
		_ = f.Close()
		release()
		return fmt.Errorf("writing %s: %w", lock, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		release()
		return fmt.Errorf("syncing %s: %w", lock, err)
	}
	if err := f.Close(); err != nil {
		release()
		return fmt.Errorf("closing %s: %w", lock, err)
	}
	if err := os.Chmod(lock, s.mode); err != nil {
		release()
		return err
	}
	if err := os.Rename(lock, s.path); err != nil {
		release()
		return err
	}
	_ = os.Remove(s.snap) // restored: the sibling copy is no longer needed
	return nil
}

// takeLock creates lock exclusively. A lock that exists is ours to remove only
// if it is newer than our add (our git, killed, left it) and no process holds
// it; any other is somebody's, and the error names it.
func (s *indexSnapshot) takeLock(lock string) (*os.File, error) {
	open := func() (*os.File, error) { return os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) }
	f, err := open()
	if err == nil || !errors.Is(err, fs.ErrExist) {
		return f, err
	}
	info, serr := os.Stat(lock)
	if serr != nil {
		return nil, fmt.Errorf("index lock %s exists and cannot be examined: %w", lock, serr)
	}
	if info.ModTime().Before(s.addStart.Add(-lockSlack)) {
		return nil, fmt.Errorf("index lock %s exists and is older than our git add: another git may hold it; "+
			"remove it if none is running", lock)
	}
	if held, known := lockHeld(lock); held || !known {
		why := "a running process holds it"
		if !known {
			why = "this system cannot tell whether a process holds it"
		}
		return nil, fmt.Errorf("index lock %s exists and %s: remove it if no git is running", lock, why)
	}
	if err := os.Remove(lock); err != nil {
		return nil, fmt.Errorf("removing the stale index lock %s: %w", lock, err)
	}
	f, err = open()
	if err != nil {
		return nil, fmt.Errorf("taking index lock %s: %w", lock, err)
	}
	return f, nil
}

// discard removes the snapshot after a successful add.
func (s *indexSnapshot) discard() {
	if s.existed {
		_ = os.Remove(s.snap)
	}
}
