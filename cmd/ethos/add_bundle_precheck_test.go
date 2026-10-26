//go:build linux || darwin

package main

import (
	"bytes"
	"errors"
	"io/fs"
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
	b, err := os.ReadFile(logFile)
	if errors.Is(err, fs.ErrNotExist) {
		return // git was never run
	}
	require.NoError(t, err)
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
		b, err := os.ReadFile(logFile)
		require.NoError(t, err)
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
		require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nwhile [ \"$1\" = -c ] || [ \"$1\" = -C ]; do shift 2; done\necho \"$1\" >> '"+logFile+"'\nexit 0\n"), 0o755))
		t.Setenv("PATH", bin)
		_, err := runAddBundle(t, url)
		require.NoError(t, err)
		b, err := os.ReadFile(logFile)
		require.NoError(t, err)
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

// isOwnGitRepo is git's own adoption test: only git's "not a gitdir" or "not a
// git repository", or an absent .git, answers false; a timeout or any other git
// failure is the error, carrying git's stderr.
func TestIsOwnGitRepoReportsFailures(t *testing.T) {
	withDotGit := func(t *testing.T) string {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o755))
		return dir
	}
	stub := func(t *testing.T, script string) {
		bin := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+script), 0o755))
		t.Setenv("PATH", bin)
	}

	t.Run("no .git", func(t *testing.T) {
		ok, _, err := isOwnGitRepo(t.TempDir())
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("timeout", func(t *testing.T) {
		dir := withDotGit(t)
		hangGit(t)
		ok, _, err := isOwnGitRepo(dir)
		assert.False(t, ok)
		require.ErrorIs(t, err, gitenv.ErrTimeout)
		assert.NotContains(t, err.Error(), "not a git repository")
	})

	t.Run("a failure with no stderr names the exit", func(t *testing.T) {
		dir := withDotGit(t)
		stub(t, "exit 3\n")
		ok, _, err := isOwnGitRepo(dir)
		assert.False(t, ok)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exit status 3")
		var ee *exec.ExitError
		assert.ErrorAs(t, err, &ee, "the exit error is in the chain")
		assert.False(t, strings.HasSuffix(err.Error(), ": "), err.Error())
	})

	t.Run("another git failure", func(t *testing.T) {
		dir := withDotGit(t)
		stub(t, "echo 'fatal: detected dubious ownership' >&2\nexit 128\n")
		ok, _, err := isOwnGitRepo(dir)
		assert.False(t, ok)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "dubious ownership")
	})

	t.Run("an empty .git directory", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		ok, _, err := isOwnGitRepo(withDotGit(t))
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("a gitfile to nothing", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /nonexistent\n"), 0o644))
		ok, _, err := isOwnGitRepo(dir)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("an empty .git file", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not installed")
		}
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), nil, 0o644))
		ok, _, err := isOwnGitRepo(dir)
		assert.False(t, ok)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid gitfile format")
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

// git's adoption test decides, not a guess at it: a clone whose config makes
// show-toplevel fail or name another tree (core.worktree elsewhere or missing,
// core.bare true) is still a repository of its own, and is handed to git
// submodule add, as on main.
func TestAddBundleAdoptionFollowsGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	setConfig := func(t *testing.T, dir, key, val string) {
		t.Helper()
		out, err := exec.Command("git", "config", "-f", filepath.Join(dir, ".git", "config"), key, val).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	clone := func(t *testing.T) string {
		dir := filepath.Join(t.TempDir(), "c")
		gitInitDir(t, dir, t.TempDir())
		return dir
	}

	for name, set := range map[string]func(t *testing.T, dir string){
		"core.worktree elsewhere":         func(t *testing.T, d string) { setConfig(t, d, "core.worktree", t.TempDir()) },
		"core.bare true":                  func(t *testing.T, d string) { setConfig(t, d, "core.bare", "true") },
		"core.worktree relative, missing": func(t *testing.T, d string) { setConfig(t, d, "core.worktree", "sub") },
		"core.worktree absolute, missing": func(t *testing.T, d string) { setConfig(t, d, "core.worktree", "/nonexistent/wt") },
	} {
		t.Run(name, func(t *testing.T) {
			dir := clone(t)
			set(t, dir)
			ok, resolved, err := isOwnGitRepo(dir)
			require.NoError(t, err)
			assert.True(t, ok)
			assert.NotEmpty(t, resolved)
		})
	}

	t.Run("a gitfile to the git dir", func(t *testing.T) {
		src := clone(t)
		dir := filepath.Join(t.TempDir(), "g")
		require.NoError(t, os.Mkdir(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(src, ".git")+"\n"), 0o644))
		ok, resolved, err := isOwnGitRepo(dir)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, filepath.Join(src, ".git"), resolved)
	})

	t.Run("plain directory", func(t *testing.T) {
		ok, _, err := isOwnGitRepo(t.TempDir())
		require.NoError(t, err)
		assert.False(t, ok)
	})

	for _, name := range []string{"core.worktree elsewhere", "core.bare true", "core.worktree relative, missing", "core.worktree absolute, missing"} {
		t.Run("add-bundle hands it to git submodule add: "+name, func(t *testing.T) {
			bundleFlags(t, false)
			t.Setenv("HOME", t.TempDir())
			repo := t.TempDir()
			gitInitDir(t, repo, t.TempDir())
			dest := filepath.Join(repo, ".punt-labs", "ethos-bundles", "b")
			require.NoError(t, os.MkdirAll(dest, 0o755))
			gitInitDir(t, dest, t.TempDir())
			switch name {
			case "core.worktree elsewhere":
				setConfig(t, dest, "core.worktree", t.TempDir())
			case "core.bare true":
				setConfig(t, dest, "core.bare", "true")
			case "core.worktree relative, missing":
				setConfig(t, dest, "core.worktree", "sub")
			default:
				setConfig(t, dest, "core.worktree", "/nonexistent/wt")
			}
			t.Chdir(repo)
			logFile := loggedGit(t)
			_, _ = runAddBundle(t, "https://example.invalid/o/b.git")
			b, err := os.ReadFile(logFile)
			require.NoError(t, err)
			assert.Contains(t, string(b), "submodule")
		})
	}
}

// A bundle path whose .git is the superproject's own git dir (a symlink or a
// gitfile to it) is refused: git submodule add would stage no gitlink, or a
// gitlink to the superproject's own HEAD.
func TestAddBundleRefusesTheSuperprojectsOwnGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for name, link := range map[string]func(t *testing.T, repo, dest string){
		"symlink": func(t *testing.T, repo, dest string) {
			require.NoError(t, os.Symlink(filepath.Join(repo, ".git"), filepath.Join(dest, ".git")))
		},
		"gitfile": func(t *testing.T, repo, dest string) {
			require.NoError(t, os.WriteFile(filepath.Join(dest, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git")+"\n"), 0o644))
		},
	} {
		t.Run(name, func(t *testing.T) {
			bundleFlags(t, false)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			repo := t.TempDir()
			gitInitDir(t, repo, t.TempDir())
			dest := filepath.Join(repo, ".punt-labs", "ethos-bundles", "b")
			require.NoError(t, os.MkdirAll(dest, 0o755))
			link(t, repo, dest)
			t.Chdir(repo)
			logFile := loggedGit(t)
			_, err := runAddBundle(t, "https://example.invalid/o/b.git")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "the bundle path's .git is this repository's own git dir")
			assertNoNetwork(t, logFile)
		})
	}
}
