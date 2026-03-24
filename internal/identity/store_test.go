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

func TestStore_LoadStripsEmptyVoice(t *testing.T) {
	s := testStore(t)

	// Write a YAML file with an empty voice block directly.
	dir := s.IdentitiesDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "test.ext"), 0o700))
	data := []byte("name: Test\nhandle: test\nkind: human\nvoice: {}\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.yaml"), data, 0o600))

	loaded, err := s.Load("test")
	require.NoError(t, err)
	// Empty voice should not create ext/vox.
	_, ok := loaded.Ext["vox"]
	assert.False(t, ok, "empty voice should not create ext/vox")
	// Voice key should be stripped from YAML.
	raw, err := os.ReadFile(filepath.Join(dir, "test.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "voice")
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

func TestStore_VoiceViaExt(t *testing.T) {
	s := testStore(t)
	id := &Identity{
		Name:   "Test",
		Handle: "test",
		Kind:   "human",
	}
	require.NoError(t, s.Save(id))

	// Write voice data via ext system.
	require.NoError(t, s.ExtSet("test", "vox", "provider", "elevenlabs"))
	require.NoError(t, s.ExtSet("test", "vox", "voice_id", "abc123"))

	loaded, err := s.Load("test")
	require.NoError(t, err)
	vox, ok := loaded.Ext["vox"]
	require.True(t, ok)
	assert.Equal(t, "elevenlabs", vox["provider"])
	assert.Equal(t, "abc123", vox["voice_id"])
}

func TestStore_SaveWithAllFields(t *testing.T) {
	s := testStore(t)

	// Create attribute files that the identity references.
	createTestAttribute(t, s.Root(), attribute.WritingStyles, "terse", "# Terse\nDirect.")
	createTestAttribute(t, s.Root(), attribute.Personalities, "analytical", "# Analytical\nData-driven.")
	createTestAttribute(t, s.Root(), attribute.Talents, "go", "# Go\nSystems programming.")
	createTestAttribute(t, s.Root(), attribute.Talents, "testing", "# Testing\nTDD.")

	id := &Identity{
		Name:         "Full Identity",
		Handle:       "full",
		Kind:         "agent",
		Email:        "full@example.com",
		GitHub:       "fullgit",
		Agent:        ".claude/agents/full.md",
		WritingStyle: "terse",
		Personality:  "analytical",
		Talents:      []string{"go", "testing"},
	}
	require.NoError(t, s.Save(id))

	// Load with resolution — content fields should be populated.
	loaded, err := s.Load("full")
	require.NoError(t, err)
	assert.Equal(t, "terse", loaded.WritingStyle)
	assert.Equal(t, "analytical", loaded.Personality)
	assert.Equal(t, []string{"go", "testing"}, loaded.Talents)
	assert.Contains(t, loaded.PersonalityContent, "Analytical")
	assert.Contains(t, loaded.WritingStyleContent, "Terse")
	assert.Len(t, loaded.TalentContents, 2)
	assert.Contains(t, loaded.TalentContents[0], "Go")
	assert.Empty(t, loaded.Warnings)

	// Load with Reference — content fields should be empty.
	ref, err := s.Load("full", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "terse", ref.WritingStyle)
	assert.Empty(t, ref.WritingStyleContent)
	assert.Empty(t, ref.PersonalityContent)
	assert.Nil(t, ref.TalentContents)
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

func TestStore_LoadMigratesVoiceToExt(t *testing.T) {
	s := testStore(t)

	// Write a YAML file with a voice block directly (legacy format).
	dir := s.IdentitiesDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	// Create ext dir so loadExtensions works.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "test.ext"), 0o700))
	data := []byte("name: Test\nhandle: test\nkind: human\nvoice:\n  provider: elevenlabs\n  voice_id: abc123\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.yaml"), data, 0o600))

	// First load should auto-migrate voice to ext/vox.
	loaded, err := s.Load("test")
	require.NoError(t, err)

	// Voice field must no longer be on the struct.
	// (Struct has no Voice field after migration, so we verify via ext.)
	vox, ok := loaded.Ext["vox"]
	require.True(t, ok, "expected ext/vox namespace after migration")
	assert.Equal(t, "elevenlabs", vox["provider"])
	assert.Equal(t, "abc123", vox["voice_id"])

	// Re-read the YAML file to verify voice key was stripped.
	raw, err := os.ReadFile(filepath.Join(dir, "test.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "voice")

	// Second load should still have ext/vox data.
	loaded2, err := s.Load("test")
	require.NoError(t, err)
	vox2, ok := loaded2.Ext["vox"]
	require.True(t, ok)
	assert.Equal(t, "elevenlabs", vox2["provider"])
}

func TestStore_LoadMigratesVoicePartial(t *testing.T) {
	s := testStore(t)

	// Write YAML with voice block containing only provider (no voice_id).
	dir := s.IdentitiesDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "test.ext"), 0o700))
	data := []byte("name: Test\nhandle: test\nkind: human\nvoice:\n  provider: elevenlabs\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.yaml"), data, 0o600))

	loaded, err := s.Load("test")
	require.NoError(t, err)

	vox, ok := loaded.Ext["vox"]
	require.True(t, ok, "expected ext/vox after partial voice migration")
	assert.Equal(t, "elevenlabs", vox["provider"])
	_, hasVoiceID := vox["voice_id"]
	assert.False(t, hasVoiceID, "voice_id should not be present when not in source")

	// Voice key should be stripped from YAML.
	raw, err := os.ReadFile(filepath.Join(dir, "test.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "voice")
}

func TestStore_LoadMigratesVoiceNonMapErrors(t *testing.T) {
	s := testStore(t)

	// Write YAML with voice as a plain string (not a map).
	dir := s.IdentitiesDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "test.ext"), 0o700))
	data := []byte("name: Test\nhandle: test\nkind: human\nvoice: elevenlabs\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.yaml"), data, 0o600))

	_, err := s.Load("test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected type")
}

func TestStore_LoadMigratesEmptyVoiceNoOp(t *testing.T) {
	s := testStore(t)

	// Write a YAML file with an empty voice block.
	dir := s.IdentitiesDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "test.ext"), 0o700))
	data := []byte("name: Test\nhandle: test\nkind: human\nvoice: {}\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.yaml"), data, 0o600))

	loaded, err := s.Load("test")
	require.NoError(t, err)

	// Empty voice should not create a vox ext.
	_, ok := loaded.Ext["vox"]
	assert.False(t, ok, "empty voice should not create ext/vox")
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
