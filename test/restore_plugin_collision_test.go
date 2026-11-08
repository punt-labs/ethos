//go:build !windows

package test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Keep filename output byte-exact: pluginReleaseRepo.git trims whitespace and is
// suitable for commit IDs, but not for Git's NUL-delimited filename inventories.
func restoreGitBytes(t *testing.T, r *pluginReleaseRepo, args ...string) []byte {
	t.Helper()
	out, err := r.run(t, "git", args...)
	require.NoError(t, err, "git %q: %s", args, out)
	return []byte(out)
}

type restoreFile struct {
	mode fs.FileMode
	data string
	link string
}

func restoreTree(t *testing.T, root string) map[string]restoreFile {
	t.Helper()
	tree := make(map[string]restoreFile)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)
		if rel == ".git" {
			return filepath.SkipDir
		}
		info, err := entry.Info()
		require.NoError(t, err)
		file := restoreFile{mode: info.Mode()}
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			file.data = string(data)
		case info.Mode()&os.ModeSymlink != 0:
			file.link, err = os.Readlink(path)
			require.NoError(t, err)
		}
		tree[rel] = file
		return nil
	})
	require.NoError(t, err)
	return tree
}

type restoreSnapshot struct {
	tree  map[string]restoreFile
	index string
	head  string
	ref   string
}

func snapshotRestore(t *testing.T, r *pluginReleaseRepo) restoreSnapshot {
	t.Helper()
	return restoreSnapshot{
		tree:  restoreTree(t, r.root),
		index: r.read(t, ".git/index"),
		head:  r.read(t, ".git/HEAD"),
		ref:   string(restoreGitBytes(t, r, "rev-parse", "HEAD")),
	}
}

// Make a command-only latest dev snapshot followed by a squash-style release.
// A safe path sorts before the collision, so refusal must precede every restore,
// including the otherwise safe manifest and this missing command.
func newRestoreCollisionRepo(t *testing.T, path string) (*pluginReleaseRepo, string, string) {
	t.Helper()
	r := newPluginReleaseRepo(t, true)
	r.write(t, "plugin/commands/00-safe-dev.md", "safe historical command\n")
	r.write(t, path, "historical command\x00\xff\n")
	require.NoError(t, os.Chmod(filepath.Join(r.root, path), 0o755))
	r.git(t, "add", "plugin/commands")
	r.git(t, "commit", "-m", "latest command-only development snapshot")
	source := r.git(t, "rev-parse", "HEAD")
	r.manifest(t, "ethos", "2.0.0")
	r.git(t, "rm", "--", path, "plugin/commands/00-safe-dev.md", "plugin/commands/identity-dev.md")
	r.git(t, "add", pluginManifest)
	r.git(t, "commit", "-m", "chore: release v2.0.0 (#42)")
	// Make a tracked file's cached stat information stale without making it
	// dirty. Even a refused `git status` must not refresh/write the index.
	r.write(t, "restat", r.read(t, "unrelated.txt"))
	require.NoError(t, os.Rename(filepath.Join(r.root, "restat"), filepath.Join(r.root, "unrelated.txt")))
	return r, source, r.git(t, "rev-parse", "HEAD")
}

