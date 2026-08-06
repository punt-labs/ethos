package role

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sidecarRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "..", "..", "internal", "seed", "sidecar")
}

func TestSidecarRolesLoad(t *testing.T) {
	store := NewStore(sidecarRoot(t))

	// Worker roles produce a structured FINDINGS handoff; leadership
	// roles (ceo, coo) direct and delegate, so they carry no
	// output_format template.
	worker := []string{
		"architect", "implementer", "researcher",
		"reviewer", "security-reviewer", "test-engineer",
	}
	leadership := []string{"ceo", "coo"}
	expected := append(append([]string{}, worker...), leadership...)

	for _, name := range worker {
		t.Run(name, func(t *testing.T) {
			r, err := store.Load(name)
			require.NoError(t, err)
			assert.Equal(t, name, r.Name)
			assert.NotEmpty(t, r.Responsibilities)
			assert.NotEmpty(t, r.Tools)
			assert.NotEmpty(t, r.Model, "sidecar role %q should have a model", name)
			assert.NoError(t, ValidateModel(r.Model), "role %q has invalid model", name)
			// Every shipped worker role carries a structured-handoff
			// template. A future edit that strips output_format from
			// one role surfaces as a per-subtest failure naming the
			// role, not a single aggregate error.
			assert.NotEmpty(t, r.OutputFormat,
				"sidecar role %q must ship with an output_format template", name)
		})
	}

	for _, name := range leadership {
		t.Run(name, func(t *testing.T) {
			r, err := store.Load(name)
			require.NoError(t, err)
			assert.Equal(t, name, r.Name)
			assert.NotEmpty(t, r.Responsibilities)
			assert.NotEmpty(t, r.Tools)
			assert.NotEmpty(t, r.Model, "sidecar role %q should have a model", name)
			assert.NoError(t, ValidateModel(r.Model), "role %q has invalid model", name)
			// Leadership roles direct and delegate — they carry no
			// structured-handoff template. Assert the exemption so an
			// accidental output_format (which would change generated agent
			// files) fails here rather than slipping through.
			assert.Empty(t, r.OutputFormat,
				"leadership role %q must not carry an output_format template", name)
		})
	}

	listed, err := store.List()
	require.NoError(t, err)
	assert.ElementsMatch(t, expected, listed)
}

// TestBundleRolesHaveModel verifies every bundle-seeded role carries a
// model tier. Bundle roles ship as an alternative starter set (gstack,
// foundation) alongside the top-level sidecar roles; a role missing
// model here means a repo seeded from that bundle gets no cost tiering
// at all, silently, until someone notices the bill.
func TestBundleRolesHaveModel(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	bundlesRoot := filepath.Join(wd, "..", "..", "internal", "seed", "sidecar", "bundles")

	bundles, err := os.ReadDir(bundlesRoot)
	require.NoError(t, err)

	for _, b := range bundles {
		if !b.IsDir() {
			continue
		}
		bundle := b.Name()
		store := NewStore(filepath.Join(bundlesRoot, bundle))
		names, err := store.List()
		require.NoError(t, err)
		for _, name := range names {
			t.Run(bundle+"/"+name, func(t *testing.T) {
				r, err := store.Load(name)
				require.NoError(t, err)
				assert.NotEmpty(t, r.Model, "bundle %q role %q should have a model", bundle, name)
				assert.NoError(t, ValidateModel(r.Model), "bundle %q role %q has invalid model", bundle, name)
			})
		}
	}
}

func TestSidecarRolesToolRestrictions(t *testing.T) {
	store := NewStore(sidecarRoot(t))

	noWriteEdit := []string{"reviewer", "security-reviewer", "researcher"}
	for _, name := range noWriteEdit {
		t.Run(name+"_no_file_modification", func(t *testing.T) {
			r, err := store.Load(name)
			require.NoError(t, err)
			for _, tool := range r.Tools {
				assert.NotEqual(t, "Write", tool, "file-modification-restricted role %q has Write", name)
				assert.NotEqual(t, "Edit", tool, "file-modification-restricted role %q has Edit", name)
			}
		})
	}

	impl := []string{"implementer", "test-engineer"}
	for _, name := range impl {
		t.Run(name+"_has_bash", func(t *testing.T) {
			r, err := store.Load(name)
			require.NoError(t, err)
			hasBash := false
			for _, tool := range r.Tools {
				if tool == "Bash" {
					hasBash = true
				}
			}
			assert.True(t, hasBash, "implementation role %q needs Bash", name)
		})
	}
}

func TestSidecarRolesModelSelection(t *testing.T) {
	store := NewStore(sidecarRoot(t))

	opus := []string{"reviewer", "architect", "security-reviewer"}
	for _, name := range opus {
		t.Run(name+"_opus", func(t *testing.T) {
			r, err := store.Load(name)
			require.NoError(t, err)
			assert.Equal(t, "opus", r.Model)
		})
	}

	sonnet := []string{"implementer", "test-engineer", "researcher"}
	for _, name := range sonnet {
		t.Run(name+"_sonnet", func(t *testing.T) {
			r, err := store.Load(name)
			require.NoError(t, err)
			assert.Equal(t, "sonnet", r.Model)
		})
	}
}
