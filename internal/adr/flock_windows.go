//go:build windows

package adr

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 0xFFFFFFFF, 0xFFFFFFFF, &overlapped)
}
