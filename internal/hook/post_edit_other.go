//go:build !unix

package hook

import "os/exec"

func editProcessGroup(_ *exec.Cmd) func() { return func() {} }
