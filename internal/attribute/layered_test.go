package attribute

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLayeredStore_EmptyRepoRoot(t *testing.T) {
	// An empty repoRoot must return a plain global store.
	global := t.TempDir()
	s := NewLayeredStore("", global, Talents)
	require.NotNil(t, s)
	assert.Nil(t, s.fallback)
	assert.Equal(t, global, s.root)
}

func TestNewLayeredStore_ReadsRepoFirst(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()

	// Seed both with different content under the same slug.
	repoStore := NewStore(repo, Talents)
	require.NoError(t, repoStore.Save(&Attribute{Slug: "go", Content: "repo go\n"}))

	globalStore := NewStore(global, Talents)
	require.NoError(t, globalStore.Save(&Attribute{Slug: "go", Content: "global go\n"}))

	s := NewLayeredStore(repo, global, Talents)

	// Load: repo wins.
	a, err := s.Load("go")
	require.NoError(t, err)
	assert.Equal(t, "repo go\n", a.Content)

	// Exists: true because repo has it.
	assert.True(t, s.Exists("go"))

	// Remove from repo and reload — should fall through to global.
	require.NoError(t, repoStore.Delete("go"))
	a, err = s.Load("go")
	require.NoError(t, err)
	assert.Equal(t, "global go\n", a.Content)
}

func TestNewLayeredStore_ListMerges(t *testing.T) {
	repo := t.TempDir()
	global := t.TempDir()

	require.NoError(t, NewStore(repo, Talents).Save(
		&Attribute{Slug: "repo-only", Content: "# r\n"}))
	require.NoError(t, NewStore(repo, Talents).Save(
		&Attribute{Slug: "shared", Content: "repo wins\n"}))

	require.NoError(t, NewStore(global, Talents).Save(
		&Attribute{Slug: "global-only", Content: "# g\n"}))
	require.NoError(t, NewStore(global, Talents).Save(
		&Attribute{Slug: "shared", Content: "global loses\n"}))

	s := NewLayeredStore(repo, global, Talents)

	result, err := s.List()
	require.NoError(t, err)
	require.Len(t, result.Attributes, 3)

	byslug := map[string]string{}
	for _, a := range result.Attributes {
		byslug[a.Slug] = a.Content
	}
	assert.Equal(t, "repo wins\n", byslug["shared"])
	assert.Contains(t, byslug, "repo-only")
	assert.Contains(t, byslug, "global-only")
}

func TestNewLayeredStore_LoadNotFound(t *testing.T) {
	// When neither repo nor global has the slug, Load returns not-found
	// from the global store and the fallback path is exercised.
	repo := t.TempDir()
	global := t.TempDir()
	// Create repo dir so listLocal sees an empty directory (not missing).
	require.NoError(t, os.MkdirAll(
		filepath.Join(repo, Talents.DirName), 0o700))

	s := NewLayeredStore(repo, global, Talents)
	_, err := s.Load("ghost")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestDefaultStore(t *testing.T) {
	// Happy path — a valid HOME yields a store rooted at
	// $HOME/.punt-labs/ethos.
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := DefaultStore(Personalities)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".punt-labs", "ethos"), s.root)
	assert.Equal(t, Personalities, s.kind)
}

func TestValidationError_Error(t *testing.T) {
	// Direct Error() call — and errors.As through the Save path.
	e := &ValidationError{Field: "slug", Message: "bad"}
	assert.Equal(t, "slug: bad", e.Error())

	s := NewStore(t.TempDir(), Talents)
	err := s.Save(&Attribute{Slug: "Bad Slug", Content: "x"})
	require.Error(t, err)
	var ve *ValidationError
	require.True(t, errors.As(err, &ve))
	assert.Equal(t, "slug", ve.Field)
}

func TestStore_ListWithInvalidSlugFile(t *testing.T) {
	// A .md file whose name fails ValidateSlug (e.g. uppercase) must be
	// reported as a warning, not crash the listing.
	s := NewStore(t.TempDir(), Talents)
	require.NoError(t, s.Save(&Attribute{Slug: "good", Content: "# g\n"}))

	// Write directly so the invalid name bypasses Save's validation.
	dir := s.Dir()
	badPath := filepath.Join(dir, "BadName.md")
	require.NoError(t, os.WriteFile(badPath, []byte("x"), 0o600))

	result, err := s.List()
	require.NoError(t, err)
	// The good entry is listed, and the bad file produces a warning.
	var goodFound bool
	for _, a := range result.Attributes {
		if a.Slug == "good" {
			goodFound = true
		}
	}
	assert.True(t, goodFound)
	assert.NotEmpty(t, result.Warnings)
}

func TestStore_LoadPropagatesPermissionError(t *testing.T) {
	// A layered store must propagate non-not-found errors from the
	// fallback, not mask them as a fall-through. Simulate by making
	// the repo file unreadable.
	repo := t.TempDir()
	global := t.TempDir()
	rs := NewStore(repo, Talents)
	require.NoError(t, rs.Save(&Attribute{Slug: "go", Content: "x\n"}))

	p, err := rs.Path("go")
	require.NoError(t, err)
	require.NoError(t, os.Chmod(p, 0o000))
	t.Cleanup(func() { os.Chmod(p, 0o600) })

	// Root-owned environments may still be able to read the file;
	// guard the assertion on an actual permission failure.
	if _, rerr := os.ReadFile(p); rerr == nil {
		t.Skip("running as root; cannot simulate permission denial")
	}

	s := NewLayeredStore(repo, global, Talents)
	_, loadErr := s.Load("go")
	require.Error(t, loadErr)
	// The error must NOT be a "not found" error — it is the underlying
	// permission error bubbled up verbatim.
	assert.NotContains(t, loadErr.Error(), "not found")
}

func TestStore_SaveInvalidSlug(t *testing.T) {
	s := NewStore(t.TempDir(), Talents)
	err := s.Save(&Attribute{Slug: "", Content: "x"})
	require.Error(t, err)
}

func TestStore_SavePathEscape(t *testing.T) {
	s := NewStore(t.TempDir(), Talents)
	// An invalid slug is caught by ValidateSlug before Path is called.
	err := s.Save(&Attribute{Slug: "..", Content: "x"})
	require.Error(t, err)
}

func TestStore_ExistsInvalidSlug(t *testing.T) {
	s := NewStore(t.TempDir(), Talents)
	// Invalid slug short-circuits to false without stat-ing anything.
	assert.False(t, s.Exists("Bad Slug"))
}

func TestStore_DeleteInvalidSlug(t *testing.T) {
	s := NewStore(t.TempDir(), Talents)
	err := s.Delete("Bad Slug")
	require.Error(t, err)
}

func TestStore_LoadInvalidSlug(t *testing.T) {
	s := NewStore(t.TempDir(), Talents)
	_, err := s.Load("Bad Slug")
	require.Error(t, err)
}
