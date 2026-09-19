package seed

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oldImplementYAML returns the shipped implement.yaml with its
// require_delegated_worker line stripped — the exact shape GH #525 found on
// an upgrader's disk: an archetype deployed before v4.19.0 added the field,
// still sitting in the seeder's skip-if-exists category.
func oldImplementYAML(t *testing.T) (shipped, stale []byte) {
	t.Helper()
	shipped, err := fs.ReadFile(Archetypes, "sidecar/archetypes/implement.yaml")
	require.NoError(t, err)
	var kept []string
	for _, line := range strings.Split(string(shipped), "\n") {
		if strings.HasPrefix(line, "require_delegated_worker:") {
			continue
		}
		kept = append(kept, line)
	}
	return shipped, []byte(strings.Join(kept, "\n"))
}

// TestAdditiveMerge_StaleArchetypeRepaired pins GH #525's fix: a pre-v4.19.0
// implement.yaml, missing only the new require_delegated_worker field, is
// repaired by appending that field — the file's other lines are untouched,
// and the appended line is the shipped line verbatim.
func TestAdditiveMerge_StaleArchetypeRepaired(t *testing.T) {
	shipped, stale := oldImplementYAML(t)
	merged, ok := additiveMerge(stale, shipped)
	require.True(t, ok)
	assert.Equal(t, string(stale)+"require_delegated_worker: true\n", string(merged))

	// The repair result parses back into the same values the shipped file
	// carries — order does not matter to a YAML consumer, only content.
	mergedMapping, ok := topLevelMapping(merged)
	require.True(t, ok)
	shippedMapping, ok := topLevelMapping(shipped)
	require.True(t, ok)
	var mergedVal, shippedVal map[string]any
	require.NoError(t, mergedMapping.Decode(&mergedVal))
	require.NoError(t, shippedMapping.Decode(&shippedVal))
	assert.Equal(t, shippedVal, mergedVal)
}

// TestAdditiveMerge_ConflictingValueStaysUnmerged proves a real user edit —
// not just a missing field — is never folded in silently: existing has the
// same fields but disagrees on one, so the shared key's value is a
// conflict, not an addition.
func TestAdditiveMerge_ConflictingValueStaysUnmerged(t *testing.T) {
	shipped, _ := oldImplementYAML(t)
	edited := strings.Replace(string(shipped),
		"allow_empty_write_set: false", "allow_empty_write_set: true", 1)
	_, ok := additiveMerge([]byte(edited), shipped)
	assert.False(t, ok, "a conflicting shared value must not be merged")
}

// TestAdditiveMerge_UserAddedKeyStaysUnmerged proves a file with a key the
// seed content does not define — a genuine local addition — is left alone
// rather than treated as purely additive on the seed's side.
func TestAdditiveMerge_UserAddedKeyStaysUnmerged(t *testing.T) {
	shipped, stale := oldImplementYAML(t)
	edited := string(stale) + "my_custom_field: true\n"
	_, ok := additiveMerge([]byte(edited), shipped)
	assert.False(t, ok, "a key the seed does not define must not be merged")
}

// TestAdditiveMerge_NonMappingContentStaysUnmerged covers the common case:
// most seeded content is Markdown (talents, personalities, writing styles),
// not a YAML mapping at all, and must fall through untouched.
func TestAdditiveMerge_NonMappingContentStaysUnmerged(t *testing.T) {
	_, ok := additiveMerge([]byte("# Talent\nold body\n"), []byte("# Talent\nnew body\n"))
	assert.False(t, ok)
}

// TestAdditiveMerge_NoMissingKeysStaysUnmerged covers content whose hash
// differs for a reason other than a missing top-level key (e.g. pure
// formatting) — additiveMerge has nothing additive to explain the diff, so
// it declines rather than guessing.
func TestAdditiveMerge_NoMissingKeysStaysUnmerged(t *testing.T) {
	_, ok := additiveMerge([]byte("name: x\n"), []byte("name: x  \n"))
	assert.False(t, ok)
}

// TestPlace_UntrackedAdditiveDiffIsRepaired drives the fix end to end
// through place: an untracked, pre-v4.19.0 implement.yaml on disk gets
// repaired in place, reported under Result.RepairedFields (not Repaired,
// which stays reserved for the zero-byte case), and its manifest entry
// recorded — so a second seed run reports it unchanged rather than
// repairing it again.
func TestPlace_UntrackedAdditiveDiffIsRepaired(t *testing.T) {
	shipped, stale := oldImplementYAML(t)
	dest := t.TempDir()
	s := testSeeder(dest, "", false)
	path := filepath.Join(dest, "archetypes", "implement.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, stale, 0o644))

	s.place(scopeEthos, path, shipped)
	require.Empty(t, s.r.Errors, "errors: %v", s.r.Errors)
	assert.Contains(t, s.r.RepairedFields, path)
	assert.Empty(t, s.r.Repaired, "an additive repair is not a zero-byte repair")
	assert.NotContains(t, s.r.Skipped, path)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	want := string(stale) + "require_delegated_worker: true\n"
	assert.Equal(t, want, string(got))

	key := s.key(scopeEthos, path)
	entry, ok := s.mf.Entries[key]
	require.True(t, ok, "a repaired file must be recorded so a later seed sees it as unchanged")
	assert.Equal(t, hashBytes([]byte(want)), entry.Hash)

	// A second seed run must see the repaired file as tracked and
	// unmodified since the repair — never treated as untracked again, and
	// never re-entering the repair path. The appended key sits in a
	// different position than the canonical shipped file, so the content
	// hash still differs; that reads as a normal tracked upgrade to the
	// canonical layout, not a repeat repair.
	s2 := testSeeder(dest, "", false)
	s2.mf = s.mf
	s2.place(scopeEthos, path, shipped)
	require.Empty(t, s2.r.Errors, "errors: %v", s2.r.Errors)
	assert.Contains(t, s2.r.Updated, path)
	assert.Empty(t, s2.r.RepairedFields)
	got2, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(shipped), string(got2))
}

// TestPlace_UntrackedConflictingDiffStaysSkipped is the counterpart: a file
// with a genuine hand-edit — not just a missing field — keeps today's
// no-clobber skip and is never touched.
func TestPlace_UntrackedConflictingDiffStaysSkipped(t *testing.T) {
	shipped, _ := oldImplementYAML(t)
	edited := strings.Replace(string(shipped),
		"allow_empty_write_set: false", "allow_empty_write_set: true", 1)
	dest := t.TempDir()
	s := testSeeder(dest, "", false)
	path := filepath.Join(dest, "archetypes", "implement.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(edited), 0o644))

	s.place(scopeEthos, path, shipped)
	require.Empty(t, s.r.Errors, "errors: %v", s.r.Errors)
	assert.Contains(t, s.r.Skipped, path)
	assert.NotContains(t, s.r.Repaired, path)
	assert.NotContains(t, s.r.RepairedFields, path)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, edited, string(got), "a genuinely edited file must be left untouched")

	key := s.key(scopeEthos, path)
	_, ok := s.mf.Entries[key]
	assert.False(t, ok, "a skipped file must not enter the manifest")
}
