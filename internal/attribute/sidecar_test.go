package attribute

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func attrSidecarRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(filename), "..", "..", "sidecar")
}

func TestSidecarTalentsLoad(t *testing.T) {
	store := NewStore(attrSidecarRoot(t), Talents)

	expected := []string{
		"api-design", "cli-design", "code-review", "devops",
		"documentation", "go", "python", "security", "testing", "typescript",
	}

	for _, slug := range expected {
		t.Run(slug, func(t *testing.T) {
			attr, err := store.Load(slug)
			require.NoError(t, err)
			assert.Equal(t, slug, attr.Slug)
			assert.NotEmpty(t, attr.Content)
			assert.Greater(t, len(attr.Content), 1000,
				"talent %q should be substantial (>1000 chars), got %d", slug, len(attr.Content))
		})
	}

	listed, err := store.List()
	require.NoError(t, err)
	var slugs []string
	for _, a := range listed.Attributes {
		slugs = append(slugs, a.Slug)
	}
	assert.ElementsMatch(t, expected, slugs)
}
