//go:build windows

package doctor

import "os/exec"

// setNewProcessGroup and reapProcessGroup are no-ops on Windows.
// hookInvocationObserved returns via errSandboxUnsupportedPlatform before
// ever constructing a command on this GOOS (see sandboxGOOS) — these exist
// only so the package still compiles under `GOOS=windows`.
func setNewProcessGroup(cmd *exec.Cmd) {}

func reapProcessGroup(cmd *exec.Cmd) error { return nil }
