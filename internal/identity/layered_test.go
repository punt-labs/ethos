package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupLayered creates repo and global stores in temp dirs.
// Returns (layered, repoStore, globalStore).
func setupLayered(t *testing.T) (*LayeredStore, *Store, *Store) {
	t.Helper()
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	repo := NewStore(repoRoot)
	global := NewStore(globalRoot)
	ls := NewLayeredStore(repo, global)
	return ls, repo, global
}

// setupLayeredNoRepo creates a layered store with no repo layer.
func setupLayeredNoRepo(t *testing.T) (*LayeredStore, *Store) {
	t.Helper()
	globalRoot := t.TempDir()
	global := NewStore(globalRoot)
	ls := NewLayeredStore(nil, global)
	return ls, global
}

// writeIdentityYAML writes an identity YAML file directly, bypassing Save
// validation (useful when the identity references attributes that don't exist
// in the same store).
func writeIdentityYAML(t *testing.T, s *Store, handle, yaml string) {
	t.Helper()
	dir := s.IdentitiesDir()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, handle+".ext"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, handle+".yaml"), []byte(yaml), 0o600))
}

func TestLayered_LoadFromRepo(t *testing.T) {
	ls, repo, _ := setupLayered(t)

	require.NoError(t, repo.Save(&Identity{
		Name: "Repo Mal", Handle: "mal", Kind: "human",
	}))

	id, err := ls.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "Repo Mal", id.Name)
}

func TestLayered_LoadFallbackToGlobal(t *testing.T) {
	ls, _, global := setupLayered(t)

	require.NoError(t, global.Save(&Identity{
		Name: "Global Mal", Handle: "mal", Kind: "human",
	}))

	id, err := ls.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "Global Mal", id.Name)
}

func TestLayered_LoadRepoWins(t *testing.T) {
	ls, repo, global := setupLayered(t)

	require.NoError(t, repo.Save(&Identity{
		Name: "Repo Mal", Handle: "mal", Kind: "human",
	}))
	require.NoError(t, global.Save(&Identity{
		Name: "Global Mal", Handle: "mal", Kind: "human",
	}))

	id, err := ls.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "Repo Mal", id.Name)
}

func TestLayered_LoadRepoIdentityGlobalExt(t *testing.T) {
	ls, repo, global := setupLayered(t)

	require.NoError(t, repo.Save(&Identity{
		Name: "Repo Mal", Handle: "mal", Kind: "human",
	}))
	// Set ext on global store.
	require.NoError(t, global.Save(&Identity{
		Name: "Global Mal", Handle: "mal", Kind: "human",
	}))
	require.NoError(t, global.ExtSet("mal", "vox", "provider", "elevenlabs"))

	id, err := ls.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "Repo Mal", id.Name)
	// Ext should come from global.
	vox, ok := id.Ext["vox"]
	require.True(t, ok, "expected ext/vox from global store")
	assert.Equal(t, "elevenlabs", vox["provider"])
}

func TestLayered_ListMerge(t *testing.T) {
	ls, repo, global := setupLayered(t)

	require.NoError(t, repo.Save(&Identity{
		Name: "Repo Alice", Handle: "alice", Kind: "human",
	}))
	require.NoError(t, repo.Save(&Identity{
		Name: "Repo Bob", Handle: "bob", Kind: "agent",
	}))
	require.NoError(t, global.Save(&Identity{
		Name: "Global Alice", Handle: "alice", Kind: "human",
	}))
	require.NoError(t, global.Save(&Identity{
		Name: "Global Carol", Handle: "carol", Kind: "human",
	}))

	result, err := ls.List()
	require.NoError(t, err)
	assert.Len(t, result.Identities, 3) // alice (repo), bob (repo), carol (global)

	handles := make(map[string]string)
	for _, id := range result.Identities {
		handles[id.Handle] = id.Name
	}
	assert.Equal(t, "Repo Alice", handles["alice"], "repo should win on collision")
	assert.Equal(t, "Repo Bob", handles["bob"])
	assert.Equal(t, "Global Carol", handles["carol"])
}

