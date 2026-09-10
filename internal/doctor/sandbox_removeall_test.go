package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRemoveSandbox pins the nit: a bare os.RemoveAll silently gives up on
// a tree containing an unwritable entry (a hook that leaves behind a
// 0o000 subdirectory), leaking the sandbox temp dir with no signal.
// removeSandbox must retry after forcing permissions open.
func TestRemoveSandbox(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "sandbox")
	locked := filepath.Join(dir, "locked")
	require.NoError(t, os.MkdirAll(locked, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(locked, "file"), []byte("x"), 0o644))
	require.NoError(t, os.Chmod(locked, 0o000))

	// A bare RemoveAll cannot descend into `locked` — confirms the fixture
	// actually reproduces the failure this helper exists to recover from.
	require.Error(t, os.RemoveAll(dir))

	removeSandbox(dir)
	_, err := os.Stat(dir)
	assert.True(t, os.IsNotExist(err), "removeSandbox must clean up a tree with an unwritable entry, not leak it")
}
