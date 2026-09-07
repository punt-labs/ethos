//go:build windows

package mission

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockShared and lockExclusive mirror flock_unix.go's LOCK_SH/LOCK_EX:
// LockFileEx with no flags is a shared lock; LOCKFILE_EXCLUSIVE_LOCK
// requests exclusive.
const (
	lockShared    = 0
	lockExclusive = windows.LOCKFILE_EXCLUSIVE_LOCK
)

// wholeFile is the byte range LockFileEx locks — the whole file
// regardless of its actual size (the same idiom internal/session's
// store_windows.go uses; see that file's doc comment for why this
// makes LockFileEx a synchronous, blocking call).
const wholeFile = 0xFFFFFFFF

// flock acquires a Windows LockFileEx lock in the given mode (lockShared
// or lockExclusive) on f. A fresh, zeroed Overlapped is safe to use here
// and in funlock independently: both always name the same (whole-file,
// offset-0) range, and Windows matches an unlock request to a lock by
// that range, not by struct identity.
func flock(f *os.File, mode int) error {
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), uint32(mode), 0, wholeFile, wholeFile, &overlapped); err != nil {
		return fmt.Errorf("LockFileEx: %w", err)
	}
	return nil
}

// funlock releases a lock acquired by flock.
func funlock(f *os.File) error {
	var overlapped windows.Overlapped
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, wholeFile, wholeFile, &overlapped); err != nil {
		return fmt.Errorf("UnlockFileEx: %w", err)
	}
	return nil
}
