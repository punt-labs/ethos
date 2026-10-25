package resolve

import (
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

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// fakeGit makes PATH hold only a git that runs the sh script body, and
// returns the directory it is in.
func fakeGit(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+body), 0o755))
	t.Setenv("PATH", dir)
	return dir
}

// initRepo makes dir a repository with one commit, using the real git.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := testhelpers.GitCommand(append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
}

// Item 1: git's messages are read in the C locale. A translated "not a git
// repository" must still be ErrNotInRepo, not a refusal.
func TestNotARepoInAnyLocale(t *testing.T) {
	t.Run("fake git answers in German unless LC_ALL=C", func(t *testing.T) {
		fakeGit(t, `[ "$1" = --version ] && { echo "git version 2.43.0"; exit 0; }
if [ "$LC_ALL" = C ] && [ -z "$LANGUAGE" ]; then
	echo "fatal: not a git repository (or any of the parent directories): .git" >&2
else
	echo "fatal: Kein Git-Repository (oder irgendeines der Elternverzeichnisse): .git" >&2
fi
exit 128
`)
		t.Setenv("LC_ALL", "de_DE.UTF-8")
		t.Setenv("LANGUAGE", "de")
		t.Chdir(t.TempDir())
		_, err := LocateRepoRoot()
		assert.ErrorIs(t, err, ErrNotInRepo)
	})
	t.Run("real git", func(t *testing.T) {
		requireGit(t)
		dir := t.TempDir()
		cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
		cmd.Env = append(os.Environ(), "LC_ALL=de_DE.UTF-8", "LANGUAGE=de")
		out, _ := cmd.CombinedOutput()
		if strings.Contains(string(out), "not a git repository") {
			t.Skip("git has no German messages on this host")
		}
		t.Setenv("LC_ALL", "de_DE.UTF-8")
		t.Setenv("LANGUAGE", "de")
		t.Chdir(dir)
		_, err := LocateRepoRoot()
		assert.ErrorIs(t, err, ErrNotInRepo)
	})
}

// Item 2: from a subdirectory git prints the common dir relative to the
// directory it ran in; the store still resolves to the work tree.
func TestRelativeCommonDirFromSubdirectory(t *testing.T) {
	requireGit(t)
	main := filepath.Join(t.TempDir(), "main")
	initRepo(t, main)
	sub := filepath.Join(main, "a", "b")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	t.Chdir(sub)
	root, err := LocateRepoRoot()
	require.NoError(t, err)
	store, err := LocateStoreRoot()
	require.NoError(t, err)
	assert.Equal(t, realpath(t, main), realpath(t, root))
	assert.Equal(t, realpath(t, main), realpath(t, store))
}

// Item 2: a git older than ethos's rev-parse form does not get a refusal. A
// git before 2.31 echoes an option it does not know (--path-format) as a
// line of output; ethos no longer asks for it. Output that is not three
// paths falls back to the hand rules with one note naming the version.
func TestOldGitOutput(t *testing.T) {
	cases := map[string]struct {
		body string
		hand bool // the hand rules decide
	}{
		"git 2.25 ignores nothing ethos asks": {body: `[ "$1" = --version ] && { echo "git version 2.25.1"; exit 0; }
shift 2 # -C dir
for a in "$@"; do case "$a" in --path-format=*) echo "$a";; esac; done
echo "$PWD_TOP"; echo "$PWD_TOP/.git"; echo ".git"
`},
		"two lines": {body: `[ "$1" = --version ] && { echo "git version 2.12.5"; exit 0; }
echo "$PWD_TOP"; echo ".git"
`, hand: true},
		"unknown option echoed": {body: `[ "$1" = --version ] && { echo "git version 2.12.5"; exit 0; }
echo "$PWD_TOP"; echo "--absolute-git-dir"; echo ".git"
`, hand: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			testhelpers.MakeGitDir(t, dir)
			t.Setenv("PWD_TOP", dir)
			fakeGit(t, c.body)
			t.Chdir(dir)
			var got string
			var err error
			out := captureStderr(t, func() { got, err = LocateStoreRoot() })
			require.NoError(t, err)
			assert.Equal(t, dir, got)
			if c.hand {
				assert.Contains(t, out, "2.12.5")
				assert.Contains(t, out, "hand rules")
			} else {
				assert.Empty(t, out)
			}
		})
	}
}

