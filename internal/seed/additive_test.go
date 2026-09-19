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
	merged, reason, ok := additiveMerge(stale, shipped)
	require.True(t, ok)
	assert.Empty(t, reason, "a successful merge carries no decline reason")
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
// conflict, not an addition. The reason names the offending key, so a skip
// line built from it says more than "exists".
func TestAdditiveMerge_ConflictingValueStaysUnmerged(t *testing.T) {
	shipped, _ := oldImplementYAML(t)
	edited := strings.Replace(string(shipped),
		"allow_empty_write_set: false", "allow_empty_write_set: true", 1)
	_, reason, ok := additiveMerge([]byte(edited), shipped)
	assert.False(t, ok, "a conflicting shared value must not be merged")
	assert.Contains(t, reason, `"allow_empty_write_set"`)
}

// TestAdditiveMerge_UserAddedKeyStaysUnmerged proves a file with a key the
// seed content does not define — a genuine local addition — is left alone
// rather than treated as purely additive on the seed's side. The reason
// names the offending key.
func TestAdditiveMerge_UserAddedKeyStaysUnmerged(t *testing.T) {
	shipped, stale := oldImplementYAML(t)
	edited := string(stale) + "my_custom_field: true\n"
	_, reason, ok := additiveMerge([]byte(edited), shipped)
	assert.False(t, ok, "a key the seed does not define must not be merged")
	assert.Contains(t, reason, `"my_custom_field"`)
}

// TestAdditiveMerge_NonMappingContentStaysUnmerged covers the common case:
// most seeded content is Markdown (talents, personalities, writing styles),
// not a YAML mapping at all, and must fall through untouched.
func TestAdditiveMerge_NonMappingContentStaysUnmerged(t *testing.T) {
	_, reason, ok := additiveMerge([]byte("# Talent\nold body\n"), []byte("# Talent\nnew body\n"))
	assert.False(t, ok)
	assert.NotEmpty(t, reason)
}

// TestAdditiveMerge_FrontMatterMarkdownStaysUnmerged pins the critical fix:
// a seeded agent/skill .md with YAML front matter is two YAML documents
// ("---\n...\n---\n" then the body), and yaml.Unmarshal silently decodes
// only the first. Treating that first document as if it described the
// whole file let additiveMerge append the shipped file's ENTIRE remaining
// text — closing "---", heading, and body — onto a user's customized
// instructions, because keyBlocks' "end of file for the last key" is wrong
// once there is a second document. This is the exact shape a reviewer
// reproduced live (front-matter agent .md, one added front-matter key)
// before the one-document requirement in topLevelMapping closed it.
func TestAdditiveMerge_FrontMatterMarkdownStaysUnmerged(t *testing.T) {
	existing := "---\nname: code-reviewer\ndescription: reviews code\ncolor: green\n---\n\n" +
		"# Code Reviewer\n\nMy locally customized instructions.\n"
	shipped := "---\nname: code-reviewer\ndescription: reviews code\ncolor: green\nmodel: opus\n---\n\n" +
		"# Code Reviewer\n\nBrand new shipped body.\n"

	merged, reason, ok := additiveMerge([]byte(existing), []byte(shipped))
	assert.False(t, ok, "front matter is not a single YAML document and must never be merged")
	assert.Nil(t, merged)
	assert.NotEmpty(t, reason)
	assert.NotContains(t, reason, "conflicting", "front matter must decline as multi-document, not misread as a value conflict")
}

// TestAdditiveMerge_FlowMappingStaysUnmerged pins the second half of the
// critical fix: a flow-style mapping ("{name: x}") puts every key on the
// SAME line, so keyBlocks' line-range slicing cannot separate them —
// appending a "missing" key's block risks producing a result that silently
// drops or corrupts a key on re-decode instead of erroring loudly. The
// post-merge verification (re-decode and compare against the shipped
// content's own decoded value) is what actually catches this; the merge
// wrongly claims success upstream of that check, which is exactly why the
// check is independent of the line-range logic it's guarding.
func TestAdditiveMerge_FlowMappingStaysUnmerged(t *testing.T) {
	_, reason, ok := additiveMerge([]byte("{name: x}\n"), []byte("name: x\nextra: 1\n"))
	assert.False(t, ok, "a flow-style mapping must not be additively merged")
	assert.Contains(t, reason, "merge verification failed")
}

