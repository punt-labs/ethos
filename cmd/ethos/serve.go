package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func runServeImpl() {
	s := server.NewMCPServer(
		"ethos",
		version,
		server.WithToolCapabilities(true),
	)

	s.AddTool(whoamiTool(), handleWhoami)
	s.AddTool(listIdentitiesTool(), handleListIdentities)
	s.AddTool(getIdentityTool(), handleGetIdentity)
	s.AddTool(createIdentityTool(), handleCreateIdentity)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "ethos: MCP server error: %v\n", err)
		os.Exit(1)
	}
}

// --- Tool Definitions ---

func whoamiTool() mcplib.Tool {
	return mcplib.NewTool("whoami",
		mcplib.WithDescription("Show or set the active identity. Without a handle, returns the active identity. With a handle, sets it."),
		mcplib.WithString("handle",
			mcplib.Description("Handle to set as active identity. Omit to show current."),
		),
	)
}

func listIdentitiesTool() mcplib.Tool {
	return mcplib.NewTool("list_identities",
		mcplib.WithDescription("List all available identities with handle, name, kind, and active status."),
	)
}

func getIdentityTool() mcplib.Tool {
	return mcplib.NewTool("get_identity",
		mcplib.WithDescription("Get full details of a specific identity by handle."),
		mcplib.WithString("handle",
			mcplib.Required(),
			mcplib.Description("The identity handle to look up."),
		),
	)
}

func createIdentityTool() mcplib.Tool {
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

func handleWhoami(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")

	if handle != "" {
		if err := setActiveIdentity(handle); err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("failed to set active identity: %v", err)), nil
		}
		return mcplib.NewToolResultText(fmt.Sprintf("Active identity set to %q", handle)), nil
	}

	id, err := activeIdentity()
	if err != nil {
		return mcplib.NewToolResultError("no active identity configured — run 'ethos create' first"), nil
	}

	return jsonResult(id)
}

func handleListIdentities(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	identities, err := listIdentities()
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to list identities: %v", err)), nil
	}

	type entry struct {
		Handle string `json:"handle"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Active bool   `json:"active"`
	}

	active, _ := activeIdentity()
	entries := make([]entry, 0, len(identities))
	for _, id := range identities {
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

func handleGetIdentity(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")
	if handle == "" {
		return mcplib.NewToolResultError("handle is required"), nil
	}

	id, err := loadIdentity(handle)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("identity not found: %v", err)), nil
	}

	return jsonResult(id)
}

func handleCreateIdentity(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	var voice *Voice
	provider := stringArg(req, "voice_provider", "")
	voiceID := stringArg(req, "voice_id", "")
	if provider != "" {
		voice = &Voice{Provider: provider, VoiceID: voiceID}
	} else if voiceID != "" {
		return mcplib.NewToolResultError("voice_id requires voice_provider"), nil
	}

	id := &Identity{
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

	if err := validateIdentity(id); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("validation failed: %v", err)), nil
	}
	if err := saveIdentity(id); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to save: %v", err)), nil
	}

	setActiveIfFirst(id.Handle)
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
