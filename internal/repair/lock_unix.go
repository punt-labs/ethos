//go:build !windows

package repair

import (
	"os"
	"syscall"
)

const supportsDeletion = true

func tryLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
