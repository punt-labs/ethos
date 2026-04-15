package bundle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFile creates parent dirs and writes content at path.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// writeRepoConfig writes .punt-labs/ethos.yaml under repoRoot.
func writeRepoConfig(t *testing.T, repoRoot, body string) {
	t.Helper()
	writeFile(t, filepath.Join(repoRoot, ".punt-labs", "ethos.yaml"), body)
}

// mkRepoBundle creates <repoRoot>/.punt-labs/ethos-bundles/<name>/.
// If manifest != "" the bundle.yaml is written.
func mkRepoBundle(t *testing.T, repoRoot, name, manifest string) string {
	t.Helper()
	p := filepath.Join(repoRoot, ".punt-labs", "ethos-bundles", name)
	require.NoError(t, os.MkdirAll(p, 0o755))
	if manifest != "" {
		writeFile(t, filepath.Join(p, "bundle.yaml"), manifest)
	}
	return p
}

// mkGlobalBundle creates <globalRoot>/bundles/<name>/.
func mkGlobalBundle(t *testing.T, globalRoot, name, manifest string) string {
	t.Helper()
	p := filepath.Join(globalRoot, "bundles", name)
	require.NoError(t, os.MkdirAll(p, 0o755))
	if manifest != "" {
		writeFile(t, filepath.Join(p, "bundle.yaml"), manifest)
	}
	return p
}

// mkLegacyDir creates <repoRoot>/.punt-labs/ethos/.
func mkLegacyDir(t *testing.T, repoRoot string) string {
	t.Helper()
	p := filepath.Join(repoRoot, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(p, 0o755))
	return p
}

// --- ResolveActive tests ---

func TestResolveActive_Empty(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()
	b, err := ResolveActive(repo, global)
	require.NoError(t, err)
	assert.Nil(t, b)
}

func TestResolveActive_LegacyDir(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()
	legacy := mkLegacyDir(t, repo)

	b, err := ResolveActive(repo, global)
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, SourceLegacy, b.Source)
	assert.Equal(t, legacy, b.Path)
}

func TestResolveActive_GlobalBundle(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()
	writeRepoConfig(t, repo, "active_bundle: foo\n")
	mkGlobalBundle(t, global, "foo", "name: foo\nversion: 1\n")

	b, err := ResolveActive(repo, global)
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, SourceGlobal, b.Source)
	assert.Equal(t, "foo", b.Name)
	assert.True(t, b.HasManifest)
	assert.Equal(t, 1, b.Manifest.Version)
}

func TestResolveActive_RepoWinsOverGlobal(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()
	writeRepoConfig(t, repo, "active_bundle: foo\n")
	repoPath := mkRepoBundle(t, repo, "foo", "name: foo\n")
	mkGlobalBundle(t, global, "foo", "name: foo\n")

	b, err := ResolveActive(repo, global)
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, SourceRepo, b.Source)
	assert.Equal(t, repoPath, b.Path)
}

func TestResolveActive_MissingBundle(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()
	writeRepoConfig(t, repo, "active_bundle: ghost\n")

	b, err := ResolveActive(repo, global)
	require.Error(t, err)
	assert.Nil(t, b)
	assert.Contains(t, err.Error(), "ghost")
	assert.Contains(t, err.Error(), "not found")
}

func TestResolveActive_ManifestName(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()
	writeRepoConfig(t, repo, "active_bundle: gstack\n")
	// Directory is "gstack" but manifest uses the same name.
	mkGlobalBundle(t, global, "gstack", "name: gstack\ndescription: starter\n")

	b, err := ResolveActive(repo, global)
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, "gstack", b.Name)
	assert.Equal(t, "starter", b.Manifest.Description)
}

func TestResolveActive_NoManifest(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()
	writeRepoConfig(t, repo, "active_bundle: bare\n")
	mkGlobalBundle(t, global, "bare", "")

	b, err := ResolveActive(repo, global)
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, "bare", b.Name)
	assert.False(t, b.HasManifest)
}

// --- List tests ---

func TestList_Empty(t *testing.T) {
	got, err := List(t.TempDir(), t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestList_Mixed(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()
	mkRepoBundle(t, repo, "r1", "name: r1\n")
	mkGlobalBundle(t, global, "g1", "name: g1\n")
	mkLegacyDir(t, repo)

	got, err := List(repo, global)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, SourceRepo, got[0].Source)
	assert.Equal(t, "r1", got[0].Name)
	assert.Equal(t, SourceGlobal, got[1].Source)
	assert.Equal(t, "g1", got[1].Name)
	assert.Equal(t, SourceLegacy, got[2].Source)
}

func TestList_SameNameBothScopes(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()
	mkRepoBundle(t, repo, "foo", "name: foo\n")
	mkGlobalBundle(t, global, "foo", "name: foo\n")

	got, err := List(repo, global)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "foo", got[0].Name)
	assert.Equal(t, SourceRepo, got[0].Source)
	assert.Equal(t, "foo", got[1].Name)
	assert.Equal(t, SourceGlobal, got[1].Source)
}

func TestList_SortsByNameWithinSource(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()
	mkRepoBundle(t, repo, "zeta", "name: zeta\n")
	mkRepoBundle(t, repo, "alpha", "name: alpha\n")

	got, err := List(repo, global)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "alpha", got[0].Name)
	assert.Equal(t, "zeta", got[1].Name)
}

// --- LoadBundle tests ---

func TestLoadBundle_WithManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bundle.yaml"),
		"name: my-bundle\nversion: 2\ndescription: test\n")

	b, err := LoadBundle(dir)
	require.NoError(t, err)
	assert.Equal(t, "my-bundle", b.Name)
	assert.True(t, b.HasManifest)
	assert.Equal(t, 2, b.Manifest.Version)
	assert.Equal(t, "test", b.Manifest.Description)
}

func TestLoadBundle_NoManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plain")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	b, err := LoadBundle(dir)
	require.NoError(t, err)
	assert.Equal(t, "plain", b.Name)
	assert.False(t, b.HasManifest)
}

func TestLoadBundle_NotADir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	writeFile(t, f, "")
	_, err := LoadBundle(f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestLoadBundle_Missing(t *testing.T) {
	_, err := LoadBundle(filepath.Join(t.TempDir(), "nope"))
	require.Error(t, err)
}

func TestLoadBundle_MalformedManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bundle.yaml"), "name: [unclosed\n")

	_, err := LoadBundle(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing")
}

// --- Validate tests ---

func TestValidate_OK(t *testing.T) {
	dir := t.TempDir()
	bundleDir := filepath.Join(dir, "gstack")
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))
	writeFile(t, filepath.Join(bundleDir, "bundle.yaml"), "name: gstack\n")

	b, err := LoadBundle(bundleDir)
	require.NoError(t, err)
	assert.NoError(t, b.Validate())
}

func TestValidate_NoManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bare")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	b, err := LoadBundle(dir)
	require.NoError(t, err)
	assert.NoError(t, b.Validate())
}

func TestValidate_NameMismatch(t *testing.T) {
	dir := t.TempDir()
	bundleDir := filepath.Join(dir, "gstack")
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))
	writeFile(t, filepath.Join(bundleDir, "bundle.yaml"), "name: other\n")

	b, err := LoadBundle(bundleDir)
	require.NoError(t, err)
	err = b.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
}

func TestValidate_MissingPath(t *testing.T) {
	b := &Bundle{Name: "ghost", Path: filepath.Join(t.TempDir(), "nope")}
	require.Error(t, b.Validate())
}
