package identity

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func repositoryIdentityStore(t *testing.T, mode layout.RecordMode) (*Store, layout.Policy) {
	t.Helper()
	root := physicalTempDir(t)
	p, err := layout.NewRepository(root, root, mode)
	require.NoError(t, err)
	s := NewStore(p.RecordRoot(), RepositoryPolicy(p))
	require.NoError(t, s.Save(&Identity{Name: "Test", Handle: "test", Kind: "agent"}))
	return s, p
}

func physicalTempDir(t *testing.T) string {
	t.Helper()
	// Darwin's /var and TMPDIR may be physical aliases such as /private/var.
	// Repository policies take canonical roots, just as the resolver returns.
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return root
}

func assertIdentityRecordBoundary(t *testing.T, p layout.Policy) {
	t.Helper()
	err := filepath.WalkDir(p.RecordRoot(), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		assert.NotContains(t, entry.Name(), layout.TmpMarker, path)
		assert.False(t, strings.HasSuffix(entry.Name(), ".lock"), path)
		assert.False(t, strings.HasSuffix(entry.Name(), ExtLocalSuffix), path)
		return nil
	})
	require.NoError(t, err)
}

func TestRepositoryOverlayPlacement(t *testing.T) {
	for _, mode := range []layout.RecordMode{layout.RecordBlame, layout.RecordFull} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			s, p := repositoryIdentityStore(t, mode)
			require.NoError(t, s.ExtSet("test", "quarry", "collection", "public"))
			require.NoError(t, s.ExtSet("test", "quarry", "api_token", "synthetic-private", Local(true)))
			require.NoError(t, s.ExtSet("test", "private", "key", "local-only", Local(true)))
			local := filepath.Join(p.LocalRoot(), "identities", "test.ext", "quarry.local.yaml")
			assert.Equal(t, local, s.extLocalPath("test", "quarry"))
			assert.Equal(t, "synthetic-private", readYAMLMap(t, local)["api_token"])
			_, err := os.Stat(s.legacyExtLocalPath("test", "quarry"))
			require.ErrorIs(t, err, os.ErrNotExist)

			names, err := s.ExtList("test")
			require.NoError(t, err)
			assert.Equal(t, []string{"private", "quarry"}, names)
			id, err := s.Load("test", Reference(true))
			require.NoError(t, err)
			assert.Equal(t, "synthetic-private", id.Ext["quarry"]["api_token"])
			// Subsequent base and identity writes must not persist merged secrets.
			require.NoError(t, s.ExtSet("test", "quarry", "context", "public"))
			require.NoError(t, s.Update("test", func(id *Identity) error { id.Name = "Updated"; return nil }))
			base, err := os.ReadFile(s.extPath("test", "quarry"))
			require.NoError(t, err)
			assert.NotContains(t, string(base), "api_token")
			core, err := os.ReadFile(s.Path("test"))
			require.NoError(t, err)
			assert.NotContains(t, string(core), "synthetic-private")
			require.NoError(t, s.ExtDel("test", "quarry", "api_token", Local(true)))
			merged, err := s.ExtGet("test", "quarry", "")
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"collection": "public", "context": "public"}, merged)
			assertIdentityRecordBoundary(t, p)
		})
	}
}

func TestRepositoryLegacyOverlayCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, legacy, local string
		want                map[string]string
		conflict, malformed bool
	}{
		{name: "legacy only", legacy: "token: old\n", want: map[string]string{"token": "old"}},
		{name: "local only", local: "token: new\n", want: map[string]string{"token": "new"}},
		{name: "equal copies", legacy: "token: same\na: b\n", local: "a: b\ntoken: same\n", want: map[string]string{"token": "same", "a": "b"}},
		{name: "divergent copies", legacy: "token: old\n", local: "token: new\n", conflict: true},
		{name: "empty versus populated", legacy: "{}\n", local: "token: new\n", conflict: true},
		{name: "malformed legacy", legacy: "token: [unclosed\n", local: "token: new\n", malformed: true},
		{name: "malformed new", legacy: "token: old\n", local: "token: [unclosed\n", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := repositoryIdentityStore(t, layout.RecordBlame)
			if tc.legacy != "" {
				writeExtFile(t, s, "test", "private.local.yaml", tc.legacy)
			}
			if tc.local != "" {
				path := s.extLocalPath("test", "private")
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(tc.local), 0o600))
			}
			m, err := s.ExtGet("test", "private", "")
			if tc.conflict {
				var conflict *OverlayConflictError
				require.ErrorAs(t, err, &conflict)
				assert.NotContains(t, err.Error(), "token:")
				_, err = s.ExtList("test")
				require.ErrorAs(t, err, &conflict)
				_, err = s.Load("test")
				require.ErrorAs(t, err, &conflict)
			} else if tc.malformed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, m)
				names, err := s.ExtList("test")
				require.NoError(t, err)
				assert.Equal(t, []string{"private"}, names)
			}
			if tc.legacy != "" {
				require.Error(t, s.ExtSet("test", "private", "x", "changed", Local(true)))
				require.Error(t, s.ExtDel("test", "private", "", Local(true)))
				data, err := os.ReadFile(s.legacyExtLocalPath("test", "private"))
				require.NoError(t, err)
				assert.Equal(t, tc.legacy, string(data), "compatibility reads/writes must not migrate or remove old data")
			}
			if tc.local != "" {
				data, err := os.ReadFile(s.extLocalPath("test", "private"))
				require.NoError(t, err)
				assert.Equal(t, tc.local, string(data))
			}
		})
	}
}

func TestRepositoryNamespaceCapacityIncludesLocalOnly(t *testing.T) {
	s, p := repositoryIdentityStore(t, layout.RecordBlame)
	for i := range MaxNamespacesPerID {
		require.NoError(t, s.ExtSet("test", fmt.Sprintf("ns%d", i), "key", "local", Local(true)))
	}
	// Adding the base half to a local-only namespace is not a new namespace.
	require.NoError(t, s.ExtSet("test", "ns0", "base", "public"))
	require.Error(t, s.ExtSet("test", "extra", "key", "no"))
	require.Error(t, s.ExtSet("test", "extra", "key", "no", Local(true)))
	names, err := s.ExtList("test")
	require.NoError(t, err)
	assert.Len(t, names, MaxNamespacesPerID)
	assertIdentityRecordBoundary(t, p)
}

func TestRepositoryConcurrentMutations(t *testing.T) {
	s, p := repositoryIdentityStore(t, layout.RecordBlame)
	otherPolicy, err := layout.NewRepository(filepath.Dir(filepath.Dir(p.RecordRoot())), physicalTempDir(t), layout.RecordFull)
	require.NoError(t, err)
	other := NewStore(p.RecordRoot(), RepositoryPolicy(otherPolicy))
	firstLock, err := s.lockPath("test")
	require.NoError(t, err)
	secondLock, err := other.lockPath("test")
	require.NoError(t, err)
	assert.Equal(t, firstLock, secondLock, "checkout and record mode cannot split the lock domain")
	const n = 40
	errs := make(chan error, n*2)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			writer := s
			if i%2 == 0 {
				writer = other
			}
			errs <- writer.Update("test", func(id *Identity) error {
				count := 0
				if id.GitHub != "" {
					var err error
					count, err = strconv.Atoi(id.GitHub)
					if err != nil {
						return err
					}
				}
				id.GitHub = strconv.Itoa(count + 1)
				return nil
			})
			errs <- writer.ExtSet("test", "private", fmt.Sprintf("key%d", i), "value", Local(true))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	id, err := s.Load("test")
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(n), id.GitHub)
	assert.Len(t, id.Ext["private"], n)
	assertIdentityRecordBoundary(t, p)
}

