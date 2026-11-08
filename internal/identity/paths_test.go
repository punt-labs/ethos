package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
)

func TestRepositoryBaseSymlinkCannotPublishLocalCredentials(t *testing.T) {
	s, _ := repositoryIdentityStore(t, layout.RecordBlame)
	require.NoError(t, s.ExtSet("test", "quarry", "api_token", "synthetic-private", Local(true)))
	require.NoError(t, s.ExtSet("test", "quarry", "collection", "private", Local(true)))
	base, local := s.extPath("test", "quarry"), s.extLocalPath("test", "quarry")
	rel, err := filepath.Rel(filepath.Dir(base), local)
	require.NoError(t, err)
	require.NoError(t, os.Symlink(rel, base))
	before, err := os.ReadFile(local)
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"get", func() error { _, err := s.ExtGet("test", "quarry", ""); return err }},
		{"list", func() error { _, err := s.ExtList("test"); return err }},
		{"load", func() error { _, err := s.Load("test"); return err }},
		{"layered load", func() error {
			ls := NewLayeredStoreWithBundle(s, nil, NewStore(t.TempDir()), true)
			_, err := ls.Load("test")
			return err
		}},
		{"set", func() error { return s.ExtSet("test", "quarry", "collection", "public") }},
		{"delete key", func() error { return s.ExtDel("test", "quarry", "collection") }},
		{"delete namespace", func() error { return s.ExtDel("test", "quarry", "") }},
		{"rewrite", func() error { return s.rewriteRaw(base, map[string]interface{}{"collection": "public"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			var boundary *repositoryPathError
			require.ErrorAs(t, err, &boundary)
			assert.Contains(t, err.Error(), "symlink")
			assert.NotContains(t, err.Error(), "synthetic-private")
			target, err := os.Readlink(base)
			require.NoError(t, err, "refusal must not replace the base symlink with a regular file")
			assert.Equal(t, rel, target)
			for _, path := range []string{base, local} {
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, before, after, path)
			}
		})
	}
}

func TestRepositoryRejectsRedirectedSourceAncestors(t *testing.T) {
	for _, component := range []string{"extension", "identities", "record", "punt-labs"} {
		t.Run(component, func(t *testing.T) {
			s, p := repositoryIdentityStore(t, layout.RecordFull)
			require.NoError(t, s.ExtSet("test", "quarry", "collection", "public"))
			require.NoError(t, s.ExtSet("test", "quarry", "kept", "public"))
			require.NoError(t, s.ExtSet("test", "quarry", "api_token", "synthetic-private", Local(true)))
			base, local := s.extPath("test", "quarry"), s.extLocalPath("test", "quarry")
			baseBytes, err := os.ReadFile(base)
			require.NoError(t, err)
			localBytes, err := os.ReadFile(local)
			require.NoError(t, err)
			link := s.ExtDir("test")
			switch component {
			case "identities":
				link = s.IdentitiesDir()
			case "record":
				link = p.RecordRoot()
			case "punt-labs":
				link = filepath.Dir(p.RecordRoot())
			}
			outside := filepath.Join(physicalTempDir(t), "redirected")
			require.NoError(t, os.Rename(link, outside))
			require.NoError(t, os.Symlink(outside, link))
			createHandle := "other"
			if component == "extension" {
				createHandle = "test" // exercise the redirected sibling directory
			}
			for _, tc := range []struct {
				name string
				run  func() error
			}{
				{"set", func() error { return s.ExtSet("test", "quarry", "collection", "changed") }},
				{"delete key", func() error { return s.ExtDel("test", "quarry", "collection") }},
				{"delete namespace", func() error { return s.ExtDel("test", "quarry", "") }},
				{"set local", func() error { return s.ExtSet("test", "quarry", "next", "private", Local(true)) }},
				{"get", func() error { _, err := s.ExtGet("test", "quarry", ""); return err }},
				{"load", func() error { _, err := s.Load("test"); return err }},
				{"list", func() error { _, err := s.ExtList("test"); return err }},
				{"rewrite", func() error { return s.rewriteRaw(base, map[string]interface{}{"collection": "changed"}) }},
				{"create", func() error { return s.Save(&Identity{Name: "Test", Handle: createHandle, Kind: "agent"}) }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					err := tc.run()
					require.ErrorContains(t, err, "symlink")
					assert.NotContains(t, err.Error(), "synthetic-private")
					target, err := os.Readlink(link)
					require.NoError(t, err)
					assert.Equal(t, outside, target)
					for path, want := range map[string][]byte{base: baseBytes, local: localBytes} {
						after, err := os.ReadFile(path)
						require.NoError(t, err)
						assert.Equal(t, want, after, path)
					}
				})
			}
		})
	}
}

