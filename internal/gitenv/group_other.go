//go:build !unix

package gitenv

import (
	"os/exec"
	"time"
)

// There are no process groups here: inOwnGroup does nothing, and terminate
// kills git at once, with no SIGTERM first and no grace.
func inOwnGroup(*exec.Cmd, bool) {}

func terminate(cmd *exec.Cmd, _ bool) error { return cmd.Process.Kill() }

func endGroup(*exec.Cmd, time.Duration) {}
