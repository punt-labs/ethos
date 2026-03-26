package mcp

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/session"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testHandler(t *testing.T) *Handler {
	t.Helper()
	s := identity.NewStore(t.TempDir())
	root := s.Root()
	return NewHandler(s,
		attribute.NewStore(root, attribute.Talents),
		attribute.NewStore(root, attribute.Personalities),
		attribute.NewStore(root, attribute.WritingStyles),
	)
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
		NewHandler(nil, nil, nil, nil)
	})
}

func TestRegisterTools(t *testing.T) {
	h := testHandler(t)
	s := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(true))
	h.RegisterTools(s)
	// If this doesn't panic, tools were registered successfully.
}

func TestHandleIdentity_Whoami_NoMatch(t *testing.T) {
	// Isolate git config so resolve chain finds nothing.
	tmp := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", tmp+"/empty.gitconfig")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("USER", "nobody")
	_ = os.WriteFile(tmp+"/empty.gitconfig", []byte(""), 0o644)

	h := testHandler(t)
	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "whoami",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleIdentity_Whoami_ResolvesFromOSUser(t *testing.T) {
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

	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "whoami",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	text := resultText(t, result)
	assert.Contains(t, text, "Alice")
}

func TestHandleIdentity_List_Empty(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := resultText(t, result)
	var entries []interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &entries))
	assert.Empty(t, entries)
}

func TestHandleIdentity_List_NoSession(t *testing.T) {
	h := testHandler(t)
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Bob", Handle: "bob", Kind: "agent",
	}))

	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
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

func TestHandleIdentity_List_PersonalityWritingStyle(t *testing.T) {
	h := testHandler(t)

	// Create the referenced attributes so Reference(true) validation passes.
	require.NoError(t, h.personalities.Save(&attribute.Attribute{
		Slug: "principal-engineer", Content: "test personality",
	}))
	require.NoError(t, h.writingStyles.Save(&attribute.Attribute{
		Slug: "concise-quantified", Content: "test writing style",
	}))

	require.NoError(t, h.store.Save(&identity.Identity{
		Name:         "Carol",
		Handle:       "carol",
		Kind:         "human",
		Personality:  "principal-engineer",
		WritingStyle: "concise-quantified",
	}))
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Dave", Handle: "dave", Kind: "agent",
	}))

	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
	require.NoError(t, err)

	text := resultText(t, result)
	var entries []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(text), &entries))
	require.Len(t, entries, 2)

	// Build a lookup by handle.
	byHandle := make(map[string]map[string]interface{})
	for _, e := range entries {
		hdl, _ := e["handle"].(string)
		byHandle[hdl] = e
	}

	// Carol has personality and writing_style.
	carol := byHandle["carol"]
	assert.Equal(t, "principal-engineer", carol["personality"])
	assert.Equal(t, "concise-quantified", carol["writing_style"])

	// Dave has neither.
	dave := byHandle["dave"]
	_, hasPers := dave["personality"]
	_, hasWS := dave["writing_style"]
	assert.False(t, hasPers, "dave should not have personality")
	assert.False(t, hasWS, "dave should not have writing_style")
}

func TestHandleIdentity_Get_Found(t *testing.T) {
	h := testHandler(t)
	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human", Email: "alice@example.com",
	}))

	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "get",
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

