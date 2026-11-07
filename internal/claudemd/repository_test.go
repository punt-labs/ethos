package claudemd

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryHostSymlinkStagingAndConcurrentLock(t *testing.T) {
	store, checkout := physicalTempDir(t), physicalTempDir(t)
	p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
	require.NoError(t, err)
	target := fixture(t, "AGENTS.md", "# host\n")
	link := filepath.Join(checkout, "CLAUDE.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f, err := createTemp(target, link, p)
	require.NoError(t, err)
	assert.Equal(t, p.TempDir(), filepath.Dir(f.Name()))
	assert.Contains(t, filepath.Base(f.Name()), "CLAUDE.md")
	assert.Contains(t, filepath.Base(f.Name()), layout.TmpMarker)
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(f.Name()))
	p2, err := layout.NewRepository(store, physicalTempDir(t), layout.RecordFull)
	require.NoError(t, err)
	l1, err := lockPathFor(target, p)
	require.NoError(t, err)
	l2, err := lockPathFor(target, p2)
	require.NoError(t, err)
	assert.Equal(t, l1, l2)
	assert.Equal(t, filepath.Join(p.LocalRoot(), "locks", "claudemd"), filepath.Dir(l1))
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			policy := p
			if i%2 == 1 {
				policy = p2
			}
			_, err := Register(link, fmt.Sprintf("@guide-%d.md", i), WithRepository(policy))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	body := read(t, target)
	for i := 0; i < 16; i++ {
		assert.Contains(t, body, fmt.Sprintf("@guide-%d.md\n", i))
	}
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestRepositoryHostStagingFailurePreservesTarget(t *testing.T) {
	root := physicalTempDir(t)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	path := filepath.Join(root, "CLAUDE.md")
	require.NoError(t, os.WriteFile(path, []byte("old\n"), 0o600))
	require.NoError(t, layout.EnsureLocalDir(p, p.LocalRoot()))
	require.NoError(t, os.WriteFile(p.TempDir(), []byte("blocked"), 0o600))
	wrote, err := Register(path, canonical, WithRepository(p))
	require.Error(t, err)
	assert.False(t, wrote)
	assert.Equal(t, "old\n", read(t, path))
	// Malformed markdown still refuses without altering the host.
	require.NoError(t, os.Remove(p.TempDir()))
	require.NoError(t, os.WriteFile(path, []byte("```\n"), 0o600))
	wrote, err = Register(path, canonical, WithRepository(p))
	require.ErrorContains(t, err, "unterminated")
	assert.False(t, wrote)
	assert.Equal(t, "```\n", read(t, path))
}

func physicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}
