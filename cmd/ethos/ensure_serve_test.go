//go:build linux || darwin

package main

import (
	"context"
	"sync"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/autorepair"
	"github.com/punt-labs/ethos/v4/internal/mcp"
)

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
	h := buildServeHandler(se.home, loc)
	h.RelocateStore(loc, locateServeStore, func(l mcp.StoreLocation) *mcp.Handler {
		return buildServeHandler(se.home, l)
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
