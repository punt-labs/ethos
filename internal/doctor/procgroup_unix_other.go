//go:build unix && !darwin

package doctor

import (
	"errors"
	"syscall"
)

func killRemainingProcessGroup(pgid int) error {
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
