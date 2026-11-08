package identity

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func extOwnershipFixture(t *testing.T, authoritative bool) (*LayeredStore, *Store, string) {
	t.Helper()
	root := physicalTempDir(t)
	repoRoot := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(repoRoot, 0o700))
	p, err := layout.NewRepository(repoRoot, repoRoot, layout.RecordBlame)
	require.NoError(t, err)
	repo := NewStore(p.RecordRoot(), RepositoryPolicy(p))
	bundle := NewStore(filepath.Join(root, "bundle"))
	global := NewStore(filepath.Join(root, "global"))
	for _, layer := range []struct {
		store *Store
		value string
	}{{repo, "repo-base"}, {bundle, "bundle-base"}, {global, "global-base"}} {
		require.NoError(t, layer.store.Save(&Identity{Name: "Agent", Handle: "test", Kind: "agent"}))
		require.NoError(t, layer.store.ExtSet("test", "quarry", "collection", layer.value))
		require.NoError(t, layer.store.ExtSet("test", "quarry", "collection", layer.value+"-overlay", Local(true)))
	}
	return NewLayeredStoreWithBundle(repo, bundle, global, authoritative), repo, root
}

func extOwnershipSnapshot(t *testing.T, root string) map[string][sha256.Size]byte {
	t.Helper()
	result := make(map[string][sha256.Size]byte)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		result[path] = sha256.Sum256([]byte(entry.Type().String()))
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = sha256.Sum256(data)
		} else if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			result[path] = sha256.Sum256([]byte(target))
		}
		return nil
	}))
	return result
}

func TestExtensionOwnershipRefusal(t *testing.T) {
	for _, kind := range []string{"inside-symlink", "outside-symlink", "dangling-symlink", "directory", "ancestor-file", "ancestor-symlink"} {
		t.Run(kind, func(t *testing.T) {
			ls, repo, root := extOwnershipFixture(t, true)
			path := repo.Path("test")
			badPath, reason := path, "symlink"
			switch kind {
			case "inside-symlink", "outside-symlink":
				target := filepath.Join(filepath.Dir(path), "original.yaml")
				if kind == "outside-symlink" {
					target = filepath.Join(root, "outside.yaml")
				}
				require.NoError(t, os.Rename(path, target))
				require.NoError(t, os.Symlink(target, path))
			case "dangling-symlink":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink(filepath.Join(root, "absent.yaml"), path))
			case "directory":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o700))
				reason = "not a regular file"
			case "ancestor-file", "ancestor-symlink":
				badPath = filepath.Dir(path)
				target := filepath.Join(root, "original-identities")
				require.NoError(t, os.Rename(badPath, target))
				if kind == "ancestor-file" {
					require.NoError(t, os.WriteFile(badPath, []byte("not a directory"), 0o600))
					reason = "not a directory"
				} else {
					require.NoError(t, os.Symlink(target, badPath))
				}
			}
			before := extOwnershipSnapshot(t, root)
			for _, operation := range []struct {
				name string
				run  func() error
			}{
				{"load", func() error { _, err := ls.Load("test", WithoutMigration()); return err }},
				{"get", func() error {
					data, err := ls.ExtGet("test", "quarry", "")
					assert.True(t, data == nil, "refusal must not return another layer's values")
					return err
				}},
				{"list", func() error {
					names, err := ls.ExtList("test")
					assert.Empty(t, names, "refusal must not return another layer's namespaces")
					return err
				}},
				{"set", func() error { return ls.ExtSet("test", "quarry", "collection", "changed") }},
				{"set-local", func() error { return ls.ExtSet("test", "quarry", "collection", "changed", Local(true)) }},
				{"delete-key", func() error { return ls.ExtDel("test", "quarry", "collection") }},
				{"delete-namespace", func() error { return ls.ExtDel("test", "quarry", "") }},
				{"delete-local", func() error { return ls.ExtDel("test", "quarry", "", Local(true)) }},
			} {
				t.Run(operation.name, func(t *testing.T) {
					err := operation.run()
					require.Error(t, err)
					assert.Contains(t, err.Error(), badPath)
					assert.Contains(t, err.Error(), reason)
					assert.NotContains(t, err.Error(), "active bundle", "rejection is not bundle ownership")
					for _, value := range []string{"repo-base", "bundle-base", "global-base"} {
						assert.NotContains(t, err.Error(), value)
					}
				})
			}
			assert.Equal(t, before, extOwnershipSnapshot(t, root), "refused operations must not write any layer")
			assert.NotEqual(t, ls.bundle.ExtDir("test"), ls.ExtDir("test"), "errorless path API must not select a lower layer on refusal")
			dir, err := ls.ExtDirChecked("test")
			require.ErrorContains(t, err, badPath)
			assert.Empty(t, dir)
			_, err = os.ReadDir(ls.ExtDir("test"))
			require.Error(t, err, "legacy path API must not permit a successful alternate-layer read")
			// Layered reads intentionally use global extensions, but ExtSet's
			// cross-layer identity precondition must not discard a refusal.
			layered := NewLayeredStoreWithBundle(repo, ls.bundle, ls.global, false)
			require.ErrorContains(t, layered.ExtSet("test", "quarry", "collection", "changed"), badPath)
			assert.Equal(t, before, extOwnershipSnapshot(t, root))
		})
	}
}

func TestExtensionOwnershipSelection(t *testing.T) {
	for _, authoritative := range []bool{true, false} {
		name := "layered-global"
		want := "global-base-overlay"
		if authoritative {
			name, want = "repo-only", "repo-base-overlay"
		}
		t.Run(name, func(t *testing.T) {
			ls, repo, _ := extOwnershipFixture(t, authoritative)
			data, err := ls.ExtGet("test", "quarry", "collection")
			require.NoError(t, err)
			assert.True(t, data["collection"] == want, "source-layer selection and overlay precedence")
			dir, err := ls.ExtDirChecked("test")
			require.NoError(t, err)
			assert.Equal(t, ls.ExtDir("test"), dir)
			baseOnly, err := os.ReadFile(filepath.Join(dir, "quarry.yaml"))
			require.NoError(t, err)
			assert.NotContains(t, string(baseOnly), "overlay", "filesystem base readers remain base-only")
			require.NoError(t, ls.ExtSet("test", "quarry", "other", "public"))
			owner := ls.global
			if authoritative {
				owner = repo
			}
			base, err := os.ReadFile(owner.extPath("test", "quarry"))
			require.NoError(t, err)
			assert.NotContains(t, string(base), "overlay", "base writes must never serialize the merged view")
			require.NoError(t, ls.ExtDel("test", "quarry", "other"))
			// Actual absence, unlike rejection, may fall back to the bundle.
			require.NoError(t, os.Remove(repo.Path("test")))
			data, err = ls.ExtGet("test", "quarry", "collection")
			require.NoError(t, err)
			if authoritative {
				assert.True(t, data["collection"] == "bundle-base-overlay")
				for _, mutate := range []func() error{
					func() error { return ls.ExtSet("test", "quarry", "collection", "changed") },
					func() error { return ls.ExtDel("test", "quarry", "") },
				} {
					require.ErrorContains(t, mutate(), "active bundle")
				}
			} else {
				assert.True(t, data["collection"] == want)
				require.NoError(t, ls.ExtSet("test", "quarry", "other", "public"))
			}
		})
	}
}
