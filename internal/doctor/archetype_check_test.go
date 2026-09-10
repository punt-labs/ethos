package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"

	"github.com/punt-labs/ethos/v4/internal/seed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCodeArchetypeNames_ThirdArchetypeAutoDetected pins P2: a THIRD
// archetype gaining require_delegated_worker: true must be picked up
// automatically, the property a hardcoded list could never have — a
// hardcoded ["implement", "test"] would silently never look at "refactor"
// here, the exact ethos-e05k shape (enforcement lost with a green check)
// recurring inside the check meant to catch it.
func TestCodeArchetypeNames_ThirdArchetypeAutoDetected(t *testing.T) {
	fsys := fstest.MapFS{
		"sidecar/archetypes/implement.yaml": {Data: []byte("name: implement\nrequire_delegated_worker: true\n")},
		"sidecar/archetypes/design.yaml":    {Data: []byte("name: design\n")}, // no flag — must not appear
		"sidecar/archetypes/refactor.yaml":  {Data: []byte("name: refactor\nrequire_delegated_worker: true\n")},
	}
	names, err := codeArchetypeNames(fsys, "sidecar/archetypes")
	require.NoError(t, err)
	assert.Equal(t, []string{"implement", "refactor"}, names,
		"a third archetype with the flag set must be monitored automatically, and one without it must not appear")
}

// TestCodeArchetypeNames_ReadError pins the broken-embed FAIL path,
// matching checklistAgentNames' precedent for the same failure shape.
func TestCodeArchetypeNames_ReadError(t *testing.T) {
	names, err := codeArchetypeNames(brokenFS{}, "sidecar/archetypes")
	require.Error(t, err)
	assert.Nil(t, names)
}

// TestCodeArchetypeNames_Real pins the production call site against the
// actual embedded seed.Archetypes: today exactly "implement" and "test"
// carry the flag. If a third archetype gains it, this test's expected
// slice needs updating — that update IS the signal the monitored set
// changed, which is the property P2 exists to guarantee doctor sees
// automatically, not the property this specific test needs to predict.
func TestCodeArchetypeNames_Real(t *testing.T) {
	names, err := codeArchetypeNames(seed.Archetypes, "sidecar/archetypes")
	require.NoError(t, err)
	assert.Equal(t, []string{"implement", "test"}, names)
}

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

	// C1: a load error that is NOT "not found" (malformed YAML, here) must
	// not be swallowed as "not deployed — not this check's concern". Pre-fix,
	// CheckDelegatedWorkerArchetypes's loop did `continue` on ANY LoadLayer
	// error, so a broken archetype file produced zero entries in `stale` and
	// the check PASSed "implement and test archetypes both require a
	// delegated worker" — a positive assertion about a file it never
	// actually read, byte-identical to the healthy control. This is
	// ethos-e05k's silent-enforcement-loss failure mode, recurring one layer
	// up inside the check written to catch it.
	t.Run("malformed repo-local archetype FAILs instead of reading as healthy", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		storeRoot := t.TempDir()
		writeArchetype(t, repoArchDir(storeRoot), "implement", "not: [valid: yaml")
		writeArchetype(t, repoArchDir(storeRoot), "test", "name: test\nrequire_delegated_worker: true\n")

		r := CheckDelegatedWorkerArchetypes(storeRoot)
		require.Equal(t, "FAIL", r.Status,
			"a malformed archetype file must FAIL, not PASS as if nothing were deployed: %+v", r)
		assert.Contains(t, r.Detail, "implement")
		assert.NotContains(t, r.Detail, "delegated worker",
			"must not emit the healthy-control PASS sentence text alongside a load failure")
	})

	// archetypeAttemptedPath's Stat call used to treat ANY error — not just
	// not-exist — as proof the repo-local file was absent, so a permission
	// error reported "global" and sent the operator to chmod or edit the
	// wrong file. Deny search permission on the repo-local archetypes
	// directory so os.ReadFile (inside LoadLayer) and os.Stat (inside
	// archetypeAttemptedPath) both fail with EACCES, not ENOENT — the exact
	// failure this check must not mistake for absence.
	t.Run("repo-local archetype unreadable (permission denied) names repo-local, not global", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("chmod 0o000 does not deny directory search on Windows; this failure mode is Unix-specific")
		}
		if os.Geteuid() == 0 {
			t.Skip("root ignores permission bits — cannot reproduce EACCES as root")
		}

		t.Setenv("HOME", t.TempDir())
		storeRoot := t.TempDir()
		archDir := repoArchDir(storeRoot)
		writeArchetype(t, archDir, "implement", "name: implement\nrequire_delegated_worker: true\n")
		writeArchetype(t, archDir, "test", "name: test\nrequire_delegated_worker: true\n")

		require.NoError(t, os.Chmod(archDir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(archDir, 0o755) }) // restore before TempDir cleanup removes it

		r := CheckDelegatedWorkerArchetypes(storeRoot)
		require.Equal(t, "FAIL", r.Status, "detail: %s", r.Detail)
		assert.Contains(t, r.Detail, "(repo-local", "an unreadable repo-local file must be named as repo-local: %s", r.Detail)
		assert.NotContains(t, r.Detail, "(global,", "a permission error on the repo-local file must not be misreported as global: %s", r.Detail)
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
