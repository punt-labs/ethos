package identity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DES-057's DES-044 carve-out. In layered mode extensions always resolve
// from global. Under repo-only global is never read, so ext must come
// from the identity's own source layer — the one `ethos vendor` copied
// the .ext/ directory into. Without this, vendor writes ext into the
// repo and resolution never looks at it, silently dropping agent memory
// wiring on a global-less checkout.
func TestRepoOnly_ExtResolvesFromSourceLayer(t *testing.T) {
	for _, tt := range []struct {
		name              string
		repoAuthoritative bool
		want              string
	}{
		{name: "layered reads global", want: "global-collection"},
		{name: "repo-only reads the repo layer", repoAuthoritative: true, want: "repo-collection"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, bundle, global := NewStore(t.TempDir()), NewStore(t.TempDir()), NewStore(t.TempDir())
			ls := NewLayeredStoreWithBundle(repo, bundle, global, tt.repoAuthoritative)

			require.NoError(t, repo.Save(&Identity{Name: "Mal", Handle: "mal", Kind: "human"}))
			require.NoError(t, global.Save(&Identity{Name: "Mal", Handle: "mal", Kind: "human"}))
			require.NoError(t, repo.ExtSet("mal", "quarry", "memory_collection", "repo-collection"))
			require.NoError(t, global.ExtSet("mal", "quarry", "memory_collection", "global-collection"))

			id, err := ls.Load("mal")
			require.NoError(t, err)
			assert.Equal(t, tt.want, id.Ext["quarry"]["memory_collection"])

			m, err := ls.ExtGet("mal", "quarry", "memory_collection")
			require.NoError(t, err)
			assert.Equal(t, tt.want, m["memory_collection"])

			ns, err := ls.ExtList("mal")
			require.NoError(t, err)
			assert.Equal(t, []string{"quarry"}, ns)
		})
	}
}

// A bundle-sourced identity reads its ext from the bundle.
func TestRepoOnly_ExtResolvesFromBundleForBundleIdentity(t *testing.T) {
	repo, bundle, global := NewStore(t.TempDir()), NewStore(t.TempDir()), NewStore(t.TempDir())
	ls := NewLayeredStoreWithBundle(repo, bundle, global, true)

	require.NoError(t, bundle.Save(&Identity{Name: "Claudia", Handle: "claudia", Kind: "agent"}))
	require.NoError(t, bundle.ExtSet("claudia", "quarry", "memory_collection", "bundle-collection"))

	id, err := ls.Load("claudia")
	require.NoError(t, err)
	assert.Equal(t, "bundle-collection", id.Ext["quarry"]["memory_collection"])
}

// Ext writes follow the same read-only bundle rule as identity updates:
// writing to global would be invisible under repo-only, and writing into
// the bundle would edit shared content the repo does not own.
func TestRepoOnly_ExtWriteToBundleIdentityRefused(t *testing.T) {
	repo, bundle, global := NewStore(t.TempDir()), NewStore(t.TempDir()), NewStore(t.TempDir())
	ls := NewLayeredStoreWithBundle(repo, bundle, global, true)

	require.NoError(t, bundle.Save(&Identity{Name: "Claudia", Handle: "claudia", Kind: "agent"}))

	err := ls.ExtSet("claudia", "quarry", "memory_collection", "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "active bundle")

	ns, listErr := bundle.ExtList("claudia")
	require.NoError(t, listErr)
	assert.Empty(t, ns, "the bundle must be untouched")
}

func TestRepoOnly_ExtWriteTargetsRepoLayer(t *testing.T) {
	repo, bundle, global := NewStore(t.TempDir()), NewStore(t.TempDir()), NewStore(t.TempDir())
	ls := NewLayeredStoreWithBundle(repo, bundle, global, true)

	require.NoError(t, repo.Save(&Identity{Name: "Mal", Handle: "mal", Kind: "human"}))
	require.NoError(t, ls.ExtSet("mal", "quarry", "memory_collection", "repo-collection"))

	m, err := repo.ExtGet("mal", "quarry", "memory_collection")
	require.NoError(t, err)
	assert.Equal(t, "repo-collection", m["memory_collection"])

	ns, err := global.ExtList("mal")
	require.NoError(t, err)
	assert.Empty(t, ns, "global must not receive repo-only ext writes")
}

// Layered mode is unchanged: ext writes land in global even when the
// identity itself lives only in the repo layer (DES-044).
func TestLayered_ExtWriteStillTargetsGlobal(t *testing.T) {
	repo, global := NewStore(t.TempDir()), NewStore(t.TempDir())
	ls := NewLayeredStoreWithBundle(repo, nil, global, false)

	require.NoError(t, repo.Save(&Identity{Name: "Mal", Handle: "mal", Kind: "human"}))
	require.NoError(t, ls.ExtSet("mal", "quarry", "memory_collection", "x"))

	m, err := global.ExtGet("mal", "quarry", "memory_collection")
	require.NoError(t, err)
	assert.Equal(t, "x", m["memory_collection"])
}

// A missing ext file must never brick a live Load: the verdict on
// completeness belongs to doctor and vendor, not to the read path.
func TestRepoOnly_MissingExtDoesNotFailLoad(t *testing.T) {
	repo, global := NewStore(t.TempDir()), NewStore(t.TempDir())
	ls := NewLayeredStoreWithBundle(repo, nil, global, true)

	require.NoError(t, repo.Save(&Identity{Name: "Mal", Handle: "mal", Kind: "human"}))
	require.NoError(t, global.Save(&Identity{Name: "Mal", Handle: "mal", Kind: "human"}))
	require.NoError(t, global.ExtSet("mal", "quarry", "memory_collection", "global-only"))

	id, err := ls.Load("mal")
	require.NoError(t, err)
	assert.Empty(t, id.Ext, "the global ext must not leak into a repo-only read")
}
