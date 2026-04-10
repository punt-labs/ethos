package hook

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitEnv returns environment variables that isolate git from the host.
func gitEnv(dir string) []string {
	return []string{
		"HOME=" + dir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"PATH=" + os.Getenv("PATH"),
	}
}

// initGitRepo creates a git repo in dir with one committed file.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	env := gitEnv(dir)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %s failed: %s", strings.Join(args, " "), out)
	}

	run("init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("hello\n"), 0o644))
	run("add", "README")
	run("commit", "-m", "initial")
}

func TestBuildWorkingContext_CleanRepo(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	// Isolate git config in the test process too.
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	out := BuildWorkingContext()
	assert.Contains(t, out, "## Working Context")
	assert.Contains(t, out, "Branch: main")
	assert.Contains(t, out, "Uncommitted changes: 0")
	// No upstream configured, so no "Unpushed commits" line.
	assert.NotContains(t, out, "Unpushed commits")
}

func TestBuildWorkingContext_DirtyRepo(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Create an untracked file and modify a tracked file.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("changed\n"), 0o644))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	out := BuildWorkingContext()
	assert.Contains(t, out, "Uncommitted changes: 2")
	assert.Contains(t, out, "README")
	assert.Contains(t, out, "new.txt")
}

func TestBuildWorkingContext_DetachedHEAD(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	env := gitEnv(dir)
	cmd := exec.Command("git", "checkout", "--detach")
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git checkout --detach failed: %s", out)

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	result := BuildWorkingContext()
	assert.Contains(t, result, "## Working Context")
	assert.Contains(t, result, "Branch:")
	// Should not say "main" since we're detached.
	assert.NotContains(t, result, "Branch: main")

	// Extract the branch value -- it should be a short SHA (7+ hex chars).
	for _, line := range strings.Split(result, "\n") {
		if strings.HasPrefix(line, "Branch: ") {
			sha := strings.TrimPrefix(line, "Branch: ")
			assert.Regexp(t, `^[0-9a-f]{7,}$`, sha, "detached HEAD should show short SHA")
			break
		}
	}
}

func TestBuildWorkingContext_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	// Ceiling must be the parent -- git only stops traversal above
	// an ancestor, not above the cwd itself.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	out := BuildWorkingContext()
	assert.Equal(t, "", out)
}

func TestBuildWorkingContext_ManyDirtyFiles(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Create 25 untracked files.
	for i := 0; i < 25; i++ {
		name := fmt.Sprintf("file-%02d.txt", i)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644))
	}

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	out := BuildWorkingContext()
	assert.Contains(t, out, "Uncommitted changes: 25")
	assert.Contains(t, out, "... and 5 more")

	// Count indented lines (file paths).
	pathLines := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "  ...") {
			pathLines++
		}
	}
	assert.Equal(t, 20, pathLines, "should show exactly 20 file paths")
}

func TestBuildWorkingContext_NoUpstreamNoCrash(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")

	// Should not panic and should not include "Unpushed commits".
	out := BuildWorkingContext()
	assert.NotEmpty(t, out)
	assert.NotContains(t, out, "Unpushed commits")
}
