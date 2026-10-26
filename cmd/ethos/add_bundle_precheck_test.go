//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
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

// What git accepts is not refused: an existing repository at the submodule path
// is handed to git submodule add (the fixture has no commit, so git itself
// refuses it; the insteadOf and relative-URL cases are in
// TestAddBundleHandsAnExistingCloneToGit), and a --global destination that is a
// symlink to an empty directory is cloned into.
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

// What git would do other than the user meant is refused before git runs: a
// symlink at the submodule path (git stages the link), a symlink that cannot be
// followed.
func TestAddBundleRefusesWhatGitWouldDoWrong(t *testing.T) {
	const url = "https://example.invalid/o/b.git"
	submodule := func(t *testing.T) (repo, dest string) {
		bundleFlags(t, false)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		repo = t.TempDir()
		gitInitDir(t, repo, t.TempDir())
		dest = filepath.Join(repo, ".punt-labs", "ethos-bundles", "b")
		require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
		t.Chdir(repo)
		return repo, dest
	}

	t.Run("symlink to a clone", func(t *testing.T) {
		_, dest := submodule(t)
		clone := t.TempDir()
		gitInitDir(t, clone, t.TempDir())
		require.NoError(t, os.Symlink(clone, dest))
		logFile := loggedGit(t)
		_, err := runAddBundle(t, url)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a symlink; git submodule add would stage the link, not a submodule")
		assertNoNetwork(t, logFile)
	})

	t.Run("global symlink that cannot be followed", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores permissions")
		}
		bundleFlags(t, true)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		locked := filepath.Join(t.TempDir(), "locked")
		require.NoError(t, os.MkdirAll(filepath.Join(locked, "x"), 0o755))
		require.NoError(t, os.Chmod(locked, 0o000))
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		dest := filepath.Join(home, ".punt-labs", "ethos", "bundles", "b")
		require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
		require.NoError(t, os.Symlink(filepath.Join(locked, "x"), dest))
		_, err := runAddBundle(t, url)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is a symlink that cannot be followed")
		assert.ErrorIs(t, err, syscall.EACCES)
	})
}

