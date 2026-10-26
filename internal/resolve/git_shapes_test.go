package resolve

import (
	"context"
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

// Hand-rule answers for a .git shape when git is not on PATH.
const (
	handInner = "inner" // accepted: the shape's own directory is the root
	handError = "error" // refused: resolution fails
)

// gitShape is a .git entry built inside a directory nested in a repository.
type gitShape struct {
	mk   func(t *testing.T, inner string) // builds inner/.git
	hand string                           // the hand rules' answer
}

// gitShapes are the .git shapes the resolver must classify. With git on PATH
// git decides each one; without it the hand rules give hand.
var gitShapes = map[string]gitShape{
	"empty directory":   {mkDir(), handError},
	"dangling symlink":  {symlinkTo("absent"), handError},
	"symlink loop":      {symlinkTo(".git"), handError},
	"empty file":        {gitfile(""), handError},
	"garbage file":      {gitfile("not a gitfile\n"), handError},
	"no gitdir prefix":  {gitfile("/somewhere/else\n"), handError},
	"gitdir to nowhere": {gitfile("gitdir: /nonexistent\n"), handError},

	"commondir only":                {mkDir("commondir=../../.git"), handError},
	"commondir only to nonexistent": {mkDir("commondir=/nonexistent"), handError},
	"commondir only empty":          {mkDir("commondir="), handError},
	"garbage HEAD":                  {mkDir("HEAD=garbage\n", "objects/", "refs/"), handError},
	"HEAD as a directory":           {mkDir("HEAD/", "objects/", "refs/"), handError},
	// git takes a HEAD symlink to refs/..., dangling (an unborn branch) or not.
	"HEAD symlink to refs, dangling":   {mkDir("HEAD->refs/heads/main", "objects/", "refs/"), handInner},
	"HEAD symlink elsewhere, dangling": {mkDir("HEAD->nowhere", "objects/", "refs/"), handError},
	"HEAD symlink elsewhere, valid":    {mkDir("HEAD->real", "real=ref: refs/heads/main\n", "objects/", "refs/"), handError},
	"objects as a file":                {mkDir("HEAD=ref: refs/heads/main\n", "objects=", "refs/"), handError},
	"hex HEAD":                         {mkDir("HEAD=0123456789abcdef0123456789abcdef01234567\n", "objects/", "refs/"), handInner},
	"dangling commondir":               {mkDir("HEAD=ref: refs/heads/main\n", "commondir=/nonexistent", "objects/", "refs/"), handError},

	"gitdir to a plain directory": {gitfileTo(func(t *testing.T, d string) {
		require.NoError(t, os.WriteFile(filepath.Join(d, "README"), []byte("x"), 0o644))
	}, "gitdir: "), handError},
	"gitdir to an empty directory": {gitfileTo(func(*testing.T, string) {}, "gitdir: "), handError},
	"gitdir without the space":     {gitfileTo(makeGitDirAt, "gitdir:"), handError},
	"gitdir with leading spaces":   {gitfileTo(makeGitDirAt, "  gitdir: "), handError},
	"gitdir with a leading tab":    {gitfileTo(makeGitDirAt, "\tgitdir: "), handError},
	"gitdir with trailing spaces":  {gitfileTo(makeGitDirAt, "gitdir: ", "  \n"), handError},
	"gitdir with CRLF":             {gitfileTo(makeGitDirAt, "gitdir: ", "\r\n"), handInner},
	"gitdir to a git directory":    {gitfileTo(makeGitDirAt, "gitdir: "), handInner},
	"linked worktree":              {linkedWorktree(0o644), handInner},
}

// mkDir builds .git as a directory holding entries: "name/" is a directory,
// "name=content" a file, "name->target" a symlink.
func mkDir(entries ...string) func(t *testing.T, inner string) {
	return func(t *testing.T, inner string) {
		t.Helper()
		dot := filepath.Join(inner, ".git")
		require.NoError(t, os.Mkdir(dot, 0o755))
		for _, e := range entries {
			if name, ok := strings.CutSuffix(e, "/"); ok {
				require.NoError(t, os.Mkdir(filepath.Join(dot, name), 0o755))
				continue
			}
			if name, target, ok := strings.Cut(e, "->"); ok {
				if err := os.Symlink(target, filepath.Join(dot, name)); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				continue
			}
			name, content, _ := strings.Cut(e, "=")
			require.NoError(t, os.WriteFile(filepath.Join(dot, name), []byte(content), 0o644))
		}
	}
}

func symlinkTo(target string) func(t *testing.T, inner string) {
	return func(t *testing.T, inner string) {
		t.Helper()
		if err := os.Symlink(filepath.Join(inner, target), filepath.Join(inner, ".git")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
}

func gitfile(content string) func(t *testing.T, inner string) {
	return func(t *testing.T, inner string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(inner, ".git"), []byte(content), 0o644))
	}
}

// makeGitDirAt makes d the minimal git directory git accepts.
func makeGitDirAt(t *testing.T, d string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(d, "objects"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(d, "refs"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(d, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
}

// gitfileTo writes .git as prefix + a target directory built by mk + suffix
// (a newline when no suffix is given).
func gitfileTo(mk func(t *testing.T, d string), prefix string, suffix ...string) func(t *testing.T, inner string) {
	end := "\n"
	if len(suffix) > 0 {
		end = suffix[0]
	}
	return func(t *testing.T, inner string) {
		t.Helper()
		target := filepath.Join(t.TempDir(), "target")
		require.NoError(t, os.Mkdir(target, 0o755))
		mk(t, target)
		require.NoError(t, os.WriteFile(filepath.Join(inner, ".git"), []byte(prefix+target+end), 0o644))
	}
}

// linkedWorktree makes inner a linked worktree of the enclosing repository by
// hand, its commondir file at mode.
func linkedWorktree(mode os.FileMode) func(t *testing.T, inner string) {
	return func(t *testing.T, inner string) {
		t.Helper()
		admin := filepath.Join(filepath.Dir(inner), ".git", "worktrees", filepath.Base(inner))
		require.NoError(t, os.MkdirAll(admin, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(admin, "HEAD"), []byte("ref: refs/heads/wt\n"), 0o644))
		cd := filepath.Join(admin, "commondir")
		require.NoError(t, os.WriteFile(cd, []byte("../..\n"), 0o644))
		require.NoError(t, os.Chmod(cd, mode))
		t.Cleanup(func() { _ = os.Chmod(cd, 0o644) })
		require.NoError(t, os.WriteFile(filepath.Join(inner, ".git"), []byte("gitdir: "+admin+"\n"), 0o644))
	}
}

// shapeIn builds shape s inside a directory nested in a fresh repository and
// makes it the working directory. It returns the inner and outer directories.
func shapeIn(t *testing.T, s gitShape) (inner, outer string) {
	t.Helper()
	outer = t.TempDir()
	makeGitDirAt(t, filepath.Join(outer, ".git"))
	inner = filepath.Join(outer, "inner")
	require.NoError(t, os.Mkdir(inner, 0o755))
	s.mk(t, inner)
	t.Chdir(inner)
	return inner, outer
}

// gitTopLevel asks git for the work tree root of dir.
func gitTopLevel(t *testing.T, dir string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	return strings.TrimSpace(string(out)), err
}

// hideGit removes git from PATH, so the hand rules decide.
func hideGit(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	_, err := exec.LookPath("git")
	require.Error(t, err, "git must not be on PATH")
}

// With git on PATH, git is the authority: every shape resolves to the root git
// reports, or fails when git fails.
func TestShapesFollowGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for name, s := range gitShapes {
		t.Run(name, func(t *testing.T) {
			inner, _ := shapeIn(t, s)
			want, gitErr := gitTopLevel(t, inner)
			var got string
			var err error
			captureStderr(t, func() { got, err = locateRepoRoot() })
			if gitErr != nil || s.hand == handError {
				require.Error(t, err, "git refuses this .git, or ethos does: %v", gitErr)
				assert.NotErrorIs(t, err, ErrNotInRepo)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, realpath(t, want), realpath(t, got))
		})
	}
}

// Without git, the strict hand rules decide each shape.
func TestShapesHandRules(t *testing.T) {
	for name, s := range gitShapes {
		t.Run(name, func(t *testing.T) {
			inner, _ := shapeIn(t, s)
			hideGit(t)
			var got string
			var err error
			captureStderr(t, func() { got, err = locateRepoRoot() })
			switch s.hand {
			case handError:
				require.Error(t, err)
				assert.NotErrorIs(t, err, ErrNotInRepo)
				assert.Empty(t, got)
				// The hand rules walk the physical path, as git does.
				assert.Contains(t, err.Error(), realpath(t, inner))
			case handInner:
				require.NoError(t, err)
				assert.Equal(t, inner, got)
			default:
				t.Fatalf("shape %q has no hand answer", name)
			}
		})
	}
}

// A .git that is there and that git does not accept is refused, with the .git
// named, whether git walks past it to an enclosing repository or says "not a git
// repository", with git and without: the global store and the repository around
// it are never answered in its place.
func TestShapesNameTheRefusedDotGit(t *testing.T) {
	for _, withGit := range []bool{true, false} {
		for name, s := range gitShapes {
			if s.hand != handError {
				continue
			}
			t.Run(name, func(t *testing.T) {
				if withGit {
					if _, err := exec.LookPath("git"); err != nil {
						t.Skip("git not installed")
					}
				}
				inner, _ := shapeIn(t, s)
				if !withGit {
					hideGit(t)
				}
				_, err := locateRepoRoot()
				require.Error(t, err)
				assert.NotErrorIs(t, err, ErrNotInRepo)
				assert.Contains(t, err.Error(), filepath.Join(inner, ".git"))
			})
		}
	}
}

// The store of a linked worktree is the main work tree's, with git and
// without; a gitfile to a git directory of its own keeps the store here.
func TestShapesStoreRoot(t *testing.T) {
	cases := map[string]bool{"linked worktree": true, "gitdir to a git directory": false}
	for _, withGit := range []bool{true, false} {
		for name, linked := range cases {
			t.Run(name, func(t *testing.T) {
				if withGit {
					if _, err := exec.LookPath("git"); err != nil {
						t.Skip("git not installed")
					}
				}
				inner, outer := shapeIn(t, gitShapes[name])
				if !withGit {
					hideGit(t)
				}
				got, err := locateStoreRoot()
				require.NoError(t, err)
				want := inner
				if linked {
					want = outer
				}
				assert.Equal(t, realpath(t, want), realpath(t, got))
			})
		}
	}
}

// A .git entry git does not accept is refused even when git reports "not a git
// repository": the global store must not answer in its place.
func TestGitNotARepoIsNotInRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := filepath.Join(t.TempDir(), "d")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	// The ceiling stops git above dir, so git reports "not a git repository"
	// whether or not the temp directory sits inside a repository.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	t.Chdir(dir)
	_, gitErr := gitTopLevel(t, dir)
	require.Error(t, gitErr)
	_, err := locateRepoRoot()
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotInRepo)
	assert.Contains(t, err.Error(), filepath.Join(dir, ".git"))
}

// A refusal is never cached: once the .git is repaired, the next call
// resolves it.
func TestRefusalIsNotCached(t *testing.T) {
	for _, withGit := range []bool{true, false} {
		t.Run(map[bool]string{true: "git", false: "no git"}[withGit], func(t *testing.T) {
			if withGit {
				if _, err := exec.LookPath("git"); err != nil {
					t.Skip("git not installed")
				}
			}
			inner, _ := shapeIn(t, gitShapes["empty file"])
			if !withGit {
				hideGit(t)
			}
			_, err := locateRepoRoot()
			require.Error(t, err)
			require.NoError(t, os.Remove(filepath.Join(inner, ".git")))
			makeGitDirAt(t, filepath.Join(inner, ".git"))
			got, err := locateRepoRoot()
			require.NoError(t, err)
			assert.Equal(t, realpath(t, inner), realpath(t, got))
		})
	}
}

// A success answer is reused for at most locateTTL, then asked again.
func TestAnswerCachedForTTL(t *testing.T) {
	clock := time.Now()
	orig := locateNow
	locateNow = func() time.Time { return clock }
	t.Cleanup(func() { locateNow = orig })

	outer := t.TempDir()
	testhelpers.MakeGitDir(t, outer)
	inner := filepath.Join(outer, "inner")
	require.NoError(t, os.Mkdir(inner, 0o755))
	t.Chdir(inner)
	hideGit(t)
	got, err := captureLocate(t)
	require.NoError(t, err)
	assert.Equal(t, outer, got)

	makeGitDirAt(t, filepath.Join(inner, ".git")) // a nearer repository appears

	clock = clock.Add(locateTTL - time.Second)
	got, err = captureLocate(t)
	require.NoError(t, err)
	assert.Equal(t, outer, got, "within the TTL the cached answer stands")

	clock = clock.Add(2 * time.Second)
	got, err = captureLocate(t)
	require.NoError(t, err)
	assert.Equal(t, inner, got, "past the TTL the answer is asked again")
	assert.LessOrEqual(t, locateTTL, 30*time.Second)
}

func captureLocate(t *testing.T) (string, error) {
	t.Helper()
	var got string
	var err error
	captureStderr(t, func() { got, err = locateRepoRoot() })
	return got, err
}

// A .git symlink that points nowhere is an error even when no repository
// encloses it: git would say "not a git repository", which is not the truth.
func TestDanglingGitOutsideARepositoryIsAnError(t *testing.T) {
	for _, withGit := range []bool{true, false} {
		t.Run(map[bool]string{true: "git", false: "no git"}[withGit], func(t *testing.T) {
			if withGit {
				if _, err := exec.LookPath("git"); err != nil {
					t.Skip("git not installed")
				}
			}
			dir := filepath.Join(t.TempDir(), "d")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			if err := os.Symlink(filepath.Join(dir, "absent"), filepath.Join(dir, ".git")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
			t.Chdir(dir)
			if !withGit {
				hideGit(t)
			}
			var err error
			captureStderr(t, func() { _, err = locateRepoRoot() })
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrNotInRepo)
			assert.Contains(t, err.Error(), filepath.Join(dir, ".git"))
		})
	}
}

// A .git that is git's own git dir, with core.worktree putting the work tree
// above it, is the one git chose, not one it walked past; the directories above
// it were never examined.
func TestCoreWorktreeGitDirIsNotWalkedPast(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", args...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	resolvesFrom := func(t *testing.T, want string, dirs ...string) {
		t.Helper()
		for _, dir := range dirs {
			t.Run(filepath.Base(dir), func(t *testing.T) {
				t.Chdir(dir)
				got, err := locateRepoRoot()
				require.NoError(t, err)
				assert.Equal(t, realpath(t, want), realpath(t, got))
			})
		}
	}

	t.Run("a git dir", func(t *testing.T) {
		cw := filepath.Join(t.TempDir(), "cw")
		inner := filepath.Join(cw, "inner")
		deeper := filepath.Join(inner, "deeper")
		require.NoError(t, os.MkdirAll(deeper, 0o755))
		git("init", "-q", inner)
		git("-C", inner, "config", "core.worktree", "../..")
		resolvesFrom(t, cw, inner, deeper)
	})

	t.Run("a gitfile", func(t *testing.T) {
		w := t.TempDir()
		gf := filepath.Join(w, "gf")
		inner := filepath.Join(gf, "inner")
		deeper := filepath.Join(inner, "deeper")
		require.NoError(t, os.MkdirAll(deeper, 0o755))
		store := filepath.Join(w, "store", "x.git")
		git("init", "-q", "--bare", store)
		git("-C", store, "config", "core.bare", "false")
		git("-C", store, "config", "core.worktree", gf)
		require.NoError(t, os.WriteFile(filepath.Join(inner, ".git"), []byte("gitdir: "+store+"\n"), 0o644))
		resolvesFrom(t, gf, inner, deeper)
	})

	t.Run("an unexamined damaged .git above", func(t *testing.T) {
		cw := filepath.Join(t.TempDir(), "cw")
		mid := filepath.Join(cw, "mid")
		inner := filepath.Join(mid, "inner")
		require.NoError(t, os.MkdirAll(inner, 0o755))
		git("init", "-q", inner)
		git("-C", inner, "config", "core.worktree", "../../..")
		require.NoError(t, os.Mkdir(filepath.Join(mid, ".git"), 0o755))
		resolvesFrom(t, cw, inner)
	})
}

