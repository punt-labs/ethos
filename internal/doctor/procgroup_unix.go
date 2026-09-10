//go:build unix

package doctor

import (
	"errors"
	"os/exec"
	"syscall"
)

// sandboxSupported is true here: the process-group primitives below are
// real on a unix host, and hookInvocationObserved's other assumptions (a
// POSIX `sh` for the ENOEXEC fallback, POSIX exec semantics) hold. See the
// declaration in procgroup_windows.go for why this fact lives in the same
// tagged file pair as the primitives themselves rather than in a GOOS
// comparison that could drift from the build tag.
var sandboxSupported = true

// setNewProcessGroup makes cmd the leader of a new process group, so
// reapProcessGroup below can kill every descendant that STAYS in that
// group — not just the direct child exec.CommandContext tracks (S4). A
// hook that backgrounds a child (`cmd &`, nohup) leaves that child in the
// same process group by default (job control is off in a non-interactive
// shell), so killing the group reaches it even after the direct child (the
// shell itself) has already exited normally — the case a context-timeout
// alone can never catch, since the timeout never fires when the direct
// child exits quickly.
//
// This is NOT "the whole tree a sandboxed hook spawns" (qodo, PR #515,
// correcting this comment's prior overclaim): a descendant that calls
// setsid(2) leaves the group entirely by definition — that is what
// setsid exists to do — and kill(-pgid) can no longer reach it by any
// signal number. `setsid sh -c 'while :; do :; done' &` inside a
// sandboxed hook demonstrably outlives hookInvocationObserved returning.
// No portable, unprivileged containment closes this on every platform
// this project ships: Linux has PR_SET_CHILD_SUBREAPER (a prctl, no
// cross-platform equivalent); darwin has nothing comparable without
// cgroups or ptrace-level containment. This was never the sandbox's
// security boundary in the first place — DES-077 documents that hook
// execution is untrusted-code execution by design, with no seccomp, no
// chroot, no network isolation; process-group cleanup only bounds
// lifetime for the common (non-setsid) case, and closing the setsid gap
// is a platform-containment design decision, tracked as a named residual
// in DES-077, not a same-shape fix to this file.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// reapProcessGroup SIGKILLs cmd's process group (the negative-PID form of
// kill(2)) rather than only cmd's direct PID — see setNewProcessGroup's
// doc comment for exactly what that does and does not reach. Called both
// as cmd's Cancel (on a sandboxTimeout context deadline) and
// unconditionally right after cmd.Run() returns, regardless of how it
// returned — a backgrounded grandchild is orphaned by its parent's normal
// exit, not by a timeout, so relying on Cancel alone misses it entirely.
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
