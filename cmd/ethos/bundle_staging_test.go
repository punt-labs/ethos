package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestConfigLocalStagingFailurePreservesOldConfig(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	f, err := createConfigTemp(p)
	require.NoError(t, err)
	assert.Equal(t, p.TempDir(), filepath.Dir(f.Name()))
	assert.Contains(t, filepath.Base(f.Name()), layout.TmpMarker)
	second, err := createConfigTemp(p)
	require.NoError(t, err)
	assert.NotEqual(t, f.Name(), second.Name())
	require.NoError(t, f.Close())
	require.NoError(t, os.Remove(f.Name()))
	require.NoError(t, second.Close())
	require.NoError(t, os.Remove(second.Name()))
	require.NoError(t, os.Remove(p.TempDir()))
	path := filepath.Join(root, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("agent: old\n"), 0o644))
	require.NoError(t, layout.EnsureLocalDir(p, p.LocalRoot()))
	require.NoError(t, os.WriteFile(p.TempDir(), []byte("blocked"), 0o600))
	require.Error(t, setConfigKey(root, "agent", "new"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "agent: old\n", string(data))
	require.NoError(t, os.Remove(p.TempDir()))
	require.NoError(t, setConfigKey(root, "agent", "new"))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "agent: new")
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.NoFileExists(t, layout.TmpPath(path))
}

func TestConfigMalformedInputDoesNotCreateStaging(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	data := []byte("agent: [\n")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	require.ErrorContains(t, setConfigKey(root, "agent", "new"), "parsing")
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, data, got)
	_, err = os.Stat(filepath.Join(root, ".punt-labs", "local"))
	assert.True(t, os.IsNotExist(err), "malformed input created local state: %v", err)
}
