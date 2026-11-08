package layout_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func physicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return root
}

func TestEnsureLocalDirRejectsRedirection(t *testing.T) {
	for _, component := range []string{".punt-labs", ".punt-labs/local", ".punt-labs/local/ethos", ".punt-labs/local/ethos/tmp"} {
		for _, targetKind := range []string{"source", "checkout", "dangling"} {
			t.Run(component+" to "+targetKind, func(t *testing.T) {
				store, checkout := physicalTempDir(t), physicalTempDir(t)
				p, err := layout.NewRepository(store, checkout, layout.RecordFull)
				require.NoError(t, err)
				target := filepath.Join(store, "source")
				if targetKind == "checkout" {
					target = checkout
				}
				if targetKind != "dangling" {
					require.NoError(t, os.MkdirAll(target, 0o700))
				}
				link := filepath.Join(store, filepath.FromSlash(component))
				require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				f, err := layout.CreateLocalTemp(p, "record", ".yaml")
				require.ErrorContains(t, err, "symlink")
				assert.Nil(t, f)
				if targetKind != "dangling" {
					entries, err := os.ReadDir(target)
					require.NoError(t, err)
					assert.Empty(t, entries, "no redirected writes")
				} else {
					assert.NoDirExists(t, target)
				}
			})
		}
	}
}

func TestEnsureLocalDirValidationAndRetry(t *testing.T) {
	root := physicalTempDir(t)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	for _, dir := range []string{"", "relative", root, filepath.Join(root, ".punt-labs", "ethos"), filepath.Join(root, ".punt-labs", "local", "ethos-other")} {
		assert.Error(t, layout.EnsureLocalDir(p, dir), dir)
	}
	assert.Error(t, layout.EnsureLocalDir(p, p.LocalRoot()+"/alias/../tmp"), "do not clean away a possible symlink ancestor")
	assert.Error(t, layout.EnsureLocalDir(layout.Policy{}, root))
	assert.NoDirExists(t, filepath.Join(root, ".punt-labs"))

	// Simulate an interrupted directory setup; retry fills in only local state.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs", "local"), 0o700))
	require.NoError(t, layout.EnsureLocalDir(p, p.TempDir()))
	require.NoError(t, layout.EnsureLocalDir(p, p.TempDir()))
	require.NoError(t, os.Remove(p.TempDir()))
	require.NoError(t, os.WriteFile(p.TempDir(), []byte("not a directory"), 0o600))
	assert.ErrorContains(t, layout.EnsureLocalDir(p, p.TempDir()), "not a directory")
	assert.NoDirExists(t, filepath.Join(root, ".punt-labs", "ethos"))

	missing := filepath.Join(root, "absent-store")
	p, err = layout.NewRepository(missing, root, layout.RecordFull)
	require.NoError(t, err)
	assert.ErrorIs(t, layout.EnsureLocalDir(p, p.TempDir()), os.ErrNotExist)
	assert.NoDirExists(t, missing)
}

func TestEnsureLocalDirRejectsSymlinkedStoreAncestor(t *testing.T) {
	base, target := physicalTempDir(t), physicalTempDir(t)
	link := filepath.Join(base, "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, root := range []string{link, filepath.Join(link, "nested")} {
		p, err := layout.NewRepository(root, base, layout.RecordBlame)
		require.NoError(t, err)
		assert.ErrorContains(t, layout.EnsureLocalDir(p, p.TempDir()), "symlink")
	}
	entries, err := os.ReadDir(target)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestLocalStagingIsUniqueAndSurvivesCheckoutRemoval(t *testing.T) {
	store, checkout := physicalTempDir(t), physicalTempDir(t)
	p, err := layout.NewRepository(store, checkout, layout.RecordFull)
	require.NoError(t, err)
	const count = 24
	type result struct {
		name string
		err  error
	}
	results := make(chan result, count)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			f, err := layout.CreateLocalTemp(p, "record", ".yaml")
			if err != nil {
				results <- result{err: err}
				return
			}
			_, writeErr := f.WriteString("unfinished record\n")
			results <- result{name: f.Name(), err: errors.Join(writeErr, f.Close())}
		})
	}
	wg.Wait()
	close(results)
	seen := make(map[string]bool)
	for r := range results {
		require.NoError(t, r.err)
		assert.False(t, seen[r.name], "unique staging names")
		seen[r.name] = true
		assert.Equal(t, filepath.Join(store, ".punt-labs", "local", "ethos", "tmp"), filepath.Dir(r.name))
		assert.Contains(t, filepath.Base(r.name), ".ethos-tmp")
		if runtime.GOOS != "windows" {
			info, err := os.Stat(r.name)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		}
	}
	assert.Len(t, seen, count)
	require.NoError(t, os.RemoveAll(checkout))
	for name := range seen {
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		assert.Equal(t, "unfinished record\n", string(data))
	}
	assert.NoDirExists(t, filepath.Join(store, ".punt-labs", "ethos"))
}

func TestLocalStagingRejectsFilenamePaths(t *testing.T) {
	root := physicalTempDir(t)
	p, err := layout.NewRepository(root, root, layout.RecordFull)
	require.NoError(t, err)
	for _, bad := range []string{"../record", `x\y`, "/absolute", "NUL\x00"} {
		_, err := layout.CreateLocalTemp(p, bad, ".yaml")
		assert.Error(t, err)
		_, err = layout.CreateLocalTemp(p, "record", bad)
		assert.Error(t, err)
	}
	assert.NoDirExists(t, filepath.Join(root, ".punt-labs"))
}
