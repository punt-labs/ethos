package doctor

import (
	"errors"
	"syscall"
	"time"
)

func killRemainingProcessGroup(pgid int) error {
	err := syscall.Kill(-pgid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if errors.Is(err, syscall.EPERM) {
		// Darwin returns EPERM for a group containing only zombies.
		// Wait for the OS to reap it, but never treat EPERM itself as
		// success or send another signal to a potentially reused group.
		for deadline := time.Now().Add(250 * time.Millisecond); time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
			probeErr := syscall.Kill(-pgid, 0)
			if errors.Is(probeErr, syscall.ESRCH) {
				return nil
			}
			if !errors.Is(probeErr, syscall.EPERM) {
				break
			}
		}
	}
	return err
}
