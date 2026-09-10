package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckDelegatedWorkerArchetypes pins ethos-e05k: an archetype missing
// require_delegated_worker must FAIL and name the resolving layer, because
// a repo-local file shadows the global one — refreshing the global layer
// with `ethos seed` does nothing when a stale repo-local copy resolves
// first.
//
// HOME is always pinned to an empty or purpose-built temp dir: the check
// falls back to the real user global archetypes dir when HOME is
// ambient, and this dev machine's own global archetypes predate
// require_delegated_worker — an unpinned test would pass or fail
// depending on whoever's machine runs it.
func TestCheckDelegatedWorkerArchetypes(t *testing.T) {
	writeArchetype := func(t *testing.T, dir, name, body string) {
		t.Helper()
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644))
	}
	repoArchDir := func(storeRoot string) string {
		return filepath.Join(storeRoot, ".punt-labs", "ethos", "archetypes")
	}

	t.Run("not in a repo", func(t *testing.T) {
		r := CheckDelegatedWorkerArchetypes("")
		assert.True(t, r.Passed())
		assert.Equal(t, "not in a repo", r.Detail)
	})

	t.Run("no archetypes deployed anywhere — not this check's concern", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		storeRoot := t.TempDir()
		r := CheckDelegatedWorkerArchetypes(storeRoot)
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
	})

	t.Run("repo-local archetype with the guard set → PASS", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		storeRoot := t.TempDir()
		writeArchetype(t, repoArchDir(storeRoot), "implement", "name: implement\nrequire_delegated_worker: true\n")
		writeArchetype(t, repoArchDir(storeRoot), "test", "name: test\nrequire_delegated_worker: true\n")
		r := CheckDelegatedWorkerArchetypes(storeRoot)
		assert.Equal(t, "PASS", r.Status, "detail: %s", r.Detail)
	})

	t.Run("repo-local archetype missing the field → FAIL, names repo-local", func(t *testing.T) {
		// This is the exact silent-zero-enforcement shape ethos-e05k names:
		// pre-field YAML on disk, the field simply absent, parsing as false.
		t.Setenv("HOME", t.TempDir())
		storeRoot := t.TempDir()
		writeArchetype(t, repoArchDir(storeRoot), "implement", "name: implement\n")
		writeArchetype(t, repoArchDir(storeRoot), "test", "name: test\nrequire_delegated_worker: true\n")
		r := CheckDelegatedWorkerArchetypes(storeRoot)
		assert.Equal(t, "FAIL", r.Status)
		assert.Contains(t, r.Detail, "implement (repo-local)")
		assert.NotContains(t, r.Detail, "test (repo-local)")
		assert.Contains(t, r.Detail, "ethos seed")
	})

	t.Run("only global has the archetype and it is stale → FAIL, names global", func(t *testing.T) {
		globalHome := t.TempDir()
		writeArchetype(t, filepath.Join(globalHome, ".punt-labs", "ethos", "archetypes"),
			"implement", "name: implement\n")
		t.Setenv("HOME", globalHome)

		storeRoot := t.TempDir() // no repo-local archetypes dir at all
		r := CheckDelegatedWorkerArchetypes(storeRoot)
		assert.Equal(t, "FAIL", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "implement (global)")
	})

	t.Run("repo-local shadows a fine global with a stale copy → FAIL still names repo-local", func(t *testing.T) {
		// This is the "shadow" scenario the bead calls out by name: a fine
		// global archetype does not help, because the repo-local file
		// resolves first.
		globalHome := t.TempDir()
		writeArchetype(t, filepath.Join(globalHome, ".punt-labs", "ethos", "archetypes"),
			"implement", "name: implement\nrequire_delegated_worker: true\n")
		t.Setenv("HOME", globalHome)

		storeRoot := t.TempDir()
		writeArchetype(t, repoArchDir(storeRoot), "implement", "name: implement\n")

		r := CheckDelegatedWorkerArchetypes(storeRoot)
		assert.Equal(t, "FAIL", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "implement (repo-local)",
			"the repo-local file resolves ahead of a fine global one and must be named, not the global layer")
	})
}
