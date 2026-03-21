package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestAttribute writes a .md file for use in identity tests.
func createTestAttribute(t *testing.T, root string, kind attribute.Kind, slug, content string) {
	t.Helper()
	s := attribute.NewStore(root, kind)
	require.NoError(t, s.Save(&attribute.Attribute{Slug: slug, Content: content}))
}

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

func testIdentity() *Identity {
	return &Identity{
		Name:   "Mal Reynolds",
		Handle: "mal",
		Kind:   "human",
		Email:  "mal@serenity.ship",
	}
}

func TestStore_SaveAndLoad(t *testing.T) {
	s := testStore(t)
	id := testIdentity()

	require.NoError(t, s.Save(id))

	loaded, err := s.Load("mal")
	require.NoError(t, err)
	assert.Equal(t, "Mal Reynolds", loaded.Name)
	assert.Equal(t, "mal", loaded.Handle)
	assert.Equal(t, "human", loaded.Kind)
	assert.Equal(t, "mal@serenity.ship", loaded.Email)
}

func TestStore_SaveDuplicate(t *testing.T) {
	s := testStore(t)
	id := testIdentity()

	require.NoError(t, s.Save(id))
	err := s.Save(id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestStore_LoadNotFound(t *testing.T) {
	s := testStore(t)
	_, err := s.Load("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_LoadNormalizesEmptyVoice(t *testing.T) {
	s := testStore(t)

	// Write a YAML file with an empty voice block directly.
	dir := s.IdentitiesDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	data := []byte("name: Test\nhandle: test\nkind: human\nvoice: {}\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.yaml"), data, 0o600))

	loaded, err := s.Load("test")
	require.NoError(t, err)
	assert.Nil(t, loaded.Voice, "empty voice should be normalized to nil")
}

func TestStore_List(t *testing.T) {
	s := testStore(t)

	// Empty list.
	result, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, result.Identities)

	// Add two identities.
	require.NoError(t, s.Save(&Identity{Name: "Alice", Handle: "alice", Kind: "human"}))
	require.NoError(t, s.Save(&Identity{Name: "Bob", Handle: "bob", Kind: "agent"}))

	result, err = s.List()
	require.NoError(t, err)
	assert.Len(t, result.Identities, 2)
}

func TestStore_ListSkipsMalformed(t *testing.T) {
	s := testStore(t)
	dir := s.IdentitiesDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))

	// Write a valid identity.
	require.NoError(t, s.Save(&Identity{Name: "Alice", Handle: "alice", Kind: "human"}))

	// Write a malformed YAML file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(":::bad yaml"), 0o600))

	result, err := s.List()
	require.NoError(t, err)
	assert.Len(t, result.Identities, 1)
	assert.Len(t, result.Warnings, 1)
	assert.Contains(t, result.Warnings[0], "bad.yaml")
}

func TestStore_ListNoDirectory(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "nonexistent"))
	result, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, result.Identities)
}

func TestStore_Exists(t *testing.T) {
	s := testStore(t)
	assert.False(t, s.Exists("mal"))

	require.NoError(t, s.Save(testIdentity()))
	assert.True(t, s.Exists("mal"))
}

func TestStore_Path(t *testing.T) {
	s := NewStore("/root/ethos")
	assert.Equal(t, "/root/ethos/identities/mal.yaml", s.Path("mal"))
}

func TestStore_PathTraversalPrevention(t *testing.T) {
	s := NewStore("/root/ethos")
	// filepath.Base strips directory traversal.
	path := s.Path("../../etc/passwd")
	assert.Equal(t, "/root/ethos/identities/passwd.yaml", path)
}

func TestStore_SaveWithVoice(t *testing.T) {
	s := testStore(t)
	id := &Identity{
		Name:   "Test",
		Handle: "test",
		Kind:   "human",
		Voice:  &Voice{Provider: "elevenlabs", VoiceID: "abc123"},
	}
	require.NoError(t, s.Save(id))

	loaded, err := s.Load("test")
	require.NoError(t, err)
	require.NotNil(t, loaded.Voice)
	assert.Equal(t, "elevenlabs", loaded.Voice.Provider)
	assert.Equal(t, "abc123", loaded.Voice.VoiceID)
}