func TestLayered_SaveToRepo(t *testing.T) {
	ls, repo, global := setupLayered(t)

	id := &Identity{Name: "New", Handle: "new", Kind: "human"}
	require.NoError(t, ls.Save(id))

	assert.True(t, repo.Exists("new"), "should be saved in repo store")
	assert.False(t, global.Exists("new"), "should not be in global store")
}

func TestLayered_SaveToGlobalWhenNoRepo(t *testing.T) {
	ls, global := setupLayeredNoRepo(t)

	id := &Identity{Name: "New", Handle: "new", Kind: "human"}
	require.NoError(t, ls.Save(id))

	assert.True(t, global.Exists("new"), "should be saved in global store")
}

func TestLayered_FindByRepoFirst(t *testing.T) {
	ls, repo, global := setupLayered(t)

	require.NoError(t, repo.Save(&Identity{
		Name: "Repo Mal", Handle: "mal", Kind: "human", Email: "mal@repo.ship",
	}))
	require.NoError(t, global.Save(&Identity{
		Name: "Global Mal", Handle: "mal", Kind: "human", Email: "mal@global.ship",
	}))

	id, err := ls.FindBy("email", "mal@repo.ship")
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, "Repo Mal", id.Name)
}

func TestLayered_FindByFallbackToGlobal(t *testing.T) {
	ls, _, global := setupLayered(t)

	require.NoError(t, global.Save(&Identity{
		Name: "Global Zoe", Handle: "zoe", Kind: "human", GitHub: "zoe-gh",
	}))

	id, err := ls.FindBy("github", "zoe-gh")
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, "Global Zoe", id.Name)
}

func TestLayered_UpdateOwningStore(t *testing.T) {
	ls, repo, global := setupLayered(t)

	require.NoError(t, repo.Save(&Identity{
		Name: "Repo Mal", Handle: "mal", Kind: "human",
	}))
	// Global also has mal — update should target repo.
	require.NoError(t, global.Save(&Identity{
		Name: "Global Mal", Handle: "mal", Kind: "human",
	}))

	require.NoError(t, ls.Update("mal", func(id *Identity) error {
		id.Email = "mal@updated.ship"
		return nil
	}))

	// Verify repo was updated.
	repoID, err := repo.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "mal@updated.ship", repoID.Email)

	// Verify global was not changed.
	globalID, err := global.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Empty(t, globalID.Email)
}

func TestLayered_UpdateFallbackToGlobal(t *testing.T) {
	ls, _, global := setupLayered(t)

	require.NoError(t, global.Save(&Identity{
		Name: "Global Mal", Handle: "mal", Kind: "human",
	}))

	require.NoError(t, ls.Update("mal", func(id *Identity) error {
		id.Email = "mal@updated.ship"
		return nil
	}))

	globalID, err := global.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "mal@updated.ship", globalID.Email)
}

func TestLayered_ExtAlwaysGlobal(t *testing.T) {
	ls, repo, global := setupLayered(t)

	// Identity must exist in global for ExtSet.
	require.NoError(t, global.Save(&Identity{
		Name: "Test", Handle: "test", Kind: "human",
	}))
	// Also in repo — ext should still go to global.
	require.NoError(t, repo.Save(&Identity{
		Name: "Test", Handle: "test", Kind: "human",
	}))

	require.NoError(t, ls.ExtSet("test", "vox", "provider", "elevenlabs"))

	// Verify it's in global, not repo.
	val, err := global.ExtGet("test", "vox", "provider")
	require.NoError(t, err)
	assert.Equal(t, "elevenlabs", val["provider"])

	// ExtGet via layered should return global's data.
	val2, err := ls.ExtGet("test", "vox", "provider")
	require.NoError(t, err)
	assert.Equal(t, "elevenlabs", val2["provider"])

	// ExtList via layered should return global's namespaces.
	ns, err := ls.ExtList("test")
	require.NoError(t, err)
	assert.Contains(t, ns, "vox")

	// ExtDel via layered should delete from global.
	require.NoError(t, ls.ExtDel("test", "vox", "provider"))
	_, err = global.ExtGet("test", "vox", "provider")
	require.Error(t, err)
}