// A working directory reached through a symlink: the .git files git passed are
// those of the physical directories, and a damaged one among them is refused.
func TestSymlinkedWorkingDirectoryRefusesDamagedDotGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	outer := filepath.Join(base, "outer")
	inner := filepath.Join(outer, "inner")
	a := filepath.Join(inner, "a")
	require.NoError(t, os.MkdirAll(a, 0o755))
	makeGitDirAt(t, filepath.Join(outer, ".git"))
	mkDir("HEAD=", "objects/", "refs/")(t, inner)
	lnk := filepath.Join(base, "elsewhere", "lnk")
	require.NoError(t, os.MkdirAll(filepath.Dir(lnk), 0o755))
	if err := os.Symlink(a, lnk); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Chdir(lnk)
	_, err := locateRepoRoot()
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotInRepo)
	assert.Contains(t, err.Error(), filepath.Join(inner, ".git"))
}

// Without git, an objects/ or refs/ that cannot be searched is refused, as the
// git-present path refuses it, not read as "not a repository".
func TestHandRulesRefuseUnsearchableObjectsAndRefs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	for _, name := range []string{"objects", "refs"} {
		t.Run(name, func(t *testing.T) {
			inner, _ := shapeIn(t, gitShapes["gitdir to a git directory"])
			dot := filepath.Join(inner, ".git")
			require.NoError(t, os.Remove(dot))
			makeGitDirAt(t, dot)
			p := filepath.Join(dot, name)
			require.NoError(t, os.Chmod(p, 0o000))
			t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
			hideGit(t)
			_, err := locateRepoRoot()
			require.Error(t, err)
			assert.NotErrorIs(t, err, ErrNotInRepo)
			assert.Contains(t, err.Error(), dot)
		})
	}
}