// isOwnGitRepo answers "not a git repository" only for git's own answer or a
// different top level; a timeout or another git failure is the error.
func TestIsOwnGitRepoReportsFailures(t *testing.T) {
	dir := t.TempDir()

	t.Run("timeout", func(t *testing.T) {
		hangGit(t)
		ok, err := isOwnGitRepo(dir)
		assert.False(t, ok)
		require.ErrorIs(t, err, gitenv.ErrTimeout)
		assert.NotContains(t, err.Error(), "not a git repository")
	})

	t.Run("a failure with no stderr names the exit", func(t *testing.T) {
		bin := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 3\n"), 0o755))
		t.Setenv("PATH", bin)
		ok, err := isOwnGitRepo(dir)
		assert.False(t, ok)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exit status 3")
		var ee *exec.ExitError
		assert.ErrorAs(t, err, &ee, "the exit error is in the chain")
		assert.False(t, strings.HasSuffix(err.Error(), ": "), err.Error())
	})

	t.Run("a top level that cannot be examined", func(t *testing.T) {
		bin := t.TempDir()
		gone := filepath.Join(t.TempDir(), "gone")
		require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho '"+gone+"'\n"), 0o755))
		t.Setenv("PATH", bin)
		ok, err := isOwnGitRepo(dir)
		assert.False(t, ok)
		require.ErrorIs(t, err, os.ErrNotExist)
		assert.Contains(t, err.Error(), gone)
	})

	t.Run("another git failure", func(t *testing.T) {
		bin := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho 'fatal: detected dubious ownership' >&2\nexit 128\n"), 0o755))
		t.Setenv("PATH", bin)
		ok, err := isOwnGitRepo(dir)
		assert.False(t, ok)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dubious ownership")
	})

	t.Run("not a repository", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
		ok, err := isOwnGitRepo(dir)
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

// An existing clone reaches git submodule add whatever its origin looks like:
// one set through insteadOf (https rewritten to ssh), and one made from a
// relative URL, are clones of exactly the URL given.
func TestAddBundleHandsAnExistingCloneToGit(t *testing.T) {
	setup := func(t *testing.T) (repo, dest string) {
		bundleFlags(t, false)
		t.Setenv("HOME", t.TempDir())
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		repo = t.TempDir()
		gitInitDir(t, repo, t.TempDir())
		dest = filepath.Join(repo, ".punt-labs", "ethos-bundles", "b")
		require.NoError(t, os.MkdirAll(dest, 0o755))
		gitInitDir(t, dest, t.TempDir())
		t.Chdir(repo)
		return repo, dest
	}
	ran := func(logFile string) string {
		b, err := os.ReadFile(logFile)
		require.NoError(t, err)
		return string(b)
	}

	t.Run("origin through insteadOf", func(t *testing.T) {
		_, dest := setup(t)
		// The rewrite is in the clone's own config, where git reads it for dest.
		gitCmd(t, dest, "config", "url.ssh://git@example.invalid/.insteadOf", "https://example.invalid/")
		gitCmd(t, dest, "remote", "add", "origin", "https://example.invalid/o/b.git")
		logFile := loggedGit(t)
		_, _ = runAddBundle(t, "https://example.invalid/o/b.git")
		assert.Contains(t, ran(logFile), "submodule")
	})

	t.Run("relative URL", func(t *testing.T) {
		repo, dest := setup(t)
		// "../src" resolved against the superproject, as git records it.
		gitCmd(t, dest, "remote", "add", "origin", filepath.Join(filepath.Dir(repo), "src"))
		addBundleName = "b" // the name would otherwise come from "../src"
		logFile := loggedGit(t)
		_, _ = runAddBundle(t, "../src")
		assert.Contains(t, ran(logFile), "submodule")
	})
}

// A clone whose core.worktree puts the work tree elsewhere is still a repository
// of its own: git submodule add adopts it, as on main. One whose core.worktree
// git cannot enter reports git's error, not "not a git repository".
func TestIsOwnGitRepoWithACoreWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	clone := func(t *testing.T, worktree string) string {
		dir := filepath.Join(t.TempDir(), "c")
		gitInitDir(t, dir, t.TempDir())
		require.NoError(t, exec.Command("git", "config", "-f", filepath.Join(dir, ".git", "config"), "core.worktree", worktree).Run())
		return dir
	}

	t.Run("elsewhere", func(t *testing.T) {
		ok, err := isOwnGitRepo(clone(t, t.TempDir()))
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("a gitfile to the git dir", func(t *testing.T) {
		src := clone(t, t.TempDir())
		dir := filepath.Join(t.TempDir(), "g")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(src, ".git")+"\n"), 0o644))
		ok, err := isOwnGitRepo(dir)
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("unenterable", func(t *testing.T) {
		ok, err := isOwnGitRepo(clone(t, "sub"))
		assert.False(t, ok)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot chdir")
	})

	t.Run("adopted through add-bundle", func(t *testing.T) {
		bundleFlags(t, false)
		t.Setenv("HOME", t.TempDir())
		repo := t.TempDir()
		gitInitDir(t, repo, t.TempDir())
		dest := filepath.Join(repo, ".punt-labs", "ethos-bundles", "b")
		require.NoError(t, os.MkdirAll(dest, 0o755))
		gitInitDir(t, dest, t.TempDir())
		require.NoError(t, exec.Command("git", "config", "-f", filepath.Join(dest, ".git", "config"), "core.worktree", t.TempDir()).Run())
		t.Chdir(repo)
		logFile := loggedGit(t)
		_, _ = runAddBundle(t, "https://example.invalid/o/b.git")
		b, err := os.ReadFile(logFile)
		require.NoError(t, err)
		assert.Contains(t, string(b), "submodule")
	})
}