func TestRestoreDevPluginCollisionRefusal(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		name := "untracked"
		if ignored {
			name = "ignored"
		}
		t.Run(name, func(t *testing.T) {
			for _, kind := range []string{
				"file", "empty directory", "populated directory", "symlink file",
				"symlink directory", "dangling symlink", "ancestor file",
				"ancestor symlink directory", "ancestor dangling symlink",
			} {
				t.Run(kind, func(t *testing.T) {
					path := "plugin/commands/retired's \n[set]/zz old-dev.md"
					r, _, prep := newRestoreCollisionRepo(t, path)
					if ignored {
						r.write(t, ".git/info/exclude", "/plugin/commands/\n")
					}
					// Targets outside the checkout must not be traversed or changed.
					external := t.TempDir()
					require.NoError(t, os.WriteFile(filepath.Join(external, "data"), []byte("operator target\x00\xff\n"), 0o751))
					collision := filepath.Join(r.root, path)
					if kind == "ancestor file" || kind == "ancestor symlink directory" || kind == "ancestor dangling symlink" {
						collision = filepath.Dir(collision)
					}
					require.NoError(t, os.MkdirAll(filepath.Dir(collision), 0o700))
					switch kind {
					case "file", "ancestor file":
						require.NoError(t, os.WriteFile(collision, []byte("operator bytes\x00\xff\n"), 0o751))
					case "empty directory":
						require.NoError(t, os.Mkdir(collision, 0o711))
					case "populated directory":
						r.write(t, filepath.Join(path, "nested", "operator"), "directory data\x00\xff\n")
					case "symlink file":
						require.NoError(t, os.Symlink(filepath.Join(external, "data"), collision))
					case "symlink directory", "ancestor symlink directory":
						require.NoError(t, os.Symlink(external, collision))
					case "dangling symlink", "ancestor dangling symlink":
						require.NoError(t, os.Symlink(filepath.Join(external, "missing"), collision))
					}
					if ignored {
						// Check the fixture really exercises ignored, not ordinary
						// untracked data. Do not parse or trim filename output.
						rel, err := filepath.Rel(r.root, collision)
						require.NoError(t, err)
						out, err := r.run(t, "git", "check-ignore", "--no-index", "--", rel)
						require.NoError(t, err, out)
					}
					before := snapshotRestore(t, r)
					targetBefore := restoreTree(t, external)
					// Test both automatic discovery and explicit prep compatibility.
					for _, args := range [][]string{nil, {prep}} {
						out, err := r.script(t, "restore-dev", args...)
						assert.Error(t, err, out)
						assert.Contains(t, out, "collision", "must diagnose before invoking restore")
						assert.Equal(t, before, snapshotRestore(t, r), "refusal must preserve every byte, mode, symlink, index and HEAD")
						assert.Equal(t, targetBefore, restoreTree(t, external))
					}
				})
			}
		})
	}
}

func TestRestoreDevPluginManifestCollision(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		t.Run(map[bool]string{false: "leaf", true: "ancestor"}[ancestor], func(t *testing.T) {
			r, _, _ := newRestoreCollisionRepo(t, "plugin/commands/zz-dev.md")
			r.git(t, "rm", pluginManifest)
			r.git(t, "commit", "-m", "remove production manifest")
			r.write(t, ".git/info/exclude", "/plugin/.claude-plugin/\n")
			if ancestor {
				r.write(t, filepath.Dir(pluginManifest), "operator obstruction\x00\xff\n")
			} else {
				r.write(t, pluginManifest, "operator manifest\x00\xff\n")
			}
			before := snapshotRestore(t, r)
			out, err := r.script(t, "restore-dev")
			assert.Error(t, err, out)
			assert.Contains(t, out, "collision")
			assert.Equal(t, before, snapshotRestore(t, r))
		})
	}
}

