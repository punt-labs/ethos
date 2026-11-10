//go:build windows

package mission

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// flockTry is flock that returns busy=true instead of waiting when another
// holder has the lock.
func flockTry(f *os.File, mode int) (busy bool, err error) {
	var overlapped windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), uint32(mode)|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, wholeFile, wholeFile, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("LockFileEx: %w", err)
	}
	return false, nil
}
