package team

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupRepoOnly is setupThreeLayer with the DES-057 repo-only policy set.
func setupRepoOnly(t *testing.T) (ls *LayeredStore, repo, bundle, global *Store) {
	t.Helper()
	repoRoot, bundleRoot, globalRoot := t.TempDir(), t.TempDir(), t.TempDir()
	repo, bundle, global = NewStore(repoRoot), NewStore(bundleRoot), NewStore(globalRoot)
	ls = NewLayeredStoreWithBundle(repoRoot, bundleRoot, globalRoot, true)
	return
}

func ghostTeam() *Team {
	return &Team{
		Name:         "ghost",
		Repositories: []string{"punt-labs/ethos"},
		Members:      []Member{{Identity: "claudia", Role: "writer"}},
	}
}

func TestRepoOnly_GlobalTeamIsInvisible(t *testing.T) {
	for _, tt := range []struct {
		name              string
		repoAuthoritative bool
		wantFound         bool
	}{
		{name: "layered falls back to global", wantFound: true},
		{name: "repo-only does not", repoAuthoritative: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repoRoot, bundleRoot, globalRoot := t.TempDir(), t.TempDir(), t.TempDir()
			require.NoError(t, NewStore(globalRoot).Save(ghostTeam(), alwaysTrue, alwaysTrue))
			ls := NewLayeredStoreWithBundle(repoRoot, bundleRoot, globalRoot, tt.repoAuthoritative)

			_, err := ls.Load("ghost")
			assert.Equal(t, tt.wantFound, err == nil)
			assert.Equal(t, tt.wantFound, ls.Exists("ghost"))

			names, listErr := ls.List()
			require.NoError(t, listErr)
			assert.Equal(t, tt.wantFound, len(names) == 1)

			found, repoErr := ls.FindByRepo("punt-labs/ethos")
			require.NoError(t, repoErr)
			assert.Equal(t, tt.wantFound, len(found) == 1)
		})
	}
}

func TestRepoOnly_ReadsStillSeeRepoAndBundle(t *testing.T) {
	ls, repo, bundle, _ := setupRepoOnly(t)
	require.NoError(t, repo.Save(&Team{
		Name: "r", Members: []Member{{Identity: "a", Role: "x"}},
	}, alwaysTrue, alwaysTrue))
	require.NoError(t, bundle.Save(&Team{
		Name: "b", Members: []Member{{Identity: "a", Role: "x"}},
	}, alwaysTrue, alwaysTrue))

	assert.True(t, ls.Exists("r"))
	assert.True(t, ls.Exists("b"))
}

func TestRepoOnly_SaveAndDeleteTargetRepo(t *testing.T) {
	ls, repo, _, global := setupRepoOnly(t)

	require.NoError(t, ls.Save(&Team{
		Name: "fresh", Members: []Member{{Identity: "a", Role: "x"}},
	}, alwaysTrue, alwaysTrue))
	assert.True(t, repo.Exists("fresh"))
	assert.False(t, global.Exists("fresh"))

	require.NoError(t, ls.Delete("fresh"))
	assert.False(t, repo.Exists("fresh"))
}

func TestRepoOnly_WriteRefusedWithoutRepoLayer(t *testing.T) {
	bundleRoot, globalRoot := t.TempDir(), t.TempDir()
	ls := NewLayeredStoreWithBundle("", bundleRoot, globalRoot, true)

	err := ls.Save(&Team{
		Name: "fresh", Members: []Member{{Identity: "a", Role: "x"}},
	}, alwaysTrue, alwaysTrue)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo-only")
	assert.False(t, NewStore(globalRoot).Exists("fresh"))
}