// Item 3: a git that does not run (the macOS xcrun shim without the command
// line tools) is probed once and the hand rules decide, with one note.
func TestGitThatDoesNotRun(t *testing.T) {
	count := filepath.Join(t.TempDir(), "count")
	fakeGit(t, `echo x >> "`+count+`"
echo "xcrun: error: invalid active developer path" >&2
exit 1
`)
	var dirs []string
	for range 2 {
		d := t.TempDir()
		testhelpers.MakeGitDir(t, d)
		dirs = append(dirs, d)
	}
	out := captureStderr(t, func() {
		for _, d := range dirs {
			t.Chdir(d)
			got, err := LocateRepoRoot()
			require.NoError(t, err)
			assert.Equal(t, d, got)
		}
	})
	assert.Equal(t, 1, strings.Count(out, "hand rules"), out)
	data, err := os.ReadFile(count)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "x"), "git --version runs once per process")
}

// Item 4: an exported GIT_DIR does not decide the answer. From a
// subdirectory it would make git call the subdirectory the top level; a
// linked worktree's pre-commit hook (GIT_DIR exported, cwd the worktree
// root) still resolves the worktree and the main store.
func TestExportedGitDirIgnored(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	main := filepath.Join(base, "main")
	initRepo(t, main)

	t.Run("subdirectory", func(t *testing.T) {
		sub := filepath.Join(main, "sub")
		require.NoError(t, os.MkdirAll(sub, 0o755))
		t.Setenv("GIT_DIR", filepath.Join(main, ".git"))
		t.Chdir(sub)
		got, err := LocateRepoRoot()
		require.NoError(t, err)
		assert.Equal(t, realpath(t, main), realpath(t, got))
	})
	t.Run("linked worktree pre-commit", func(t *testing.T) {
		wt := filepath.Join(base, "wt")
		cmd := testhelpers.GitCommand("-C", main, "worktree", "add", "-q", wt)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		t.Setenv("GIT_DIR", filepath.Join(main, ".git", "worktrees", "wt"))
		t.Setenv("GIT_INDEX_FILE", filepath.Join(main, ".git", "worktrees", "wt", "index"))
		t.Chdir(wt)
		root, err := LocateRepoRoot()
		require.NoError(t, err)
		store, err := LocateStoreRoot()
		require.NoError(t, err)
		assert.Equal(t, realpath(t, wt), realpath(t, root))
		assert.Equal(t, realpath(t, main), realpath(t, store))
	})
}

// Item 5: the "at <.git>" hint is added only when it tells the reader
// something: not when git's message already names the place, and not for a
// .git above a ceiling, which git never examined.
func TestRefusalHint(t *testing.T) {
	t.Run("git names the .git itself", func(t *testing.T) {
		requireGit(t)
		inner, _ := shapeIn(t, gitShapes["empty file"])
		_, err := LocateRepoRoot()
		require.Error(t, err)
		assert.Equal(t, 1, strings.Count(err.Error(), filepath.Join(inner, ".git")), err.Error())
	})

	boom := `[ "$1" = --version ] && { echo "git version 2.43.0"; exit 0; }
echo "fatal: something failed" >&2
exit 128
`
	layout := func(t *testing.T) (outer, ceiling, start string) {
		t.Helper()
		outer = t.TempDir()
		testhelpers.MakeGitDir(t, outer)
		ceiling = filepath.Join(outer, "c")
		start = filepath.Join(ceiling, "d")
		require.NoError(t, os.MkdirAll(start, 0o755))
		return outer, ceiling, start
	}
	t.Run("a .git above a ceiling is not named", func(t *testing.T) {
		outer, ceiling, start := layout(t)
		fakeGit(t, boom)
		t.Setenv("GIT_CEILING_DIRECTORIES", ceiling)
		t.Chdir(start)
		_, err := LocateRepoRoot()
		require.Error(t, err)
		assert.NotContains(t, err.Error(), filepath.Join(outer, ".git"))
		assert.Contains(t, err.Error(), "something failed")
	})
	t.Run("a .git git examined is named", func(t *testing.T) {
		outer, _, start := layout(t)
		fakeGit(t, boom)
		t.Setenv("GIT_CEILING_DIRECTORIES", "")
		t.Chdir(start)
		_, err := LocateRepoRoot()
		require.Error(t, err)
		assert.Contains(t, err.Error(), filepath.Join(outer, ".git"))
	})
}

