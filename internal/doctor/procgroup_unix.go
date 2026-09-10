//go:build !windows

package doctor

import (
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
func reapProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
