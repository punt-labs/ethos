//go:build linux || darwin

package resolve

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	// git skips a .git that is neither a directory nor a regular file, and
	// opening a FIFO would block the walk forever.
	skippedGitCases["fifo"] = func(dot string) error { return syscall.Mkfifo(dot, 0o644) }
}

// A .git directory git cannot read for want of permission is an error: skipping
// it would resolve the enclosing repository.
func TestWalkRefusesUnreadableGit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	inner := unexaminableGit(t, func(dot string) error { return os.Mkdir(dot, 0o755) })
	testhelpers.MakeGitDir(t, inner)
	dot := filepath.Join(inner, ".git")
	require.NoError(t, os.Chmod(dot, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dot, 0o755) })

	got, err := LocateRepoRoot()
	assert.Empty(t, got)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotInRepo)
	assert.Contains(t, err.Error(), dot)
}
