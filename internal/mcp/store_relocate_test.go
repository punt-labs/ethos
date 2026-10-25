package mcp

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/identity"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The store is located again on every tool call: repairing .git takes effect
// in a running server, with stores rebuilt for the new answer, and breaking
// it again refuses writes again. No restart.
func TestStoreRelocatedPerCall(t *testing.T) {
	var broken atomic.Bool
	broken.Store(true)
	locate := func() error {
		if broken.Load() {
			return errNoStore
		}
		return nil
	}
	var builds atomic.Int32
	build := func(storeErr error) *Handler {
		builds.Add(1)
		s := identity.NewStore(t.TempDir())
		if storeErr == nil {
			// The repository layer: what the repaired store holds.
			require.NoError(t, s.Save(&identity.Identity{Name: "Mal Reynolds", Handle: "mal", Kind: "human"}))
		}
		return NewHandlerWithOptions(s,
			attribute.NewStore(s.Root(), attribute.Talents),
			attribute.NewStore(s.Root(), attribute.Personalities),
			attribute.NewStore(s.Root(), attribute.WritingStyles),
			WithStoreError(storeErr),
		)
	}

	h := build(locate())
	h.RelocateStore(locate, build)
	srv := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(true))
	h.RegisterTools(srv)
	builds.Store(0)

	call := func(method string) string {
		t.Helper()
		st := srv.GetTool("identity")
		require.NotNil(t, st)
		res, err := st.Handler(context.Background(), callTool(map[string]interface{}{"method": method}))
		require.NoError(t, err)
		text := ""
		for _, c := range res.Content {
			tc, ok := c.(mcplib.TextContent)
			require.True(t, ok, "%T", c)
			text += tc.Text + "\n"
		}
		return text
	}

	assert.Contains(t, call("create"), "refusing", "unlocated: writes refuse")
	assert.Equal(t, int32(0), builds.Load(), "no rebuild while the answer is unchanged")

	broken.Store(false) // the operator repairs .git
	out := call("list")
	assert.Contains(t, out, "mal", "the rebuilt repository store answers")
	assert.NotContains(t, out, "could not be located")
	assert.NotContains(t, call("create"), "refusing", "located: writes run again")
	assert.Equal(t, int32(1), builds.Load(), "one rebuild for the one change")

	broken.Store(true) // and breaks it again
	assert.Contains(t, call("create"), "refusing")
	assert.Equal(t, int32(2), builds.Load())
}
