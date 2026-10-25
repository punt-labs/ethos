//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupShapes builds inner/.git in the shapes two reviews found the hand rules
// misjudging. outer is the enclosing repository.
var setupShapes = map[string]func(t *testing.T, outer, inner string){
	"commondir only": func(t *testing.T, _, inner string) {
		writeEntries(t, filepath.Join(inner, ".git"), "commondir=../../.git")
	},
	"garbage HEAD": func(t *testing.T, _, inner string) {
		writeEntries(t, filepath.Join(inner, ".git"), "HEAD=garbage\n", "objects/", "refs/")
	},
	"objects as a file": func(t *testing.T, _, inner string) {
		writeEntries(t, filepath.Join(inner, ".git"), "HEAD=ref: refs/heads/main\n", "objects=", "refs/")
	},
	"gitdir to a plain directory": func(t *testing.T, _, inner string) {
		plain := t.TempDir()
		writeEntries(t, plain, "README=x")
		writeGitfile(t, inner, "gitdir: "+plain+"\n")
	},
	"gitdir without the space": func(t *testing.T, _, inner string) {
		writeGitfile(t, inner, "gitdir:"+realGitDir(t)+"\n")
	},
	"gitdir with leading spaces": func(t *testing.T, _, inner string) {
		writeGitfile(t, inner, "  gitdir: "+realGitDir(t)+"\n")
	},
	"empty gitfile": func(t *testing.T, _, inner string) {
		writeGitfile(t, inner, "")
	},
	"mode-000 .git directory": func(t *testing.T, _, inner string) {
		dot := filepath.Join(inner, ".git")
		writeEntries(t, dot, "HEAD=ref: refs/heads/main\n", "objects/", "refs/")
		unreadable(t, dot, 0o755)
	},
	"mode-000 gitfile": func(t *testing.T, _, inner string) {
		writeGitfile(t, inner, "gitdir: "+realGitDir(t)+"\n")
		unreadable(t, filepath.Join(inner, ".git"), 0o644)
	},
	"linked worktree with unreadable commondir": func(t *testing.T, outer, inner string) {
		admin := filepath.Join(outer, ".git", "worktrees", "inner")
		writeEntries(t, admin, "HEAD=ref: refs/heads/wt\n", "commondir=../..\n")
		writeGitfile(t, inner, "gitdir: "+admin+"\n")
		unreadable(t, filepath.Join(admin, "commondir"), 0o644)
	},
}

// writeEntries makes dir holding entries: "name/" a directory, "name=content"
// a file.
func writeEntries(t *testing.T, dir string, entries ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e, "/"); ok {
			require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0o755))
			continue
		}
		name, content, _ := strings.Cut(e, "=")
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
}

func writeGitfile(t *testing.T, inner, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(inner, ".git"), []byte(content), 0o644))
}

// realGitDir returns a git directory git accepts, outside every work tree.
func realGitDir(t *testing.T) string {
	t.Helper()
	gd := filepath.Join(t.TempDir(), "real.git")
	writeEntries(t, gd, "HEAD=ref: refs/heads/main\n", "objects/", "refs/")
	return gd
}

func unreadable(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	require.NoError(t, os.Chmod(p, 0o000))
	t.Cleanup(func() { _ = os.Chmod(p, mode) })
}

// Setup run from inside each .git shape writes only into the tree git itself
// chooses, and writes nothing when git refuses the shape.
func TestSetup_InsideGitShapesWritesOnlyWhereGitResolves(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for name, mk := range setupShapes {
		t.Run(name, func(t *testing.T) {
			home, outer := setupTestEnv(t)
			inner := filepath.Join(outer, "inner")
			require.NoError(t, os.Mkdir(inner, 0o755))
			mk(t, outer, inner)
			t.Chdir(inner)

			top, gitErr := exec.Command("git", "-C", inner, "rev-parse", "--show-toplevel").Output()
			cfgPath := filepath.Join(t.TempDir(), "setup.yaml")
			writeSetupFile(t, cfgPath, "name: Shape User\nhandle: shape-user\nemail: shape@example.com\n")
			_, stderr, err := execHandler(t, "setup", "--file", cfgPath)

			outerCfg := filepath.Join(outer, ".punt-labs")
			innerCfg := filepath.Join(inner, ".punt-labs")
			if gitErr != nil {
				require.Error(t, err, "git refuses this .git, so setup must refuse")
				assert.NoDirExists(t, outerCfg)
				assert.NoDirExists(t, innerCfg)
				assert.NoFileExists(t, filepath.Join(home, ".punt-labs", "ethos", "identities", "shape-user.yaml"),
					"a refusal must precede every write")
				return
			}
			require.NoError(t, err, "stderr: %s", stderr)
			chosen, other := outer, inner
			if sameDir(t, strings.TrimSpace(string(top)), inner) {
				chosen, other = inner, outer
			}
			assert.FileExists(t, filepath.Join(chosen, ".punt-labs", "ethos.yaml"))
			assert.NoDirExists(t, filepath.Join(other, ".punt-labs"))
		})
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ai, err := os.Stat(a)
	require.NoError(t, err)
	bi, err := os.Stat(b)
	require.NoError(t, err)
	return os.SameFile(ai, bi)
}