func TestLayered_Exists(t *testing.T) {
	ls, repo, global := setupLayered(t)

	assert.False(t, ls.Exists("mal"))

	require.NoError(t, global.Save(&Identity{
		Name: "Global Mal", Handle: "mal", Kind: "human",
	}))
	assert.True(t, ls.Exists("mal"))

	require.NoError(t, repo.Save(&Identity{
		Name: "Repo Zoe", Handle: "zoe", Kind: "human",
	}))
	assert.True(t, ls.Exists("zoe"))
}

func TestLayered_AttributeResolutionAcrossLayers(t *testing.T) {
	ls, repo, global := setupLayered(t)

	// Create attribute in global only.
	createTestAttribute(t, global.Root(), attribute.Personalities, "analytical", "# Analytical\nData-driven.\n")
	createTestAttribute(t, global.Root(), attribute.Talents, "go", "# Go\nSystems programming.\n")

	// Write identity in repo that references global attributes.
	writeIdentityYAML(t, repo, "mal",
		"name: Mal\nhandle: mal\nkind: human\npersonality: analytical\ntalents:\n  - go\n")

	id, err := ls.Load("mal")
	require.NoError(t, err)
	assert.Contains(t, id.PersonalityContent, "Analytical")
	assert.Len(t, id.TalentContents, 1)
	assert.Contains(t, id.TalentContents[0], "Go")
	assert.Empty(t, id.Warnings, "no warnings when global resolves the attribute")
}

func TestLayered_AttributeResolutionRepoWins(t *testing.T) {
	ls, repo, global := setupLayered(t)

	// Create attribute in both repo and global with different content.
	createTestAttribute(t, repo.Root(), attribute.Personalities, "analytical", "# Repo Analytical\n")
	createTestAttribute(t, global.Root(), attribute.Personalities, "analytical", "# Global Analytical\n")

	writeIdentityYAML(t, repo, "mal",
		"name: Mal\nhandle: mal\nkind: human\npersonality: analytical\n")

	id, err := ls.Load("mal")
	require.NoError(t, err)
	assert.Contains(t, id.PersonalityContent, "Repo Analytical", "repo attribute should win")
}

func TestLayered_RootAndPaths(t *testing.T) {
	ls, repo, global := setupLayered(t)

	assert.Equal(t, repo.Root(), ls.Root())
	assert.Equal(t, repo.IdentitiesDir(), ls.IdentitiesDir())
	assert.Equal(t, repo.Path("mal"), ls.Path("mal"))
	// ExtDir always delegates to global.
	assert.Equal(t, global.ExtDir("mal"), ls.ExtDir("mal"))
}

func TestLayered_RootAndPathsNoRepo(t *testing.T) {
	ls, global := setupLayeredNoRepo(t)

	assert.Equal(t, global.Root(), ls.Root())
	assert.Equal(t, global.IdentitiesDir(), ls.IdentitiesDir())
	assert.Equal(t, global.Path("mal"), ls.Path("mal"))
	assert.Equal(t, global.ExtDir("mal"), ls.ExtDir("mal"))
}

func TestLayered_ValidateRefsChecksRepo(t *testing.T) {
	ls, repo, _ := setupLayered(t)

	createTestAttribute(t, repo.Root(), attribute.Personalities, "analytical", "# Analytical\n")

	id := &Identity{
		Name:        "Mal",
		Handle:      "mal",
		Kind:        "human",
		Personality: "analytical",
	}
	assert.NoError(t, ls.ValidateRefs(id))
}

func TestLayered_ValidateRefsFallsBackToGlobal(t *testing.T) {
	ls, _, global := setupLayered(t)

	createTestAttribute(t, global.Root(), attribute.Personalities, "analytical", "# Analytical\n")

	id := &Identity{
		Name:        "Mal",
		Handle:      "mal",
		Kind:        "human",
		Personality: "analytical",
	}
	assert.NoError(t, ls.ValidateRefs(id))
}

