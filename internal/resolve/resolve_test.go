package resolve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve_RepoLocalConfig(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(configDir, "config.yaml"),
		[]byte("active: repo-user\n"),
		0o644,
	))

	handle, err := Resolve(root)
	require.NoError(t, err)
	assert.Equal(t, "repo-user", handle)
}

func TestResolve_GlobalActive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ethosDir := filepath.Join(home, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(ethosDir, "active"),
		[]byte("global-user\n"),
		0o644,
	))

	// Use a repo root with no local config.
	repoRoot := t.TempDir()
	handle, err := Resolve(repoRoot)
	require.NoError(t, err)
	assert.Equal(t, "global-user", handle)
}

func TestResolve_RepoLocalOverridesGlobal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Set up global active.
	ethosDir := filepath.Join(home, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(ethosDir, "active"),
		[]byte("global-user\n"),
		0o644,
	))

	// Set up repo-local config.
	repoRoot := t.TempDir()
	configDir := filepath.Join(repoRoot, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(configDir, "config.yaml"),
		[]byte("active: local-user\n"),
		0o644,
	))

	handle, err := Resolve(repoRoot)
	require.NoError(t, err)
	assert.Equal(t, "local-user", handle)
}

func TestResolve_NoConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoRoot := t.TempDir()

	_, err := Resolve(repoRoot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no active identity")
}

func TestResolve_EmptyActiveFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	ethosDir := filepath.Join(home, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(ethosDir, "active"),
		[]byte("   \n"),
		0o644,
	))

	repoRoot := t.TempDir()
	_, err := Resolve(repoRoot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestResolve_MalformedRepoConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Set up global active as fallback.
	ethosDir := filepath.Join(home, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(ethosDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(ethosDir, "active"),
		[]byte("global-user\n"),
		0o644,
	))

	// Set up malformed repo config.
	repoRoot := t.TempDir()
	configDir := filepath.Join(repoRoot, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(configDir, "config.yaml"),
		[]byte(":::bad yaml"),
		0o644,
	))

	// Should fall through to global.
	handle, err := Resolve(repoRoot)
	require.NoError(t, err)
	assert.Equal(t, "global-user", handle)
}
