package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestStore_ActiveAndSetActive(t *testing.T) {
	s := testStore(t)
	require.NoError(t, s.Save(&Identity{Name: "Alice", Handle: "alice", Kind: "human"}))

	// No active identity initially.
	_, err := s.Active()
	require.Error(t, err)

	// Set active.
	require.NoError(t, s.SetActive("alice"))

	// Verify.
	active, err := s.Active()
	require.NoError(t, err)
	assert.Equal(t, "alice", active.Handle)
}

func TestStore_SetActiveNonexistent(t *testing.T) {
	s := testStore(t)
	err := s.SetActive("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
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
	id := &Identity{
		Name:         "Full Identity",
		Handle:       "full",
		Kind:         "agent",
		Email:        "full@example.com",
		GitHub:       "fullgit",
		Voice:        &Voice{Provider: "elevenlabs", VoiceID: "v1"},
		Agent:        ".claude/agents/full.md",
		WritingStyle: "Terse and direct.",
		Personality:  "Analytical.",
		Skills:       []string{"go", "testing"},
	}
	require.NoError(t, s.Save(id))

	loaded, err := s.Load("full")
	require.NoError(t, err)
	assert.Equal(t, id.Name, loaded.Name)
	assert.Equal(t, id.Email, loaded.Email)
	assert.Equal(t, id.GitHub, loaded.GitHub)
	assert.Equal(t, id.Agent, loaded.Agent)
	assert.Equal(t, id.WritingStyle, loaded.WritingStyle)
	assert.Equal(t, id.Personality, loaded.Personality)
	assert.Equal(t, id.Skills, loaded.Skills)
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
