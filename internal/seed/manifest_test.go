package seed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManifest_RoundTrip pins the on-disk schema: a saved manifest reloads to
// an equal value, with the schema version and every entry field preserved.
func TestManifest_RoundTrip(t *testing.T) {
	root := t.TempDir()
	m := &Manifest{
		Schema: manifestSchema,
		Entries: map[string]Entry{
			"roles/architect.yaml": {
				Scope:   scopeEthos,
				Hash:    hashBytes([]byte("role")),
				Version: "4.7.0",
				Written: "2026-07-26T10:04:11Z",
			},
			"skills/mission/SKILL.md": {
				Scope:   scopeSkills,
				Hash:    hashBytes([]byte("skill")),
				Version: "4.7.0",
				Written: "2026-07-26T10:04:12Z",
			},
		},
	}
	require.NoError(t, m.save(root))

	got, err := loadManifest(root)
	require.NoError(t, err)
	assert.Equal(t, manifestSchema, got.Schema)
	assert.Equal(t, m.Entries, got.Entries)

	// The file is valid JSON with the documented top-level keys.
	data, err := os.ReadFile(filepath.Join(root, ManifestName))
	require.NoError(t, err)
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw, "schema")
	assert.Contains(t, raw, "entries")
}

// TestLoadManifest_MissingIsEmpty pins that a machine with no manifest yields
// an empty, usable manifest rather than an error.
func TestLoadManifest_MissingIsEmpty(t *testing.T) {
	m, err := loadManifest(t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, manifestSchema, m.Schema)
	assert.Empty(t, m.Entries)
}

// TestLoadManifest_CorruptErrors pins that a present-but-unparseable manifest
// is a hard error — garbage must not be silently discarded.
func TestLoadManifest_CorruptErrors(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, ManifestName), []byte("{not json"), 0o644))
	_, err := loadManifest(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing seed manifest")
}

// TestLoadManifest_UnreadableErrors pins that a present-but-unreadable manifest
// is a hard error, not a silent empty. Treating it as empty would reclassify
// tracked files as untracked and drop their upgrades.
func TestLoadManifest_UnreadableErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; cannot simulate an unreadable file")
	}
	root := t.TempDir()
	mfPath := filepath.Join(root, ManifestName)
	require.NoError(t, os.WriteFile(mfPath, []byte(`{"schema":1,"entries":{}}`), 0o000))
	t.Cleanup(func() { os.Chmod(mfPath, 0o600) })

	_, err := loadManifest(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading seed manifest")
}

// TestSeed_CorruptManifestNotClobbered pins the durability guarantee behind the
// hard error: when the manifest is unreadable, seed writes nothing — it neither
// overwrites the manifest with a fresh one nor touches library files, so the
// tracking is recoverable once the manifest is fixed.
func TestSeed_CorruptManifestNotClobbered(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	// A pre-existing library file that must not be touched.
	rolesDir := filepath.Join(dest, "roles")
	require.NoError(t, os.MkdirAll(rolesDir, 0o755))
	rolePath := filepath.Join(rolesDir, "implementer.yaml")
	require.NoError(t, os.WriteFile(rolePath, []byte("user content\n"), 0o644))

	// A corrupt manifest.
	mfPath := filepath.Join(dest, ManifestName)
	corrupt := []byte("{ this is not json")
	require.NoError(t, os.WriteFile(mfPath, corrupt, 0o644))

	result, err := Seed(dest, skills, false)
	require.Error(t, err)
	require.NotNil(t, result)
	assert.NotEmpty(t, result.Errors)

	// The corrupt manifest is left intact — not overwritten with a fresh one.
	got, rerr := os.ReadFile(mfPath)
	require.NoError(t, rerr)
	assert.Equal(t, corrupt, got, "a corrupt manifest must not be clobbered by save()")

	// No library file was written — seed never proceeded.
	role, rerr := os.ReadFile(rolePath)
	require.NoError(t, rerr)
	assert.Equal(t, "user content\n", string(role),
		"seed must write nothing when the manifest is unreadable")
	assert.NoFileExists(t, filepath.Join(rolesDir, "reviewer.yaml"),
		"seed must not deploy new files when the manifest is unreadable")
}

