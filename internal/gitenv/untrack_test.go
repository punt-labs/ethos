package gitenv

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUntrackPaths(t *testing.T) {
	dir, env := unstageRepo(t)
	t.Setenv("HOME", t.TempDir())
	_ = env
	err := UntrackPaths(context.Background(), dir, []string{".punt-labs/local/ethos/tracked.txt"})
	require.NoError(t, err)
	assert.Equal(t, "D\t.punt-labs/local/ethos/tracked.txt", staged(t, dir, env))
	assert.FileExists(t, filepath.Join(dir, ".punt-labs/local/ethos/tracked.txt"))
	require.NoError(t, UntrackPaths(context.Background(), dir, nil), "no paths is a no-op")
}

func TestUntrackPathsBusyIndex(t *testing.T) {
	dir, _ := unstageRepo(t)
	lock := filepath.Join(dir, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lock, nil, 0o644))
	err := UntrackPaths(context.Background(), dir, []string{".punt-labs/local/ethos/tracked.txt"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIndexBusy)
}
