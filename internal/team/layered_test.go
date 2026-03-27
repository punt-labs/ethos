package team

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLayeredStore(t *testing.T) (repo, global *Store, ls *LayeredStore) {
	t.Helper()
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	repo = NewStore(repoRoot)
	global = NewStore(globalRoot)
	ls = NewLayeredStore(repoRoot, globalRoot)
	return repo, global, ls
}

func TestLayeredStore_Load_UsesErrNotFound(t *testing.T) {
	_, global, ls := testLayeredStore(t)

	// Team only in global layer.
	require.NoError(t, global.Save(&Team{
		Name:    "ops",
		Members: []Member{{Identity: "a", Role: "r"}},
	}, alwaysTrue, alwaysTrue))

	got, err := ls.Load("ops")
	require.NoError(t, err)
	assert.Equal(t, "ops", got.Name)

	// Team not in either layer.
	_, err = ls.Load("nonexistent")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound))
}

func TestLayeredStore_Load_RepoWins(t *testing.T) {
	repo, global, ls := testLayeredStore(t)

	// Same name in both layers, repo version should win.
	require.NoError(t, repo.Save(&Team{
		Name:         "eng",
		Repositories: []string{"punt-labs/ethos"},
		Members:      []Member{{Identity: "repo-alice", Role: "dev"}},
	}, alwaysTrue, alwaysTrue))
	require.NoError(t, global.Save(&Team{
		Name:         "eng",
		Repositories: []string{"punt-labs/ethos"},
		Members:      []Member{{Identity: "global-alice", Role: "dev"}},
	}, alwaysTrue, alwaysTrue))

	got, err := ls.Load("eng")
	require.NoError(t, err)
	assert.Equal(t, "repo-alice", got.Members[0].Identity)
}

func TestLayeredStore_FindByRepo(t *testing.T) {
	tests := []struct {
		name      string
		repoTeams []*Team
		globalTeams []*Team
		repo      string
		wantNames []string
		noRepo    bool // if true, create layered store with no repo layer
	}{
		{
			name: "same team in both layers, repo wins",
			repoTeams: []*Team{
				{Name: "eng", Repositories: []string{"punt-labs/ethos"}, Members: []Member{{Identity: "repo-a", Role: "r"}}},
			},
			globalTeams: []*Team{
				{Name: "eng", Repositories: []string{"punt-labs/ethos"}, Members: []Member{{Identity: "global-a", Role: "r"}}},
			},
			repo:      "punt-labs/ethos",
			wantNames: []string{"eng"},
		},
		{
			name: "team only in repo layer",
			repoTeams: []*Team{
				{Name: "eng", Repositories: []string{"punt-labs/ethos"}, Members: []Member{{Identity: "a", Role: "r"}}},
			},
			globalTeams: nil,
			repo:        "punt-labs/ethos",
			wantNames:   []string{"eng"},
		},
		{
			name:      "team only in global layer",
			repoTeams: nil,
			globalTeams: []*Team{
				{Name: "ops", Repositories: []string{"punt-labs/ethos"}, Members: []Member{{Identity: "b", Role: "r"}}},
			},
			repo:      "punt-labs/ethos",
			wantNames: []string{"ops"},
		},
		{
			name: "no match",
			repoTeams: []*Team{
				{Name: "eng", Repositories: []string{"punt-labs/ethos"}, Members: []Member{{Identity: "a", Role: "r"}}},
			},
			globalTeams: []*Team{
				{Name: "ops", Repositories: []string{"punt-labs/infra"}, Members: []Member{{Identity: "b", Role: "r"}}},
			},
			repo:      "punt-labs/other",
			wantNames: nil,
		},
		{
			name:   "no repo store, only global results",
			noRepo: true,
			globalTeams: []*Team{
				{Name: "ops", Repositories: []string{"punt-labs/ethos"}, Members: []Member{{Identity: "b", Role: "r"}}},
			},
			repo:      "punt-labs/ethos",
			wantNames: []string{"ops"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ls *LayeredStore
			if tt.noRepo {
				globalRoot := t.TempDir()
				global := NewStore(globalRoot)
				for _, team := range tt.globalTeams {
					require.NoError(t, global.Save(team, alwaysTrue, alwaysTrue))
				}
				ls = NewLayeredStore("", globalRoot)
			} else {
				repo, global, layered := testLayeredStore(t)
				ls = layered
				for _, team := range tt.repoTeams {
					require.NoError(t, repo.Save(team, alwaysTrue, alwaysTrue))
				}
				for _, team := range tt.globalTeams {
					require.NoError(t, global.Save(team, alwaysTrue, alwaysTrue))
				}
			}

			got, err := ls.FindByRepo(tt.repo)
			require.NoError(t, err)
			require.NotNil(t, got, "FindByRepo must return non-nil slice")

			var gotNames []string
			for _, team := range got {
				gotNames = append(gotNames, team.Name)
			}
			if tt.wantNames == nil {
				assert.Empty(t, got)
			} else {
				assert.ElementsMatch(t, tt.wantNames, gotNames)
			}

			// When dedup applies, verify repo version wins.
			if tt.name == "same team in both layers, repo wins" {
				assert.Equal(t, "repo-a", got[0].Members[0].Identity)
			}
		})
	}
}
