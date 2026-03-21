package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/session"

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

func TestHandleWhoami_NoMatch(t *testing.T) {
	// Isolate git config so resolve chain finds nothing.
	tmp := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", tmp+"/empty.gitconfig")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("USER", "nobody")
	_ = os.WriteFile(tmp+"/empty.gitconfig", []byte(""), 0o644)

	h := testHandler(t)
	result, err := h.handleWhoami(context.Background(), callTool(nil))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleWhoami_ResolvesFromOSUser(t *testing.T) {
	// Isolate git config, set USER to match identity handle.
	tmp := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", tmp+"/empty.gitconfig")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("USER", "alice")
	_ = os.WriteFile(tmp+"/empty.gitconfig", []byte(""), 0o644)

	h := testHandler(t)
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))

	result, err := h.handleWhoami(context.Background(), callTool(nil))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := resultText(t, result)
	assert.Contains(t, text, "Alice")
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

func TestHandleListIdentities_NoSession(t *testing.T) {
	h := testHandler(t)
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Bob", Handle: "bob", Kind: "agent",
	}))

	result, err := h.handleListIdentities(context.Background(), callTool(nil))
	require.NoError(t, err)

	text := resultText(t, result)
	var entries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &entries))
	assert.Len(t, entries, 2)

	// No session → no active markers.
	for _, e := range entries {
		assert.False(t, e["active"].(bool))
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

func TestHandleCreateIdentity_WithSkills(t *testing.T) {
	h := testHandler(t)

	// Create skill attribute files that the identity will reference.
	root := h.store.Root()
	for _, slug := range []string{"go", "testing"} {
		s := attribute.NewStore(root, attribute.Skills)
		require.NoError(t, s.Save(&attribute.Attribute{Slug: slug, Content: "# " + slug + "\n"}))
	}

	result, err := h.handleCreateIdentity(context.Background(), callTool(map[string]interface{}{
		"name":   "Alice",
		"handle": "alice",
		"kind":   "human",
		"skills": []interface{}{"go", "testing"},
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	loaded, err := h.store.Load("alice", identity.Reference(true))
	require.NoError(t, err)
	assert.Equal(t, []string{"go", "testing"}, loaded.Skills)
}

// --- Attribute Tool Tests ---

func TestHandleSkill_CreateAndShow(t *testing.T) {
	h := testHandler(t)

	result, err := h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method":  "create",
		"slug":    "go-dev",
		"content": "# Go Development\nExpert in Go.",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	result, err = h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method": "show",
		"slug":   "go-dev",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "Go Development")
}

func TestHandleSkill_ListAndDelete(t *testing.T) {
	h := testHandler(t)

	result, err := h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method": "create", "slug": "a", "content": "# A\n",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	result, err = h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method": "create", "slug": "b", "content": "# B\n",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	result, err = h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
	require.NoError(t, err)
	assert.Contains(t, resultText(t, result), "a")
	assert.Contains(t, resultText(t, result), "b")

	result, err = h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method": "delete", "slug": "a",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestHandleSkill_AddAndRemove(t *testing.T) {
	h := testHandler(t)
	root := h.store.Root()
	s := attribute.NewStore(root, attribute.Skills)
	require.NoError(t, s.Save(&attribute.Attribute{Slug: "test-skill", Content: "# Test\n"}))

	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))

	result, err := h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method": "add", "handle": "alice", "slug": "test-skill",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	// Duplicate add should error.
	result, err = h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method": "add", "handle": "alice", "slug": "test-skill",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)

	result, err = h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method": "remove", "handle": "alice", "slug": "test-skill",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	// Remove non-existent should error.
	result, err = h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method": "remove", "handle": "alice", "slug": "test-skill",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandlePersonality_SetOnIdentity(t *testing.T) {
	h := testHandler(t)
	root := h.store.Root()
	ps := attribute.NewStore(root, attribute.Personalities)
	require.NoError(t, ps.Save(&attribute.Attribute{Slug: "friendly", Content: "# Friendly\n"}))

	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))

	result, err := h.handlePersonality(context.Background(), callTool(map[string]interface{}{
		"method": "set", "handle": "alice", "slug": "friendly",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	loaded, err := h.store.Load("alice", identity.Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "friendly", loaded.Personality)
}

func TestHandleWritingStyle_SetOnIdentity(t *testing.T) {
	h := testHandler(t)
	root := h.store.Root()
	ws := attribute.NewStore(root, attribute.WritingStyles)
	require.NoError(t, ws.Save(&attribute.Attribute{Slug: "concise", Content: "# Concise\n"}))

	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))

	result, err := h.handleWritingStyle(context.Background(), callTool(map[string]interface{}{
		"method": "set", "handle": "alice", "slug": "concise",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	loaded, err := h.store.Load("alice", identity.Reference(true))
	require.NoError(t, err)
	assert.Equal(t, "concise", loaded.WritingStyle)
}

func TestHandleSkill_MissingSlug(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method": "show",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "slug is required")
}

func TestHandleSkill_UnknownMethod(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleSkill(context.Background(), callTool(map[string]interface{}{
		"method": "bogus",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "unknown method")
}

// --- Ext Tool Tests ---

func TestHandleExt_SetAndGet(t *testing.T) {
	h := testHandler(t)
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))

	result, err := h.handleExt(context.Background(), callTool(map[string]interface{}{
		"method": "set", "persona": "alice", "namespace": "biff", "key": "tty", "value": "s001",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	result, err = h.handleExt(context.Background(), callTool(map[string]interface{}{
		"method": "get", "persona": "alice", "namespace": "biff",
	}))
	require.NoError(t, err)
	assert.Contains(t, resultText(t, result), "tty")
}

func TestHandleExt_SetMissingNamespace(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleExt(context.Background(), callTool(map[string]interface{}{
		"method": "set", "persona": "alice", "key": "x", "value": "y",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "namespace is required")
}

func TestHandleExt_SetMissingKey(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleExt(context.Background(), callTool(map[string]interface{}{
		"method": "set", "persona": "alice", "namespace": "biff", "value": "y",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "key is required")
}

// --- Session Tool Tests ---

func testHandlerWithSession(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)
	return NewHandler(s, ss)
}

func TestHandleSession_RosterNotFound(t *testing.T) {
	h := testHandlerWithSession(t)
	result, err := h.handleSession(context.Background(), callTool(map[string]interface{}{
		"method":     "roster",
		"session_id": "nonexistent",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleSession_JoinAndRoster(t *testing.T) {
	h := testHandlerWithSession(t)

	require.NoError(t, h.sessionStore.Create("test-sess",
		session.Participant{AgentID: "user1", Persona: "user1"},
		session.Participant{AgentID: "12345", Persona: "archie", Parent: "user1"},
	))

	result, err := h.handleSession(context.Background(), callTool(map[string]interface{}{
		"method":     "join",
		"session_id": "test-sess",
		"agent_id":   "sub-1",
		"persona":    "reviewer",
		"parent":     "12345",
		"agent_type": "code-reviewer",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	result, err = h.handleSession(context.Background(), callTool(map[string]interface{}{
		"method":     "roster",
		"session_id": "test-sess",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := resultText(t, result)
	var roster map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &roster))
	participants := roster["participants"].([]interface{})
	assert.Len(t, participants, 3)
}

func TestHandleSession_Iam(t *testing.T) {
	h := testHandlerWithSession(t)

	require.NoError(t, h.sessionStore.Create("test-iam",
		session.Participant{AgentID: "user1", Persona: "user1"},
		session.Participant{AgentID: "12345", Persona: "archie", Parent: "user1"},
	))

	result, err := h.handleSession(context.Background(), callTool(map[string]interface{}{
		"method":     "iam",
		"session_id": "test-iam",
		"agent_id":   "12345",
		"persona":    "new-persona",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	roster, err := h.sessionStore.Load("test-iam")
	require.NoError(t, err)
	p := roster.FindParticipant("12345")
	assert.Equal(t, "new-persona", p.Persona)
}

func TestHandleSession_Leave(t *testing.T) {
	h := testHandlerWithSession(t)

	require.NoError(t, h.sessionStore.Create("test-leave",
		session.Participant{AgentID: "user1", Persona: "user1"},
		session.Participant{AgentID: "12345", Persona: "archie", Parent: "user1"},
	))
	require.NoError(t, h.sessionStore.Join("test-leave",
		session.Participant{AgentID: "sub-1", Persona: "reviewer", Parent: "12345"},
	))

	result, err := h.handleSession(context.Background(), callTool(map[string]interface{}{
		"method":     "leave",
		"session_id": "test-leave",
		"agent_id":   "sub-1",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	roster, err := h.sessionStore.Load("test-leave")
	require.NoError(t, err)
	assert.Len(t, roster.Participants, 2)
}

func TestHandleSession_NoStore(t *testing.T) {
	h := testHandler(t) // No session store.
	result, err := h.handleSession(context.Background(), callTool(map[string]interface{}{
		"method":     "roster",
		"session_id": "any",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "not configured")
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