// TestAdditiveMerge_PostMergeVerificationCatchesBadMerge exercises the
// verification step directly and in isolation from any specific line-slicing
// bug: even a merge that reached the point of returning true is re-decoded
// and checked against the shipped content's own decoded value before it is
// trusted. This is the same case as the flow-mapping test above, stated as
// "the safety net itself does its job" rather than "this specific input
// trips it".
func TestAdditiveMerge_PostMergeVerificationCatchesBadMerge(t *testing.T) {
	_, reason, ok := additiveMerge([]byte("{name: x}\n"), []byte("name: x\nextra: 1\n"))
	require.False(t, ok)
	assert.Contains(t, reason, "does not match the shipped content")
}

// TestAdditiveMerge_NoMissingKeysStaysUnmerged covers content whose hash
// differs for a reason other than a missing top-level key (e.g. pure
// formatting) — additiveMerge has nothing additive to explain the diff, so
// it declines rather than guessing.
func TestAdditiveMerge_NoMissingKeysStaysUnmerged(t *testing.T) {
	_, reason, ok := additiveMerge([]byte("name: x\n"), []byte("name: x  \n"))
	assert.False(t, ok)
	assert.NotEmpty(t, reason)
}

// TestPlace_UntrackedAdditiveDiffIsRepaired drives the fix end to end
// through place: an untracked, pre-v4.19.0 implement.yaml on disk gets
// repaired in place, reported under Result.RepairedFields (not Repaired,
// which stays reserved for the zero-byte case).
//
// The repair is deliberately NOT recorded in the manifest (2026-09-19
// operator ruling, GH #525) — see repairAdditive's doc comment for why:
// additiveMerge's own DeepEqual-based verification is blind to comments,
// key order, and formatting, so recording the write would make the very
// next seed run treat the repaired file as tracked-but-differing-from-cur
// and re-marshal it to the canonical shipped layout, discarding the exact
// formatting the additive merge just preserved. A second seed run must
// therefore leave the repaired file's bytes untouched: with every key now
// present, additiveMerge has nothing left to add, so it declines
// ("no missing keys explain the difference") and the file is reported as
// an ordinary skip, not a repeat repair or a canonicalizing update.
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
	_, tracked := s.mf.Entries[key]
	assert.False(t, tracked, "a repair must not be recorded — see repairAdditive's doc comment")

	// A second seed run finds every key already present, declines the
	// repair as having nothing left to add, and leaves the file's bytes —
	// including its non-canonical key order — exactly as the repair left
	// them.
	s2 := testSeeder(dest, "", false)
	s2.mf = s.mf
	s2.place(scopeEthos, path, shipped)
	require.Empty(t, s2.r.Errors, "errors: %v", s2.r.Errors)
	assert.Contains(t, s2.r.Skipped, path)
	assert.Contains(t, s2.r.SkipReasons[path], "no missing keys")
	assert.Empty(t, s2.r.RepairedFields, "nothing left to repair a second time")
	assert.Empty(t, s2.r.Updated, "a repaired file must not be silently canonicalized on the next run")
	got2, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, string(got2), "the repaired file's bytes must survive a second seed run untouched")
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
	assert.Contains(t, s.r.SkipReasons[path], `"allow_empty_write_set"`,
		"a skip additiveMerge actually evaluated must carry the reason it declined")

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, edited, string(got), "a genuinely edited file must be left untouched")

	key := s.key(scopeEthos, path)
	_, ok := s.mf.Entries[key]
	assert.False(t, ok, "a skipped file must not enter the manifest")
}
