package mcp

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/resolve"

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
	locate := func() StoreLocation {
		if broken.Load() {
			return StoreLocation{Err: errNoStore}
		}
		return StoreLocation{Root: "/r", EthosRoot: "/r/.punt-labs/ethos"}
	}
	var builds atomic.Int32
	build := func(l StoreLocation) *Handler {
		builds.Add(1)
		s := identity.NewStore(t.TempDir())
		if l.Err == nil {
			// The repository layer: what the repaired store holds.
			require.NoError(t, s.Save(&identity.Identity{Name: "Mal Reynolds", Handle: "mal", Kind: "human"}))
		}
		return NewHandlerWithOptions(s,
			attribute.NewStore(s.Root(), attribute.Talents),
			attribute.NewStore(s.Root(), attribute.Personalities),
			attribute.NewStore(s.Root(), attribute.WritingStyles),
			WithStoreError(l.Err),
		)
	}

	loc := locate()
	h := build(loc)
	h.RelocateStore(loc, locate, build)
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

// The handler is rebuilt whenever the location changes, not only its error:
// a server started outside any repository that is then run in one (or whose
// repository gains a store from enable or setup) uses the repository store.
func TestStoreRelocatedOnLocationChange(t *testing.T) {
	var loc atomic.Value
	loc.Store(StoreLocation{})
	locate := func() StoreLocation { return loc.Load().(StoreLocation) }
	var builds atomic.Int32
	build := func(l StoreLocation) *Handler {
		builds.Add(1)
		s := identity.NewStore(t.TempDir())
		return NewHandlerWithOptions(s,
			attribute.NewStore(s.Root(), attribute.Talents),
			attribute.NewStore(s.Root(), attribute.Personalities),
			attribute.NewStore(s.Root(), attribute.WritingStyles),
			WithStoreError(l.Err),
		)
	}
	h := build(StoreLocation{})
	h.RelocateStore(locate(), locate, build)
	builds.Store(0)

	cur, _ := h.current()
	assert.Same(t, h, cur, "unchanged: the first handler serves")

	steps := []StoreLocation{
		{Root: "/r"}, // not in a repository → in one, no store yet
		{Root: "/r", EthosRoot: "/r/.punt-labs/ethos"}, // enable or setup created the store
		{Root: "/s", EthosRoot: "/s/.punt-labs/ethos"}, // another repository
		{Root: "/s", EthosRoot: "/s/.punt-labs/ethos", HasConfig: true, Config: resolve.RepoConfig{Record: "full"}},
		{Root: "/s", EthosRoot: "/s/.punt-labs/ethos", HasConfig: true, Config: resolve.RepoConfig{Record: "blame"}},
		{}, // outside any repository again
	}
	for i, step := range steps {
		loc.Store(step)
		next, _ := h.current()
		assert.NotSame(t, cur, next, "step %d: %+v rebuilds", i, step)
		assert.Equal(t, int32(i+1), builds.Load())
		cur2, _ := h.current()
		assert.Same(t, next, cur2, "step %d: no rebuild while unchanged", i)
		cur = next
	}
}

// One tool call locates the store once: the repair gate receives the
// location the call already found instead of locating again (DES-084
// wiring, round-1 LOW-2).
func TestToolCallLocatesOnce(t *testing.T) {
	var locates atomic.Int32
	locate := func() StoreLocation {
		locates.Add(1)
		return StoreLocation{}
	}
	build := func(StoreLocation) *Handler {
		s := identity.NewStore(t.TempDir())
		return NewHandlerWithOptions(s,
			attribute.NewStore(s.Root(), attribute.Talents),
			attribute.NewStore(s.Root(), attribute.Personalities),
			attribute.NewStore(s.Root(), attribute.WritingStyles),
		)
	}
	var gateSaw []StoreLocation
	h := build(StoreLocation{})
	h.ensure = func(_ context.Context, loc StoreLocation) string {
		gateSaw = append(gateSaw, loc)
		return ""
	}
	h.RelocateStore(StoreLocation{}, locate, build)
	srv := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(true))
	h.RegisterTools(srv)
	locates.Store(0)

	st := srv.GetTool("identity")
	require.NotNil(t, st)
	_, err := st.Handler(context.Background(), callTool(map[string]interface{}{"method": "list"}))
	require.NoError(t, err)
	assert.Equal(t, int32(1), locates.Load(), "one locate per call")
	require.Len(t, gateSaw, 1, "the gate ran once")
}
