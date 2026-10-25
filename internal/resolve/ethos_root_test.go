package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ethosRootOf is "" only when .punt-labs/ethos does not exist. Any other
// failure to stat it is an error: a dangling symlink, a regular file, a
// permission error or a symlink loop is a store ethos cannot read, and must
// not read as "no store yet".
func TestEthosRootOf(t *testing.T) {
	mk := func(t *testing.T) (root, ethos string) {
		root = t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
		return root, filepath.Join(root, ".punt-labs", "ethos")
	}
	t.Run("directory", func(t *testing.T) {
		root, ethos := mk(t)
		require.NoError(t, os.Mkdir(ethos, 0o755))
		got, err := ethosRootOf(root)
		require.NoError(t, err)
		assert.Equal(t, ethos, got)
	})
	t.Run("absent", func(t *testing.T) {
		root, _ := mk(t)
		got, err := ethosRootOf(root)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
	t.Run("dangling symlink", func(t *testing.T) {
		root, ethos := mk(t)
		if err := os.Symlink(filepath.Join(root, "nowhere"), ethos); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		got, err := ethosRootOf(root)
		require.Error(t, err)
		assert.Empty(t, got)
		assert.Contains(t, err.Error(), ethos)
	})
	t.Run("symlink loop", func(t *testing.T) {
		root, ethos := mk(t)
		if err := os.Symlink(ethos, ethos); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		_, err := ethosRootOf(root)
		require.Error(t, err)
	})
	t.Run("regular file", func(t *testing.T) {
		root, ethos := mk(t)
		require.NoError(t, os.WriteFile(ethos, []byte("x"), 0o644))
		_, err := ethosRootOf(root)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a directory")
	})
	t.Run("permission denied", func(t *testing.T) {
		skipIfRoot(t)
		root, _ := mk(t)
		chmodUnreadable(t, filepath.Join(root, ".punt-labs"), 0o755)
		_, err := ethosRootOf(root)
		require.Error(t, err)
	})
}
