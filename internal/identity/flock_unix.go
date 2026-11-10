//go:build !windows

package identity

import (
	"errors"
	"os"
	"syscall"
)

func flock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func funlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// tryFlock takes the exclusive lock without waiting; a lock held elsewhere
// yields ErrLockBusy.
func tryFlock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrLockBusy
	}
	return err
}
