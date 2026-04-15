package role

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupThreeLayer creates repo, bundle, and global role stores in temp dirs
// and returns a three-layer LayeredStore wired to them.
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

	require.NoError(t, repo.Save(&Role{Name: "foo", Responsibilities: []string{"repo"}}))
	require.NoError(t, bundle.Save(&Role{Name: "foo", Responsibilities: []string{"bundle"}}))

	r, err := ls.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, []string{"repo"}, r.Responsibilities)
}

func TestThreeLayer_BundleWins(t *testing.T) {
	ls, _, bundle, global := setupThreeLayer(t)

	require.NoError(t, bundle.Save(&Role{Name: "foo", Responsibilities: []string{"bundle"}}))
	require.NoError(t, global.Save(&Role{Name: "foo", Responsibilities: []string{"global"}}))

	r, err := ls.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, []string{"bundle"}, r.Responsibilities)
}

func TestThreeLayer_GlobalFallback(t *testing.T) {
	ls, _, _, global := setupThreeLayer(t)

	require.NoError(t, global.Save(&Role{Name: "foo", Responsibilities: []string{"global"}}))

	r, err := ls.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, []string{"global"}, r.Responsibilities)
}

func TestThreeLayer_ListDedupes(t *testing.T) {
	ls, repo, bundle, global := setupThreeLayer(t)

	require.NoError(t, repo.Save(&Role{Name: "shared"}))
	require.NoError(t, repo.Save(&Role{Name: "repo-only"}))
	require.NoError(t, bundle.Save(&Role{Name: "shared"}))
	require.NoError(t, bundle.Save(&Role{Name: "bundle-only"}))
	require.NoError(t, global.Save(&Role{Name: "shared"}))
	require.NoError(t, global.Save(&Role{Name: "global-only"}))

	names, err := ls.List()
	require.NoError(t, err)
	assert.Len(t, names, 4)
	assert.Contains(t, names, "shared")
	assert.Contains(t, names, "repo-only")
	assert.Contains(t, names, "bundle-only")
	assert.Contains(t, names, "global-only")

	// No duplicates.
	counts := map[string]int{}
	for _, n := range names {
		counts[n]++
	}
	for n, c := range counts {
		assert.Equal(t, 1, c, "%q appeared %d times", n, c)
	}
}

func TestThreeLayer_ExistsAcrossLayers(t *testing.T) {
	ls, repo, bundle, global := setupThreeLayer(t)

	require.NoError(t, repo.Save(&Role{Name: "r"}))
	require.NoError(t, bundle.Save(&Role{Name: "b"}))
	require.NoError(t, global.Save(&Role{Name: "g"}))

	assert.True(t, ls.Exists("r"))
	assert.True(t, ls.Exists("b"))
	assert.True(t, ls.Exists("g"))
	assert.False(t, ls.Exists("nope"))
}

func TestThreeLayer_SaveTargetsGlobalOnly(t *testing.T) {
	ls, repo, bundle, global := setupThreeLayer(t)

	require.NoError(t, ls.Save(&Role{Name: "new"}))

	assert.True(t, global.Exists("new"))
	assert.False(t, repo.Exists("new"))
	assert.False(t, bundle.Exists("new"))

	// Verify the bundle directory on disk contains no new files.
	entries, err := os.ReadDir(filepath.Join(bundle.root, "roles"))
	if err == nil {
		assert.Empty(t, entries, "bundle roles dir should be untouched")
	}
}

// TestThreeLayer_BundleIOErrorPropagates verifies that an I/O failure
// in the bundle layer (e.g. permission denied on the role file) is
// surfaced rather than masked by silently falling through to global.
func TestThreeLayer_BundleIOErrorPropagates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission tests do not apply to root")
	}
	ls, _, bundle, global := setupThreeLayer(t)

	require.NoError(t, bundle.Save(&Role{Name: "foo", Responsibilities: []string{"bundle"}}))
	require.NoError(t, global.Save(&Role{Name: "foo", Responsibilities: []string{"global"}}))

	// Make the bundle role file unreadable.
	p := filepath.Join(bundle.root, "roles", "foo.yaml")
	require.NoError(t, os.Chmod(p, 0o000))
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })

	_, err := ls.Load("foo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundle role layer")
	// Must not have silently resolved to the global copy.
	assert.NotContains(t, err.Error(), "global")
}

func TestThreeLayer_NoBundleMatchesLegacyBehavior(t *testing.T) {
	// NewLayeredStore (2-layer wrapper) must behave exactly as before.
	repoRoot := t.TempDir()
	globalRoot := t.TempDir()
	repo := NewStore(repoRoot)
	global := NewStore(globalRoot)

	require.NoError(t, repo.Save(&Role{Name: "foo", Responsibilities: []string{"repo"}}))
	require.NoError(t, global.Save(&Role{Name: "foo", Responsibilities: []string{"global"}}))

	ls := NewLayeredStore(repoRoot, globalRoot)
	r, err := ls.Load("foo")
	require.NoError(t, err)
	assert.Equal(t, []string{"repo"}, r.Responsibilities)
}
