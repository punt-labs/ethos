package hook

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/testhelpers"
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
		cmd := testhelpers.GitCommand(args...)
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

	out := BuildWorkingContext(dir)
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

	out := BuildWorkingContext(dir)
	assert.Contains(t, out, "Uncommitted changes: 2")
	assert.Contains(t, out, "README")
	assert.Contains(t, out, "new.txt")
}

func TestBuildWorkingContext_DetachedHEAD(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	env := gitEnv(dir)
	cmd := testhelpers.GitCommand("checkout", "--detach")
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

	result := BuildWorkingContext(dir)
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

	out := BuildWorkingContext("")
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

	out := BuildWorkingContext(dir)
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
	out := BuildWorkingContext(dir)
	assert.NotEmpty(t, out)
	assert.NotContains(t, out, "Unpushed commits")
}

// The section describes the checkout it is given, not the working directory:
// a hook started elsewhere (a subdirectory of another repository, a scratch
// directory) reports the located checkout's state.
func TestBuildWorkingContext_ReadsTheGivenCheckout(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644))
	t.Chdir(t.TempDir())
	t.Setenv("GIT_CEILING_DIRECTORIES", dir)

	out := BuildWorkingContext(dir)
	assert.Contains(t, out, "Branch: main")
	assert.Contains(t, out, "Uncommitted changes: 1")
}

// A git run from inside a hook (GIT_DIR exported) and under a translated
// locale still answers for the checkout, and a branch with no upstream is
// still no "Unpushed commits" line, not an unknown one.
func TestBuildWorkingContext_ParsedEnvironment(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	other := t.TempDir()
	initGitRepo(t, other)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")

	out := BuildWorkingContext(dir)
	assert.Contains(t, out, "Branch: main")
	assert.NotContains(t, out, "Unpushed commits")
	assert.NotContains(t, out, "unknown")
}

// A git that fails is said so in the section: the session is not told nothing.
func TestBuildWorkingContext_ReportsGitFailures(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	t.Run("status fails", func(t *testing.T) {
		bin := t.TempDir()
		real, err := exec.LookPath("git")
		require.NoError(t, err)
		script := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = status ] && { echo 'fatal: index file corrupt' >&2; exit 128; }; done\nexec " + real + " \"$@\"\n"
		require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755))
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		out := BuildWorkingContext(dir)
		assert.Contains(t, out, "Branch: main")
		assert.Contains(t, out, "Uncommitted changes: unknown")
		assert.Contains(t, out, "index file corrupt")
	})
	t.Run("git does not answer", func(t *testing.T) {
		testhelpers.HangingGit(t)
		orig := gitTimeout
		gitTimeout = 200 * time.Millisecond
		t.Cleanup(func() { gitTimeout = orig })
		start := time.Now()
		out := BuildWorkingContext(dir)
		assert.Less(t, time.Since(start), 2*time.Second, "one timeout, not one per question")
		assert.Contains(t, out, "Uncommitted changes: unknown")
		assert.Contains(t, out, "Unpushed commits: unknown")
		assert.Contains(t, out, "Branch: unknown")
		assert.Contains(t, out, "did not answer")
	})
}
