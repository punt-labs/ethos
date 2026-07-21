package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/audit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFindSessionDir_CollisionResolvesStructurally is B3 for the live-tree
// resolver: findSessionDir must resolve a session to its exact
// <date>-<sessionID> directory, never a longer-suffixed sibling. The live-tree
// resolver (writer/reader/migrate) and the sealed-tree resolver
// (audit.FindSealedSessionDir, seal path) share audit.SessionDirMatches, so
// both resolve a colliding id to the SAME directory — seal and read can never
// target different trees.
func TestFindSessionDir_CollisionResolvesStructurally(t *testing.T) {
	repoRoot := t.TempDir()
	base := audit.SealedSessionsBase(repoRoot)
	mkdir := func(name string) {
		require.NoError(t, os.MkdirAll(filepath.Join(base, name), 0o700))
	}
	mkdir("2026-07-21-abc")
	mkdir("2026-07-21-x-abc")

	for _, id := range []string{"abc", "x-abc"} {
		live, err := findSessionDir(base, id)
		require.NoError(t, err)
		sealed, err := audit.FindSealedSessionDir(repoRoot, id)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(base, "2026-07-21-"+id), live,
			"live-tree resolver must match the exact session dir")
		assert.Equal(t, sealed, live,
			"live and sealed resolvers must agree for id %q", id)
	}
}

// TestFindSessionDir_NoFalseMatch confirms a distinct id does not resolve to a
// longer-suffixed directory — it falls through to the create-new path.
func TestFindSessionDir_NoFalseMatch(t *testing.T) {
	repoRoot := t.TempDir()
	base := audit.SealedSessionsBase(repoRoot)
	require.NoError(t, os.MkdirAll(filepath.Join(base, "2026-07-21-x-abc"), 0o700))

	live, err := findSessionDir(base, "abc")
	require.NoError(t, err)
	assert.Empty(t, live, "id abc must not match 2026-07-21-x-abc")

	sealed, err := audit.FindSealedSessionDir(repoRoot, "abc")
	require.NoError(t, err)
	assert.Equal(t, sealed, live, "live and sealed resolvers must agree (both no-match)")
}
