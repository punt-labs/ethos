// Package mcp provides MCP tool definitions and handlers for ethos.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/session"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Handler groups MCP tool handlers with shared stores.
type Handler struct {
	store        *identity.Store
	sessionStore *session.Store
}

// NewHandler creates a Handler with the given stores.
// Panics if identity store is nil. Session store may be nil (session
// tools will return errors if called without it).
func NewHandler(s *identity.Store, ss ...*session.Store) *Handler {
	if s == nil {
		panic("mcp.NewHandler: store must not be nil")
	}
	h := &Handler{store: s}
	if len(ss) > 0 {
		h.sessionStore = ss[0]
	}
	return h
}

// RegisterTools adds all ethos MCP tools to the given server.
func (h *Handler) RegisterTools(s *mcpserver.MCPServer) {
	s.AddTool(h.whoamiTool(), h.handleWhoami)
	s.AddTool(h.listIdentitiesTool(), h.handleListIdentities)
	s.AddTool(h.getIdentityTool(), h.handleGetIdentity)
	s.AddTool(h.createIdentityTool(), h.handleCreateIdentity)
	s.AddTool(h.extGetTool(), h.handleExtGet)
	s.AddTool(h.extSetTool(), h.handleExtSet)
	s.AddTool(h.extDelTool(), h.handleExtDel)
	s.AddTool(h.extListTool(), h.handleExtList)
	s.AddTool(h.sessionIamTool(), h.handleSessionIam)
	s.AddTool(h.sessionRosterTool(), h.handleSessionRoster)
	s.AddTool(h.sessionJoinTool(), h.handleSessionJoin)
	s.AddTool(h.sessionLeaveTool(), h.handleSessionLeave)
}

// --- Tool Definitions ---

func (h *Handler) whoamiTool() mcplib.Tool {
	return mcplib.NewTool("whoami",
		mcplib.WithDescription("Show or set the active identity. Without a handle, returns the active identity. With a handle, sets it."),
		mcplib.WithString("handle",
			mcplib.Description("Handle to set as active identity. Omit to show current."),
		),
	)
}

func (h *Handler) listIdentitiesTool() mcplib.Tool {
	return mcplib.NewTool("list_identities",
		mcplib.WithDescription("List all available identities with handle, name, kind, and active status."),
	)
}

func (h *Handler) getIdentityTool() mcplib.Tool {
	return mcplib.NewTool("get_identity",
		mcplib.WithDescription("Get full details of a specific identity by handle."),
		mcplib.WithString("handle",
			mcplib.Required(),
			mcplib.Description("The identity handle to look up."),
		),
	)
}

func (h *Handler) createIdentityTool() mcplib.Tool {
	return mcplib.NewTool("create_identity",
		mcplib.WithDescription("Create a new identity from provided fields."),
		mcplib.WithString("name", mcplib.Required(), mcplib.Description("Display name")),
		mcplib.WithString("handle", mcplib.Required(), mcplib.Description("Unique handle (lowercase, alphanumeric, hyphens)")),
		mcplib.WithString("kind", mcplib.Required(), mcplib.Description("Either 'human' or 'agent'")),
		mcplib.WithString("email", mcplib.Description("Email address (beadle binding)")),
		mcplib.WithString("github", mcplib.Description("GitHub username (biff binding)")),
		mcplib.WithString("voice_provider", mcplib.Description("Voice provider name (vox binding)")),
		mcplib.WithString("voice_id", mcplib.Description("Voice ID for the provider")),
		mcplib.WithString("agent", mcplib.Description("Path to Claude Code agent .md file")),
		mcplib.WithString("writing_style", mcplib.Description("Writing style description")),
		mcplib.WithString("personality", mcplib.Description("Personality description")),
		mcplib.WithArray("skills", mcplib.Description("List of skill tags"), mcplib.WithStringItems()),
	)
}

// --- Tool Handlers ---

func (h *Handler) handleWhoami(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")

	if handle != "" {
		if err := h.store.SetActive(handle); err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to set active identity: %v", err)), nil
		}
		return mcplib.NewToolResultText(fmt.Sprintf("Active identity set to %q", handle)), nil
	}

	id, err := h.store.Active()
	if err != nil {
		return mcplib.NewToolResultError("no active identity configured — run 'ethos create' first"), nil
	}

	return jsonResult(id)
}

