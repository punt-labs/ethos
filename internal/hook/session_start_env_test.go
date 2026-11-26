package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// Claude Code sources CLAUDE_ENV_FILE as a shell preamble before each Bash
	// tool command. Live Claude Code is not invoked in unit tests, so source the
	// generated file in bash directly and verify both inheritance and that a
	// subsequent git status leaves a stale .git/index untouched.
	t.Run("sourced in bash propagates GIT_OPTIONAL_LOCKS=0 to git status", func(t *testing.T) {
		bash, err := exec.LookPath("bash")
		if err != nil {
			t.Skip("bash not available")
		}
		dir := t.TempDir()
		initGitRepo(t, dir)

		envFile := filepath.Join(t.TempDir(), "claude-env.sh")
		t.Setenv("CLAUDE_ENV_FILE", envFile)
		unsetEnv(t, "GIT_OPTIONAL_LOCKS")
		exportSessionEnv()

		readme := filepath.Join(dir, "README")
		staleTime := time.Now().Add(-5 * time.Second).Truncate(time.Second)
		require.NoError(t, os.Chtimes(readme, staleTime, staleTime))

		indexPath := filepath.Join(dir, ".git", "index")
		indexTime := time.Now().Add(-60 * time.Second).Truncate(time.Second)
		require.NoError(t, os.Chtimes(indexPath, indexTime, indexTime))
		beforeInfo, err := os.Stat(indexPath)
		require.NoError(t, err)

		cmd := exec.Command(bash, "-c", `. "$1" && printf '%s' "$GIT_OPTIONAL_LOCKS" && git status --porcelain`, "_", envFile)
		cmd.Dir = dir
		cmd.Env = gitEnv(dir)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "bash sourcing CLAUDE_ENV_FILE failed: %s", out)
		assert.Equal(t, "0", strings.TrimSpace(string(out)))

		afterInfo, err := os.Stat(indexPath)
		require.NoError(t, err)
		assert.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime(),
			"git status after sourcing CLAUDE_ENV_FILE must not refresh .git/index")
	})
}

func TestRepoEnabledMarker(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, repoEnabled(""), "empty checkout is not enabled")
	assert.False(t, repoEnabled(dir), "repo without marker is not enabled")

	marker := filepath.Join(dir, enabledMarkerRel)
	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o755))
	require.NoError(t, os.WriteFile(marker, nil, 0o644))
	assert.True(t, repoEnabled(dir), "repo with regular marker is enabled")

	// After ethos disable removes the marker, SessionStart must not export.
	require.NoError(t, os.Remove(marker))
	assert.False(t, repoEnabled(dir), "disabled repo must not be treated as enabled")
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
