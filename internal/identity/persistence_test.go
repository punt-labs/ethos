package identity

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func identityAncestorDirs(p layout.Policy, path string) []string {
	root := filepath.Dir(filepath.Dir(p.RecordRoot()))
	var dirs []string
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if dir == root {
			return dirs
		}
	}
}

func TestRepositoryPublicationSyncOrdering(t *testing.T) {
	for _, operation := range []string{"create", "identity", "base", "local", "delete namespace", "delete last key"} {
		t.Run(operation, func(t *testing.T) {
			s, p := repositoryIdentityStore(t, layout.RecordBlame)
			path := s.extPath("test", "quarry")
			var run func() error
			deleting := false
			switch operation {
			case "create":
				path = s.Path("new")
				run = func() error { return s.Save(&Identity{Name: "New", Handle: "new", Kind: "agent"}) }
			case "identity":
				path = s.Path("test")
				run = func() error { return s.Update("test", func(id *Identity) error { id.Name = "New"; return nil }) }
			case "base", "local":
				// Entire namespace directories are new, not merely the leaf file.
				require.NoError(t, os.Remove(s.ExtDir("test")))
				local := operation == "local"
				path = s.extWritePath("test", "quarry", local)
				run = func() error { return s.ExtSet("test", "quarry", "key", "new", Local(local)) }
			default:
				deleting = true
				require.NoError(t, s.ExtSet("test", "quarry", "key", "old"))
				key := ""
				if operation == "delete last key" {
					key = "key"
				}
				run = func() error { return s.ExtDel("test", "quarry", key) }
			}
			var events []string
			var staged *os.File
			s.syncFile = func(f *os.File) error {
				staged = f
				events = append(events, "file")
				return f.Sync()
			}
			s.rename = func(from, to string) error {
				events = append(events, "rename")
				require.NotNil(t, staged)
				_, err := staged.Stat()
				require.ErrorIs(t, err, os.ErrClosed, "stage must be closed before publication")
				return os.Rename(from, to)
			}
			s.syncDir = func(dir string) error {
				// Create's empty ext directory is established before file staging.
				if staged != nil || deleting {
					_, err := os.Stat(path)
					if deleting {
						require.ErrorIs(t, err, os.ErrNotExist, "unlink precedes directory sync")
					} else {
						require.NoError(t, err, "link/rename precedes directory sync")
						_, err = staged.Stat()
						require.ErrorIs(t, err, os.ErrClosed)
					}
				}
				events = append(events, dir)
				return syncDirectory(dir)
			}
			require.NoError(t, run())
			want := identityAncestorDirs(p, path)
			if !deleting {
				if operation != "create" {
					want = append([]string{"file", "rename"}, want...)
				} else {
					// Empty sibling and its ancestors, then file sync, link, and
					// destination directory/ancestors. The link is observed above.
					prefix := identityAncestorDirs(p, filepath.Join(s.ExtDir("new"), "unused"))
					want = append(append(prefix, "file"), want...)
				}
			}
			assert.Equal(t, want, events)
			entries, err := os.ReadDir(p.TempDir())
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestRepositoryPostPublicationSyncFailureIsApplied(t *testing.T) {
	for _, operation := range []string{"create", "identity", "base", "local", "delete namespace", "delete last key", "rewrite"} {
		t.Run(operation, func(t *testing.T) {
			// Inject at every level, including S: success requires durable new
			// ancestors, not only fsync of the immediate containing directory.
			probe, p := repositoryIdentityStore(t, layout.RecordFull)
			probePath := probe.extPath("test", "quarry")
			switch operation {
			case "create", "identity", "rewrite":
				probePath = probe.Path("test")
			case "local":
				probePath = probe.extLocalPath("test", "quarry")
			}
			for index := range identityAncestorDirs(p, probePath) {
				t.Run(strconv.Itoa(index), func(t *testing.T) {
					s, p := repositoryIdentityStore(t, layout.RecordFull)
					path := s.extPath("test", "quarry")
					var run func() error
					deleting := false
					switch operation {
					case "create":
						path = s.Path("new")
						run = func() error { return s.Save(&Identity{Name: "New", Handle: "new", Kind: "agent"}) }
					case "identity":
						path = s.Path("test")
						run = func() error { return s.Update("test", func(id *Identity) error { id.Name = "New"; return nil }) }
					case "rewrite":
						path = s.Path("test")
						run = func() error { return s.rewriteRaw(path, map[string]interface{}{"name": "New"}) }
					case "base", "local":
						local := operation == "local"
						path = s.extWritePath("test", "quarry", local)
						run = func() error { return s.ExtSet("test", "quarry", "key", "new", Local(local)) }
					default:
						deleting = true
						require.NoError(t, s.ExtSet("test", "quarry", "key", "old"))
						key := ""
						if operation == "delete last key" {
							key = "key"
						}
						run = func() error { return s.ExtDel("test", "quarry", key) }
					}
					dirs := identityAncestorDirs(p, path)
					injected := errors.New("injected directory sync failure")
					fileSynced := false
					s.syncFile = func(f *os.File) error { fileSynced = true; return f.Sync() }
					s.syncDir = func(dir string) error {
						if (fileSynced || deleting) && dir == dirs[index] {
							return injected
						}
						return syncDirectory(dir)
					}
					err := run()
					var applied *PublicationError
					require.ErrorAs(t, err, &applied)
					require.ErrorIs(t, err, injected)
					assert.Equal(t, path, applied.Path)
					assert.Contains(t, err.Error(), "change applied")
					assert.Contains(t, err.Error(), "durability unconfirmed")
					data, err := os.ReadFile(path)
					if deleting {
						require.ErrorIs(t, err, os.ErrNotExist)
					} else {
						require.NoError(t, err)
						want := "new"
						if operation == "create" || operation == "identity" || operation == "rewrite" {
							want = "New"
						}
						assert.Contains(t, string(data), want)
					}
					entries, err := os.ReadDir(p.TempDir())
					require.NoError(t, err)
					assert.Empty(t, entries)
					if operation == "create" {
						s.syncDir = nil
						require.ErrorIs(t, run(), os.ErrExist, "already-applied creation must not have been rolled back")
					}
				})
			}
		})
	}
}

func TestRepositoryPrePublicationSyncFailurePreservesTarget(t *testing.T) {
	s, p := repositoryIdentityStore(t, layout.RecordBlame)
	before, err := os.ReadFile(s.Path("test"))
	require.NoError(t, err)
	injected := errors.New("injected file sync failure")
	s.syncFile = func(*os.File) error { return injected }
	s.rename = func(string, string) error { t.Fatal("publication must not run"); return nil }
	err = s.Update("test", func(id *Identity) error { id.Name = "Changed"; return nil })
	require.ErrorIs(t, err, injected)
	var applied *PublicationError
	assert.False(t, errors.As(err, &applied))
	after, err := os.ReadFile(s.Path("test"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)

	err = s.Save(&Identity{Name: "New", Handle: "new", Kind: "agent"})
	require.ErrorIs(t, err, injected)
	assert.False(t, errors.As(err, &applied))
	_, err = os.Stat(s.Path("new"))
	require.ErrorIs(t, err, os.ErrNotExist, "failed staging sync cannot publish even a new identity")

	s.syncDir = func(string) error { return injected }
	err = s.Save(&Identity{Name: "New", Handle: "new", Kind: "agent"})
	require.ErrorIs(t, err, injected)
	assert.False(t, errors.As(err, &applied), "empty sibling establishment failed before publication")
	_, err = os.Stat(s.Path("new"))
	require.ErrorIs(t, err, os.ErrNotExist)
	s.syncFile, s.syncDir, s.rename = nil, nil, nil
	require.NoError(t, s.Save(&Identity{Name: "New", Handle: "new", Kind: "agent"}), "retry must establish even the directories left by the interrupted attempt")
}

func TestRepositoryLegacyMigrationReportsAppliedRewrite(t *testing.T) {
	for _, layered := range []bool{false, true} {
		t.Run(map[bool]string{false: "store", true: "layered"}[layered], func(t *testing.T) {
			s, _ := repositoryIdentityStore(t, layout.RecordBlame)
			require.NoError(t, os.WriteFile(s.Path("test"), []byte("name: Test\nhandle: test\nkind: agent\nvoice:\n  provider: test\n  voice_id: synthetic\n"), 0o600))
			coreApplied := false
			s.rename = func(from, to string) error {
				err := os.Rename(from, to)
				if err == nil && to == s.Path("test") {
					coreApplied = true
				}
				return err
			}
			injected := errors.New("injected rewrite directory sync failure")
			s.syncDir = func(dir string) error {
				if coreApplied {
					return injected
				}
				return syncDirectory(dir)
			}
			var store IdentityStore = s
			if layered {
				store = NewLayeredStoreWithBundle(s, nil, NewStore(t.TempDir()), true)
			}
			_, err := store.Load("test")
			var applied *PublicationError
			require.ErrorAs(t, err, &applied)
			require.ErrorIs(t, err, injected)
			assert.Equal(t, s.Path("test"), applied.Path)
			data, err := os.ReadFile(s.Path("test"))
			require.NoError(t, err)
			assert.NotContains(t, string(data), "voice")
			s.syncDir = nil
			id, err := store.Load("test")
			require.NoError(t, err)
			assert.Equal(t, "synthetic", id.Ext["vox"]["voice_id"])
		})
	}
}
