//go:build linux || darwin

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

func TestCLI_RepositoryIdentityLocalBoundary(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "this acceptance check requires the actual built CLI")
	for _, mode := range []string{"blame", "full"} {
		t.Run(mode, func(t *testing.T) {
			se := setupCLISubprocessEnv(t)
			config := "resolution: repo-only\nrecord: " + mode + "\n"
			require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"), []byte(config), 0o600))
			recordRoot := filepath.Join(se.repo, ".punt-labs", "ethos")
			require.NoError(t, os.WriteFile(filepath.Join(recordRoot, "identities", "test-agent.yaml"),
				[]byte("name: Repository Agent\nhandle: test-agent\nkind: agent\n"), 0o600))
			for _, args := range [][]string{
				{"ext", "set", "test-agent", "quarry", "collection", "public"},
				{"ext", "set", "test-agent", "quarry", "api_token", "synthetic-private", "--local"},
				{"ext", "set", "test-agent", "private", "key", "local-only", "--local"},
				{"ext", "set", "test-agent", "quarry", "context", "public"},
			} {
				_, stderr, exit := runCLI(t, se, args...)
				require.Equal(t, 0, exit, "%v: %s", args, stderr)
			}
			base, err := os.ReadFile(filepath.Join(recordRoot, "identities", "test-agent.ext", "quarry.yaml"))
			require.NoError(t, err)
			assert.Contains(t, string(base), "collection: public")
			assert.NotContains(t, string(base), "api_token")
			localFile := filepath.Join(se.repo, ".punt-labs", "local", "ethos", "identities", "test-agent.ext", "quarry.local.yaml")
			local, err := os.ReadFile(localFile)
			require.NoError(t, err)
			assert.Contains(t, string(local), "synthetic-private")
			stdout, stderr, exit := runCLI(t, se, "ext", "get", "test-agent", "quarry")
			require.Equal(t, 0, exit, stderr)
			assert.Contains(t, stdout, "synthetic-private")
			stdout, stderr, exit = runCLI(t, se, "ext", "list", "test-agent")
			require.Equal(t, 0, exit, stderr)
			assert.Contains(t, stdout, "private")
			assert.NotContains(t, stdout, "private.local")
			_, stderr, exit = runCLI(t, se, "ext", "delete", "test-agent", "quarry", "api_token", "--local")
			require.Equal(t, 0, exit, stderr)
			_, err = os.Stat(localFile)
			require.ErrorIs(t, err, os.ErrNotExist)
			require.NoError(t, filepath.WalkDir(recordRoot, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				assert.False(t, strings.HasSuffix(entry.Name(), ".local.yaml"), path)
				assert.False(t, strings.HasSuffix(entry.Name(), ".lock"), path)
				assert.NotContains(t, entry.Name(), ".ethos-tmp", path)
				return nil
			}))
		})
	}
}

func TestCLI_RepositoryIdentityOverlayDivergence(t *testing.T) {
	require.NotEmpty(t, ethosBinary)
	se := setupCLISubprocessEnv(t)
	setResolution(t, se, "repo-only")
	record := filepath.Join(se.repo, ".punt-labs", "ethos", "identities")
	require.NoError(t, os.WriteFile(filepath.Join(record, "test-agent.yaml"),
		[]byte("name: Agent\nhandle: test-agent\nkind: agent\n"), 0o600))
	legacy := filepath.Join(record, "test-agent.ext", "private.local.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacy), 0o700))
	require.NoError(t, os.WriteFile(legacy, []byte("key: old-private\n"), 0o600))
	stdout, stderr, exit := runCLI(t, se, "ext", "get", "test-agent", "private")
	require.Equal(t, 0, exit, stderr)
	assert.Contains(t, stdout, "old-private", "legacy-only compatibility read")
	_, stderr, exit = runCLI(t, se, "ext", "set", "test-agent", "private", "key", "no", "--local")
	require.Equal(t, 1, exit)
	assert.Contains(t, stderr, "not in the local zone yet")
	local := filepath.Join(se.repo, ".punt-labs", "local", "ethos", "identities", "test-agent.ext", "private.local.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(local), 0o700))
	require.NoError(t, os.WriteFile(local, []byte("key: new-private\n"), 0o600))
	for _, args := range [][]string{
		{"ext", "get", "test-agent", "private"},
		{"ext", "list", "test-agent"},
		{"identity", "show", "test-agent"},
	} {
		_, stderr, exit := runCLI(t, se, args...)
		require.Equal(t, 1, exit, "%v", args)
		assert.Contains(t, stderr, "divergent local extension overlays")
		assert.NotContains(t, stderr, "old-private")
		assert.NotContains(t, stderr, "new-private")
	}
}

func TestIdentityConstructorUsesLocatedPolicySnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	record := filepath.Join(root, ".punt-labs", "ethos")
	require.NoError(t, os.MkdirAll(filepath.Join(record, "identities"), 0o700))
	// The selected policy remains full even when the file is replaced after
	// location. This constructor must consume the supplied snapshot, not reread.
	located := resolve.Located{Store: root, Checkout: root, Ethos: record, Config: &resolve.RepoConfig{Record: "full"}}
	require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("record: invalid\n"), 0o600))
	s, err := newIdentityStore(record, "", true, located)
	require.NoError(t, err)
	require.NoError(t, s.Save(&identity.Identity{Name: "Agent", Handle: "agent", Kind: "agent"}))
	require.NoError(t, s.ExtSet("agent", "private", "key", "value", identity.Local(true)))
	assert.FileExists(t, filepath.Join(root, ".punt-labs", "local", "ethos", "identities", "agent.ext", "private.local.yaml"))
	located.Config = &resolve.RepoConfig{Record: "invalid"}
	_, err = newIdentityStore(record, "", true, located)
	require.ErrorContains(t, err, "unknown value")
	// Failed reconstruction cannot mutate a store already handed to a handler.
	require.NoError(t, s.ExtSet("agent", "private", "next", "value", identity.Local(true)))
}

