package role

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayeredStore_RepoOverridesGlobal(t *testing.T) {
	repoDir := t.TempDir()
	globalDir := t.TempDir()

	repo := NewStore(repoDir)
	global := NewStore(globalDir)

	// Save different roles to each layer.
	require.NoError(t, repo.Save(&Role{
		Name:             "coo",
		Responsibilities: []string{"repo version"},
	}))
	require.NoError(t, global.Save(&Role{
		Name:             "coo",
		Responsibilities: []string{"global version"},
	}))

	ls := NewLayeredStore(repoDir, globalDir)

	// Load should return repo version.
	r, err := ls.Load("coo")
	require.NoError(t, err)
	assert.Equal(t, []string{"repo version"}, r.Responsibilities)
}

func TestLayeredStore_FallsBackToGlobal(t *testing.T) {
	repoDir := t.TempDir()
	globalDir := t.TempDir()

	global := NewStore(globalDir)
	require.NoError(t, global.Save(&Role{
		Name:             "go-specialist",
		Responsibilities: []string{"Go code"},
	}))

	ls := NewLayeredStore(repoDir, globalDir)

	r, err := ls.Load("go-specialist")
	require.NoError(t, err)
	assert.Equal(t, []string{"Go code"}, r.Responsibilities)
}

func TestLayeredStore_ListMerges(t *testing.T) {
	repoDir := t.TempDir()
	globalDir := t.TempDir()

	repo := NewStore(repoDir)
	global := NewStore(globalDir)

	require.NoError(t, repo.Save(&Role{Name: "coo"}))
	require.NoError(t, global.Save(&Role{Name: "coo"}))      // duplicate
	require.NoError(t, global.Save(&Role{Name: "engineer"})) // global-only

	ls := NewLayeredStore(repoDir, globalDir)

	names, err := ls.List()
	require.NoError(t, err)
	assert.Len(t, names, 2)
	assert.Contains(t, names, "coo")
	assert.Contains(t, names, "engineer")
}

func TestLayeredStore_ExistsChecksRepo(t *testing.T) {
	repoDir := t.TempDir()
	globalDir := t.TempDir()

	repo := NewStore(repoDir)
	require.NoError(t, repo.Save(&Role{Name: "coo"}))

	ls := NewLayeredStore(repoDir, globalDir)
	assert.True(t, ls.Exists("coo"))
	assert.False(t, ls.Exists("nonexistent"))
}

func TestLayeredStore_EmptyRepoRoot(t *testing.T) {
	globalDir := t.TempDir()
	global := NewStore(globalDir)
	require.NoError(t, global.Save(&Role{Name: "coo"}))

	ls := NewLayeredStore("", globalDir)

	r, err := ls.Load("coo")
	require.NoError(t, err)
	assert.Equal(t, "coo", r.Name)
}

func TestLayeredStore_SaveGoesToGlobal(t *testing.T) {
	repoDir := t.TempDir()
	globalDir := t.TempDir()

	ls := NewLayeredStore(repoDir, globalDir)
	require.NoError(t, ls.Save(&Role{Name: "new-role"}))

	// Should be in global, not repo.
	global := NewStore(globalDir)
	assert.True(t, global.Exists("new-role"))

	repo := NewStore(repoDir)
	assert.False(t, repo.Exists("new-role"))
}

func TestLayeredStore_DeleteFromGlobal(t *testing.T) {
	repoDir := t.TempDir()
	globalDir := t.TempDir()

	global := NewStore(globalDir)
	require.NoError(t, global.Save(&Role{Name: "old-role"}))

	ls := NewLayeredStore(repoDir, globalDir)
	require.NoError(t, ls.Delete("old-role"))

	assert.False(t, global.Exists("old-role"))
}
