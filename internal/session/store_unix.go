//go:build !windows

package session

import (
	"fmt"
	"os"
	"syscall"
)

// withLock executes fn while holding an exclusive flock on the
// session's lock file. The public WithSessionLock wraps this helper
// and shares no fd state with it. See store.go's doc comment on
// withLock/isProcessAlive for the re-entrancy warning shared with the
// Windows implementation.
func (s *Store) withLock(sessionID string, fn func() error) error {
	if err := os.MkdirAll(s.sessionsDir(), 0o700); err != nil {
		return fmt.Errorf("creating sessions directory: %w", err)
	}
	lockFile := s.lockPath(sessionID)
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening lock file: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	return fn()
}

// isProcessAlive checks whether a process with the given PID is running.
//
// Limitations:
//   - Zombie processes (exited but not yet waited on) still respond to
//     signal 0, so this function returns true for zombies.
//   - PID reuse is not addressed. If the original process exits and the OS
//     assigns the same PID to an unrelated process, this returns a false
//     positive. On modern Linux/macOS the PID space is large enough that
//     reuse within a single session lifetime is unlikely but not impossible.
func isProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Send signal 0 to check.
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but we can't signal it.
	return err == syscall.EPERM
}
