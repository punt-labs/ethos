package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A start inside a git directory, or inside a bare repository, is not a work
// tree: git refuses it ("must be run in a work tree"), and so do the hand
// rules. Neither is ErrNotInRepo.
func TestStartInsideGitDirRefused(t *testing.T) {
	layouts := map[string]func(t *testing.T, base string) string{
		"inside .git": func(t *testing.T, base string) string {
			main := filepath.Join(base, "main")
			initRepo(t, main)
			return filepath.Join(main, ".git", "refs")
		},
		"inside .git itself": func(t *testing.T, base string) string {
			main := filepath.Join(base, "main")
			initRepo(t, main)
			return filepath.Join(main, ".git")
		},
		"inside a bare repository": func(t *testing.T, base string) string {
			bare := filepath.Join(base, "proj.git")
			gitRun(t, "init", "-q", "--bare", bare)
			return filepath.Join(bare, "refs")
		},
	}
	for _, withGit := range []bool{true, false} {
		for name, mk := range layouts {
			t.Run(map[bool]string{true: "git ", false: "no git "}[withGit]+name, func(t *testing.T) {
				requireGit(t)
				start := mk(t, t.TempDir())
				t.Chdir(start)
				if !withGit {
					hideGit(t)
				}
				var got string
				var err error
				captureStderr(t, func() { got, err = locateRepoRoot() })
				require.Error(t, err)
				assert.NotErrorIs(t, err, ErrNotInRepo)
				assert.Empty(t, got)
				assert.Contains(t, err.Error(), "work tree")
			})
		}
	}
}

// fakeDevices makes every path under boundary report a different device from
// everything else, so the hand rules see a mount point at boundary.
func fakeDevices(t *testing.T, boundary string) {
	t.Helper()
	orig := deviceOf
	deviceOf = func(p string) (uint64, bool) {
		if p == boundary || strings.HasPrefix(p, boundary+string(filepath.Separator)) {
			return 2, true
		}
		return 1, true
	}
	t.Cleanup(func() { deviceOf = orig })
}

// Without git, the hand rules stop at a filesystem boundary as git does,
// unless GIT_DISCOVERY_ACROSS_FILESYSTEM is a true boolean.
func TestHandRulesStopAtFilesystemBoundary(t *testing.T) {
	cases := map[string]struct {
		env   string // "" leaves it unset
		cross bool
	}{
		"unset":   {},
		"false":   {env: "false"},
		"true":    {env: "true", cross: true},
		"1":       {env: "1", cross: true},
		"yes":     {env: "Yes", cross: true},
		"integer": {env: "2", cross: true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			outer := t.TempDir()
			makeGitDirAt(t, filepath.Join(outer, ".git"))
			mount := filepath.Join(outer, "mnt")
			start := filepath.Join(mount, "a")
			require.NoError(t, os.MkdirAll(start, 0o755))
			fakeDevices(t, realpath(t, mount))
			if c.env != "" {
				t.Setenv("GIT_DISCOVERY_ACROSS_FILESYSTEM", c.env)
			}
			t.Chdir(start)
			hideGit(t)
			var got string
			var err error
			captureStderr(t, func() { got, err = locateRepoRoot() })
			if !c.cross {
				assert.ErrorIs(t, err, ErrNotInRepo, "the walk must stop at the mount point (got %q)", got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, realpath(t, outer), realpath(t, got))
		})
	}
}

// The boundary does not stop a walk that finds the repository on the same
// filesystem first.
func TestHandRulesBoundaryAboveRepository(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "mnt", "repo")
	start := filepath.Join(repo, "a")
	require.NoError(t, os.MkdirAll(start, 0o755))
	makeGitDirAt(t, filepath.Join(repo, ".git"))
	fakeDevices(t, realpath(t, filepath.Join(base, "mnt")))
	t.Chdir(start)
	hideGit(t)
	got, err := locateRepoRoot()
	require.NoError(t, err)
	assert.Equal(t, realpath(t, repo), realpath(t, got))
}
