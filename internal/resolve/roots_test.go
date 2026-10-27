package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Roots validates the whole .punt-labs/ethos path, not only its leaf: a
// dangling symlink at any level, a non-directory, or a permission error is an
// error; only a true ENOENT is "no store yet".
func testhelpersGitDir(t *testing.T, dir string) { testhelpers.MakeGitDir(t, dir) }

func TestRootsValidatesTheWholeStorePath(t *testing.T) {
	mk := func(t *testing.T) string {
		root := t.TempDir()
		testhelpersGitDir(t, root)
		t.Setenv("ETHOS_REPO_ROOT", "")
		t.Chdir(root)
		return root
	}
	t.Run("no .punt-labs at all is no store yet", func(t *testing.T) {
		mk(t)
		l := Roots()
		require.NoError(t, l.Err())
		assert.NotEmpty(t, l.Store)
		assert.Empty(t, l.Ethos)
	})
	t.Run("a store directory", func(t *testing.T) {
		root := mk(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "ethos"), 0o755))
		l := Roots()
		require.NoError(t, l.Err())
		assert.Equal(t, realpath(t, filepath.Join(root, ".punt-labs", "ethos")), realpath(t, l.Ethos))
	})
	t.Run("dangling .punt-labs", func(t *testing.T) {
		root := mk(t)
		if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, ".punt-labs")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		l := Roots()
		require.Error(t, l.EthosErr)
		assert.Empty(t, l.Ethos)
		assert.Error(t, l.Err())
	})
	t.Run("dangling .punt-labs/ethos", func(t *testing.T) {
		root := mk(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
		if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, ".punt-labs", "ethos")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		require.Error(t, Roots().EthosErr)
	})
	t.Run(".punt-labs is a regular file", func(t *testing.T) {
		root := mk(t)
		require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs"), []byte("x"), 0o644))
		require.Error(t, Roots().EthosErr)
	})
	t.Run(".punt-labs/ethos is a regular file", func(t *testing.T) {
		root := mk(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos"), []byte("x"), 0o644))
		require.Error(t, Roots().EthosErr)
	})
	t.Run("permission denied", func(t *testing.T) {
		skipIfRoot(t)
		root := mk(t)
		require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "ethos"), 0o755))
		chmodUnreadable(t, filepath.Join(root, ".punt-labs"), 0o755)
		require.Error(t, Roots().EthosErr)
	})
}

// A dangling ethos.yaml is a config ethos cannot read, not "no config".
func TestRootsDanglingConfigIsAnError(t *testing.T) {
	root := t.TempDir()
	testhelpersGitDir(t, root)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "ethos"), 0o755))
	if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(root, ".punt-labs", "ethos.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("ETHOS_REPO_ROOT", "")
	t.Chdir(root)
	l := Roots()
	require.Error(t, l.ConfigErr)
	assert.Nil(t, l.Config)
	assert.Error(t, l.Err())
}

// Outside a repository nothing is an error.
func TestRootsOutsideARepository(t *testing.T) {
	t.Setenv("ETHOS_REPO_ROOT", "")
	t.Chdir(t.TempDir())
	l := Roots()
	require.NoError(t, l.Err())
	assert.Empty(t, l.Store)
	assert.Empty(t, l.Checkout)
}

// A store directory ethos cannot list or search is an error, not a missing
// store: mode 000 and mode 600 (listable, not searchable) both.
func TestRootsUnreadableStoreDirectoryIsAnError(t *testing.T) {
	for _, mode := range []os.FileMode{0o000, 0o600} {
		t.Run(mode.String(), func(t *testing.T) {
			skipIfRoot(t)
			root := t.TempDir()
			store := filepath.Join(root, ".punt-labs", "ethos")
			require.NoError(t, os.MkdirAll(store, 0o755))
			require.NoError(t, os.Chmod(store, mode))
			t.Cleanup(func() { _ = os.Chmod(store, 0o755) })
			t.Setenv("ETHOS_REPO_ROOT", root)

			loc := Roots()
			require.Error(t, loc.StoreFailure())
			assert.Contains(t, loc.StoreFailure().Error(), store)
			assert.Empty(t, loc.Ethos)
			_, err := EthosDirOf(root)
			require.Error(t, err)
		})
	}
}

// An intermediate directory of the store path that cannot be listed (mode 0111)
// is an error as the final one is, and doctor reads the marker through the same
// rule as the hooks.
func TestRootsUnreadableIntermediateDirectoryIsAnError(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	punt := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(filepath.Join(punt, "ethos"), 0o755))
	require.NoError(t, os.Chmod(punt, 0o111))
	t.Cleanup(func() { _ = os.Chmod(punt, 0o755) })
	t.Setenv("ETHOS_REPO_ROOT", root)

	loc := Roots()
	require.Error(t, loc.StoreFailure())
	assert.Contains(t, loc.StoreFailure().Error(), punt)
	_, err := EnabledMarker(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), punt)
}
