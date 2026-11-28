//go:build linux || darwin

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/mcp"
	"github.com/punt-labs/ethos/v4/internal/resolve"
)

func TestServeStartupSkipsRepairInDormantCheckout(t *testing.T) {
	for _, marker := range []string{"", resolve.LocalDisabledFile, resolve.ProjectOptOutFile} {
		t.Run(marker, func(t *testing.T) {
			se := enableSubprocessRepo(t)
			t.Setenv("HOME", se.home)
			t.Chdir(se.repo)
			if marker != "" {
				path := filepath.Join(se.repo, marker)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, nil, 0o644))
			}
			n := fakeEnsure(t, autorepair.Result{})
			t.Cleanup(func() { cmdRoots.Store(nil) })
			require.NoError(t, establishRoots(serveCmd, nil))
			assert.Zero(t, *n)
		})
	}
}

func TestPersonalMCPDoesNotRewriteSharedGitignore(t *testing.T) {
	se := enableSubprocessRepo(t)
	t.Setenv("HOME", se.home)
	t.Chdir(se.repo)
	exclude := filepath.Join(se.repo, ".git", "info", "exclude")
	require.NoError(t, os.WriteFile(exclude, []byte(".punt-labs/local/\nCLAUDE.local.md\n.claude/settings.local.json\n"), 0o644))
	_, stderr, code := runCLI(t, se, "enable")
	require.Zero(t, code, stderr)
	t.Cleanup(func() { cmdRoots.Store(nil) })
	require.NoError(t, establishRoots(serveCmd, nil))
	_ = ensureForServe(t.Context(), locateServeStore())
	require.NoFileExists(t, filepath.Join(se.repo, ".gitignore"))

	_, stderr, code = runCLI(t, se, "doctor")
	require.FileExists(t, filepath.Join(se.repo, ".gitignore"), "doctor exit %d: %s", code, stderr)
}

func TestPersonalMCPExtReadReconcilesLegacyOverlay(t *testing.T) {
	se := enableSubprocessRepo(t)
	t.Setenv("HOME", se.home)
	t.Chdir(se.repo)
	_, stderr, code := runCLI(t, se, "enable")
	require.Zero(t, code, stderr)
	write := func(rel, data string) string {
		path := filepath.Join(se.repo, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
		return path
	}
	write(".punt-labs/ethos.yaml", "agent: teammate\nresolution: repo-only\n")
	write(".punt-labs/ethos/identities/teammate.yaml", "name: Teammate\nhandle: teammate\nkind: human\n")
	legacy := write(".punt-labs/ethos/identities/teammate.ext/vox.local.yaml", "voice: old-value\n")
	local := write(".punt-labs/local/ethos/identities/teammate.ext/vox.local.yaml", "voice: local-value\n")
	const original = "# team Git ignore rules\n*.build\n"
	ignore := write(".gitignore", original)
	gitRunEnv(t, se.repo, se.env, "add", ".gitignore")
	t.Cleanup(func() { cmdRoots.Store(nil) })
	srv := server.NewMCPServer("test", "0", server.WithToolCapabilities(true))
	build := func(loc mcp.StoreLocation) *mcp.Handler { return buildServeHandler(defaultGlobalRoot(), loc) }
	loc := locateServeStore()
	h := build(loc)
	h.RelocateStore(loc, locateServeStore, build)
	h.RegisterTools(srv)
	tool := srv.GetTool("ext")
	require.NotNil(t, tool)
	var req mcplib.CallToolRequest
	req.Params.Name = "ext"
	req.Params.Arguments = map[string]any{"method": "get", "handle": "teammate", "namespace": "vox", "key": "voice"}
	res, err := tool.Handler(t.Context(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.False(t, res.IsError, "%+v", res.Content)
	assert.Contains(t, res.Content[0].(mcplib.TextContent).Text, "local-value")
	require.NoFileExists(t, legacy)
	require.FileExists(t, local)
	got, err := os.ReadFile(ignore)
	require.NoError(t, err)
	assert.Equal(t, original, string(got))
}

// The MCP stdio server answers tool calls on several goroutines, and every
// call passes the real repair gate. Run under -race: the gate must only read
// the binary version it records, never write it per call. HOME is isolated;
// the real global store is never touched.
func TestServeToolCallsPassEnsureConcurrently(t *testing.T) {
	se := setupCLISubprocessEnv(t)
	t.Setenv("HOME", se.home)
	t.Chdir(se.repo)

	srv := server.NewMCPServer("test", "0", server.WithToolCapabilities(true))
	loc := locateServeStore()
	h := buildServeHandler(filepath.Join(se.home, ".punt-labs", "ethos"), loc)
	h.RelocateStore(loc, locateServeStore, func(l mcp.StoreLocation) *mcp.Handler {
		return buildServeHandler(filepath.Join(se.home, ".punt-labs", "ethos"), l)
	})
	h.RegisterTools(srv)
	st := srv.GetTool("talent")
	require.NotNil(t, st)

	const workers, calls = 5, 8
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range calls {
				var req mcplib.CallToolRequest
				req.Params.Name = "talent"
				req.Params.Arguments = map[string]any{"method": "list"}
				res, err := st.Handler(context.Background(), req)
				assert.NoError(t, err)
				assert.NotNil(t, res)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, resolveVersion(), autorepair.BinaryVersion, "the version is set once, at startup")
}