// linkedWorktreeOf makes inner a linked worktree whose common dir is common,
// built by hand as git would.
func linkedWorktreeOf(t *testing.T, common, inner string) {
	t.Helper()
	makeGitDirAt(t, common)
	admin := filepath.Join(common, "worktrees", filepath.Base(inner))
	require.NoError(t, os.MkdirAll(admin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "HEAD"), []byte("ref: refs/heads/wt\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o644))
	require.NoError(t, os.MkdirAll(inner, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(inner, ".git"), []byte("gitdir: "+admin+"\n"), 0o644))
}

// Item 6: a linked worktree whose common dir is not the .git of a work tree
// (a separate git dir, a submodule's .git/modules/<name>) has no store ethos
// can name: the store is refused with the reason, never guessed. The work
// tree itself still resolves.
func TestLinkedWorktreeWithoutMainWorkTree(t *testing.T) {
	shapes := map[string]func(base string) string{
		"separate git dir":   func(base string) string { return filepath.Join(base, "store", "repo.git") },
		"submodule worktree": func(base string) string { return filepath.Join(base, "super", ".git", "modules", "sub") },
	}
	for _, withGit := range []bool{true, false} {
		for name, common := range shapes {
			t.Run(map[bool]string{true: "git ", false: "no git "}[withGit]+name, func(t *testing.T) {
				if withGit {
					requireGit(t)
				}
				base := t.TempDir()
				c := common(base)
				wt := filepath.Join(base, "wt")
				linkedWorktreeOf(t, c, wt)
				t.Chdir(wt)
				if !withGit {
					hideGit(t)
				}
				store, err := LocateStoreRoot()
				require.Error(t, err)
				assert.NotErrorIs(t, err, ErrNotInRepo)
				assert.Empty(t, store)
				assert.Contains(t, err.Error(), "linked worktree")
				assert.Contains(t, filepath.ToSlash(err.Error()), filepath.Base(c))
				root, err := LocateRepoRoot()
				require.NoError(t, err)
				assert.Equal(t, realpath(t, wt), realpath(t, root))
			})
		}
	}
}

// git prints a relative common dir against the PHYSICAL working directory
// (getcwd resolves symlinks), so from a symlinked subdirectory at a different
// depth the logical path is the wrong base: the store must still be the work
// tree, never the directory above it.
func TestRelativeCommonDirFromSymlinkedSubdirectory(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	main := filepath.Join(base, "main")
	initRepo(t, main)
	sub := filepath.Join(main, "a", "b", "c")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	link := filepath.Join(base, "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Chdir(link)
	store, err := LocateStoreRoot()
	require.NoError(t, err)
	assert.Equal(t, realpath(t, main), realpath(t, store))
}

// A failed probe is retried after locateTTL: a git that starts to run (the
// command line tools installed under a running server) is used again. A
// successful probe is kept.
func TestFailedGitProbeRetried(t *testing.T) {
	clock := time.Now()
	orig := locateNow
	locateNow = func() time.Time { return clock }
	t.Cleanup(func() { locateNow = orig })

	count := filepath.Join(t.TempDir(), "count")
	ok := filepath.Join(t.TempDir(), "ok")
	fakeGit(t, `echo x >> "`+count+`"
[ -e "`+ok+`" ] && { echo "git version 2.43.0"; exit 0; }
echo "xcrun: error: invalid active developer path" >&2
exit 1
`)
	runs := func() int {
		data, err := os.ReadFile(count)
		require.NoError(t, err)
		return strings.Count(string(data), "x")
	}
	captureStderr(t, func() { assert.Empty(t, usableGit()) })
	require.NoError(t, os.WriteFile(ok, nil, 0o644))

	clock = clock.Add(locateTTL - time.Second)
	captureStderr(t, func() { assert.Empty(t, usableGit(), "within the TTL the failure stands") })
	assert.Equal(t, 1, runs())

	clock = clock.Add(2 * time.Second)
	assert.NotEmpty(t, usableGit(), "past the TTL the probe runs again")
	assert.Equal(t, 2, runs())

	clock = clock.Add(10 * locateTTL)
	assert.NotEmpty(t, usableGit())
	assert.Equal(t, 2, runs(), "a successful probe is kept")
}
