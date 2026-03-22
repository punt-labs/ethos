// Package mcp provides MCP tool definitions and handlers for ethos.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/process"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/session"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Handler groups MCP tool handlers with shared stores.
type Handler struct {
	store        *identity.Store
	sessionStore *session.Store
	talents      *attribute.Store
	personalities *attribute.Store
	writingStyles *attribute.Store
}

// NewHandler creates a Handler with the given stores.
// Panics if identity store is nil. Session store may be nil (session
// tools will return errors if called without it). Attribute stores are
// derived from the identity store's root.
func NewHandler(s *identity.Store, ss ...*session.Store) *Handler {
	if s == nil {
		panic("mcp.NewHandler: store must not be nil")
	}
	root := s.Root()
	h := &Handler{
		store:         s,
		talents:       attribute.NewStore(root, attribute.Talents),
		personalities: attribute.NewStore(root, attribute.Personalities),
		writingStyles: attribute.NewStore(root, attribute.WritingStyles),
	}
	if len(ss) > 0 {
		h.sessionStore = ss[0]
	}
	return h
}

// RegisterTools adds all ethos MCP tools to the given server.
func (h *Handler) RegisterTools(s *mcpserver.MCPServer) {
	// Identity tool (consolidated)
	s.AddTool(h.identityTool(), h.handleIdentity)
	// Extension tool (consolidated)
	s.AddTool(h.extTool(), h.handleExt)
	// Session tool (consolidated)
	s.AddTool(h.sessionTool(), h.handleSession)
	// Attribute tools (consolidated)
	h.registerAttributeTools(s)
}

// --- Tool Definitions ---

func (h *Handler) identityTool() mcplib.Tool {
	return mcplib.NewTool("identity",
		mcplib.WithDescription("Manage identities. Methods: whoami, list, get, create."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("whoami", "list", "get", "create"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("handle",
			mcplib.Description("Identity handle. Required for get, create."),
		),
		mcplib.WithBoolean("reference",
			mcplib.Description("If true, return attribute slugs only without resolving .md content. For whoami, get."),
		),
		mcplib.WithString("name", mcplib.Description("Display name. Required for create.")),
		mcplib.WithString("kind", mcplib.Description("Either 'human' or 'agent'. Required for create.")),
		mcplib.WithString("email", mcplib.Description("Email address (beadle binding). For create.")),
		mcplib.WithString("github", mcplib.Description("GitHub username (biff binding). For create.")),
		mcplib.WithString("voice_provider", mcplib.Description("Voice provider name (vox binding). For create.")),
		mcplib.WithString("voice_id", mcplib.Description("Voice ID for the provider. For create.")),
		mcplib.WithString("agent", mcplib.Description("Path to Claude Code agent .md file. For create.")),
		mcplib.WithString("writing_style", mcplib.Description("Writing style slug. For create.")),
		mcplib.WithString("personality", mcplib.Description("Personality slug. For create.")),
		mcplib.WithArray("talents", mcplib.Description("List of talent slugs. For create."), mcplib.WithStringItems()),
	)
}

// --- Tool Handlers ---

func (h *Handler) handleIdentity(ctx context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	method := stringArg(req, "method", "")
	switch method {
	case "whoami":
		return h.handleWhoami(ctx, req)
	case "list":
		return h.handleListIdentities(ctx, req)
	case "get":
		return h.handleGetIdentity(ctx, req)
	case "create":
		return h.handleCreateIdentity(ctx, req)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

func (h *Handler) handleWhoami(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle, err := resolve.Resolve(h.store, h.sessionStore)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("no identity resolved: %v", err)), nil
	}

	var opts []identity.LoadOption
	if boolArg(req, "reference", false) {
		opts = append(opts, identity.Reference(true))
	}

	id, loadErr := h.store.Load(handle, opts...)
	if loadErr != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("identity %q not found: %v", handle, loadErr)), nil
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

	// Mark session participants as active.
	activeHandles := h.sessionParticipantHandles()
	entries := make([]entry, 0, len(result.Identities))
	for _, id := range result.Identities {
		entries = append(entries, entry{
			Handle: id.Handle,
			Name:   id.Name,
			Kind:   id.Kind,
			Active: activeHandles[id.Handle],
		})
	}

	return jsonResult(entries)
}

// sessionParticipantHandles returns the set of persona handles that are
// active in the current session. Returns an empty map if no session.
func (h *Handler) sessionParticipantHandles() map[string]bool {
	handles := make(map[string]bool)
	if h.sessionStore == nil {
		return handles
	}
	claudePID := process.FindClaudePID()
	sessionID, err := h.sessionStore.ReadCurrentSession(claudePID)
	if err != nil {
		return handles
	}
	roster, err := h.sessionStore.Load(sessionID)
	if err != nil {
		return handles
	}
	for _, p := range roster.Participants {
		if p.Persona != "" {
			handles[p.Persona] = true
		}
	}
	return handles
}

func (h *Handler) handleGetIdentity(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")
	if handle == "" {
		return mcplib.NewToolResultError("handle is required"), nil
	}

	var opts []identity.LoadOption
	if boolArg(req, "reference", false) {
		opts = append(opts, identity.Reference(true))
	}

	id, err := h.store.Load(handle, opts...)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("identity not found: %v", err)), nil
	}

	return jsonResult(id)
}