func TestRepositoryRejectsSymlinkedCompatibilityAndLocalOverlays(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "legacy"}[legacy], func(t *testing.T) {
			s, _ := repositoryIdentityStore(t, layout.RecordBlame)
			require.NoError(t, s.ExtSet("test", "quarry", "collection", "public"))
			base := s.extPath("test", "quarry")
			link := s.extLocalPath("test", "quarry")
			if legacy {
				link = s.legacyExtLocalPath("test", "quarry")
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
			require.NoError(t, os.Symlink(base, link))
			before, err := os.ReadFile(base)
			require.NoError(t, err)
			_, err = s.ExtGet("test", "quarry", "")
			require.ErrorContains(t, err, "symlink")
			_, err = s.Load("test")
			require.ErrorContains(t, err, "symlink")
			require.ErrorContains(t, s.ExtSet("test", "quarry", "next", "value", Local(true)), "symlink")
			require.ErrorContains(t, s.ExtDel("test", "quarry", "", Local(true)), "symlink")
			after, err := os.ReadFile(base)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			target, err := os.Readlink(link)
			require.NoError(t, err)
			assert.Equal(t, base, target)
		})
	}
}

func TestRepositoryLegacyRewriteRejectsIdentitySymlink(t *testing.T) {
	s, p := repositoryIdentityStore(t, layout.RecordBlame)
	private := filepath.Join(p.LocalRoot(), "legacy-identity.yaml")
	body := []byte("name: Test\nhandle: test\nkind: agent\nvoice:\n  provider: private\n  voice_id: synthetic-private\n")
	require.NoError(t, os.WriteFile(private, body, 0o600))
	require.NoError(t, os.Remove(s.Path("test")))
	require.NoError(t, os.Symlink(private, s.Path("test")))
	ls := NewLayeredStoreWithBundle(s, nil, NewStore(t.TempDir()), true)
	for _, run := range []func() error{
		func() error { _, err := s.Load("test"); return err },
		func() error { _, err := ls.Load("test"); return err },
		func() error { return s.rewriteRaw(s.Path("test"), map[string]interface{}{"name": "Changed"}) },
		func() error { return s.Save(&Identity{Name: "Changed", Handle: "test", Kind: "agent"}) },
		func() error {
			return s.Update("test", func(*Identity) error { t.Fatal("mutation must not run"); return nil })
		},
	} {
		err := run()
		require.ErrorContains(t, err, "symlink")
		assert.NotContains(t, err.Error(), "synthetic-private")
		after, err := os.ReadFile(private)
		require.NoError(t, err)
		assert.Equal(t, body, after)
		link, err := os.Readlink(s.Path("test"))
		require.NoError(t, err)
		assert.Equal(t, private, link)
		_, err = os.Stat(s.extPath("test", "vox"))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}

func TestLayeredUpdateDoesNotRedirectRejectedRepositoryToGlobal(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		t.Run(map[bool]string{false: "leaf", true: "ancestor"}[ancestor], func(t *testing.T) {
			repo, _ := repositoryIdentityStore(t, layout.RecordBlame)
			global := NewStore(t.TempDir())
			require.NoError(t, global.Save(&Identity{Name: "Global", Handle: "test", Kind: "agent"}))
			before, err := os.ReadFile(global.Path("test"))
			require.NoError(t, err)
			link := repo.Path("test")
			if ancestor {
				link = repo.IdentitiesDir()
			}
			target := filepath.Join(t.TempDir(), "original")
			require.NoError(t, os.Rename(link, target))
			require.NoError(t, os.Symlink(target, link))
			repoBefore, err := os.ReadFile(repo.Path("test"))
			require.NoError(t, err)
			layered := NewLayeredStore(repo, global)
			err = layered.Update("test", func(*Identity) error {
				t.Fatal("a rejected owner must not invoke the mutation")
				return nil
			})
			require.ErrorContains(t, err, "symlink")
			after, err := os.ReadFile(global.Path("test"))
			require.NoError(t, err)
			assert.Equal(t, before, after)
			repoAfter, err := os.ReadFile(repo.Path("test"))
			require.NoError(t, err)
			assert.Equal(t, repoBefore, repoAfter)
		})
	}
}
