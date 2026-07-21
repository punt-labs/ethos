//go:build !windows

package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// WithLock executes fn while holding an exclusive flock on lockPath. DES-058
// places the append/seal lock beside the live file so appends, monotonic-ts
// allocation, and seals in one checkout serialize on one inode. The lock and
// its parent directory are created if needed.
//
// Not re-entrant: a nested call opens a second fd on the same inode and
// deadlocks the goroutine against itself. Acquire once at the top of a
// live-write or seal path.
func WithLock(lockPath string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("creating lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock %s: %w", lockPath, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring lock %s: %w", lockPath, err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}