func TestCLI_RepositoryIdentityRefusesRedirectedBase(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "this regression requires the actual CLI")
	for _, mode := range []string{"blame", "full"} {
		for _, redirect := range []string{"file", "directory"} {
			t.Run(mode+"/"+redirect, func(t *testing.T) {
				se := setupCLISubprocessEnv(t)
				require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"),
					[]byte("resolution: repo-only\nrecord: "+mode+"\n"), 0o600))
				ids := filepath.Join(se.repo, ".punt-labs", "ethos", "identities")
				require.NoError(t, os.WriteFile(filepath.Join(ids, "test-agent.yaml"),
					[]byte("name: Agent\nhandle: test-agent\nkind: agent\n"), 0o600))
				for _, args := range [][]string{
					{"ext", "set", "test-agent", "quarry", "api_token", "synthetic-private", "--local"},
					{"ext", "set", "test-agent", "quarry", "collection", "private", "--local"},
				} {
					_, stderr, exit := runCLI(t, se, args...)
					require.Equal(t, 0, exit, stderr)
				}
				base := filepath.Join(ids, "test-agent.ext", "quarry.yaml")
				local := filepath.Join(se.repo, ".punt-labs", "local", "ethos", "identities", "test-agent.ext", "quarry.local.yaml")
				require.NoError(t, os.MkdirAll(filepath.Dir(base), 0o700))
				link, target := base, local
				if redirect == "directory" {
					require.NoError(t, os.WriteFile(base, []byte("collection: public\nkept: public\n"), 0o600))
					link = filepath.Dir(base)
					target = filepath.Join(t.TempDir(), "redirected")
					require.NoError(t, os.Rename(link, target))
				}
				require.NoError(t, os.Symlink(target, link))
				beforeBase, err := os.ReadFile(base)
				require.NoError(t, err)
				beforeLocal, err := os.ReadFile(local)
				require.NoError(t, err)
				for _, args := range [][]string{
					{"ext", "get", "test-agent", "quarry"},
					{"ext", "list", "test-agent"},
					{"identity", "show", "test-agent"},
					{"ext", "set", "test-agent", "quarry", "collection", "changed"},
					{"ext", "delete", "test-agent", "quarry", "collection"},
					{"ext", "delete", "test-agent", "quarry"},
				} {
					stdout, stderr, exit := runCLI(t, se, args...)
					require.Equal(t, 1, exit, "%v", args)
					assert.Contains(t, stderr, "symlink")
					assert.NotContains(t, stdout+stderr, "synthetic-private")
					gotTarget, err := os.Readlink(link)
					require.NoError(t, err, "refusal must preserve the symlink")
					assert.Equal(t, target, gotTarget)
					for path, want := range map[string][]byte{base: beforeBase, local: beforeLocal} {
						got, err := os.ReadFile(path)
						require.NoError(t, err)
						assert.Equal(t, want, got, "%v: %s", args, path)
					}
				}
			})
		}
	}
}
