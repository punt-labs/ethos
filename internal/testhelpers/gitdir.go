package testhelpers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// MakeGitDir creates dir/.git as the minimal directory git accepts as a git
// directory (HEAD, objects/, refs/), so a fixture is found by a repository
// walk as git itself would find it. An empty .git/ is skipped by git.
func MakeGitDir(t *testing.T, dir string) {
	t.Helper()
	gd := filepath.Join(dir, ".git")
	require.NoError(t, os.MkdirAll(filepath.Join(gd, "objects"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(gd, "refs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gd, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
}
