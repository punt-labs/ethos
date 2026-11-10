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

// untrackRepo tracks every path in rels, force-added, in one commit.
func untrackRepo(t *testing.T, rels ...string) (dir string, env []string) {
	t.Helper()
	dir, env = unstageRepo(t)
	for _, rel := range rels {
		write(t, dir, rel)
		_, err := OutputEnv(dir, "git", env, "add", "-f", rel)
		require.NoError(t, err)
	}
	_, err := OutputEnv(dir, "git", env, "-c", "commit.gpgsign=false", "commit", "-q", "-m", "more")
	require.NoError(t, err)
	return dir, env
}

func TestUntrackPathsIsLiteral(t *testing.T) {
	const star = "s/audit-*.jsonl"
	const sibling = "s/audit-1-2.jsonl"
	dir, env := untrackRepo(t, star, sibling)
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, UntrackPaths(context.Background(), dir, []string{star}))
	assert.Equal(t, "D\t"+star, staged(t, dir, env), "only the literal path is untracked")
}

func TestUnstageUnderIsLiteral(t *testing.T) {
	dir, env := untrackRepo(t, "p/a*b", "p/axb")
	n, err := UnstageUnder(dir, env, "p/")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, "D\tp/a*b\nD\tp/axb", staged(t, dir, env))
	// A glob in the listing must not widen the removal either.
	dir, env = untrackRepo(t, "q/a*b", "q/axb")
	require.NoError(t, UntrackPaths(context.Background(), dir, []string{"q/a*b"}))
	assert.Equal(t, "D\tq/a*b", staged(t, dir, env))
}

func TestUntrackPathsPartlyStaged(t *testing.T) {
	dir, env := untrackRepo(t, ".punt-labs/local/ethos/part.txt")
	t.Setenv("HOME", t.TempDir())
	p := filepath.Join(dir, ".punt-labs/local/ethos/part.txt")
	require.NoError(t, os.WriteFile(p, []byte("staged\n"), 0o644))
	_, err := OutputEnv(dir, "git", env, "add", "-f", ".punt-labs/local/ethos/part.txt")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, []byte("edited after staging\n"), 0o644))

	require.NoError(t, UntrackPaths(context.Background(), dir, []string{".punt-labs/local/ethos/part.txt"}))
	assert.Equal(t, "D\t.punt-labs/local/ethos/part.txt", staged(t, dir, env))
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	assert.Equal(t, "edited after staging\n", string(b), "the working file is untouched")

	dir, env = untrackRepo(t, ".punt-labs/local/ethos/part.txt")
	p = filepath.Join(dir, ".punt-labs/local/ethos/part.txt")
	require.NoError(t, os.WriteFile(p, []byte("staged\n"), 0o644))
	_, err = OutputEnv(dir, "git", env, "add", "-f", ".punt-labs/local/ethos/part.txt")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, []byte("edited after staging\n"), 0o644))
	n, err := UnstageUnder(dir, env, ".punt-labs/local/")
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}

func TestUntrackPathsBusyDetectionIsExact(t *testing.T) {
	// A path that merely contains "index.lock" is not a busy index.
	dir, _ := untrackRepo(t, "x/index.lock.txt")
	t.Setenv("HOME", t.TempDir())
	err := UntrackPaths(context.Background(), dir, []string{"x/absent-index.lock"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrIndexBusy)
}
