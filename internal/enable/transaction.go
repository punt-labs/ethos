package enable

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

type fileSnapshot struct {
	path   string
	mode   os.FileMode
	data   []byte
	exists bool
}

func snapshotFile(path string) (s fileSnapshot, err error) {
	s.path = path
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.exists, s.mode = true, info.Mode()
	switch {
	case s.mode.IsRegular():
		s.data, err = os.ReadFile(path)
	case s.mode&os.ModeSymlink != 0:
		var target string
		target, err = os.Readlink(path)
		s.data = []byte(target)
	default:
		err = fmt.Errorf("%s is not a regular file or symlink", path)
	}
	return s, err
}

type fileTransaction struct {
	p     layout.Policy
	files []fileSnapshot
}

func (t *fileTransaction) snapshot(paths ...string) error {
	for _, path := range paths {
		s, err := snapshotFile(path)
		if err != nil {
			return fmt.Errorf("snapshotting %s: %w", path, err)
		}
		t.files = append(t.files, s)
	}
	return nil
}

func (t *fileTransaction) finish(cause *error) {
	if *cause == nil {
		return
	}
	for i := len(t.files) - 1; i >= 0; i-- {
		s := t.files[i]
		if err := s.restore(t.p); err != nil {
			*cause = errors.Join(*cause, fmt.Errorf("restoring %s: %w", s.path, err))
		}
	}
}

func (s fileSnapshot) restore(p layout.Policy) error {
	now, err := snapshotFile(s.path)
	if err != nil {
		return err
	}
	if now.exists == s.exists && now.mode == s.mode && bytes.Equal(now.data, s.data) {
		return nil
	}
	if !s.exists {
		if err := os.Remove(s.path); err != nil {
			return err
		}
	} else if err := s.replace(p); err != nil {
		return err
	}
	return layout.SyncRepositoryDestination(p, s.path)
}

func (s fileSnapshot) replace(p layout.Policy) error {
	f, err := layout.CreateLocalTemp(p, "enable-rollback", "")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if s.mode&os.ModeSymlink != 0 {
		if err := errors.Join(f.Close(), os.Remove(name)); err != nil {
			return err
		}
		if err := os.Symlink(string(s.data), name); err != nil {
			return err
		}
	} else {
		_, writeErr := f.Write(s.data)
		if err := errors.Join(writeErr, f.Chmod(s.mode), f.Sync(), f.Close()); err != nil {
			return err
		}
	}
	return os.Rename(name, s.path)
}

func hookSnapshotTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) || err == nil && info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	target, err := os.Readlink(path)
	if err != nil || filepath.IsAbs(target) {
		return target, err
	}
	return filepath.Join(filepath.Dir(path), target), nil
}
