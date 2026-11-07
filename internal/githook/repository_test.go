package githook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryHookSymlinkUsesLocalStaging(t *testing.T) {
	store, checkout := physicalTempDir(t), physicalTempDir(t)
	p, err := layout.NewRepository(store, checkout, layout.RecordFull)
	require.NoError(t, err)
	target := filepath.Join(t.TempDir(), "host-hook")
	require.NoError(t, os.WriteFile(target, []byte("#!/bin/sh\necho host\n"), 0o700))
	link := filepath.Join(checkout, "pre-commit")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f, err := createTemp(target, p)
	require.NoError(t, err)
	assert.Equal(t, p.TempDir(), filepath.Dir(f.Name()))
	assert.Contains(t, filepath.Base(f.Name()), layout.TmpMarker)
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(f.Name()))
	for i := 0; i < 2; i++ {
		_, err := Chain(link, src, tag, ident, WithRepository(p))
		require.NoError(t, err)
	}
	assert.Equal(t, 1, countBegin(read(t, target)))
	assert.Contains(t, read(t, target), "echo host\n")
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	_, err = Unchain(link, tag, ident, WithRepository(p))
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\necho host\n", read(t, target))
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestRepositoryHookFailureNeverFallsBackAdjacent(t *testing.T) {
	root := physicalTempDir(t)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	target := filepath.Join(root, "pre-commit")
	host := "#!/bin/sh\necho host\n"
	require.NoError(t, os.WriteFile(target, []byte(host), 0o700))
	require.NoError(t, layout.EnsureLocalDir(p, p.LocalRoot()))
	require.NoError(t, os.WriteFile(p.TempDir(), []byte("blocked"), 0o600))
	_, err = Chain(target, src, tag, ident, WithRepository(p))
	require.Error(t, err)
	assert.Equal(t, host, read(t, target))
	// A commit failure also cleans its local staging file and preserves the
	// old destination. No rename-over-directory workaround is attempted.
	require.NoError(t, os.Remove(p.TempDir()))
	dir := filepath.Join(root, "not-a-file")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.Error(t, writeExec(dir, []byte("new"), p))
	assert.DirExists(t, dir)
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func physicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}
