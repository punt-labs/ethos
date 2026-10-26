//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sshAdvice = "add-bundle runs ssh without a terminal"

// loggedGit puts a git wrapper first on PATH that records each invocation's
// subcommand in the returned file and runs the real git. A network subcommand
// in the log means add-bundle reached out.
func loggedGit(t *testing.T) (logFile string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	logFile = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nwhile [ \"$1\" = -c ]; do shift 2; done\necho \"$1\" >> '" + logFile + "'\nexec " + real + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

func assertNoNetwork(t *testing.T, logFile string) {
	t.Helper()
	b, _ := os.ReadFile(logFile)
	for _, c := range strings.Fields(string(b)) {
		assert.NotContains(t, []string{"clone", "submodule", "ls-remote", "fetch"}, c, "git reached out: %s", b)
	}
}

func runAddBundle(t *testing.T, url string) (stderr string, err error) {
	t.Helper()
	var eb bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&eb)
	err = runTeamAddBundle(cmd, url)
	return eb.String(), err
}

func bundleFlags(t *testing.T, global bool) {
	t.Helper()
	prevApply, prevGlobal, prevName := addBundleApply, addBundleGlobal, addBundleName
	addBundleApply, addBundleGlobal, addBundleName = true, global, ""
	t.Cleanup(func() { addBundleApply, addBundleGlobal, addBundleName = prevApply, prevGlobal, prevName })
}

// A destination git would refuse for a local reason is refused before git
// runs: no clone, no ls-remote, and no ssh advice for a failure that has
// nothing to do with ssh.
func TestAddBundleLocalPreconditionsAreRefusedBeforeGit(t *testing.T) {
	const url = "git@example.invalid:o/b.git"

	t.Run("global destination not empty", func(t *testing.T) {
		bundleFlags(t, true)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		dest := filepath.Join(home, ".punt-labs", "ethos", "bundles", "b")
		require.NoError(t, os.MkdirAll(dest, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dest, "x"), []byte("x"), 0o644))
		logFile := loggedGit(t)
		_, err := runAddBundle(t, url)
		require.Error(t, err)
		assert.Contains(t, err.Error(), dest)
		assert.Contains(t, err.Error(), "not an empty directory")
		assert.NotContains(t, err.Error(), sshAdvice)
		assertNoNetwork(t, logFile)
	})

	submodule := func(t *testing.T) string {
		bundleFlags(t, false)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		repo := t.TempDir()
		gitInitDir(t, repo, t.TempDir())
		t.Chdir(repo)
		return repo
	}

	t.Run("submodule path exists and is not a git repository", func(t *testing.T) {
		repo := submodule(t)
		dest := filepath.Join(repo, ".punt-labs", "ethos-bundles", "b")
		require.NoError(t, os.MkdirAll(dest, 0o755))
		logFile := loggedGit(t)
		_, err := runAddBundle(t, url)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not a git repository")
		assert.NotContains(t, err.Error(), sshAdvice)
		assertNoNetwork(t, logFile)
	})

	t.Run("submodule path is in the index", func(t *testing.T) {
		repo := submodule(t)
		rel := filepath.Join(".punt-labs", "ethos-bundles", "b", "f")
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(repo, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(repo, rel), []byte("x"), 0o644))
		gitCmd(t, repo, "add", rel)
		require.NoError(t, os.RemoveAll(filepath.Join(repo, ".punt-labs", "ethos-bundles", "b")))
		logFile := loggedGit(t)
		_, err := runAddBundle(t, url)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already in the index")
		assert.NotContains(t, err.Error(), sshAdvice)
		assertNoNetwork(t, logFile)
	})
}

