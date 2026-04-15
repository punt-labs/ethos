package team

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupThreeLayer(t *testing.T) (ls *LayeredStore, repo, bundle, global *Store) {
	t.Helper()
	repoRoot := t.TempDir()
	bundleRoot := t.TempDir()
	globalRoot := t.TempDir()
	repo = NewStore(repoRoot)
	bundle = NewStore(bundleRoot)
	global = NewStore(globalRoot)
	ls = NewLayeredStoreWithBundle(repoRoot, bundleRoot, globalRoot)
	return
}

func TestThreeLayer_RepoWins(t *testing.T) {
	ls, repo, bundle, _ := setupThreeLayer(t)

	require.NoError(t, repo.Save(&Team{
		Name: "foo", Members: []Member{{Identity: "repo-a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))
	require.NoError(t, bundle.Save(&Team{
		Name: "foo", Members: []Member{{Identity: "bundle-a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))

	got, err := ls.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, "repo-a", got.Members[0].Identity)
}

func TestThreeLayer_BundleWins(t *testing.T) {
	ls, _, bundle, global := setupThreeLayer(t)

	require.NoError(t, bundle.Save(&Team{
		Name: "foo", Members: []Member{{Identity: "bundle-a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))
	require.NoError(t, global.Save(&Team{
		Name: "foo", Members: []Member{{Identity: "global-a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))

	got, err := ls.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, "bundle-a", got.Members[0].Identity)
}

func TestThreeLayer_GlobalFallback(t *testing.T) {
	ls, _, _, global := setupThreeLayer(t)

	require.NoError(t, global.Save(&Team{
		Name: "foo", Members: []Member{{Identity: "global-a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))

	got, err := ls.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, "global-a", got.Members[0].Identity)
}

func TestThreeLayer_ListDedupes(t *testing.T) {
	ls, repo, bundle, global := setupThreeLayer(t)

	save := func(s *Store, name string) {
		require.NoError(t, s.Save(&Team{
			Name: name, Members: []Member{{Identity: "a", Role: "r"}},
		}, alwaysTrue, alwaysTrue))
	}
	save(repo, "shared")
	save(repo, "repo-only")
	save(bundle, "shared")
	save(bundle, "bundle-only")
	save(global, "shared")
	save(global, "global-only")

	names, err := ls.List()
	require.NoError(t, err)
	assert.Len(t, names, 4)
	assert.Contains(t, names, "shared")
	assert.Contains(t, names, "repo-only")
	assert.Contains(t, names, "bundle-only")
	assert.Contains(t, names, "global-only")
}

func TestThreeLayer_BundleReadOnly(t *testing.T) {
	ls, _, bundle, _ := setupThreeLayer(t)

	require.NoError(t, bundle.Save(&Team{
		Name: "foo", Members: []Member{{Identity: "a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))

	err := ls.AddMember("foo", Member{Identity: "b", Role: "r"}, alwaysTrue, alwaysTrue)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundle-only")

	err = ls.RemoveMember("foo", "a", "r")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundle-only")

	err = ls.AddCollaboration("foo", Collaboration{From: "a", To: "b", Type: "reports_to"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundle-only")
}

func TestThreeLayer_RepoStillTakesPrecedenceOverBundle(t *testing.T) {
	// checkNotRepoOnly must report "repo-tracked" when the team is in
	// both repo and bundle, not "bundle-only".
	ls, repo, bundle, _ := setupThreeLayer(t)

	require.NoError(t, repo.Save(&Team{
		Name: "foo", Members: []Member{{Identity: "a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))
	require.NoError(t, bundle.Save(&Team{
		Name: "foo", Members: []Member{{Identity: "b", Role: "r"}},
	}, alwaysTrue, alwaysTrue))

	err := ls.AddMember("foo", Member{Identity: "c", Role: "r"}, alwaysTrue, alwaysTrue)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo-tracked")
}

func TestThreeLayer_FindByRepoMergesAndDedupes(t *testing.T) {
	ls, repo, bundle, global := setupThreeLayer(t)

	require.NoError(t, repo.Save(&Team{
		Name: "shared", Repositories: []string{"pl/ethos"},
		Members: []Member{{Identity: "repo-a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))
	require.NoError(t, bundle.Save(&Team{
		Name: "shared", Repositories: []string{"pl/ethos"},
		Members: []Member{{Identity: "bundle-a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))
	require.NoError(t, bundle.Save(&Team{
		Name: "bundle-team", Repositories: []string{"pl/ethos"},
		Members: []Member{{Identity: "b", Role: "r"}},
	}, alwaysTrue, alwaysTrue))
	require.NoError(t, global.Save(&Team{
		Name: "global-team", Repositories: []string{"pl/ethos"},
		Members: []Member{{Identity: "g", Role: "r"}},
	}, alwaysTrue, alwaysTrue))

	got, err := ls.FindByRepo("pl/ethos")
	require.NoError(t, err)
	assert.Len(t, got, 3)

	byName := map[string]*Team{}
	for _, t := range got {
		byName[t.Name] = t
	}
	assert.Equal(t, "repo-a", byName["shared"].Members[0].Identity, "repo wins on shared name")
}

func TestThreeLayer_NoBundleMatchesLegacyBehavior(t *testing.T) {
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	repo := NewStore(repoRoot)
	global := NewStore(globalRoot)

	require.NoError(t, repo.Save(&Team{
		Name: "foo", Members: []Member{{Identity: "repo-a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))
	require.NoError(t, global.Save(&Team{
		Name: "foo", Members: []Member{{Identity: "global-a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))

	ls := NewLayeredStore(repoRoot, globalRoot)
	got, err := ls.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, "repo-a", got.Members[0].Identity)
}