// TestLoadManifest_UnsupportedSchemaErrors pins fail-closed behavior: a
// manifest written by a newer ethos (schema above what this binary supports) is
// a hard error, not silently adopted and rewritten at the old schema (which
// would drop fields this binary does not understand).
func TestLoadManifest_UnsupportedSchemaErrors(t *testing.T) {
	root := t.TempDir()
	mfPath := filepath.Join(root, ManifestName)
	future := []byte(`{"schema":999,"entries":{"roles/x.yaml":{"scope":"ethos","hash":"sha256:ab"}}}`)
	require.NoError(t, os.WriteFile(mfPath, future, 0o644))

	_, err := loadManifest(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported schema")

	// The file is not rewritten by a failed load.
	got, rerr := os.ReadFile(mfPath)
	require.NoError(t, rerr)
	assert.Equal(t, future, got, "an unsupported-schema manifest must not be rewritten")
}

// TestSeed_UnsupportedSchemaNotClobbered pins that a future-schema manifest
// stops seed before it writes or overwrites anything.
func TestSeed_UnsupportedSchemaNotClobbered(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	mfPath := filepath.Join(dest, ManifestName)
	future := []byte(`{"schema":999,"entries":{}}`)
	require.NoError(t, os.WriteFile(mfPath, future, 0o644))

	result, err := Seed(dest, skills, false)
	require.Error(t, err)
	require.NotNil(t, result)
	assert.NotEmpty(t, result.Errors)

	got, rerr := os.ReadFile(mfPath)
	require.NoError(t, rerr)
	assert.Equal(t, future, got, "seed must not rewrite a future-schema manifest")
	assert.NoDirExists(t, filepath.Join(dest, "roles"),
		"seed must write nothing when the manifest schema is unsupported")
}

// TestSeederKey_EscapingDestUsesFullPath pins the collision-safe fallback: a
// dest not under its root (Rel yields a "../" path) is keyed by its full cleaned
// path, never by a relative path that could collide with a real key.
func TestSeederKey_EscapingDestUsesFullPath(t *testing.T) {
	s := &seeder{destRoot: "/a/b", skillsRoot: "/a/b"}
	assert.Equal(t, "/a/c/x.yaml", s.key(scopeEthos, "/a/c/x.yaml"),
		"an escaping dest must use its full cleaned path")
	assert.Equal(t, "roles/x.yaml", s.key(scopeEthos, "/a/b/roles/x.yaml"),
		"a dest under the root still keys relative")
}

// TestSeed_PostSeedInvariant pins the coverage invariant rsc asked for: after
// a normal seed of a fresh destination, every seeded file has a manifest entry
// whose recorded hash matches the file's content on disk.
func TestSeed_PostSeedInvariant(t *testing.T) {
	dest := t.TempDir()
	skills := t.TempDir()

	result, err := SeedVersion(dest, skills, "4.7.0", false)
	require.NoError(t, err)
	require.Empty(t, result.Errors)
	require.NotEmpty(t, result.Deployed)

	mf, err := loadManifest(dest)
	require.NoError(t, err)

	// Every deployed file has an entry whose hash matches disk.
	deployed := append(append([]string{}, result.Deployed...), result.Repaired...)
	for _, dst := range deployed {
		key := keyFor(dest, skills, dst)
		entry, ok := mf.Entries[key]
		require.True(t, ok, "seeded file %q must have a manifest entry (key %q)", dst, key)
		assert.Equal(t, "4.7.0", entry.Version, "entry %q must record the version", key)

		data, err := os.ReadFile(dst)
		require.NoError(t, err)
		assert.Equal(t, hashBytes(data), entry.Hash,
			"manifest hash for %q must match the file on disk", key)
	}
}

// keyFor mirrors seeder.key for tests: a skills-root path becomes
// "skills/<rel>", any other path is dest-relative.
func keyFor(destRoot, skillsRoot, dst string) string {
	if rel, err := filepath.Rel(skillsRoot, dst); err == nil &&
		strings.HasPrefix(dst, skillsRoot) {
		return "skills/" + filepath.ToSlash(rel)
	}
	rel, err := filepath.Rel(destRoot, dst)
	if err != nil {
		rel = filepath.Base(dst)
	}
	return filepath.ToSlash(rel)
}
