package attribute

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T, kind Kind) *Store {
	t.Helper()
	return NewStore(t.TempDir(), kind)
}

func TestStore_SaveAndLoad(t *testing.T) {
	s := testStore(t, Talents)

	a := &Attribute{Slug: "formal-methods", Content: "# Formal Methods\n\nZ specs and proofs.\n"}
	require.NoError(t, s.Save(a))

	loaded, err := s.Load("formal-methods")
	require.NoError(t, err)
	assert.Equal(t, "formal-methods", loaded.Slug)
	assert.Equal(t, "# Formal Methods\n\nZ specs and proofs.\n", loaded.Content)
}

func TestStore_SaveDuplicate(t *testing.T) {
	s := testStore(t, Personalities)

	a := &Attribute{Slug: "terse", Content: "# Terse\n"}
	require.NoError(t, s.Save(a))

	err := s.Save(a)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestStore_SaveEmptyContent(t *testing.T) {
	s := testStore(t, Talents)

	a := &Attribute{Slug: "empty", Content: ""}
	err := s.Save(a)
	require.Error(t, err)
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "content", ve.Field)
}

func TestStore_LoadNotFound(t *testing.T) {
	s := testStore(t, Talents)

	_, err := s.Load("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_List(t *testing.T) {
	s := testStore(t, WritingStyles)

	require.NoError(t, s.Save(&Attribute{Slug: "concise", Content: "# Concise\n"}))
	require.NoError(t, s.Save(&Attribute{Slug: "formal", Content: "# Formal\n"}))

	result, err := s.List()
	require.NoError(t, err)
	assert.Len(t, result.Attributes, 2)

	slugs := make([]string, len(result.Attributes))
	for i, a := range result.Attributes {
		slugs[i] = a.Slug
	}
	assert.Contains(t, slugs, "concise")
	assert.Contains(t, slugs, "formal")
}

func TestStore_ListEmpty(t *testing.T) {
	s := testStore(t, Talents)

	result, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, result.Attributes)
}

func TestStore_ListSkipsREADME(t *testing.T) {
	s := testStore(t, Talents)

	require.NoError(t, s.Save(&Attribute{Slug: "go", Content: "# Go\n"}))

	// Write a README.md that should be skipped
	dir := s.Dir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Talents\n"), 0o600))

	result, err := s.List()
	require.NoError(t, err)
	assert.Len(t, result.Attributes, 1)
	assert.Equal(t, "go", result.Attributes[0].Slug)
}

func TestStore_ListWarnsOnUnreadable(t *testing.T) {
	s := testStore(t, Talents)

	require.NoError(t, s.Save(&Attribute{Slug: "good", Content: "# Good\n"}))

	// Write a file with no read permissions
	dir := s.Dir()
	badPath := filepath.Join(dir, "bad.md")
	require.NoError(t, os.WriteFile(badPath, []byte("content"), 0o000))
	t.Cleanup(func() { os.Chmod(badPath, 0o600) })

	result, err := s.List()
	require.NoError(t, err)
	assert.Len(t, result.Attributes, 1)
	assert.Len(t, result.Warnings, 1)
}

func TestStore_Exists(t *testing.T) {
	s := testStore(t, Talents)

	assert.False(t, s.Exists("nope"))

	require.NoError(t, s.Save(&Attribute{Slug: "go", Content: "# Go\n"}))
	assert.True(t, s.Exists("go"))
}

func TestStore_Delete(t *testing.T) {
	s := testStore(t, Talents)

	require.NoError(t, s.Save(&Attribute{Slug: "go", Content: "# Go\n"}))
	assert.True(t, s.Exists("go"))

	require.NoError(t, s.Delete("go"))
	assert.False(t, s.Exists("go"))
}

func TestStore_DeleteNotFound(t *testing.T) {
	s := testStore(t, Talents)

	err := s.Delete("nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_PathTraversal(t *testing.T) {
	s := testStore(t, Talents)

	_, err := s.Path("../../etc/passwd")
	assert.Error(t, err)
}

func TestStore_InvalidSlug(t *testing.T) {
	cases := []struct {
		slug string
		ok   bool
	}{
		{"formal-methods", true},
		{"go", true},
		{"a1b2", true},
		{"Go", false},
		{"-bad", false},
		{"bad-", false},
		{"bad slug", false},
		{"bad.slug", false},
		{"", false},
	}

	for _, tc := range cases {
		err := ValidateSlug(tc.slug)
		if tc.ok {
			assert.NoError(t, err, "slug %q should be valid", tc.slug)
		} else {
			assert.Error(t, err, "slug %q should be invalid", tc.slug)
		}
	}
}

func TestStore_FilePermissions(t *testing.T) {
	s := testStore(t, Talents)

	require.NoError(t, s.Save(&Attribute{Slug: "go", Content: "# Go\n"}))

	// Check directory permissions
	dirInfo, err := os.Stat(s.Dir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	// Check file permissions
	p, err := s.Path("go")
	require.NoError(t, err)
	fileInfo, err := os.Stat(p)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())
}

func TestStore_TrailingNewline(t *testing.T) {
	s := testStore(t, Talents)

	// Content without trailing newline should get one added
	require.NoError(t, s.Save(&Attribute{Slug: "no-newline", Content: "# No newline"}))

	loaded, err := s.Load("no-newline")
	require.NoError(t, err)
	assert.True(t, len(loaded.Content) > 0)
	assert.Equal(t, byte('\n'), loaded.Content[len(loaded.Content)-1])
}

func TestStore_AllKinds(t *testing.T) {
	root := t.TempDir()

	for _, kind := range []Kind{Talents, Personalities, WritingStyles} {
		s := NewStore(root, kind)
		require.NoError(t, s.Save(&Attribute{Slug: "test", Content: "# Test\n"}))
		assert.True(t, s.Exists("test"))

		loaded, err := s.Load("test")
		require.NoError(t, err)
		assert.Equal(t, "# Test\n", loaded.Content)
	}
}
