package attribute

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Under DES-057 repo-only the global layer leaves the read chain, and
// writes — which layered mode sends to global — move to the repo layer.
// The layered row is the regression guard for today's behavior.
func TestRepoOnly_ReadAndWriteLayers(t *testing.T) {
	tests := []struct {
		name              string
		repoAuthoritative bool
		wantLoad          string // content Load returns for the global-only slug
		wantWriteRoot     string // "repo" or "global"
	}{
		{name: "layered", wantLoad: "global\n", wantWriteRoot: "global"},
		{name: "repo-only", repoAuthoritative: true, wantWriteRoot: "repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, bundle, global := t.TempDir(), t.TempDir(), t.TempDir()
			writeLayer(t, global, "ghost", "global\n")

			s := NewLayeredStoreWithBundle(repo, bundle, global, Talents, tt.repoAuthoritative)

			a, err := s.Load("ghost")
			if tt.wantLoad == "" {
				require.Error(t, err)
				assert.False(t, s.Exists("ghost"))
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantLoad, a.Content)
			}

			require.NoError(t, s.Save(&Attribute{Slug: "fresh", Content: "new\n"}))
			roots := map[string]string{"repo": repo, "global": global}
			assert.True(t, NewStore(roots[tt.wantWriteRoot], Talents).Exists("fresh"),
				"write should land in the %s layer", tt.wantWriteRoot)
		})
	}
}

func TestRepoOnly_ReadsStillSeeRepoAndBundle(t *testing.T) {
	repo, bundle, global := t.TempDir(), t.TempDir(), t.TempDir()
	writeLayer(t, repo, "r", "repo\n")
	writeLayer(t, bundle, "b", "bundle\n")

	s := NewLayeredStoreWithBundle(repo, bundle, global, Talents, true)

	a, err := s.Load("r")
	require.NoError(t, err)
	assert.Equal(t, "repo\n", a.Content)

	a, err = s.Load("b")
	require.NoError(t, err)
	assert.Equal(t, "bundle\n", a.Content)
}

// Repo still wins over bundle: repo-only drops the global tail, it does
// not reorder the layers above it.
func TestRepoOnly_RepoStillWinsOverBundle(t *testing.T) {
	repo, bundle, global := t.TempDir(), t.TempDir(), t.TempDir()
	writeLayer(t, repo, "foo", "repo\n")
	writeLayer(t, bundle, "foo", "bundle\n")

	s := NewLayeredStoreWithBundle(repo, bundle, global, Talents, true)
	a, err := s.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, "repo\n", a.Content)
}

func TestRepoOnly_ListOmitsGlobal(t *testing.T) {
	repo, bundle, global := t.TempDir(), t.TempDir(), t.TempDir()
	writeLayer(t, repo, "r", "repo\n")
	writeLayer(t, global, "ghost", "global\n")

	s := NewLayeredStoreWithBundle(repo, bundle, global, Talents, true)
	result, err := s.List()
	require.NoError(t, err)

	var slugs []string
	for _, a := range result.Attributes {
		slugs = append(slugs, a.Slug)
	}
	assert.Equal(t, []string{"r"}, slugs)
}

// With content coming from a read-only bundle and no repo layer, there
// is no legal write target and the write must be refused rather than
// silently editing shared bundle content.
func TestRepoOnly_WriteRefusedWithoutRepoLayer(t *testing.T) {
	bundle, global := t.TempDir(), t.TempDir()
	s := NewLayeredStoreWithBundle("", bundle, global, Talents, true)

	err := s.Save(&Attribute{Slug: "fresh", Content: "new\n"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo-only")
	assert.False(t, NewStore(bundle, Talents).Exists("fresh"))
	assert.False(t, NewStore(global, Talents).Exists("fresh"))

	err = s.Delete("fresh")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "repo-only")
}

func TestRepoOnly_DeleteTargetsRepo(t *testing.T) {
	repo, bundle, global := t.TempDir(), t.TempDir(), t.TempDir()
	writeLayer(t, repo, "doomed", "repo\n")
	writeLayer(t, global, "doomed", "global\n")

	s := NewLayeredStoreWithBundle(repo, bundle, global, Talents, true)
	require.NoError(t, s.Delete("doomed"))

	assert.False(t, NewStore(repo, Talents).Exists("doomed"))
	assert.True(t, NewStore(global, Talents).Exists("doomed"), "global must be untouched")
}
