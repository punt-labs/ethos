package identity

import (
	"errors"
	"os"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupRepoOnly builds the same three stores as setupThreeLayer but with
// the DES-057 repo-only policy set, so a test can hold the two modes
// side by side over identical fixtures.
func setupRepoOnly(t *testing.T) (ls *LayeredStore, repo, bundle, global *Store) {
	t.Helper()
	repo = NewStore(t.TempDir())
	bundle = NewStore(t.TempDir())
	global = NewStore(t.TempDir())
	ls = NewLayeredStoreWithBundle(repo, bundle, global, true)
	return
}

// Every read surface must stop before the global layer. The layered
// column is the regression guard: the same fixture, the same call, and
// the answer today's ethos gives.
func TestRepoOnly_GlobalIsInvisibleToEveryReadSurface(t *testing.T) {
	tests := []struct {
		name string
		// check runs one read surface and reports what it found for the
		// global-only handle "ghost".
		check func(t *testing.T, ls *LayeredStore) (found bool)
	}{
		{
			name: "Load",
			check: func(t *testing.T, ls *LayeredStore) bool {
				_, err := ls.Load("ghost", Reference(true))
				return err == nil
			},
		},
		{
			name: "Exists",
			check: func(t *testing.T, ls *LayeredStore) bool {
				return ls.Exists("ghost")
			},
		},
		{
			name: "FindBy",
			check: func(t *testing.T, ls *LayeredStore) bool {
				id, err := ls.FindBy("email", "ghost@example.com")
				require.NoError(t, err)
				return id != nil
			},
		},
		{
			name: "List",
			check: func(t *testing.T, ls *LayeredStore) bool {
				result, err := ls.List()
				require.NoError(t, err)
				for _, id := range result.Identities {
					if id.Handle == "ghost" {
						return true
					}
				}
				return false
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Run("layered still falls back", func(t *testing.T) {
				ls, _, _, global := setupThreeLayer(t)
				saveGhost(t, global)
				assert.True(t, tt.check(t, ls))
			})
			t.Run("repo-only does not", func(t *testing.T) {
				ls, _, _, global := setupRepoOnly(t)
				saveGhost(t, global)
				assert.False(t, tt.check(t, ls))
			})
		})
	}
}

func saveGhost(t *testing.T, s *Store) {
	t.Helper()
	require.NoError(t, s.Save(&Identity{
		Name: "Ghost", Handle: "ghost", Kind: "human", Email: "ghost@example.com",
	}))
}

func TestRepoOnly_ReadsStillSeeRepoAndBundle(t *testing.T) {
	ls, repo, bundle, _ := setupRepoOnly(t)
	require.NoError(t, repo.Save(&Identity{Name: "R", Handle: "r", Kind: "human"}))
	require.NoError(t, bundle.Save(&Identity{Name: "B", Handle: "b", Kind: "human"}))

	assert.True(t, ls.Exists("r"))
	assert.True(t, ls.Exists("b"))

	result, err := ls.List()
	require.NoError(t, err)
	assert.Len(t, result.Identities, 2)
}

// The miss must name where ethos looked and say which mode suppressed
// the fallback — a bare "not found" leaves the user unable to tell a
// typo from an incomplete vendored set.
func TestRepoOnly_MissNamesTheMode(t *testing.T) {
	ls, repo, _, global := setupRepoOnly(t)
	saveGhost(t, global)

	_, err := ls.Load("ghost", Reference(true))
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	assert.Contains(t, err.Error(), "repo-only")
	assert.Contains(t, err.Error(), repo.Root())
	assert.NotContains(t, err.Error(), global.Root())
}

// Attribute content must not resolve from global either. In layered mode
// the global copy supplies it; under repo-only the reference becomes a
// hard ErrIncompleteRepoSet instead of a warning the caller may never
// print.
func TestRepoOnly_AttributeChainDropsGlobal(t *testing.T) {
	for _, tt := range []struct {
		name              string
		repoAuthoritative bool
	}{
		{name: "layered resolves from global"},
		{name: "repo-only fails loud", repoAuthoritative: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, bundle, global := NewStore(t.TempDir()), NewStore(t.TempDir()), NewStore(t.TempDir())
			ls := NewLayeredStoreWithBundle(repo, bundle, global, tt.repoAuthoritative)

			createTestAttribute(t, global.Root(), attribute.Personalities, "analytical", "# Global Analytical\n")
			writeIdentityYAML(t, repo, "mal", "name: Mal\nhandle: mal\nkind: human\npersonality: analytical\n")

			id, err := ls.Load("mal")
			if !tt.repoAuthoritative {
				require.NoError(t, err)
				assert.Contains(t, id.PersonalityContent, "Global Analytical")
				assert.Empty(t, id.Warnings)
				return
			}

			var incomplete *ErrIncompleteRepoSet
			require.ErrorAs(t, err, &incomplete)
			assert.Equal(t, "mal", incomplete.Handle)
			require.Len(t, incomplete.Missing, 1)
			assert.Equal(t, "personalities/analytical", incomplete.Missing[0].String())
			assert.Contains(t, incomplete.Missing[0].Path, repo.Root())
		})
	}
}

