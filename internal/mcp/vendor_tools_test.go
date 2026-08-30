package mcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/vendor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func vendorHandler(t *testing.T, run VendorRunner) *Handler {
	t.Helper()
	s := identity.NewStore(t.TempDir())
	root := s.Root()
	return NewHandlerWithOptions(s,
		attribute.NewStore(root, attribute.Talents),
		attribute.NewStore(root, attribute.Personalities),
		attribute.NewStore(root, attribute.WritingStyles),
		WithVendorRunner(run),
	)
}

func TestVendorToolPassesOptionsThrough(t *testing.T) {
	var got vendor.Options
	h := vendorHandler(t, func(o vendor.Options) (*vendor.Plan, error) {
		got = o
		return &vendor.Plan{Seeds: []string{"bwk"}, Identities: []string{"bwk"}}, nil
	})

	result, err := h.handleVendor(context.Background(), callTool(map[string]interface{}{
		"handles":       []interface{}{"bwk"},
		"apply":         true,
		"prune":         true,
		"to":            "/tmp/dest",
		"allow_ext_key": []interface{}{"quarry/api_token"},
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, resultText(t, result))

	assert.Equal(t, []string{"bwk"}, got.Handles)
	assert.True(t, got.Apply)
	assert.True(t, got.Prune)
	assert.Equal(t, "/tmp/dest", got.Dest)
	assert.Equal(t, []string{"quarry/api_token"}, got.AllowExtKeys)
	assert.Contains(t, resultText(t, result), `"identities"`)
}

// Vendor plans by default on every surface. An MCP caller that omits
// apply must not write into git-tracked space.
func TestVendorToolPlansByDefault(t *testing.T) {
	var got vendor.Options
	h := vendorHandler(t, func(o vendor.Options) (*vendor.Plan, error) {
		got = o
		return &vendor.Plan{}, nil
	})

	_, err := h.handleVendor(context.Background(), callTool(map[string]interface{}{
		"handles": []interface{}{"bwk"},
	}))
	require.NoError(t, err)
	assert.False(t, got.Apply)
}

// The credential refusal names the keys and the exact override flag.
// Flattening it into a generic failure would strip the only information
// the caller can act on.
func TestVendorToolSurfacesTheRefusalVerbatim(t *testing.T) {
	h := vendorHandler(t, func(vendor.Options) (*vendor.Plan, error) {
		return nil, fmt.Errorf("refusing to vendor credential-named extension keys into git-tracked space: bwk quarry/api_token")
	})

	result, err := h.handleVendor(context.Background(), callTool(map[string]interface{}{
		"handles": []interface{}{"bwk"},
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "bwk quarry/api_token")
}

func TestVendorToolRejectsWrongTypedArgs(t *testing.T) {
	h := vendorHandler(t, func(vendor.Options) (*vendor.Plan, error) {
		t.Fatal("runner must not be reached with malformed args")
		return nil, nil
	})

	for _, args := range []map[string]interface{}{
		{"handles": "bwk"},
		{"handles": []interface{}{1}},
		{"team": 7},
		{"allow_ext_key": "quarry/api_token"},
	} {
		result, err := h.handleVendor(context.Background(), callTool(args))
		require.NoError(t, err)
		assert.True(t, result.IsError, "args %v should be rejected", args)
	}
}

// Without a runner the tool is not registered at all, rather than
// registered and failing at call time.
func TestVendorToolAbsentWithoutARunner(t *testing.T) {
	h := testHandler(t)
	s := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(true))
	h.RegisterTools(s)

	result, err := h.handleVendor(context.Background(), callTool(map[string]interface{}{}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "not available")
}