func TestHandleIdentity_Get_NotFound(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "get",
		"handle": "nobody",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleIdentity_Get_MissingHandle(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "get",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleIdentity_Create_Valid(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "create",
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

func TestHandleIdentity_Create_ValidationError(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "create",
		"name":   "Alice",
		"handle": "INVALID",
		"kind":   "human",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleIdentity_Create_WithSkills(t *testing.T) {
	h := testHandler(t)

	// Create talent attribute files that the identity will reference.
	root := h.store.Root()
	for _, slug := range []string{"go", "testing"} {
		s := attribute.NewStore(root, attribute.Talents)
		require.NoError(t, s.Save(&attribute.Attribute{Slug: slug, Content: "# " + slug + "\n"}))
	}

	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method":  "create",
		"name":    "Alice",
		"handle":  "alice",
		"kind":    "human",
		"talents": []interface{}{"go", "testing"},
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	loaded, err := h.store.Load("alice", identity.Reference(true))
	require.NoError(t, err)
	assert.Equal(t, []string{"go", "testing"}, loaded.Talents)
}

func TestHandleIdentity_UnknownMethod(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleIdentity(context.Background(), callTool(map[string]interface{}{
		"method": "bogus",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "unknown method")
}

func TestHandleIdentity_Create_MissingRequired(t *testing.T) {
	h := testHandler(t)

	tests := []struct {
		name string
		args map[string]interface{}
		want string
	}{
		{"missing name", map[string]interface{}{"method": "create", "handle": "alice", "kind": "human"}, "name is required"},
		{"missing handle", map[string]interface{}{"method": "create", "name": "Alice", "kind": "human"}, "handle is required"},
		{"missing kind", map[string]interface{}{"method": "create", "name": "Alice", "handle": "alice"}, "kind is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := h.handleIdentity(context.Background(), callTool(tt.args))
			require.NoError(t, err)
			assert.True(t, result.IsError)
			assert.Contains(t, resultText(t, result), tt.want)
		})
	}
}

// --- Attribute Tool Tests ---

func TestHandleSkill_CreateAndShow(t *testing.T) {
	h := testHandler(t)

	result, err := h.handleTalent(context.Background(), callTool(map[string]interface{}{
		"method":  "create",
		"slug":    "go-dev",
		"content": "# Go Development\nExpert in Go.",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	result, err = h.handleTalent(context.Background(), callTool(map[string]interface{}{
		"method": "show",
		"slug":   "go-dev",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "Go Development")
}

func TestHandleSkill_ListAndDelete(t *testing.T) {
	h := testHandler(t)

	result, err := h.handleTalent(context.Background(), callTool(map[string]interface{}{
		"method": "create", "slug": "a", "content": "# A\n",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	result, err = h.handleTalent(context.Background(), callTool(map[string]interface{}{
		"method": "create", "slug": "b", "content": "# B\n",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	result, err = h.handleTalent(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
	require.NoError(t, err)
	assert.Contains(t, resultText(t, result), "a")
	assert.Contains(t, resultText(t, result), "b")

	result, err = h.handleTalent(context.Background(), callTool(map[string]interface{}{
		"method": "delete", "slug": "a",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
}

func TestHandleSkill_AddAndRemove(t *testing.T) {
	h := testHandler(t)
	root := h.store.Root()
	s := attribute.NewStore(root, attribute.Talents)
	require.NoError(t, s.Save(&attribute.Attribute{Slug: "test-talent", Content: "# Test\n"}))

	require.NoError(t, h.store.Save(&identity.Identity{
		Name: "Alice", Handle: "alice", Kind: "human",
	}))

	result, err := h.handleTalent(context.Background(), callTool(map[string]interface{}{
		"method": "add", "handle": "alice", "slug": "test-talent",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	// Duplicate add should error.
	result, err = h.handleTalent(context.Background(), callTool(map[string]interface{}{
		"method": "add", "handle": "alice", "slug": "test-talent",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)

	result, err = h.handleTalent(context.Background(), callTool(map[string]interface{}{
		"method": "remove", "handle": "alice", "slug": "test-talent",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	// Remove non-existent should error.
	result, err = h.handleTalent(context.Background(), callTool(map[string]interface{}{
		"method": "remove", "handle": "alice", "slug": "test-talent",
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
	result, err := h.handleTalent(context.Background(), callTool(map[string]interface{}{
		"method": "show",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "slug is required")
}

func TestHandleSkill_UnknownMethod(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleTalent(context.Background(), callTool(map[string]interface{}{
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
		"method": "set", "handle": "alice", "namespace": "biff", "key": "tty", "value": "s001",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	result, err = h.handleExt(context.Background(), callTool(map[string]interface{}{
		"method": "get", "handle": "alice", "namespace": "biff",
	}))
	require.NoError(t, err)
	assert.Contains(t, resultText(t, result), "tty")
}

func TestHandleExt_SetMissingNamespace(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleExt(context.Background(), callTool(map[string]interface{}{
		"method": "set", "handle": "alice", "key": "x", "value": "y",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "namespace is required")
}

func TestHandleExt_SetMissingKey(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleExt(context.Background(), callTool(map[string]interface{}{
		"method": "set", "handle": "alice", "namespace": "biff", "value": "y",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "key is required")
}

func TestHandleExt_MissingHandle(t *testing.T) {
	h := testHandler(t)
	result, err := h.handleExt(context.Background(), callTool(map[string]interface{}{
		"method": "get", "namespace": "biff",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "handle is required")
}

// --- Session Tool Tests ---

func testHandlerWithSession(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	s := identity.NewStore(dir)
	ss := session.NewStore(dir)
	return NewHandler(s,
		attribute.NewStore(dir, attribute.Talents),
		attribute.NewStore(dir, attribute.Personalities),
		attribute.NewStore(dir, attribute.WritingStyles),
		ss,
	)
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

	// iam uses resolveSessionID which needs session_id passed explicitly
	// since FindClaudePID won't match the test PID.
	result, err := h.handleSession(context.Background(), callTool(map[string]interface{}{
		"method":     "iam",
		"session_id": "test-iam",
		"persona":    "new-persona",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "new-persona")

	// Verify the persona was actually set in the session store.
	roster, err := h.sessionStore.Load("test-iam")
	require.NoError(t, err)
	// iam uses FindClaudePID() as agent_id, so look for any participant
	// with the new persona.
	found := false
	for _, p := range roster.Participants {
		if p.Persona == "new-persona" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected participant with persona 'new-persona' in roster")
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

// --- Doctor Tool Tests ---

func TestHandleDoctor_ReturnsCheckResults(t *testing.T) {
	// Isolate git config so resolve chain is deterministic.
	tmp := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", tmp+"/empty.gitconfig")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	_ = os.WriteFile(tmp+"/empty.gitconfig", []byte(""), 0o644)

	h := testHandler(t)
	result, err := h.handleDoctor(context.Background(), callTool(map[string]interface{}{}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := resultText(t, result)
	// Summary line should contain check count.
	assert.Contains(t, text, "4 checks")
	// Table should contain check names.
	assert.Contains(t, text, "Identity directory")
	assert.Contains(t, text, "Human identity")
	assert.Contains(t, text, "Default agent")
	assert.Contains(t, text, "Duplicate fields")
	// Table should contain status values.
	assert.Contains(t, text, "PASS")
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
		"talents": []interface{}{"go", "testing"},
	})
	assert.Equal(t, []string{"go", "testing"}, stringArrayArg(req, "talents"))
	assert.Nil(t, stringArrayArg(req, "missing"))
}

// --- Role Tool Tests ---

func testHandlerWithRoles(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	s := identity.NewStore(dir)
	root := s.Root()
	rs := role.NewLayeredStore("", root)
	return NewHandlerWithOptions(s,
		attribute.NewStore(root, attribute.Talents),
		attribute.NewStore(root, attribute.Personalities),
		attribute.NewStore(root, attribute.WritingStyles),
		WithRoleStore(rs),
	)
}

func TestHandleRole_CreateAndShow(t *testing.T) {
	h := testHandlerWithRoles(t)

	result, err := h.handleRole(context.Background(), callTool(map[string]interface{}{
		"method":           "create",
		"name":             "coo",
		"responsibilities": []interface{}{"execution quality"},
		"permissions":      []interface{}{"approve-merges"},
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	text := resultText(t, result)
	assert.Contains(t, text, "coo")
	assert.Contains(t, text, "execution quality")

	result, err = h.handleRole(context.Background(), callTool(map[string]interface{}{
		"method": "show",
		"name":   "coo",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "approve-merges")
}

func TestHandleRole_ListAndDelete(t *testing.T) {
	h := testHandlerWithRoles(t)

	_, err := h.handleRole(context.Background(), callTool(map[string]interface{}{
		"method": "create", "name": "role-a",
	}))
	require.NoError(t, err)

	_, err = h.handleRole(context.Background(), callTool(map[string]interface{}{
		"method": "create", "name": "role-b",
	}))
	require.NoError(t, err)

	result, err := h.handleRole(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
	require.NoError(t, err)
	text := resultText(t, result)
	assert.Contains(t, text, "role-a")
	assert.Contains(t, text, "role-b")

	result, err = h.handleRole(context.Background(), callTool(map[string]interface{}{
		"method": "delete", "name": "role-a",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	result, err = h.handleRole(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
	require.NoError(t, err)
	text = resultText(t, result)
	assert.NotContains(t, text, "role-a")
	assert.Contains(t, text, "role-b")
}

func TestHandleRole_CreateMissingName(t *testing.T) {
	h := testHandlerWithRoles(t)

	result, err := h.handleRole(context.Background(), callTool(map[string]interface{}{
		"method": "create",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "name is required")
}

func TestHandleRole_ShowNotFound(t *testing.T) {
	h := testHandlerWithRoles(t)

	result, err := h.handleRole(context.Background(), callTool(map[string]interface{}{
		"method": "show",
		"name":   "nonexistent",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "not found")
}

func TestHandleRole_UnknownMethod(t *testing.T) {
	h := testHandlerWithRoles(t)

	result, err := h.handleRole(context.Background(), callTool(map[string]interface{}{
		"method": "bogus",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "unknown method")
}

func TestHandleRole_InvalidName(t *testing.T) {
	h := testHandlerWithRoles(t)

	result, err := h.handleRole(context.Background(), callTool(map[string]interface{}{
		"method": "create",
		"name":   "INVALID",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}
