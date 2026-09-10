//go:build !windows

package doctor

import (
	"errors"
	"os/exec"
	"syscall"
)

// setNewProcessGroup makes cmd the leader of a new process group, so
// reapProcessGroup below can kill the whole tree a sandboxed hook spawns —
// not just the direct child exec.CommandContext tracks (S4). A hook that
// backgrounds a child (`cmd &`, nohup) leaves that child in the same
// process group by default (job control is off in a non-interactive
// shell), so killing the group reaches it even after the direct child (the
// shell itself) has already exited normally — the case a context-timeout
// alone can never catch, since the timeout never fires when the direct
// child exits quickly.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// reapProcessGroup SIGKILLs cmd's entire process group (the negative-PID
// form of kill(2)) rather than only cmd's direct PID. Called both as cmd's
// Cancel (on a sandboxTimeout context deadline) and unconditionally right
// after cmd.Run() returns, regardless of how it returned — a backgrounded
// grandchild is orphaned by its parent's normal exit, not by a timeout, so
// relying on Cancel alone misses it entirely.
//
// ESRCH ("no such process") is the expected result, not a failure: this is
// called unconditionally after every Run(), and the common case is a group
// that has already exited on its own with nothing left to reap (Copilot, PR
// #515). Surfacing that as a sandbox error would read as a confusing
// spurious failure on a perfectly healthy hook. Every other errno (EPERM, a
// genuinely wedged process) is still returned.
func reapProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
