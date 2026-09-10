//go:build !unix

package doctor

import "os/exec"

// setNewProcessGroup and reapProcessGroup are no-ops on every non-unix
// GOOS (Windows, plan9, js/wasm, and any future target the "unix" build
// constraint does not cover — qodo, PR #515: procgroup_unix.go's prior
// `!windows` tag claimed every non-Windows target, including several the
// syscall.SysProcAttr.Setpgid/negative-PID Kill it uses do not exist on).
// hookInvocationObserved returns via errSandboxUnsupportedPlatform before
// ever constructing a command on any GOOS reaching this file (see
// sandboxGOOS) — these exist only so the package still compiles here.
func setNewProcessGroup(cmd *exec.Cmd) {}

func reapProcessGroup(cmd *exec.Cmd) error { return nil }
