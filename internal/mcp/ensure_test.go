package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/vendor"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ensureLine = "ethos: upgraded storage (2 items; see ~/.punt-labs/ethos/logs/repair.log)"

// Every registered tool passes the repair gate before it reads storage, and a
// repair adds a second text block, leaving the tool's own first block alone.
// The walk is over the registered tool table, so a tool added later is
// covered or the test fails (DES-084 Wiring).
func TestEveryToolPassesEnsure(t *testing.T) {
	var calls int
	srv, _ := fullServer(t, WithEnsure(func(context.Context, StoreLocation) string {
		calls++
		return ensureLine
	}), WithVendorRunner(func(vendor.Options) (*vendor.Plan, error) {
		return nil, errors.New("vendor not exercised")
	}))
	tools := srv.ListTools()
	require.NotEmpty(t, tools)
	for name, st := range tools {
		for _, method := range methodEnum(t, st.Tool) {
			t.Run(name+" "+method, func(t *testing.T) {
				before := calls
				result := callRegistered(t, srv, name, method)
				assert.Equal(t, before+1, calls, "%s %q did not pass Ensure exactly once", name, method)
				require.NotNil(t, result)
				require.GreaterOrEqual(t, len(result.Content), 2, "%s %q result lacks the repair block", name, method)
				assert.Equal(t, ensureLine, resultTextAt(t, result, len(result.Content)-1))
			})
		}
	}
}

func resultTextAt(t *testing.T, result *mcplib.CallToolResult, i int) string {
	t.Helper()
	tc, ok := result.Content[i].(mcplib.TextContent)
	require.True(t, ok, "content %d is %T, not TextContent", i, result.Content[i])
	return tc.Text
}

func TestEnsureSilentAddsNoBlock(t *testing.T) {
	srv, _ := fullServer(t, WithEnsure(func(context.Context, StoreLocation) string { return "" }))
	result := callRegistered(t, srv, "identity", "list")
	for i := range result.Content {
		assert.NotContains(t, resultTextAt(t, result, i), "upgraded storage")
	}
}

func TestNoEnsureOptionIsAllowed(t *testing.T) {
	srv, _ := fullServer(t)
	result := callRegistered(t, srv, "identity", "list")
	require.NotNil(t, result)
}
