//go:build !windows

package audit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// TryLock runs fn while holding an exclusive flock on lockPath, without
// waiting. It reports busy=true and does not run fn when another holder has
// the lock. Like WithLock it is not re-entrant.
func TryLock(lockPath string, fn func() error) (busy bool, err error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return false, fmt.Errorf("creating lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, fmt.Errorf("opening lock %s: %w", lockPath, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, fmt.Errorf("acquiring lock %s: %w", lockPath, err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return false, fn()
}
