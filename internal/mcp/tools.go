// Package mcp provides MCP tool definitions and handlers for ethos.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/punt-labs/ethos/internal/identity"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// Handler groups MCP tool handlers with a shared identity store.
type Handler struct {
	store *identity.Store
}

// NewHandler creates a Handler with the given identity store.
// Panics if s is nil — callers must provide a valid store.
func NewHandler(s *identity.Store) *Handler {
	if s == nil {
		panic("mcp.NewHandler: store must not be nil")
	}
	return &Handler{store: s}
}

// RegisterTools adds all ethos MCP tools to the given server.
func (h *Handler) RegisterTools(s *mcpserver.MCPServer) {
	s.AddTool(h.whoamiTool(), h.handleWhoami)
	s.AddTool(h.listIdentitiesTool(), h.handleListIdentities)
	s.AddTool(h.getIdentityTool(), h.handleGetIdentity)
	s.AddTool(h.createIdentityTool(), h.handleCreateIdentity)
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

	if len(result.Warnings) > 0 {
		return jsonResult(struct {
			Identities []entry  `json:"identities"`
			Warnings   []string `json:"warnings"`
		}{entries, result.Warnings})
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
	var warnings []string
	listResult, listErr := h.store.List()
	if listErr == nil && len(listResult.Identities) == 1 {
		if err := h.store.SetActive(id.Handle); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not set as active: %v", err))
		}
	}

	if len(warnings) > 0 {
		return jsonResult(struct {
			Identity *identity.Identity `json:"identity"`
			Warnings []string           `json:"warnings"`
		}{id, warnings})
	}
	return jsonResult(id)
}

// --- Helpers ---

func stringArg(req mcplib.CallToolRequest, key, fallback string) string {
	args := req.GetArguments()
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok && s != "" {
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
