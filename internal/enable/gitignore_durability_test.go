package enable

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestGitignoreDurabilityAndEqualRetry(t *testing.T) {
	for _, maintainer := range []string{"enable", "setup-vendor"} {
		for _, host := range []string{"store", "checkout"} {
			for _, failure := range []string{"file-sync", "rename-exdev", "directory-sync", "none"} {
				t.Run(maintainer+"/"+host+"/"+failure, func(t *testing.T) {
					t.Parallel() // Every hook belongs to this call, not a package global.
					store, checkout := gitRepo(t), gitRepo(t)
					p, err := layout.NewRepository(store, checkout, layout.RecordFull)
					require.NoError(t, err)
					root := store
					if host == "checkout" {
						root = checkout
					}
					path := filepath.Join(root, ".gitignore")
					const old = "# user rules\n*.log\n"
					require.NoError(t, os.WriteFile(path, []byte(old), 0o600))
					before, err := os.Stat(path)
					require.NoError(t, err)
					injected := errors.New("injected ignore sync failure")
					io := defaultGitignoreIO()
					var staged *os.File
					var order []string
					io.syncFile = func(f *os.File) error {
						order = append(order, "file-sync")
						staged = f
						assert.Equal(t, p.TempDir(), filepath.Dir(f.Name()))
						assert.Equal(t, old, readFile(t, path))
						if failure == "file-sync" {
							return injected
						}
						return f.Sync()
					}
					io.rename = func(from, to string) error {
						order = append(order, "rename")
						require.NotNil(t, staged)
						_, err := staged.Stat()
						assert.ErrorIs(t, err, os.ErrClosed, "payload must close before publication")
						assert.Equal(t, path, to)
						assert.Equal(t, old, readFile(t, path))
						if failure == "rename-exdev" {
							return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
						}
						return os.Rename(from, to)
					}
					io.syncDestination = func(got layout.Policy, dest string) error {
						order = append(order, "directory-sync")
						assert.Equal(t, p, got, "use the captured S/C/mode snapshot")
						assert.Equal(t, path, dest)
						assert.Contains(t, readFile(t, dest), LocalIgnoreRule)
						if failure == "directory-sync" {
							return injected
						}
						return layout.SyncRepositoryDestination(got, dest)
					}
					maintain := func() error {
						if maintainer == "enable" {
							_, _, err := ensureGitignoreWithIO(root, p, io)
							return err
						}
						_, err := ensureLocalIgnoredWithIO(root, p, io)
						return err
					}
					err = maintain()
					var applied *layout.InstallDurabilityError
					switch failure {
					case "file-sync":
						require.ErrorIs(t, err, injected)
						assert.False(t, errors.As(err, &applied))
						assert.Equal(t, []string{"file-sync"}, order)
					case "rename-exdev":
						require.ErrorIs(t, err, syscall.EXDEV)
						assert.False(t, errors.As(err, &applied))
						assert.Equal(t, []string{"file-sync", "rename"}, order)
					case "directory-sync":
						require.ErrorIs(t, err, injected)
						require.ErrorAs(t, err, &applied)
						assert.Equal(t, path, applied.Path)
						assert.Equal(t, []string{"file-sync", "rename", "directory-sync"}, order)
					case "none":
						require.NoError(t, err)
						assert.Equal(t, []string{"file-sync", "rename", "directory-sync", "directory-sync"}, order)
					}
					after, err := os.Stat(path)
					require.NoError(t, err)
					if failure == "file-sync" || failure == "rename-exdev" {
						assert.True(t, os.SameFile(before, after), "preinstall failure preserves old inode")
						assert.Equal(t, old, readFile(t, path))
					} else {
						assert.False(t, os.SameFile(before, after), "replace atomically, never truncate in place")
						assert.Contains(t, readFile(t, path), LocalIgnoreRule)
					}
					if runtime.GOOS != "windows" {
						assert.Equal(t, os.FileMode(0o600), after.Mode().Perm())
					}
					entries, err := os.ReadDir(p.TempDir())
					require.NoError(t, err)
					assert.Empty(t, entries, "no adjacent staging or copy-over fallback")
					if failure == "directory-sync" {
						body := readFile(t, path)
						order = nil
						err = maintain() // A matching rule does not prove the last sync succeeded.
						require.ErrorAs(t, err, &applied)
						assert.ErrorIs(t, err, injected)
						assert.Equal(t, []string{"directory-sync"}, order)
						assert.Equal(t, body, readFile(t, path), "applied state must not be rolled back")
					}
					io = defaultGitignoreIO()
					require.NoError(t, maintain(), "retry can confirm or finish the installation")
					assert.Contains(t, readFile(t, path), LocalIgnoreRule)
				})
			}
		}
	}
}

