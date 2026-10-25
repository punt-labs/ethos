//go:build unix

package gitenv

import (
	"os/exec"
	"syscall"
)

// inOwnGroup puts git in its own process group, so a timeout reaches what git
// started (a clean or smudge filter, a credential helper) and not git alone.
func inOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate asks git, and its group, to stop: git removes its index.lock on
// SIGTERM.
func terminate(cmd *exec.Cmd) error {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// killGroup kills whatever is left in git's group once git has been reaped: a
// filter that ignored SIGTERM. Called only after a timeout; a git that exits
// normally may leave a daemon (fsmonitor, gc --auto) on purpose.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