func TestRestoreDevPluginCollisionScope(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "explicit prep"}[explicit], func(t *testing.T) {
			// Quotes, tabs, newline, wildcard characters, whitespace and invalid
			// UTF-8 must survive Git -z inventories and subprocess arguments.
			path := "plugin/commands/ \t'[*?\xff]\n/ odd \t-dev.md"
			r, source, prep := newRestoreCollisionRepo(t, path)
			r.write(t, ".git/info/exclude", "/.punt-labs/local/\n/plugin/commands/ignored/\n")
			r.write(t, ".punt-labs/local/ethos/runtime", "local runtime\x00\xff\n")
			r.write(t, "plugin/commands/ignored/operator-dev.md", "ignored command\x00\xff\n")
			r.write(t, "plugin/commands/untracked-dev.md", "untracked command\x00\xff\n")
			r.write(t, "operator.txt", "unrelated local bytes\x00\xff\n")
			require.NoError(t, os.Chmod(filepath.Join(r.root, "operator.txt"), 0o751))
			before := restoreTree(t, r.root)
			var args []string
			if explicit {
				args = []string{prep}
			}
			out, err := r.script(t, "restore-dev", args...)
			require.NoError(t, err, out)
			assert.Contains(t, out, source[:12])
			assert.Equal(t, prep, r.git(t, "rev-parse", "HEAD"))
			r.assertManifest(t, "ethos-dev", "1.0.0")
			assert.Equal(t, "historical command\x00\xff\n", r.read(t, path))
			info, err := os.Stat(filepath.Join(r.root, path))
			require.NoError(t, err)
			assert.Equal(t, fs.FileMode(0o111), info.Mode().Perm()&0o111)
			assert.Equal(t, "safe historical command\n", r.read(t, "plugin/commands/00-safe-dev.md"))
			assert.Equal(t, "original dev command\n", r.read(t, "plugin/commands/identity-dev.md"))
			assert.Empty(t, restoreGitBytes(t, r, "diff", "--name-only", "-z"))
			after := restoreTree(t, r.root)
			for name, file := range before {
				if name != pluginManifest {
					assert.Equal(t, file, after[name], "unselected path %q", name)
				}
			}
			assert.Equal(t, []byte(pluginManifest+"\x00"+path+"\x00plugin/commands/00-safe-dev.md\x00plugin/commands/identity-dev.md\x00"),
				restoreGitBytes(t, r, "diff", "--cached", "--name-only", "-z"))
		})
	}
}

func TestRestoreDevPluginRefusalSnapshot(t *testing.T) {
	for _, kind := range []string{"unstaged", "staged", "index lock"} {
		t.Run(kind, func(t *testing.T) {
			r, _, _ := newRestoreCollisionRepo(t, "plugin/commands/zz-dev.md")
			want := "repository has uncommitted changes"
			if kind == "index lock" {
				r.write(t, ".git/index.lock", "another writer owns the index\n")
				want = "index.lock"
			} else {
				r.write(t, "unrelated.txt", "operator tracked change\x00\xff\n")
				if kind == "staged" {
					r.git(t, "add", "unrelated.txt")
				}
			}
			before := snapshotRestore(t, r)
			out, err := r.script(t, "restore-dev")
			assert.Error(t, err, out)
			assert.Contains(t, out, want)
			assert.Equal(t, before, snapshotRestore(t, r))
			if kind == "index lock" {
				assert.Equal(t, "another writer owns the index\n", r.read(t, ".git/index.lock"))
			}
		})
	}
}

func TestRestoreDevPluginHistoricalSymlink(t *testing.T) {
	r := newPluginReleaseRepo(t, true)
	path := "plugin/commands/identity-dev.md"
	require.NoError(t, os.Remove(filepath.Join(r.root, path)))
	require.NoError(t, os.Symlink("missing operator target", filepath.Join(r.root, path)))
	r.git(t, "add", path)
	r.git(t, "commit", "-m", "development symlink command")
	r.manifest(t, "ethos", "2.0.0")
	r.git(t, "rm", path)
	r.git(t, "add", pluginManifest)
	r.git(t, "commit", "-m", "squashed release")
	head := r.git(t, "rev-parse", "HEAD")
	out, err := r.script(t, "restore-dev")
	require.NoError(t, err, out)
	link, err := os.Readlink(filepath.Join(r.root, path))
	require.NoError(t, err)
	assert.Equal(t, "missing operator target", link)
	assert.Equal(t, head, r.git(t, "rev-parse", "HEAD"))
	assert.Contains(t, string(restoreGitBytes(t, r, "ls-files", "--stage", "-z", "--", path)), "120000 ")
}
