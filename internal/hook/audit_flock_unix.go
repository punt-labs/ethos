//go:build !windows

package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// WithLiveAuditLock executes fn while holding an exclusive flock on the
// per-session live-zone lock (liveAuditLockPath). DES-058 moves the append
// and seal lock beside the live file so appends, monotonic-ts allocation,
// and seals in one checkout serialize on one inode. The lock is created if
// needed; the containing directory is created too so a first append can
// take it.
//
// Not re-entrant: a nested call opens a second fd on the same inode and
// deadlocks the goroutine against itself. Acquire once at the top of a
// live-write or seal path.
func WithLiveAuditLock(repoRoot, sessionID string, fn func() error) error {
	lockPath := liveAuditLockPath(repoRoot, sessionID)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("creating live lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening live lock %s: %w", lockPath, err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring live lock %s: %w", lockPath, err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	return fn()
}