func TestRepositoryConcurrentNamespaceLimit(t *testing.T) {
	s, _ := repositoryIdentityStore(t, layout.RecordBlame)
	errs := make(chan error, MaxNamespacesPerID+8)
	var wg sync.WaitGroup
	for i := range cap(errs) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- s.ExtSet("test", fmt.Sprintf("ns%d", i), "key", "value", Local(true))
		}()
	}
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
			continue
		}
		assert.Contains(t, err.Error(), "namespaces (max")
	}
	assert.Equal(t, MaxNamespacesPerID, accepted)
	names, err := s.ExtList("test")
	require.NoError(t, err)
	assert.Len(t, names, MaxNamespacesPerID)
}

func TestRepositoryFailedRenamePreservesTarget(t *testing.T) {
	for _, operation := range []string{"identity", "layered identity", "base set", "local set", "base del", "local del"} {
		t.Run(operation, func(t *testing.T) {
			s, p := repositoryIdentityStore(t, layout.RecordFull)
			require.NoError(t, s.ExtSet("test", "quarry", "first", "old"))
			require.NoError(t, s.ExtSet("test", "quarry", "second", "kept"))
			require.NoError(t, s.ExtSet("test", "quarry", "first", "private", Local(true)))
			require.NoError(t, s.ExtSet("test", "quarry", "second", "kept", Local(true)))
			path := s.Path("test")
			local := strings.HasPrefix(operation, "local")
			if strings.Contains(operation, "set") || strings.Contains(operation, "del") {
				path = s.extWritePath("test", "quarry", local)
			}
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			called := false
			s.rename = func(from, to string) error {
				called = true
				assert.Equal(t, p.TempDir(), filepath.Dir(from))
				assert.Contains(t, filepath.Base(from), layout.TmpMarker)
				assert.Equal(t, path, to)
				_, err := os.Stat(from)
				assert.NoError(t, err, "test observes staging before publication")
				assertIdentityRecordBoundary(t, p)
				return &os.LinkError{Op: "rename", Old: from, New: to, Err: syscall.EXDEV}
			}
			switch operation {
			case "identity":
				err = s.Update("test", func(id *Identity) error { id.Name = "New"; return nil })
			case "layered identity":
				ls := NewLayeredStoreWithBundle(s, nil, NewStore(t.TempDir()), true)
				err = ls.Update("test", func(id *Identity) error { id.Name = "New"; return nil })
			case "base set", "local set":
				err = s.ExtSet("test", "quarry", "first", "new", Local(local))
			default:
				err = s.ExtDel("test", "quarry", "first", Local(local))
			}
			require.ErrorIs(t, err, syscall.EXDEV)
			require.True(t, called)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			entries, err := os.ReadDir(p.TempDir())
			require.NoError(t, err)
			assert.Empty(t, entries)
			assertIdentityRecordBoundary(t, p)
			s.rename = nil
			require.NoError(t, s.ExtSet("test", "quarry", "retry", "works", Local(local)))
		})
	}
}

func TestRepositoryRejectsRedirectedLocalAncestors(t *testing.T) {
	for _, component := range []string{"local", "locks", "tmp", "overlay"} {
		t.Run(component, func(t *testing.T) {
			s, p := repositoryIdentityStore(t, layout.RecordBlame)
			var link string
			switch component {
			case "local":
				link = p.LocalRoot()
			case "locks":
				link = filepath.Join(p.LocalRoot(), "locks")
			case "tmp":
				link = p.TempDir()
			case "overlay":
				link = filepath.Dir(s.extLocalPath("test", "private"))
			}
			require.NoError(t, os.RemoveAll(link))
			require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
			require.NoError(t, os.Symlink(s.ExtDir("test"), link))
			err := s.ExtSet("test", "private", "key", "secret", Local(true))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "symlink")
			assertIdentityRecordBoundary(t, p)
		})
	}
}

