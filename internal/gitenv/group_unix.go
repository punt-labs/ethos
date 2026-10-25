//go:build unix

package gitenv

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// inOwnGroup puts git in its own process group, so a timeout reaches what git
// started (a clean or smudge filter, a credential helper) and not git alone.
func inOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate asks git, and its group when it has one, to stop: git removes its
// index.lock on SIGTERM.
func terminate(cmd *exec.Cmd, group bool) error {
	if group {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err == nil {
			return nil
		}
	} else if err := cmd.Process.Signal(syscall.SIGTERM); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// endGroup ends what is left in git's group once git has been reaped: it
// polls until the group is empty or grace has passed, so a filter that is
// cleaning up after SIGTERM finishes, then SIGKILLs the rest. Called only
// after a timeout; a git that exits normally may leave a daemon (fsmonitor,
// gc --auto) on purpose.
func endGroup(cmd *exec.Cmd, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	for deadline := time.Now().Add(grace); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
