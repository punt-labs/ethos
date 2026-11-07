package repair

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowsDeletionRefusesEvenWithoutLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	require.NoError(t, os.WriteFile(path, []byte("retained"), 0o600))
	p := &Plan{Items: []Item{{Path: path}}}
	removed, err := p.Apply(context.Background())
	require.ErrorContains(t, err, "unsupported on Windows")
	assert.Empty(t, removed)
	assert.FileExists(t, path)
}