func TestRepositoryPolicyIsExplicit(t *testing.T) {
	root := physicalTempDir(t)
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	// Even this repository-shaped pathname retains global semantics without
	// the option; HOME and path shape never infer scope.
	s := NewStore(p.RecordRoot())
	require.NoError(t, s.Save(&Identity{Name: "Global", Handle: "test", Kind: "agent"}))
	require.NoError(t, s.ExtSet("test", "private", "key", "secret", Local(true)))
	assert.FileExists(t, filepath.Join(s.ExtDir("test"), "private.local.yaml"))
	require.NoError(t, s.Update("test", func(id *Identity) error { id.Name = "Updated"; return nil }))
	assert.FileExists(t, s.Path("test")+".lock")
	_, err = os.Stat(p.LocalRoot())
	require.ErrorIs(t, err, os.ErrNotExist)
	for _, broken := range []*Store{
		NewStore(p.RecordRoot(), RepositoryPolicy(layout.Policy{})),
		NewStore(t.TempDir(), RepositoryPolicy(p)),
	} {
		_, err := broken.List()
		require.Error(t, err)
		assert.False(t, errors.Is(err, os.ErrNotExist))
		require.Error(t, broken.ExtSet("test", "ns", "key", "value", Local(true)))
	}
}

func TestRepositoryLayeredExtensionOwnership(t *testing.T) {
	repo, p := repositoryIdentityStore(t, layout.RecordBlame)
	bundleStore, global := NewStore(t.TempDir()), NewStore(t.TempDir())
	require.NoError(t, bundleStore.Save(&Identity{Name: "Bundle", Handle: "bundled", Kind: "agent"}))
	require.NoError(t, global.Save(&Identity{Name: "Global", Handle: "test", Kind: "agent"}))
	require.NoError(t, repo.ExtSet("test", "private", "key", "repo-local", Local(true)))
	require.NoError(t, global.ExtSet("test", "private", "key", "global-local", Local(true)))
	require.NoError(t, bundleStore.ExtSet("bundled", "private", "key", "bundle-local", Local(true)))
	for _, authoritative := range []bool{false, true} {
		t.Run(fmt.Sprint(authoritative), func(t *testing.T) {
			ls := NewLayeredStoreWithBundle(repo, bundleStore, global, authoritative)
			want := "global-local"
			if authoritative {
				want = "repo-local"
			}
			id, err := ls.Load("test")
			require.NoError(t, err)
			assert.Equal(t, want, id.Ext["private"]["key"])
			m, err := ls.ExtGet("test", "private", "")
			require.NoError(t, err)
			assert.Equal(t, want, m["key"])
			if authoritative {
				id, err := ls.Load("bundled")
				require.NoError(t, err)
				assert.Equal(t, "bundle-local", id.Ext["private"]["key"])
				require.ErrorContains(t, ls.ExtSet("bundled", "private", "key", "no", Local(true)), "active bundle")
				require.ErrorContains(t, ls.ExtDel("bundled", "private", "", Local(true)), "active bundle")
			}
			require.ErrorContains(t, ls.Update("bundled", func(id *Identity) error { id.Name = "No"; return nil }), "active bundle")
		})
	}
	assertIdentityRecordBoundary(t, p)
}

func TestRepositoryAtomicCreateDoesNotClobber(t *testing.T) {
	s, p := repositoryIdentityStore(t, layout.RecordBlame)
	before, err := os.ReadFile(s.Path("test"))
	require.NoError(t, err)
	err = s.Save(&Identity{Name: "Different", Handle: "test", Kind: "agent"})
	require.ErrorIs(t, err, os.ErrExist)
	after, err := os.ReadFile(s.Path("test"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	entries, err := os.ReadDir(p.TempDir())
	require.NoError(t, err)
	assert.Empty(t, entries)
	assertIdentityRecordBoundary(t, p)
}
