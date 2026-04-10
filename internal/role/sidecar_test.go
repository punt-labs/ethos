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

	expected := []string{
		"architect", "implementer", "researcher",
		"reviewer", "security-reviewer", "test-engineer",
	}

	for _, name := range expected {
		t.Run(name, func(t *testing.T) {
			r, err := store.Load(name)
			require.NoError(t, err)
			assert.Equal(t, name, r.Name)
			assert.NotEmpty(t, r.Responsibilities)
			assert.NotEmpty(t, r.Tools)
			assert.NotEmpty(t, r.Model, "sidecar role %q should have a model", name)
			assert.NoError(t, ValidateModel(r.Model), "role %q has invalid model", name)
			// Every shipped sidecar role carries a structured-handoff
			// template. A future edit that strips output_format from
			// one role surfaces as a per-subtest failure naming the
			// role, not a single aggregate error.
			assert.NotEmpty(t, r.OutputFormat,
				"sidecar role %q must ship with an output_format template", name)
		})
	}

	listed, err := store.List()
	require.NoError(t, err)
	assert.ElementsMatch(t, expected, listed)
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