func TestStore_SaveWithAllFields(t *testing.T) {
	s := testStore(t)

	// Create attribute files that the identity references.
	createTestAttribute(t, s.Root(), attribute.WritingStyles, "terse", "# Terse\nDirect.")
	createTestAttribute(t, s.Root(), attribute.Personalities, "analytical", "# Analytical\nData-driven.")
	createTestAttribute(t, s.Root(), attribute.Skills, "go", "# Go\nSystems programming.")
	createTestAttribute(t, s.Root(), attribute.Skills, "testing", "# Testing\nTDD.")

	id := &Identity{
		Name:         "Full Identity",
		Handle:       "full",
		Kind:         "agent",
		Email:        "full@example.com",
		GitHub:       "fullgit",
		Voice:        &Voice{Provider: "elevenlabs", VoiceID: "v1"},
		Agent:        ".claude/agents/full.md",
		WritingStyle: "terse",
		Personality:  "analytical",
		Skills:       []string{"go", "testing"},
	}
	require.NoError(t, s.Save(id))

	// Load with resolution — content fields should be populated.
	loaded, err := s.Load("full")
	require.NoError(t, err)
	assert.Equal(t, "terse", loaded.WritingStyle)
	assert.Equal(t, "analytical", loaded.Personality)
	assert.Equal(t, []string{"go", "testing"}, loaded.Skills)
	assert.Contains(t, loaded.PersonalityContent, "Analytical")
	assert.Contains(t, loaded.WritingStyleContent, "Terse")
	assert.Len(t, loaded.SkillContents, 2)
	assert.Contains(t, loaded.SkillContents[0], "Go")
	assert.Empty(t, loaded.Warnings)

	// Load with Reference — content fields should be empty.
	ref, err := s.Load("full", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "terse", ref.WritingStyle)
	assert.Empty(t, ref.WritingStyleContent)
	assert.Empty(t, ref.PersonalityContent)
	assert.Nil(t, ref.SkillContents)
}

func TestStore_SaveRejectsMissingRef(t *testing.T) {
	s := testStore(t)
	id := &Identity{
		Name:        "Bad Ref",
		Handle:      "badref",
		Kind:        "human",
		Personality: "nonexistent",
	}
	err := s.Save(id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_LoadMissingAttributeWarns(t *testing.T) {
	s := testStore(t)

	// Create identity with no attribute files.
	dir := s.identitiesDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	data := []byte("name: Test\nhandle: test\nkind: human\npersonality: missing-personality\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.yaml"), data, 0o600))

	loaded, err := s.Load("test")
	require.NoError(t, err)
	assert.Equal(t, "missing-personality", loaded.Personality)
	assert.Empty(t, loaded.PersonalityContent)
	assert.Len(t, loaded.Warnings, 1)
	assert.Contains(t, loaded.Warnings[0], "missing-personality")
}

func TestStore_Update(t *testing.T) {
	s := testStore(t)

	createTestAttribute(t, s.Root(), attribute.Personalities, "kind", "# Kind\n")
	createTestAttribute(t, s.Root(), attribute.Personalities, "stern", "# Stern\n")

	id := &Identity{
		Name:        "Updatable",
		Handle:      "updatable",
		Kind:        "human",
		Personality: "kind",
	}
	require.NoError(t, s.Save(id))

	// Update personality.
	require.NoError(t, s.Update("updatable", func(id *Identity) error {
		id.Personality = "stern"
		return nil
	}))

	loaded, err := s.Load("updatable", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "stern", loaded.Personality)
}

func TestStore_FindByGitHub(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.Save(&Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human", GitHub: "mal-github",
	}))
	id, err := s.FindBy("github", "mal-github")
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, "mal", id.Handle)
}

func TestStore_FindByEmail(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.Save(&Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human", Email: "mal@serenity.ship",
	}))
	id, err := s.FindBy("email", "mal@serenity.ship")
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, "mal", id.Handle)
}

func TestStore_FindByHandle(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.Save(&Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human",
	}))
	id, err := s.FindBy("handle", "mal")
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, "mal", id.Handle)
}

func TestStore_FindByNoMatch(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.Save(&Identity{
		Name: "Mal Reynolds", Handle: "mal", Kind: "human",
	}))
	id, err := s.FindBy("github", "nobody")
	require.NoError(t, err)
	assert.Nil(t, id)
}

func TestStore_FindByUnsupportedField(t *testing.T) {
	s := testStore(t)
	_, err := s.FindBy("name", "Mal")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestStore_FindByEmptyValue(t *testing.T) {
	s := testStore(t)
	id, err := s.FindBy("github", "")
	require.NoError(t, err)
	assert.Nil(t, id)
}

func TestStore_FilePermissions(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.Save(testIdentity()))

	info, err := os.Stat(s.Path("mal"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dirInfo, err := os.Stat(s.IdentitiesDir())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
}