func TestGitignoreSymlinkDestinationDurability(t *testing.T) {
	store, checkout, external := physicalTempDir(t), physicalTempDir(t), physicalTempDir(t)
	p, err := layout.NewRepository(store, checkout, layout.RecordBlame)
	require.NoError(t, err)
	target := filepath.Join(external, "ignore-rules")
	require.NoError(t, os.WriteFile(target, []byte("*.log\n"), 0o600))
	link := filepath.Join(checkout, ".gitignore")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	io := defaultGitignoreIO()
	injected := errors.New("external destination directory sync failed")
	io.syncDestination = func(got layout.Policy, path string) error {
		assert.Equal(t, p, got)
		assert.Equal(t, target, path, "sync physical destination ancestry, not logical checkout")
		return injected
	}
	err = writeGitignoreWithIO(link, []byte(LocalIgnoreRule+"\n"), p, io)
	var applied *layout.InstallDurabilityError
	require.ErrorAs(t, err, &applied)
	assert.ErrorIs(t, err, injected)
	assert.Equal(t, target, applied.Path)
	assert.Equal(t, LocalIgnoreRule+"\n", readFile(t, target))
	got, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, target, got, "preserve the user symlink after an applied/unconfirmed result")
	require.NoError(t, writeGitignore(link, []byte(LocalIgnoreRule+"\n"), p))
}

func TestGitignoreCoveredNestedRuleReconfirmsDurability(t *testing.T) {
	root := gitRepo(t)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	nested := filepath.Join(root, ".punt-labs/.gitignore")
	require.NoError(t, os.MkdirAll(filepath.Dir(nested), 0o755))
	require.NoError(t, os.WriteFile(nested, []byte("local/\n"), 0o644))
	io := defaultGitignoreIO()
	injected := errors.New("nested ignore directory sync failed")
	io.syncDestination = func(got layout.Policy, path string) error {
		assert.Equal(t, p, got)
		assert.Equal(t, nested, path)
		return injected
	}
	added, err := ensureLocalIgnoredWithIO(root, p, io)
	assert.False(t, added)
	var applied *layout.InstallDurabilityError
	require.ErrorAs(t, err, &applied)
	assert.ErrorIs(t, err, injected)
	assert.NoFileExists(t, filepath.Join(root, ".gitignore"), "no redundant rule on a no-write retry")
}

func TestEnableGitignoreUnconfirmedDurabilityBlocksNewMarker(t *testing.T) {
	for _, host := range []string{"store", "checkout"} {
		t.Run(host, func(t *testing.T) {
			store := gitRepo(t)
			gitRun(t, store, "commit", "--allow-empty", "-qm", "initial")
			checkout := filepath.Join(physicalTempDir(t), "linked")
			gitRun(t, store, "worktree", "add", "-qb", "ignore-durability", checkout)
			root := store
			if host == "checkout" {
				root = checkout
			}
			path := filepath.Join(root, ".gitignore")
			io := defaultGitignoreIO()
			injected := errors.New("ignore destination sync remains unconfirmed")
			io.syncDestination = func(p layout.Policy, dest string) error {
				if dest == path {
					return injected
				}
				return layout.SyncRepositoryDestination(p, dest)
			}
			var first os.FileInfo
			for attempt := range 2 {
				_, err := enableTo(checkout, store, io)
				var applied *layout.InstallDurabilityError
				require.ErrorAs(t, err, &applied)
				assert.ErrorIs(t, err, injected)
				assert.Equal(t, path, applied.Path)
				assert.NoFileExists(t, filepath.Join(checkout, markerRel))
				assert.Contains(t, readFile(t, path), LocalIgnoreRule, "never roll back installed protection")
				current, err := os.Stat(path)
				require.NoError(t, err)
				if attempt == 0 {
					first = current
				} else {
					assert.True(t, os.SameFile(first, current), "equal retry must confirm, not duplicate the rule")
				}
			}
			_, err := EnableTo(checkout, store)
			require.NoError(t, err)
			assert.FileExists(t, filepath.Join(checkout, markerRel))
		})
	}
}
