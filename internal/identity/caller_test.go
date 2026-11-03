package identity

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func saveHuman(t *testing.T, s *Store, handle string) {
	t.Helper()
	require.NoError(t, s.Save(&Identity{Name: handle, Handle: handle, Kind: "human", Email: handle + "@example.com"}))
}

// Under repo-only the caller is looked up in the repo, then in global; the rest
// of the store still stops at the repo.
func TestForCaller_RepoOnlyFallsBackToGlobalForTheCaller(t *testing.T) {
	ls, repo, _, global := setupRepoOnly(t)
	saveHuman(t, repo, "impl")
	saveHuman(t, global, "claude")
	cs := ForCaller(ls)

	id, err := cs.FindBy("email", "claude@example.com")
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, "claude", id.Handle)

	got, fromGlobal, err := cs.(*CallerStore).LoadCaller("claude", Reference(true))
	require.NoError(t, err)
	assert.True(t, fromGlobal)
	assert.Equal(t, "claude", got.Handle)

	got, fromGlobal, err = cs.(*CallerStore).LoadCaller("impl", Reference(true))
	require.NoError(t, err)
	assert.False(t, fromGlobal, "a repo identity is not reported as global")
	assert.Equal(t, "impl", got.Handle)

	// The store the team and agents resolve through is unchanged.
	assert.False(t, ls.Exists("claude"))
	_, err = ls.Load("claude", Reference(true))
	assert.Error(t, err)
}

func TestForCaller_InNeitherStoreIsAnError(t *testing.T) {
	ls, repo, _, _ := setupRepoOnly(t)
	saveHuman(t, repo, "impl")
	cs := ForCaller(ls)

	id, err := cs.FindBy("handle", "ghost")
	require.NoError(t, err)
	assert.Nil(t, id)
	_, err = cs.Load("ghost", Reference(true))
	assert.Error(t, err)
}

// A layered store already reads global, so it is returned as it is.
func TestForCaller_LayeredStoreIsUnchanged(t *testing.T) {
	ls := NewLayeredStoreWithBundle(NewStore(t.TempDir()), nil, NewStore(t.TempDir()), false)
	assert.Same(t, ls, ForCaller(ls))
}

// A damaged repo identity file may be the one sought: FindBy reports it rather
// than answering from the global store.
func TestForCaller_DamagedRepoFileBlocksTheGlobalFallback(t *testing.T) {
	ls, repo, _, global := setupRepoOnly(t)
	saveHuman(t, repo, "impl")
	saveHuman(t, global, "claude")
	require.NoError(t, os.WriteFile(repo.Path("broken"), []byte("handle: [unclosed\n"), 0o600))

	_, err := ForCaller(ls).FindBy("email", "claude@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken")
}

// A global store that fails for any reason but a miss is reported, not read as
// "not found".
func TestForCaller_GlobalLoadFailureIsReported(t *testing.T) {
	ls, repo, _, global := setupRepoOnly(t)
	saveHuman(t, repo, "impl")
	require.NoError(t, os.MkdirAll(global.IdentitiesDir(), 0o700))
	require.NoError(t, os.WriteFile(global.Path("claude"), []byte("handle: [unclosed\n"), 0o600))

	_, _, err := ForCaller(ls).(*CallerStore).LoadCaller("claude", Reference(true))
	require.Error(t, err)
	assert.False(t, errors.Is(err, os.ErrNotExist), "a damaged global file is not a miss: %v", err)
	assert.Contains(t, err.Error(), "global store")
}
