//go:build !windows

package mission

import (
	"os"
	"syscall"
)

// lockShared and lockExclusive are the portable names this package's
// lock call sites use for flock's LOCK_SH/LOCK_EX, so the mode a caller
// picks (shared vs exclusive) reads the same on every platform even
// though the underlying primitive differs (flock here, real LockFileEx
// in flock_windows.go).
const (
	lockShared    = syscall.LOCK_SH
	lockExclusive = syscall.LOCK_EX
)

// flock acquires a lock in the given mode (lockShared or lockExclusive)
// on f.
func flock(f *os.File, mode int) error {
	return syscall.Flock(int(f.Fd()), mode)
}

// funlock releases a lock acquired by flock.
func funlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
