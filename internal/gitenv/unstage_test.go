package gitenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unstageRepo is a repository with one commit holding a tracked local file and
// a tracked ordinary file.
func unstageRepo(t *testing.T) (dir string, env []string) {
	t.Helper()
	dir = t.TempDir()
	home := t.TempDir()
	env = append(Parsed(),
		"HOME="+home, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x",
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	git := func(args ...string) {
		t.Helper()
		_, err := OutputEnv(dir, "git", env, args...)
		require.NoError(t, err, "git %v", args)
	}
	git("init", "-q", "-b", "main")
	write(t, dir, "keep.txt")
	write(t, dir, ".punt-labs/local/ethos/tracked.txt")
	git("add", "-f", "keep.txt", ".punt-labs/local/ethos/tracked.txt")
	git("-c", "commit.gpgsign=false", "commit", "-q", "-m", "base")
	return dir, env
}

func write(t *testing.T, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(rel+"\n"), 0o644))
}

func staged(t *testing.T, dir string, env []string) string {
	t.Helper()
	out, err := OutputEnv(dir, "git", env, "diff", "--cached", "--name-status")
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestUnstageUnder(t *testing.T) {
	dir, env := unstageRepo(t)
	write(t, dir, ".punt-labs/local/ethos/new.txt")
	write(t, dir, ".punt-labs/local/other/deep/new2.txt")
	write(t, dir, "src.txt")
	_, err := OutputEnv(dir, "git", env, "add", "-f", ".punt-labs/local/ethos/new.txt", ".punt-labs/local/other/deep/new2.txt", "src.txt")
	require.NoError(t, err)

	n, err := UnstageUnder(dir, env, ".punt-labs/local/")
	require.NoError(t, err)
	assert.Equal(t, 3, n, "two new paths and the one HEAD tracks")
	assert.Equal(t, "D\t.punt-labs/local/ethos/tracked.txt\nA\tsrc.txt", staged(t, dir, env), "the tracked path is staged for removal; the ordinary file is untouched")
	for _, rel := range []string{".punt-labs/local/ethos/new.txt", ".punt-labs/local/other/deep/new2.txt", ".punt-labs/local/ethos/tracked.txt"} {
		assert.FileExists(t, filepath.Join(dir, rel), "files stay on disk")
	}

	n, err = UnstageUnder(dir, env, ".punt-labs/local/")
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left to unstage")
}

func TestUnstageUnderEditsTheGivenIndex(t *testing.T) {
	dir, env := unstageRepo(t)
	private := filepath.Join(t.TempDir(), "private-index")
	data, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(private, data, 0o644))

	n, err := UnstageUnder(dir, WithIndex(env, private), ".punt-labs/local/")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, staged(t, dir, env), "the default index is untouched")
	assert.Equal(t, "D\t.punt-labs/local/ethos/tracked.txt", staged(t, dir, WithIndex(env, private)))
}

func TestUnstageUnderGitFailureCarriesGitsMessage(t *testing.T) {
	dir, env := unstageRepo(t)
	corrupt := filepath.Join(t.TempDir(), "corrupt-index")
	require.NoError(t, os.WriteFile(corrupt, []byte("not an index"), 0o644))
	_, err := UnstageUnder(dir, WithIndex(env, corrupt), ".punt-labs/local/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listing index paths under .punt-labs/local/")
	assert.Contains(t, err.Error(), "git ls-files")
}
