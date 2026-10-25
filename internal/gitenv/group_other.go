//go:build !unix

package gitenv

import "os/exec"

// There are no process groups here: inOwnGroup does nothing, and terminate
// kills git at once, with no SIGTERM first and no grace.
func inOwnGroup(*exec.Cmd) {}

func terminate(cmd *exec.Cmd) error { return cmd.Process.Kill() }

func killGroup(*exec.Cmd) {}
