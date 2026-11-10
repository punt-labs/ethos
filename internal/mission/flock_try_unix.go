//go:build !windows

package mission

import (
	"errors"
	"os"
	"syscall"
)

// flockTry is flock that returns busy=true instead of waiting when another
// holder has the lock.
func flockTry(f *os.File, mode int) (busy bool, err error) {
	err = syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	return false, err
}
