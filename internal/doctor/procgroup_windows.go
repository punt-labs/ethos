//go:build !unix

package doctor

import "os/exec"

// sandboxSupported is false here: without the process-group primitives
// below, hookInvocationObserved has no way to bound what a hook's
// backgrounded children outlive, and the sandbox also assumes a POSIX `sh`
// for its ENOEXEC fallback. It refuses to execute anything at all when this
// is false.
//
// Declared in this file — the same tagged pair that decides whether the
// process-group functions are real or no-ops — so the two cannot drift
// apart (Copilot, PR #515). The previous guard tested `sandboxGOOS ==
// "windows"`, a case enumeration that did not match this file's `!unix`
// tag: it named one of the platforms reaching this file and silently
// admitted the rest. Adding a platform to one side of the split now
// necessarily means editing the declaration on the other.
//
// A var, not a const, only so a test can drive the unsupported-platform
// arm from a supported host; see sandboxGOOS in sandbox.go for how the two
// seams differ and why both exist.
var sandboxSupported = false

// setNewProcessGroup and reapProcessGroup are no-ops on every non-unix
// GOOS (Windows, plan9, js/wasm, and any future target the "unix" build
// constraint does not cover — qodo, PR #515: procgroup_unix.go's prior
// `!windows` tag claimed every non-Windows target, including several the
// syscall.SysProcAttr.Setpgid/negative-PID Kill it uses do not exist on).
// They exist only so the package still compiles here; because
// sandboxSupported above is false, hookInvocationObserved returns
// errSandboxUnsupportedPlatform before constructing any command, so neither
// is ever reached through it.
//
// Windows is currently the only non-unix target that reaches this file at
// all: `GOOS=plan9` and `GOOS=js` fail to build internal/doctor outright,
// via internal/enable → internal/resolve → internal/process, whose tree.go
// carries `//go:build linux || darwin || windows`. That is a transitive
// constraint in another package, not a guarantee this one makes — it is
// recorded here so a reader knows plan9 is unreachable today rather than
// handled, and so the sandboxSupported split above is understood as the
// thing actually holding the line if that constraint ever widens.
func setNewProcessGroup(cmd *exec.Cmd) {}

func reapProcessGroup(cmd *exec.Cmd) error { return nil }