func (h *Handler) handleCreateIdentity(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	name := stringArg(req, "name", "")
	if name == "" {
		return mcplib.NewToolResultError("name is required for create"), nil
	}
	handle := stringArg(req, "handle", "")
	if handle == "" {
		return mcplib.NewToolResultError("handle is required for create"), nil
	}
	kind := stringArg(req, "kind", "")
	if kind == "" {
		return mcplib.NewToolResultError("kind is required for create"), nil
	}

	var voice *identity.Voice
	provider := stringArg(req, "voice_provider", "")
	voiceID := stringArg(req, "voice_id", "")
	if provider != "" {
		voice = &identity.Voice{Provider: provider, VoiceID: voiceID}
	} else if voiceID != "" {
		return mcplib.NewToolResultError("voice_id requires voice_provider"), nil
	}

	id := &identity.Identity{
		Name:         name,
		Handle:       handle,
		Kind:         kind,
		Email:        stringArg(req, "email", ""),
		GitHub:       stringArg(req, "github", ""),
		Voice:        voice,
		Agent:        stringArg(req, "agent", ""),
		WritingStyle: stringArg(req, "writing_style", ""),
		Personality:  stringArg(req, "personality", ""),
		Talents:      stringArrayArg(req, "talents"),
	}

	if err := id.Validate(); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("validation failed: %v", err)), nil
	}
	if err := h.store.Save(id); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to save: %v", err)), nil
	}

	return jsonResult(id)
}

// --- Extension Tool (consolidated) ---

