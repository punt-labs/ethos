//go:build linux || darwin

package mcp_test

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mcpExtOwnershipSnapshot(t *testing.T, roots ...string) map[string][sha256.Size]byte {
	t.Helper()
	result := make(map[string][sha256.Size]byte)
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			result[path] = sha256.Sum256([]byte(entry.Type().String()))
			if entry.Type().IsRegular() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				result[path] = sha256.Sum256(data)
			} else if entry.Type()&os.ModeSymlink != 0 {
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				result[path] = sha256.Sum256([]byte(target))
			}
			return nil
		}))
	}
	return result
}

func mcpExtOwnershipFixture(t *testing.T, env *mcpTestEnv) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(env.repo, ".punt-labs", "ethos.yaml"),
		[]byte("resolution: repo-only\nactive_bundle: ownership\n"), 0o600))
	bundle := filepath.Join(env.repo, ".punt-labs", "ethos-bundles", "ownership")
	require.NoError(t, os.MkdirAll(bundle, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(bundle, "bundle.yaml"), []byte("name: ownership\n"), 0o600))
	record := filepath.Join(env.repo, ".punt-labs", "ethos")
	for _, layer := range []struct{ root, value string }{
		{record, "repo-base"}, {bundle, "bundle-base"}, {filepath.Join(env.home, ".punt-labs", "ethos"), "global-base"},
	} {
		ids := filepath.Join(layer.root, "identities")
		require.NoError(t, os.MkdirAll(filepath.Join(ids, "test-agent.ext"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(ids, "test-agent.yaml"),
			[]byte("name: Agent\nhandle: test-agent\nkind: agent\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(ids, "test-agent.ext", "quarry.yaml"),
			[]byte("collection: "+layer.value+"\n"), 0o600))
	}
	local := filepath.Join(env.repo, ".punt-labs", "local", "ethos", "identities", "test-agent.ext")
	require.NoError(t, os.MkdirAll(local, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(local, "quarry.local.yaml"), []byte("collection: repo-overlay\n"), 0o600))
	return filepath.Join(record, "identities", "test-agent.yaml")
}

func TestMCP_ExtensionOwnershipRefusal(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "regression requires the registered tool in ethos serve")
	for _, kind := range []string{"inside-symlink", "outside-symlink", "dangling-symlink", "directory", "ancestor-file", "ancestor-symlink"} {
		t.Run(kind, func(t *testing.T) {
			env := setupMCPTestEnv(t)
			path := mcpExtOwnershipFixture(t, env)
			c := startMCPClient(t, env)
			control := callTool(t, c, "ext", map[string]interface{}{"method": "get", "handle": "test-agent", "namespace": "quarry"})
			require.False(t, control.IsError, "valid repository control must resolve")
			require.True(t, strings.Contains(resultText(t, control), "repo-overlay"), "repository overlay must win")
			badPath, reason := path, "symlink"
			switch kind {
			case "inside-symlink", "outside-symlink":
				target := filepath.Join(filepath.Dir(path), "original.yaml")
				if kind == "outside-symlink" {
					target = filepath.Join(env.home, "outside.yaml")
				}
				require.NoError(t, os.Rename(path, target))
				require.NoError(t, os.Symlink(target, path))
			case "dangling-symlink":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink(filepath.Join(env.home, "absent.yaml"), path))
			case "directory":
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Mkdir(path, 0o700))
				reason = "not a regular file"
			case "ancestor-file", "ancestor-symlink":
				badPath = filepath.Dir(path)
				target := filepath.Join(env.home, "original-identities")
				require.NoError(t, os.Rename(badPath, target))
				if kind == "ancestor-file" {
					require.NoError(t, os.WriteFile(badPath, []byte("not a directory"), 0o600))
					reason = "not a directory"
				} else {
					require.NoError(t, os.Symlink(target, badPath))
				}
			}
			before := mcpExtOwnershipSnapshot(t, env.repo, env.home)
			for _, method := range []string{"get", "list", "set", "del"} {
				result := callTool(t, c, "ext", map[string]interface{}{
					"method": method, "handle": "test-agent", "namespace": "quarry", "key": "collection", "value": "changed",
				})
				assert.True(t, result.IsError, "registered ext/%s must refuse", method)
				text := resultText(t, result)
				assert.Contains(t, text, badPath)
				assert.Contains(t, text, reason)
				assert.NotContains(t, text, "active bundle", "the error must retain the rejected owner")
				for _, value := range []string{"repo-base", "repo-overlay", "bundle-base", "global-base"} {
					assert.False(t, strings.Contains(text, value), "refusal must not include extension data")
				}
			}
			assert.Equal(t, before, mcpExtOwnershipSnapshot(t, env.repo, env.home), "all layers and redirected targets must remain unchanged")
		})
	}
}

func TestMCP_ExtensionOwnershipSelection(t *testing.T) {
	require.NotEmpty(t, ethosBinary)
	for _, mode := range []string{"repo-only", "layered"} {
		t.Run(mode, func(t *testing.T) {
			env := setupMCPTestEnv(t)
			path := mcpExtOwnershipFixture(t, env)
			require.NoError(t, os.WriteFile(filepath.Join(env.repo, ".punt-labs", "ethos.yaml"),
				[]byte("resolution: "+mode+"\nactive_bundle: ownership\n"), 0o600))
			c := startMCPClient(t, env)
			get := func() string {
				result := callTool(t, c, "ext", map[string]interface{}{"method": "get", "handle": "test-agent", "namespace": "quarry"})
				require.False(t, result.IsError)
				return resultText(t, result)
			}
			want := "repo-overlay"
			if mode == "layered" {
				want = "global-base"
			}
			assert.True(t, strings.Contains(get(), want), "source selection and overlay precedence")
			set := callTool(t, c, "ext", map[string]interface{}{
				"method": "set", "handle": "test-agent", "namespace": "quarry", "key": "other", "value": "public",
			})
			require.False(t, set.IsError)
			base := filepath.Join(filepath.Dir(path), "test-agent.ext", "quarry.yaml")
			if mode == "layered" {
				base = filepath.Join(env.home, ".punt-labs", "ethos", "identities", "test-agent.ext", "quarry.yaml")
			}
			data, err := os.ReadFile(base)
			require.NoError(t, err)
			assert.True(t, strings.Contains(string(data), "other: public"))
			assert.False(t, strings.Contains(string(data), "repo-overlay"), "base writes must not serialize overlays")
			require.NoError(t, os.Remove(path))
			if mode == "repo-only" {
				want = "bundle-base"
			}
			assert.True(t, strings.Contains(get(), want), "only actual absence permits bundle fallback in repo-only mode")
			for _, method := range []string{"set", "del"} {
				result := callTool(t, c, "ext", map[string]interface{}{
					"method": method, "handle": "test-agent", "namespace": "quarry", "key": "other", "value": "public",
				})
				if mode == "repo-only" {
					assert.True(t, result.IsError)
					assert.Contains(t, resultText(t, result), "active bundle")
				} else {
					assert.False(t, result.IsError)
				}
			}
		})
	}
}
