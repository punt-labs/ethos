//go:build !windows

package doctor

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReapProcessGroup_ESRCH pins the Copilot fix (PR #515): reapProcessGroup
// runs unconditionally after every hookInvocationObserved Run(), and the
// common case is a process group that has already exited normally with
// nothing left to reap — kill(-pgid, SIGKILL) then returns ESRCH. That must
// not surface as a sandbox error; every other errno still must.
func TestReapProcessGroup_ESRCH(t *testing.T) {
	cmd := exec.Command("true")
	setNewProcessGroup(cmd)
	require.NoError(t, cmd.Run())

	err := reapProcessGroup(cmd)
	assert.NoError(t, err, "ESRCH (nothing left to reap) must be swallowed, not surfaced as a sandbox error")
}

// TestReapProcessGroup_NilProcess pins the existing guard: a cmd whose
// Process was never started (Process == nil) is a no-op, not a nil-pointer
// dereference.
func TestReapProcessGroup_NilProcess(t *testing.T) {
	cmd := exec.Command("true")
	assert.NoError(t, reapProcessGroup(cmd))
}
