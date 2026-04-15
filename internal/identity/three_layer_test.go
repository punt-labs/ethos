package identity

import (
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupThreeLayer creates repo, bundle, and global identity stores in
// temp dirs and returns a three-layer LayeredStore.
func setupThreeLayer(t *testing.T) (ls *LayeredStore, repo, bundle, global *Store) {
	t.Helper()
	repo = NewStore(t.TempDir())
	bundle = NewStore(t.TempDir())
	global = NewStore(t.TempDir())
	ls = NewLayeredStoreWithBundle(repo, bundle, global)
	return
}

func TestThreeLayer_RepoWins(t *testing.T) {
	ls, repo, bundle, _ := setupThreeLayer(t)

	require.NoError(t, repo.Save(&Identity{Name: "Repo Mal", Handle: "mal", Kind: "human"}))
	require.NoError(t, bundle.Save(&Identity{Name: "Bundle Mal", Handle: "mal", Kind: "human"}))

	id, err := ls.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "Repo Mal", id.Name)
}

func TestThreeLayer_BundleWins(t *testing.T) {
	ls, _, bundle, global := setupThreeLayer(t)

	require.NoError(t, bundle.Save(&Identity{Name: "Bundle Mal", Handle: "mal", Kind: "human"}))
	require.NoError(t, global.Save(&Identity{Name: "Global Mal", Handle: "mal", Kind: "human"}))

	id, err := ls.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "Bundle Mal", id.Name)
}

func TestThreeLayer_GlobalFallback(t *testing.T) {
	ls, _, _, global := setupThreeLayer(t)

	require.NoError(t, global.Save(&Identity{Name: "Global Mal", Handle: "mal", Kind: "human"}))

	id, err := ls.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "Global Mal", id.Name)
}

func TestThreeLayer_ListDedupes(t *testing.T) {
	ls, repo, bundle, global := setupThreeLayer(t)

	require.NoError(t, repo.Save(&Identity{Name: "Repo Shared", Handle: "shared", Kind: "human"}))
	require.NoError(t, repo.Save(&Identity{Name: "Repo Only", Handle: "repo-only", Kind: "human"}))
	require.NoError(t, bundle.Save(&Identity{Name: "Bundle Shared", Handle: "shared", Kind: "human"}))
	require.NoError(t, bundle.Save(&Identity{Name: "Bundle Only", Handle: "bundle-only", Kind: "human"}))
	require.NoError(t, global.Save(&Identity{Name: "Global Shared", Handle: "shared", Kind: "human"}))
	require.NoError(t, global.Save(&Identity{Name: "Global Only", Handle: "global-only", Kind: "human"}))

	result, err := ls.List()
	require.NoError(t, err)
	assert.Len(t, result.Identities, 4)

	names := map[string]string{}
	for _, id := range result.Identities {
		names[id.Handle] = id.Name
	}
	assert.Equal(t, "Repo Shared", names["shared"], "repo wins on collision")
	assert.Equal(t, "Repo Only", names["repo-only"])
	assert.Equal(t, "Bundle Only", names["bundle-only"])
	assert.Equal(t, "Global Only", names["global-only"])
}

func TestThreeLayer_ExistsAcrossLayers(t *testing.T) {
	ls, repo, bundle, global := setupThreeLayer(t)

	require.NoError(t, repo.Save(&Identity{Name: "R", Handle: "r", Kind: "human"}))
	require.NoError(t, bundle.Save(&Identity{Name: "B", Handle: "b", Kind: "human"}))
	require.NoError(t, global.Save(&Identity{Name: "G", Handle: "g", Kind: "human"}))

	assert.True(t, ls.Exists("r"))
	assert.True(t, ls.Exists("b"))
	assert.True(t, ls.Exists("g"))
	assert.False(t, ls.Exists("nope"))
}

func TestThreeLayer_SaveTargetsWritableLayer(t *testing.T) {
	ls, repo, bundle, global := setupThreeLayer(t)

	require.NoError(t, ls.Save(&Identity{Name: "New", Handle: "new", Kind: "human"}))

	// Repo is the writable primary when repo is non-nil.
	assert.True(t, repo.Exists("new"))
	assert.False(t, bundle.Exists("new"))
	assert.False(t, global.Exists("new"))
}

func TestThreeLayer_BundleIdentityReadOnly(t *testing.T) {
	ls, _, bundle, _ := setupThreeLayer(t)

	require.NoError(t, bundle.Save(&Identity{Name: "B", Handle: "b", Kind: "human"}))

	// Update on a bundle-only handle must fail.
	err := ls.Update("b", func(id *Identity) error {
		id.Email = "b@example.com"
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundle-only")
}

func TestThreeLayer_AttributeResolutionBundleLayer(t *testing.T) {
	ls, _, bundle, _ := setupThreeLayer(t)

	// Attribute lives in bundle only.
	createTestAttribute(t, bundle.Root(), attribute.Personalities, "analytical", "# Bundle Analytical\n")

	// Identity lives in bundle, references the attribute.
	writeIdentityYAML(t, bundle, "mal",
		"name: Mal\nhandle: mal\nkind: human\npersonality: analytical\n")

	id, err := ls.Load("mal")
	require.NoError(t, err)
	assert.Contains(t, id.PersonalityContent, "Bundle Analytical")
}

func TestThreeLayer_RepoIdentityResolvesBundleAttribute(t *testing.T) {
	ls, repo, bundle, _ := setupThreeLayer(t)

	// Attribute in bundle only.
	createTestAttribute(t, bundle.Root(), attribute.Personalities, "analytical", "# Bundle Analytical\n")

	// Identity in repo references the bundle-layer attribute.
	writeIdentityYAML(t, repo, "mal",
		"name: Mal\nhandle: mal\nkind: human\npersonality: analytical\n")

	id, err := ls.Load("mal")
	require.NoError(t, err)
	assert.Contains(t, id.PersonalityContent, "Bundle Analytical")
	assert.Empty(t, id.Warnings, "attribute resolved cleanly across layers")
}

func TestThreeLayer_ValidateRefsAcceptsBundleAttr(t *testing.T) {
	ls, _, bundle, _ := setupThreeLayer(t)

	createTestAttribute(t, bundle.Root(), attribute.Personalities, "analytical", "# B\n")

	id := &Identity{
		Name: "Mal", Handle: "mal", Kind: "human",
		Personality: "analytical",
	}
	assert.NoError(t, ls.ValidateRefs(id))
}

func TestThreeLayer_NoBundleMatchesLegacyBehavior(t *testing.T) {
	// NewLayeredStore (2-layer wrapper) must behave exactly as the
	// original two-layer store.
	repo := NewStore(t.TempDir())
	global := NewStore(t.TempDir())

	require.NoError(t, repo.Save(&Identity{Name: "Repo", Handle: "mal", Kind: "human"}))
	require.NoError(t, global.Save(&Identity{Name: "Global", Handle: "mal", Kind: "human"}))

	ls := NewLayeredStore(repo, global)
	id, err := ls.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "Repo", id.Name)
	assert.Equal(t, "", ls.BundleRoot())
}

func TestThreeLayer_FindByChecksBundle(t *testing.T) {
	ls, _, bundle, _ := setupThreeLayer(t)

	require.NoError(t, bundle.Save(&Identity{
		Name: "Bundle Zoe", Handle: "zoe", Kind: "human", Email: "zoe@bundle.ship",
	}))

	id, err := ls.FindBy("email", "zoe@bundle.ship")
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, "Bundle Zoe", id.Name)
}