func (h *Handler) extTool() mcplib.Tool {
	return mcplib.NewTool("ext",
		mcplib.WithDescription("Manage tool-scoped extensions on identities. Methods: get, set, del, list."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("get", "set", "del", "list"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("persona", mcplib.Required(),
			mcplib.Description("Identity persona name."),
		),
		mcplib.WithString("namespace",
			mcplib.Description("Tool namespace (e.g. beadle, biff). Required for get, set, del."),
		),
		mcplib.WithString("key",
			mcplib.Description("Key name. Required for set. Optional for get (omit for all keys) and del (omit to delete namespace)."),
		),
		mcplib.WithString("value",
			mcplib.Description("Value to store. Required for set."),
		),
	)
}

func (h *Handler) handleExt(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	method := stringArg(req, "method", "")
	persona := stringArg(req, "persona", "")
	namespace := stringArg(req, "namespace", "")
	key := stringArg(req, "key", "")
	value := stringArg(req, "value", "")

	switch method {
	case "get":
		if namespace == "" {
			return mcplib.NewToolResultError("namespace is required for get"), nil
		}
		m, err := h.store.ExtGet(persona, namespace, key)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		return jsonResult(m)
	case "set":
		if namespace == "" {
			return mcplib.NewToolResultError("namespace is required for set"), nil
		}
		if key == "" {
			return mcplib.NewToolResultError("key is required for set"), nil
		}
		if err := h.store.ExtSet(persona, namespace, key, value); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		return mcplib.NewToolResultText(fmt.Sprintf("set %s/%s/%s", persona, namespace, key)), nil
	case "del":
		if namespace == "" {
			return mcplib.NewToolResultError("namespace is required for del"), nil
		}
		if err := h.store.ExtDel(persona, namespace, key); err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		if key == "" {
			return mcplib.NewToolResultText(fmt.Sprintf("deleted namespace %s/%s", persona, namespace)), nil
		}
		return mcplib.NewToolResultText(fmt.Sprintf("deleted %s/%s/%s", persona, namespace, key)), nil
	case "list":
		namespaces, err := h.store.ExtList(persona)
		if err != nil {
			return mcplib.NewToolResultError(err.Error()), nil
		}
		if namespaces == nil {
			namespaces = []string{}
		}
		return jsonResult(namespaces)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

// --- Session Tool (consolidated) ---

func (h *Handler) sessionTool() mcplib.Tool {
	return mcplib.NewTool("session",
		mcplib.WithDescription("Manage session roster. Methods: iam, roster, join, leave. Session ID is auto-discovered if omitted."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("iam", "roster", "join", "leave"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("session_id",
			mcplib.Description("Session ID. Required for iam. Omit for other methods to auto-discover via process tree."),
		),
		mcplib.WithString("agent_id",
			mcplib.Description("Agent ID. Required for iam, join, leave."),
		),
		mcplib.WithString("persona",
			mcplib.Description("Persona handle. Required for iam. Optional for join."),
		),
		mcplib.WithString("parent",
			mcplib.Description("Parent agent ID. Optional for join."),
		),
		mcplib.WithString("agent_type",
			mcplib.Description("Agent type (e.g. code-reviewer, Explore). Optional for join."),
		),
	)
}

// resolveSessionID auto-discovers the session ID from the process tree
// when not explicitly provided.
func (h *Handler) resolveSessionID(req mcplib.CallToolRequest) (string, error) {
	sessionID := stringArg(req, "session_id", "")
	if sessionID != "" {
		return sessionID, nil
	}
	if h.sessionStore == nil {
		return "", fmt.Errorf("session store not configured")
	}
	claudePID := process.FindClaudePID()
	sid, err := h.sessionStore.ReadCurrentSession(claudePID)
	if err != nil {
		return "", fmt.Errorf("no active session (could not discover from PID %s): %v", claudePID, err)
	}
	return sid, nil
}

func (h *Handler) handleSession(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	if h.sessionStore == nil {
		return mcplib.NewToolResultError("session store not configured"), nil
	}
	method := stringArg(req, "method", "")
	sessionID, err := h.resolveSessionID(req)
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	switch method {
	case "iam":
		agentID := stringArg(req, "agent_id", "")
		persona := stringArg(req, "persona", "")
		if agentID == "" {
			return mcplib.NewToolResultError("agent_id is required for iam"), nil
		}
		if persona == "" {
			return mcplib.NewToolResultError("persona is required for iam"), nil
		}
		if err := h.sessionStore.Join(sessionID, session.Participant{
			AgentID: agentID,
			Persona: persona,
		}); err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to set persona: %v", err)), nil
		}
		return mcplib.NewToolResultText(fmt.Sprintf("Set persona %q for %s in session %s", persona, agentID, sessionID)), nil

	case "roster":
		roster, err := h.sessionStore.Load(sessionID)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to load roster: %v", err)), nil
		}
		return jsonResult(roster)

	case "join":
		if stringArg(req, "agent_id", "") == "" {
			return mcplib.NewToolResultError("agent_id is required for join"), nil
		}
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

	case "leave":
		agentID := stringArg(req, "agent_id", "")
		if agentID == "" {
			return mcplib.NewToolResultError("agent_id is required for leave"), nil
		}
		if err := h.sessionStore.Leave(sessionID, agentID); err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to leave: %v", err)), nil
		}
		return mcplib.NewToolResultText(fmt.Sprintf("Removed %s from session %s", agentID, sessionID)), nil

	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
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

func boolArg(req mcplib.CallToolRequest, key string, fallback bool) bool {
	args := req.GetArguments()
	if v, ok := args[key]; ok {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			return strings.EqualFold(b, "true") || b == "1"
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
