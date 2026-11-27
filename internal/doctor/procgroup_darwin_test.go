package doctor

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReapProcessGroupWaitsForDarwinZombie(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	setNewProcessGroup(cmd)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	})

	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(-cmd.Process.Pid, 0), syscall.EPERM)
	}, 2*time.Second, time.Millisecond)

	waited := make(chan error, 1)
	go func() {
		time.Sleep(25 * time.Millisecond)
		waited <- cmd.Wait()
	}()
	err := reapProcessGroup(cmd, 0)
	require.NoError(t, <-waited)
	require.NoError(t, err)
}