func (h *Handler) handleListIdentities(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	result, err := h.store.List()
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to list identities: %v", err)), nil
	}

	type entry struct {
		Handle string `json:"handle"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Active bool   `json:"active"`
	}

	active, _ := h.store.Active()
	entries := make([]entry, 0, len(result.Identities))
	for _, id := range result.Identities {
		isActive := active != nil && active.Handle == id.Handle
		entries = append(entries, entry{
			Handle: id.Handle,
			Name:   id.Name,
			Kind:   id.Kind,
			Active: isActive,
		})
	}

	return jsonResult(entries)
}

func (h *Handler) handleGetIdentity(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")
	if handle == "" {
		return mcplib.NewToolResultError("handle is required"), nil
	}

	id, err := h.store.Load(handle)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("identity not found: %v", err)), nil
	}

	return jsonResult(id)
}

func (h *Handler) handleCreateIdentity(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	var voice *identity.Voice
	provider := stringArg(req, "voice_provider", "")
	voiceID := stringArg(req, "voice_id", "")
	if provider != "" {
		voice = &identity.Voice{Provider: provider, VoiceID: voiceID}
	} else if voiceID != "" {
		return mcplib.NewToolResultError("voice_id requires voice_provider"), nil
	}

	id := &identity.Identity{
		Name:         stringArg(req, "name", ""),
		Handle:       stringArg(req, "handle", ""),
		Kind:         stringArg(req, "kind", ""),
		Email:        stringArg(req, "email", ""),
		GitHub:       stringArg(req, "github", ""),
		Voice:        voice,
		Agent:        stringArg(req, "agent", ""),
		WritingStyle: stringArg(req, "writing_style", ""),
		Personality:  stringArg(req, "personality", ""),
		Skills:       stringArrayArg(req, "skills"),
	}

	if err := id.Validate(); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("validation failed: %v", err)), nil
	}
	if err := h.store.Save(id); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to save: %v", err)), nil
	}

	// Set as active if it's the first identity.
	// Best-effort: set as active if first identity.
	listResult, listErr := h.store.List()
	if listErr == nil && len(listResult.Identities) == 1 {
		_ = h.store.SetActive(id.Handle)
	}

	return jsonResult(id)
}

// --- Extension Tool Definitions ---

func (h *Handler) extGetTool() mcplib.Tool {
	return mcplib.NewTool("ext_get",
		mcplib.WithDescription("Read extension key(s) for a persona. Returns all keys if key is omitted."),
		mcplib.WithString("persona", mcplib.Required(), mcplib.Description("Identity persona name")),
		mcplib.WithString("namespace", mcplib.Required(), mcplib.Description("Tool namespace (e.g. beadle, biff)")),
		mcplib.WithString("key", mcplib.Description("Specific key to read. Omit to read all keys.")),
	)
}

func (h *Handler) extSetTool() mcplib.Tool {
	return mcplib.NewTool("ext_set",
		mcplib.WithDescription("Write an extension key-value pair for a persona."),
		mcplib.WithString("persona", mcplib.Required(), mcplib.Description("Identity persona name")),
		mcplib.WithString("namespace", mcplib.Required(), mcplib.Description("Tool namespace (e.g. beadle, biff)")),
		mcplib.WithString("key", mcplib.Required(), mcplib.Description("Key name")),
		mcplib.WithString("value", mcplib.Required(), mcplib.Description("Value to store")),
	)
}

func (h *Handler) extDelTool() mcplib.Tool {
	return mcplib.NewTool("ext_del",
		mcplib.WithDescription("Delete an extension key or entire namespace for a persona."),
		mcplib.WithString("persona", mcplib.Required(), mcplib.Description("Identity persona name")),
		mcplib.WithString("namespace", mcplib.Required(), mcplib.Description("Tool namespace")),
		mcplib.WithString("key", mcplib.Description("Key to delete. Omit to delete entire namespace.")),
	)
}

func (h *Handler) extListTool() mcplib.Tool {
	return mcplib.NewTool("ext_list",
		mcplib.WithDescription("List all extension namespaces for a persona."),
		mcplib.WithString("persona", mcplib.Required(), mcplib.Description("Identity persona name")),
	)
}

// --- Extension Tool Handlers ---

func (h *Handler) handleExtGet(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	persona := stringArg(req, "persona", "")
	namespace := stringArg(req, "namespace", "")
	key := stringArg(req, "key", "")

	m, err := h.store.ExtGet(persona, namespace, key)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	return jsonResult(m)
}

func (h *Handler) handleExtSet(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	persona := stringArg(req, "persona", "")
	namespace := stringArg(req, "namespace", "")
	key := stringArg(req, "key", "")
	value := stringArg(req, "value", "")

	if err := h.store.ExtSet(persona, namespace, key, value); err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("set %s/%s/%s", persona, namespace, key)), nil
}

func (h *Handler) handleExtDel(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	persona := stringArg(req, "persona", "")
	namespace := stringArg(req, "namespace", "")
	key := stringArg(req, "key", "")

	if err := h.store.ExtDel(persona, namespace, key); err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if key == "" {
		return mcplib.NewToolResultText(fmt.Sprintf("deleted namespace %s/%s", persona, namespace)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("deleted %s/%s/%s", persona, namespace, key)), nil
}

func (h *Handler) handleExtList(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	persona := stringArg(req, "persona", "")

	namespaces, err := h.store.ExtList(persona)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if namespaces == nil {
		namespaces = []string{}
	}
	return jsonResult(namespaces)
}

// --- Session Tool Definitions ---

func (h *Handler) sessionIamTool() mcplib.Tool {
	return mcplib.NewTool("session_iam",
		mcplib.WithDescription("Declare persona for the current participant in a session."),
		mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
		mcplib.WithString("agent_id", mcplib.Required(), mcplib.Description("Agent ID of the participant")),
		mcplib.WithString("persona", mcplib.Required(), mcplib.Description("Persona handle to set")),
	)
}

func (h *Handler) sessionRosterTool() mcplib.Tool {
	return mcplib.NewTool("session_roster",
		mcplib.WithDescription("Return the full participant roster for a session."),
		mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
	)
}

func (h *Handler) sessionJoinTool() mcplib.Tool {
	return mcplib.NewTool("session_join",
		mcplib.WithDescription("Register a new participant in a session."),
		mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
		mcplib.WithString("agent_id", mcplib.Required(), mcplib.Description("Unique agent ID")),
		mcplib.WithString("persona", mcplib.Description("Persona handle")),
		mcplib.WithString("parent", mcplib.Description("Parent agent ID")),
		mcplib.WithString("agent_type", mcplib.Description("Agent type (e.g. code-reviewer, Explore)")),
	)
}

func (h *Handler) sessionLeaveTool() mcplib.Tool {
	return mcplib.NewTool("session_leave",
		mcplib.WithDescription("Remove a participant from a session."),
		mcplib.WithString("session_id", mcplib.Required(), mcplib.Description("Session ID")),
		mcplib.WithString("agent_id", mcplib.Required(), mcplib.Description("Agent ID to remove")),
	)
}

// --- Session Tool Handlers ---

func (h *Handler) handleSessionIam(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if h.sessionStore == nil {
		return mcplib.NewToolResultError("session store not configured"), nil
	}
	sessionID := stringArg(req, "session_id", "")
	agentID := stringArg(req, "agent_id", "")
	persona := stringArg(req, "persona", "")

	if err := h.sessionStore.Join(sessionID, session.Participant{
		AgentID: agentID,
		Persona: persona,
	}); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to set persona: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Set persona %q for %s in session %s", persona, agentID, sessionID)), nil
}

func (h *Handler) handleSessionRoster(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if h.sessionStore == nil {
		return mcplib.NewToolResultError("session store not configured"), nil
	}
	sessionID := stringArg(req, "session_id", "")
	roster, err := h.sessionStore.Load(sessionID)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to load roster: %v", err)), nil
	}
	return jsonResult(roster)
}

func (h *Handler) handleSessionJoin(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if h.sessionStore == nil {
		return mcplib.NewToolResultError("session store not configured"), nil
	}
	sessionID := stringArg(req, "session_id", "")
	p := session.Participant{
		AgentID:   stringArg(req, "agent_id", ""),
		Persona:   stringArg(req, "persona", ""),
		Parent:    stringArg(req, "parent", ""),
		AgentType: stringArg(req, "agent_type", ""),
	}
	if err := h.sessionStore.Join(sessionID, p); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to join: %v", err)), nil
	}
	return jsonResult(p)
}

func (h *Handler) handleSessionLeave(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if h.sessionStore == nil {
		return mcplib.NewToolResultError("session store not configured"), nil
	}
	sessionID := stringArg(req, "session_id", "")
	agentID := stringArg(req, "agent_id", "")
	if err := h.sessionStore.Leave(sessionID, agentID); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to leave: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Removed %s from session %s", agentID, sessionID)), nil
}

// --- Helpers ---

func stringArg(req mcplib.CallToolRequest, key, fallback string) string {
	args := req.GetArguments()
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return fallback
}

func stringArrayArg(req mcplib.CallToolRequest, key string) []string {
	args := req.GetArguments()
	raw, ok := args[key].([]interface{})
	if !ok {
		return nil
	}
	var result []string
	for _, v := range raw {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func jsonResult(v any) (*mcplib.CallToolResult, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("JSON marshal error: %v", err)), nil
	}
	return mcplib.NewToolResultText(string(data)), nil
}