func TestLayered_ValidateRefsFailsWhenMissing(t *testing.T) {
	ls, _, _ := setupLayered(t)

	id := &Identity{
		Name:        "Mal",
		Handle:      "mal",
		Kind:        "human",
		Personality: "nonexistent",
	}
	assert.Error(t, ls.ValidateRefs(id))
}

func TestLayered_LoadNotFound(t *testing.T) {
	ls, _, _ := setupLayered(t)

	_, err := ls.Load("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestLayered_VoiceMigrationWritesToGlobal(t *testing.T) {
	ls, repo, global := setupLayered(t)

	// Global identity must exist for ExtSet to have a target ext dir.
	require.NoError(t, global.Save(&Identity{
		Name: "Mal", Handle: "mal", Kind: "human",
	}))

	// Write repo identity with legacy voice field (bypass Save to include voice).
	writeIdentityYAML(t, repo, "mal",
		"name: Mal\nhandle: mal\nkind: human\nvoice:\n  provider: elevenlabs\n  voice_id: abc123\n")

	id, err := ls.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "Mal", id.Name)

	// Vox ext should be in global, not repo.
	globalVox, err := global.ExtGet("mal", "vox", "provider")
	require.NoError(t, err)
	assert.Equal(t, "elevenlabs", globalVox["provider"])

	globalVoiceID, err := global.ExtGet("mal", "vox", "voice_id")
	require.NoError(t, err)
	assert.Equal(t, "abc123", globalVoiceID["voice_id"])

	// Repo ext dir should NOT have vox data.
	repoNS, _ := repo.ExtList("mal")
	assert.NotContains(t, repoNS, "vox", "vox ext must not exist in repo store")

	// Voice field should be stripped from repo YAML.
	reloaded, err := repo.loadNoMigrate("mal")
	require.NoError(t, err)
	assert.Equal(t, "Mal", reloaded.Name)
}

func TestLayered_UpdateCrossLayerValidation(t *testing.T) {
	ls, repo, global := setupLayered(t)

	// Attribute exists only in global.
	createTestAttribute(t, global.Root(), attribute.Personalities, "analytical", "# Analytical\n")

	// Identity in repo, no personality yet.
	require.NoError(t, repo.Save(&Identity{
		Name: "Mal", Handle: "mal", Kind: "human",
	}))

	// Update should succeed — cross-layer validation finds global attribute.
	err := ls.Update("mal", func(id *Identity) error {
		id.Personality = "analytical"
		return nil
	})
	require.NoError(t, err)

	// Verify the mutation stuck.
	updated, err := repo.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "analytical", updated.Personality)
}

func TestLayered_UpdateRejectsMissingRef(t *testing.T) {
	ls, repo, _ := setupLayered(t)

	require.NoError(t, repo.Save(&Identity{
		Name: "Mal", Handle: "mal", Kind: "human",
	}))

	// Update with a ref that doesn't exist in either layer — should fail.
	err := ls.Update("mal", func(id *Identity) error {
		id.Personality = "nonexistent"
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent")
}

func TestLayered_FindByPropagatesRepoError(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()

	repo := NewStore(repoRoot)
	global := NewStore(globalRoot)
	ls := NewLayeredStore(repo, global)

	require.NoError(t, global.Save(&Identity{
		Name: "Zoe", Handle: "zoe", Kind: "human", Email: "zoe@serenity.ship",
	}))

	// Make repo identities dir unreadable to force I/O error.
	idDir := repo.IdentitiesDir()
	require.NoError(t, os.MkdirAll(idDir, 0o700))
	require.NoError(t, os.Chmod(idDir, 0o000))
	t.Cleanup(func() { os.Chmod(idDir, 0o700) })

	// Repo I/O errors are propagated, not silently swallowed.
	_, err := ls.FindBy("email", "zoe@serenity.ship")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}
