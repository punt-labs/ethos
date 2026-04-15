package attribute

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeLayer(t *testing.T, root, slug, content string) {
	t.Helper()
	require.NoError(t, NewStore(root, Talents).Save(&Attribute{Slug: slug, Content: content}))
}

func TestThreeLayer_RepoWins(t *testing.T) {
	repo := t.TempDir()
	bundle := t.TempDir()
	global := t.TempDir()

	writeLayer(t, repo, "foo", "repo\n")
	writeLayer(t, bundle, "foo", "bundle\n")

	s := NewLayeredStoreWithBundle(repo, bundle, global, Talents)
	a, err := s.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, "repo\n", a.Content)
}

func TestThreeLayer_BundleWins(t *testing.T) {
	repo := t.TempDir()
	bundle := t.TempDir()
	global := t.TempDir()

	writeLayer(t, bundle, "foo", "bundle\n")
	writeLayer(t, global, "foo", "global\n")

	s := NewLayeredStoreWithBundle(repo, bundle, global, Talents)
	a, err := s.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, "bundle\n", a.Content)
}

func TestThreeLayer_GlobalFallback(t *testing.T) {
	repo := t.TempDir()
	bundle := t.TempDir()
	global := t.TempDir()

	writeLayer(t, global, "foo", "global\n")

	s := NewLayeredStoreWithBundle(repo, bundle, global, Talents)
	a, err := s.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, "global\n", a.Content)
}

func TestThreeLayer_ListDedupes(t *testing.T) {
	repo := t.TempDir()
	bundle := t.TempDir()
	global := t.TempDir()

	writeLayer(t, repo, "shared", "repo\n")
	writeLayer(t, repo, "repo-only", "r\n")
	writeLayer(t, bundle, "shared", "bundle\n")
	writeLayer(t, bundle, "bundle-only", "b\n")
	writeLayer(t, global, "shared", "global\n")
	writeLayer(t, global, "global-only", "g\n")

	s := NewLayeredStoreWithBundle(repo, bundle, global, Talents)
	result, err := s.List()
	require.NoError(t, err)
	require.Len(t, result.Attributes, 4)

	byslug := map[string]string{}
	for _, a := range result.Attributes {
		byslug[a.Slug] = a.Content
	}
	assert.Equal(t, "repo\n", byslug["shared"], "repo wins on collision")
	assert.Contains(t, byslug, "repo-only")
	assert.Contains(t, byslug, "bundle-only")
	assert.Contains(t, byslug, "global-only")
}

func TestThreeLayer_BundleReadOnly(t *testing.T) {
	// Verifies Save still targets global when all three layers are present.
	repo := t.TempDir()
	bundle := t.TempDir()
	global := t.TempDir()

	s := NewLayeredStoreWithBundle(repo, bundle, global, Talents)
	require.NoError(t, s.Save(&Attribute{Slug: "new", Content: "x\n"}))

	// Only the global layer should have the new file.
	assert.FileExists(t, filepath.Join(global, Talents.DirName, "new.md"))
	assert.NoFileExists(t, filepath.Join(bundle, Talents.DirName, "new.md"))
	assert.NoFileExists(t, filepath.Join(repo, Talents.DirName, "new.md"))
}

func TestThreeLayer_NoBundleMatchesLegacyBehavior(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()

	writeLayer(t, repo, "foo", "repo\n")
	writeLayer(t, global, "foo", "global\n")

	s := NewLayeredStore(repo, global, Talents)
	a, err := s.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, "repo\n", a.Content)
}

func TestThreeLayer_BundleOnly(t *testing.T) {
	// repoRoot empty, bundle set.
	bundle := t.TempDir()
	global := t.TempDir()

	writeLayer(t, bundle, "foo", "bundle\n")

	s := NewLayeredStoreWithBundle("", bundle, global, Talents)
	a, err := s.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, "bundle\n", a.Content)
}
