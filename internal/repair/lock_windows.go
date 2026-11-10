//go:build windows

package repair

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLock takes an exclusive whole-file lock without waiting. It fails when
// another process holds the lock.
func tryLock(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 0xFFFFFFFF, 0xFFFFFFFF, &overlapped)
}