// Every miss is reported at once, sorted, so a single `ethos vendor`
// round closes the whole gap.
func TestRepoOnly_IncompleteSetAggregatesEveryMiss(t *testing.T) {
	ls, repo, _, _ := setupRepoOnly(t)
	createTestAttribute(t, repo.Root(), attribute.Personalities, "analytical", "# P\n")
	writeIdentityYAML(t, repo, "mal",
		"name: Mal\nhandle: mal\nkind: human\npersonality: analytical\n"+
			"writing_style: terse\ntalents:\n  - formal-methods\n  - go\n")

	_, err := ls.Load("mal")

	var incomplete *ErrIncompleteRepoSet
	require.ErrorAs(t, err, &incomplete)

	var refs []string
	for _, m := range incomplete.Missing {
		refs = append(refs, m.String())
	}
	assert.Equal(t, []string{
		"talents/formal-methods", "talents/go", "writing-styles/terse",
	}, refs, "sorted by kind then slug, and the resolved personality is absent")
	assert.Contains(t, err.Error(), "ethos vendor mal")
}

// Reference mode does not read attribute content, so it has no misses to
// aggregate — a repo-only store must still answer `List`-style lookups
// for an identity whose attributes were not vendored.
func TestRepoOnly_ReferenceModeSkipsCompletenessCheck(t *testing.T) {
	ls, repo, _, _ := setupRepoOnly(t)
	writeIdentityYAML(t, repo, "mal", "name: Mal\nhandle: mal\nkind: human\npersonality: analytical\n")

	id, err := ls.Load("mal", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "analytical", id.Personality)
	assert.Empty(t, id.PersonalityContent)
}

// ValidateRefs consults the same chain as resolution. Accepting a
// reference the reader cannot resolve would let repo-only writes create
// identities that immediately fail to load.
func TestRepoOnly_ValidateRefsRejectsGlobalOnlyAttribute(t *testing.T) {
	ls, _, _, global := setupRepoOnly(t)
	createTestAttribute(t, global.Root(), attribute.Personalities, "analytical", "# G\n")

	err := ls.ValidateRefs(&Identity{
		Name: "Mal", Handle: "mal", Kind: "human", Personality: "analytical",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "analytical")
}

func TestRepoOnly_SaveTargetsRepo(t *testing.T) {
	ls, repo, _, global := setupRepoOnly(t)

	require.NoError(t, ls.Save(&Identity{Name: "New", Handle: "new", Kind: "human"}))
	assert.True(t, repo.Exists("new"))
	assert.False(t, global.Exists("new"))
}

// With identities coming from a read-only bundle and no repo layer,
// there is no legal write target. Writing to global would be invisible
// to every read — the write-then-invisible footgun.
func TestRepoOnly_SaveRefusedWithoutRepoLayer(t *testing.T) {
	bundle, global := NewStore(t.TempDir()), NewStore(t.TempDir())
	ls := NewLayeredStoreWithBundle(nil, bundle, global, true)

	err := ls.Save(&Identity{Name: "New", Handle: "new", Kind: "human"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo-only")
	assert.False(t, global.Exists("new"))
}

func TestRepoOnly_UpdateRefusesGlobalOnlyHandle(t *testing.T) {
	ls, _, _, global := setupRepoOnly(t)
	saveGhost(t, global)

	err := ls.Update("ghost", func(id *Identity) error {
		id.Email = "changed@example.com"
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo-only")

	g, err := global.Load("ghost", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "ghost@example.com", g.Email, "the global copy must be untouched")
}

func TestRepoOnly_UpdateStillWritesRepoLayer(t *testing.T) {
	ls, repo, _, _ := setupRepoOnly(t)
	require.NoError(t, repo.Save(&Identity{Name: "R", Handle: "r", Kind: "human"}))

	require.NoError(t, ls.Update("r", func(id *Identity) error {
		id.Email = "r@example.com"
		return nil
	}))

	got, err := repo.Load("r", Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "r@example.com", got.Email)
}

func TestRepoAuthoritativeAccessor(t *testing.T) {
	ls, _, _, _ := setupRepoOnly(t)
	assert.True(t, ls.RepoAuthoritative())

	layered, _, _, _ := setupThreeLayer(t)
	assert.False(t, layered.RepoAuthoritative())
}
