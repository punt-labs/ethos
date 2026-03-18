package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testHandler(t *testing.T) *Handler {
	t.Helper()
	s := identity.NewStore(t.TempDir())
	return NewHandler(s)
}

func callTool(args map[string]interface{}) mcplib.CallToolRequest {
	return mcplib.CallToolRequest{
		Params: mcplib.CallToolParams{
			Arguments: args,
		},
	}
}

func resultText(t *testing.T, result *mcplib.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, result.Content, "expected non-empty Content")
	tc, ok := result.Content[0].(mcplib.TextContent)
	require.True(t, ok, "expected TextContent, got %T", result.Content[0])
	return tc.Text
}

func TestNewHandler_NilPanics(t *testing.T) {
	assert.Panics(t, func() {
		NewHandler(nil)
	})
}

func TestRegisterTools(t *testing.T) {
	h := testHandler(t)
	s := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(true))
	h.RegisterTools(s)
	// If this doesn't panic, tools were registered successfully.
}

func TestHandleWhoami_NoActive(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleWhoami(context.Background(), callTool(nil))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleWhoami_SetAndGet(t *testing.T) {
	h := testHandler(t)

	// Create an identity first.
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))

	// Set active.
	result, err := h.handleWhoami(context.Background(), callTool(map[string]interface{}{
		"handle": "alice",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := resultText(t, result)
	assert.Contains(t, text, "alice")

	// Get active.
	result, err = h.handleWhoami(context.Background(), callTool(nil))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text = resultText(t, result)
	assert.Contains(t, text, "Alice")
}

func TestHandleWhoami_SetNonexistent(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleWhoami(context.Background(), callTool(map[string]interface{}{
		"handle": "nobody",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleListIdentities_Empty(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleListIdentities(context.Background(), callTool(nil))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := resultText(t, result)
	var entries []interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &entries))
	assert.Empty(t, entries)
}

func TestHandleListIdentities_WithActive(t *testing.T) {
	h := testHandler(t)
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Bob", Handle: "bob", Kind: "agent",
	}))
	require.NoError(t, h.store.SetActive("alice"))

	result, err := h.handleListIdentities(context.Background(), callTool(nil))
	require.NoError(t, err)

	text := resultText(t, result)
	var entries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &entries))
	assert.Len(t, entries, 2)

	// Find alice and verify active.
	for _, e := range entries {
		if e["handle"] == "alice" {
			assert.True(t, e["active"].(bool))
		} else {
			assert.False(t, e["active"].(bool))
		}
	}
}

func TestHandleGetIdentity_Found(t *testing.T) {
	h := testHandler(t)
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human", Email: "alice@example.com",
	}))

	result, err := h.handleGetIdentity(context.Background(), callTool(map[string]interface{}{
		"handle": "alice",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := resultText(t, result)
	var id map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &id))
	assert.Equal(t, "Alice", id["name"])
	assert.Equal(t, "alice@example.com", id["email"])
}

func TestHandleGetIdentity_NotFound(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleGetIdentity(context.Background(), callTool(map[string]interface{}{
		"handle": "nobody",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleGetIdentity_MissingHandle(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleGetIdentity(context.Background(), callTool(nil))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleCreateIdentity_Valid(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleCreateIdentity(context.Background(), callTool(map[string]interface{}{
		"name":   "Alice",
		"handle": "alice",
		"kind":   "human",
		"email":  "alice@example.com",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	// Verify persisted.
	loaded, err := h.store.Load("alice")
	require.NoError(t, err)
	assert.Equal(t, "Alice", loaded.Name)
}

func TestHandleCreateIdentity_ValidationError(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleCreateIdentity(context.Background(), callTool(map[string]interface{}{
		"name":   "Alice",
		"handle": "INVALID",
		"kind":   "human",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleCreateIdentity_VoiceIDWithoutProvider(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleCreateIdentity(context.Background(), callTool(map[string]interface{}{
		"name":     "Alice",
		"handle":   "alice",
		"kind":     "human",
		"voice_id": "abc123",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	text := resultText(t, result)
	assert.Contains(t, text, "voice_id requires voice_provider")
}

func TestHandleCreateIdentity_WithVoice(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleCreateIdentity(context.Background(), callTool(map[string]interface{}{
		"name":           "Alice",
		"handle":         "alice",
		"kind":           "human",
		"voice_provider": "elevenlabs",
		"voice_id":       "v1",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	loaded, err := h.store.Load("alice")
	require.NoError(t, err)
	require.NotNil(t, loaded.Voice)
	assert.Equal(t, "elevenlabs", loaded.Voice.Provider)
}

func TestHandleCreateIdentity_SetsActiveIfFirst(t *testing.T) {
	h := testHandler(t)
	_, err := h.handleCreateIdentity(context.Background(), callTool(map[string]interface{}{
		"name":   "Alice",
		"handle": "alice",
		"kind":   "human",
	}))
	require.NoError(t, err)

	active, err := h.store.Active()
	require.NoError(t, err)
	assert.Equal(t, "alice", active.Handle)
}

func TestHandleCreateIdentity_WithSkills(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleCreateIdentity(context.Background(), callTool(map[string]interface{}{
		"name":   "Alice",
		"handle": "alice",
		"kind":   "human",
		"skills": []interface{}{"go", "testing"},
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	loaded, err := h.store.Load("alice")
	require.NoError(t, err)
	assert.Equal(t, []string{"go", "testing"}, loaded.Skills)
}

func TestStringArg(t *testing.T) {
	req := callTool(map[string]interface{}{
		"name": "Alice",
	})
	assert.Equal(t, "Alice", stringArg(req, "name", ""))
	assert.Equal(t, "default", stringArg(req, "missing", "default"))
	assert.Equal(t, "", stringArg(req, "missing", ""))
}

func TestStringArrayArg(t *testing.T) {
	req := callTool(map[string]interface{}{
		"skills": []interface{}{"go", "testing"},
	})
	assert.Equal(t, []string{"go", "testing"}, stringArrayArg(req, "skills"))
	assert.Nil(t, stringArrayArg(req, "missing"))
}
