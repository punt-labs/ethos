//go:build unix

package gitenv

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// inOwnGroup puts git in its own process group, so a timeout reaches what git
// started (a clean or smudge filter, a credential helper) and not git alone.
// With session set it gets its own session too (Setsid, which also makes it a
// group leader): it then has no controlling terminal, and a prompt of ssh's own
// (an unknown host key, a passphrase for a key the agent lacks) fails at once
// instead of waiting on a terminal nobody is watching.
func inOwnGroup(cmd *exec.Cmd, session bool) {
	if session {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		return
	}
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

// dieOf ends the process as sig demands: the default disposition is restored
// and sig is raised on the process, so the shell sees the usual signal status
// (130 for SIGINT). It does not return unless the signal is not a syscall one.
func dieOf(sig os.Signal) {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	signal.Reset(s)
	if err := syscall.Kill(os.Getpid(), s); err != nil {
		return
	}
	// Delivery to self is asynchronous; wait for it rather than return.
	for {
		time.Sleep(time.Minute)
	}
}
