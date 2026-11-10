//go:build !windows

package repair

import (
	"os"
	"syscall"
)

// TryLock takes an exclusive whole-file lock without waiting. It fails when
// another process holds the lock.
func TryLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
