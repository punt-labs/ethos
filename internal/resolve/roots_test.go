package resolve

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestPhysicalRootPreservesAbsenceAndErrors(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, []byte("not a directory"), 0o600))
	for _, part := range []string{"store", "checkout"} {
		t.Run(part, func(t *testing.T) {
			got, err := physicalRoot("", part)
			require.NoError(t, err)
			assert.Empty(t, got)
			for _, tt := range []struct {
				name string
				path string
			}{
				{"missing root", filepath.Join(root, "missing")},
				{"NUL root", root + "\x00"},
				{"non-directory ancestor", filepath.Join(file, "child")},
			} {
				t.Run(tt.name, func(t *testing.T) {
					got, err := physicalRoot(tt.path, part)
					assert.Empty(t, got, "normalization failure must not return the logical path")
					require.ErrorContains(t, err, "resolving physical "+part+" root")
					if tt.name != "non-directory ancestor" {
						var pathErr *os.PathError
						require.ErrorAs(t, err, &pathErr)
					}
					if tt.name == "missing root" {
						assert.ErrorIs(t, err, os.ErrNotExist)
					}
				})
			}
			t.Run("dangling root alias", func(t *testing.T) {
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(filepath.Join(root, "missing"), alias); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				got, err := physicalRoot(alias, part)
				assert.Empty(t, got)
				assert.ErrorContains(t, err, "resolving physical "+part+" root")
				assert.ErrorIs(t, err, os.ErrNotExist)
			})
		})
	}
}

func TestRootsNormalizesSuccessfulPartsWithoutHidingErrors(t *testing.T) {
	root := t.TempDir()
	testhelpersGitDir(t, root)
	t.Chdir(root)
	t.Run("relative override without store", func(t *testing.T) {
		t.Setenv("ETHOS_REPO_ROOT", ".")
		loc := Roots()
		assert.ErrorIs(t, loc.StoreErr, ErrOverrideNoStore)
		assert.Empty(t, loc.Store)
		assert.Empty(t, loc.Ethos)
		assert.Nil(t, loc.Config)
		assert.NoError(t, loc.CheckoutErr)
		assert.Equal(t, realpath(t, root), loc.Checkout)
		_, err := loc.Layout()
		assert.Same(t, loc.StoreErr, err)
	})
	t.Run("invalid override refuses both parts rather than rediscovering", func(t *testing.T) {
		t.Setenv("ETHOS_REPO_ROOT", filepath.Join(root, "missing"))
		loc := Roots()
		require.Error(t, loc.StoreErr)
		require.Error(t, loc.CheckoutErr)
		assert.Empty(t, loc.Store)
		assert.Empty(t, loc.Checkout)
		assert.Empty(t, loc.Ethos)
		assert.Nil(t, loc.Config)
		_, err := loc.Layout()
		assert.Same(t, loc.StoreErr, err)
	})
}

func TestPhysicalRootResolvesLinksBeforeParentComponents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix symlink/.. path semantics")
	}
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o700))
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(filepath.Join("repo", "sub"), alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, tt := range []struct {
		name, cwd, path string
	}{
		{"logical cwd parent", alias, ".."},
		{"absolute alias parent", base, alias + "/.."},
		{"relative alias parent", base, "alias/.."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(tt.cwd)
			t.Setenv("PWD", tt.cwd)
			got, err := physicalRoot(tt.path, "store")
			require.NoError(t, err)
			assert.Equal(t, realpath(t, root), got)
		})
	}
}
