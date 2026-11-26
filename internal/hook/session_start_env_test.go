package hook

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExportSessionEnv(t *testing.T) {
	t.Run("writes export when CLAUDE_ENV_FILE is set and var unset", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "claude-env.sh")
		t.Setenv("CLAUDE_ENV_FILE", envFile)
		unsetEnv(t, "GIT_OPTIONAL_LOCKS")

		exportSessionEnv()

		data, err := os.ReadFile(envFile)
		require.NoError(t, err)
		assert.Equal(t, "export GIT_OPTIONAL_LOCKS=0\n", string(data))

		// Second call is idempotent.
		exportSessionEnv()
		data, err = os.ReadFile(envFile)
		require.NoError(t, err)
		assert.Equal(t, "export GIT_OPTIONAL_LOCKS=0\n", string(data))
	})

	t.Run("appends with newline separator to existing file", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "claude-env.sh")
		require.NoError(t, os.WriteFile(envFile, []byte("export FOO=bar"), 0o600))
		t.Setenv("CLAUDE_ENV_FILE", envFile)
		unsetEnv(t, "GIT_OPTIONAL_LOCKS")

		exportSessionEnv()

		data, err := os.ReadFile(envFile)
		require.NoError(t, err)
		assert.Equal(t, "export FOO=bar\nexport GIT_OPTIONAL_LOCKS=0\n", string(data))
	})

	t.Run("preserves existing process env override", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "claude-env.sh")
		t.Setenv("CLAUDE_ENV_FILE", envFile)
		t.Setenv("GIT_OPTIONAL_LOCKS", "1")

		exportSessionEnv()

		_, err := os.Stat(envFile)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("preserves existing assignment in CLAUDE_ENV_FILE", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "claude-env.sh")
		initial := "export GIT_OPTIONAL_LOCKS=1\n"
		require.NoError(t, os.WriteFile(envFile, []byte(initial), 0o600))
		t.Setenv("CLAUDE_ENV_FILE", envFile)
		unsetEnv(t, "GIT_OPTIONAL_LOCKS")

		exportSessionEnv()

		data, err := os.ReadFile(envFile)
		require.NoError(t, err)
		assert.Equal(t, initial, string(data))
	})

	t.Run("ignores commented assignment in CLAUDE_ENV_FILE", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "claude-env.sh")
		initial := "# export GIT_OPTIONAL_LOCKS=1\n"
		require.NoError(t, os.WriteFile(envFile, []byte(initial), 0o600))
		t.Setenv("CLAUDE_ENV_FILE", envFile)
		unsetEnv(t, "GIT_OPTIONAL_LOCKS")

		exportSessionEnv()

		data, err := os.ReadFile(envFile)
		require.NoError(t, err)
		assert.Equal(t, initial+"export GIT_OPTIONAL_LOCKS=0\n", string(data))
	})

	t.Run("refuses symlink without modifying target", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.sh")
		require.NoError(t, os.WriteFile(target, []byte("keep\n"), 0o600))
		link := filepath.Join(dir, "link.sh")
		require.NoError(t, os.Symlink(target, link))
		t.Setenv("CLAUDE_ENV_FILE", link)
		unsetEnv(t, "GIT_OPTIONAL_LOCKS")

		exportSessionEnv()

		data, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "keep\n", string(data))
	})
}

func TestBuildWorkingContext_DoesNotRefreshIndex(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	unsetEnv(t, "GIT_OPTIONAL_LOCKS")

	// Make the stat cache stale without changing file content, then pin
	// .git/index mtime to a known past timestamp. Without --no-optional-locks,
	// git status refreshes .git/index via .git/index.lock and updates its mtime.
	readme := filepath.Join(dir, "README")
	staleTime := time.Now().Add(-5 * time.Second).Truncate(time.Second)
	require.NoError(t, os.Chtimes(readme, staleTime, staleTime))

	indexPath := filepath.Join(dir, ".git", "index")
	indexTime := time.Now().Add(-60 * time.Second).Truncate(time.Second)
	require.NoError(t, os.Chtimes(indexPath, indexTime, indexTime))

	beforeInfo, err := os.Stat(indexPath)
	require.NoError(t, err)

	out := BuildWorkingContext(dir)
	assert.Contains(t, out, "Uncommitted changes: 0")

	afterInfo, err := os.Stat(indexPath)
	require.NoError(t, err)
	assert.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime(),
		"BuildWorkingContext must not refresh .git/index via optional lock")
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	if prev, ok := os.LookupEnv(key); ok {
		t.Cleanup(func() { _ = os.Setenv(key, prev) })
		require.NoError(t, os.Unsetenv(key))
	} else {
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}
}
