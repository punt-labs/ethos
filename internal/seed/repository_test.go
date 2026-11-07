package seed

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryAgentStagingRetainsInstallSemantics(t *testing.T) {
	store, checkout := physicalTempDir(t), physicalTempDir(t)
	p, err := layout.NewRepository(store, checkout, layout.RecordFull)
	require.NoError(t, err)
	dir := filepath.Join(checkout, ".claude", "agents")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	dest := filepath.Join(dir, "reviewer.md")
	f, err := createTemp(dest, p)
	require.NoError(t, err)
	assert.Equal(t, p.TempDir(), filepath.Dir(f.Name()))
	assert.Contains(t, filepath.Base(f.Name()), layout.TmpMarker)
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(f.Name()))
	require.NoError(t, linkInstall(dest, []byte("first"), p))
	require.ErrorIs(t, linkInstall(dest, []byte("second"), p), os.ErrExist)
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "first", string(data))
	require.NoError(t, atomicWrite(dest, []byte("replacement"), p))
	data, err = os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "replacement", string(data))
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestRepositoryAgentsPreserveEditedFilesAndGlobalPlacement(t *testing.T) {
	store, checkout, global := physicalTempDir(t), physicalTempDir(t), t.TempDir()
	p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
	require.NoError(t, err)
	dest, skills := filepath.Join(global, "ethos"), filepath.Join(global, "skills")
	agents := filepath.Join(checkout, ".claude", "agents")
	opts := []Option{WithRepositoryAgents(p)}
	r, err := SeedVersion(dest, skills, agents, "test", false, opts...)
	require.NoError(t, err)
	agent := filepath.Join(agents, "code-reviewer.md")
	assert.Contains(t, r.Deployed, agent)
	// The manifest remains global, using the same key/scope as an old seed.
	m, err := loadManifest(dest)
	require.NoError(t, err)
	assert.Equal(t, scopeEthos, m.Entries[filepath.ToSlash(agent)].Scope)
	assert.FileExists(t, filepath.Join(dest, ManifestName))
	assert.NoFileExists(t, filepath.Join(p.LocalRoot(), ManifestName))
	require.NoError(t, os.WriteFile(agent, []byte("user edit\n"), 0o644))
	r, err = SeedVersion(dest, skills, agents, "next", false, opts...)
	require.NoError(t, err)
	assert.Contains(t, r.Edited, agent)
	data, err := os.ReadFile(agent)
	require.NoError(t, err)
	assert.Equal(t, "user edit\n", string(data))
	// A blocked repository staging area must not move global writes there.
	require.NoError(t, os.Remove(p.TempDir()))
	require.NoError(t, os.WriteFile(p.TempDir(), []byte("blocked"), 0o600))
	r, err = SeedVersion(dest, skills, agents, "next", true, opts...)
	require.Error(t, err)
	assert.NotEmpty(t, r.Errors)
	data, err = os.ReadFile(agent)
	require.NoError(t, err)
	assert.Equal(t, "user edit\n", string(data))
	// Seed without a repository destination is still wholly global.
	_, err = SeedVersion(dest, skills, "", "next", false, opts...)
	require.NoError(t, err)
}

func TestRepositoryAgentFailuresLeaveNoSourceStaging(t *testing.T) {
	root := physicalTempDir(t)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	dir := filepath.Join(root, ".claude", "agents")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	dest := filepath.Join(dir, "reviewer.md")
	require.NoError(t, layout.EnsureLocalDir(p, p.LocalRoot()))
	require.NoError(t, os.WriteFile(p.TempDir(), []byte("blocked"), 0o600))
	require.Error(t, linkInstall(dest, []byte("new"), p))
	assert.NoFileExists(t, dest)
	require.NoError(t, os.Remove(p.TempDir()))
	// Failed publication cleans local staging while preserving the old entry.
	require.NoError(t, os.Mkdir(dest, 0o700))
	require.Error(t, atomicWrite(dest, []byte("new"), p))
	assert.DirExists(t, dest)
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestRepositoryAgentConcurrentCreateDoesNotClobber(t *testing.T) {
	root := physicalTempDir(t)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	dir := filepath.Join(root, ".claude", "agents")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	dest := filepath.Join(dir, "reviewer.md")
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- linkInstall(dest, []byte("complete\n"), p)
		}()
	}
	wg.Wait()
	close(errs)
	winners := 0
	for err := range errs {
		if err == nil {
			winners++
		} else {
			assert.ErrorIs(t, err, os.ErrExist)
		}
	}
	assert.Equal(t, 1, winners)
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "complete\n", string(data))
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