// An unreadable system config is fatal for git, so add-bundle refuses before the
// clone and names the file; unreadable global and XDG files are named in a
// notice each, and the run goes on.
func TestAddBundleUnreadableConfigFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	unreadable := func(t *testing.T, name string) string {
		p := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(p, []byte("[core]\n\tsshCommand = ssh -i k\n"), 0o644))
		require.NoError(t, os.Chmod(p, 0o000))
		t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
		return p
	}

	t.Run("system", func(t *testing.T) {
		bundleFlags(t, true)
		t.Setenv("HOME", t.TempDir())
		sys := unreadable(t, "system")
		t.Setenv("GIT_CONFIG_SYSTEM", sys)
		t.Setenv("GIT_CONFIG_GLOBAL", unreadable(t, "global"))
		logFile := loggedGit(t)
		stderr, err := runAddBundle(t, "https://example.invalid/o/b.git")
		require.Error(t, err)
		assert.Contains(t, err.Error(), sys)
		assert.Contains(t, err.Error(), "git will refuse")
		assert.Contains(t, stderr, "git will ignore", "the global file is still named")
		assertNoNetwork(t, logFile)
	})

	t.Run("every global and XDG file is named", func(t *testing.T) {
		bundleFlags(t, true)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		xdg := filepath.Join(home, "xdg")
		require.NoError(t, os.MkdirAll(filepath.Join(xdg, "git"), 0o755))
		t.Setenv("XDG_CONFIG_HOME", xdg)
		xp := filepath.Join(xdg, "git", "config")
		hp := filepath.Join(home, ".gitconfig")
		for _, p := range []string{xp, hp} {
			require.NoError(t, os.WriteFile(p, []byte("[core]\n\tsshCommand = ssh -i k\n"), 0o644))
			require.NoError(t, os.Chmod(p, 0o000))
			t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
		}
		bin := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 1\n"), 0o755))
		t.Setenv("PATH", bin)
		stderr, err := runAddBundle(t, "https://example.invalid/o/b.git")
		require.Error(t, err)
		assert.Contains(t, stderr, "git will ignore "+xp)
		assert.Contains(t, stderr, "git will ignore "+hp)
	})
}

// What git accepts is not refused: an existing clone at the submodule path is
// handed to git submodule add, which adopts it, and a --global destination that
// is a symlink to an empty directory is cloned into.
func TestAddBundleLeavesToGitWhatGitAccepts(t *testing.T) {
	const url = "https://example.invalid/o/b.git"

	t.Run("pre-cloned submodule path", func(t *testing.T) {
		bundleFlags(t, false)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		repo := t.TempDir()
		gitInitDir(t, repo, t.TempDir())
		dest := filepath.Join(repo, ".punt-labs", "ethos-bundles", "b")
		require.NoError(t, os.MkdirAll(dest, 0o755))
		gitInitDir(t, dest, t.TempDir())
		t.Chdir(repo)
		logFile := loggedGit(t)
		_, _ = runAddBundle(t, url)
		b, _ := os.ReadFile(logFile)
		assert.Contains(t, string(b), "submodule", "git submodule add was run, not refused beforehand")
	})

	t.Run("global symlink to an empty directory", func(t *testing.T) {
		bundleFlags(t, true)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		empty := t.TempDir()
		dest := filepath.Join(home, ".punt-labs", "ethos", "bundles", "b")
		require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
		require.NoError(t, os.Symlink(empty, dest))
		logFile := filepath.Join(t.TempDir(), "calls.log")
		bin := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nwhile [ \"$1\" = -c ]; do shift 2; done\necho \"$1\" >> '"+logFile+"'\nexit 0\n"), 0o755))
		t.Setenv("PATH", bin)
		_, err := runAddBundle(t, url)
		require.NoError(t, err)
		b, _ := os.ReadFile(logFile)
		assert.Contains(t, string(b), "clone")
	})

	t.Run("global symlink to nothing is refused", func(t *testing.T) {
		bundleFlags(t, true)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		dest := filepath.Join(home, ".punt-labs", "ethos", "bundles", "b")
		require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
		require.NoError(t, os.Symlink(filepath.Join(home, "absent"), dest))
		logFile := loggedGit(t)
		_, err := runAddBundle(t, url)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink to nothing")
		assertNoNetwork(t, logFile)
	})
}
