package mcp

import (
	"context"
	"fmt"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// registerAttributeTools adds consolidated attribute tools (one per resource).
func (h *Handler) registerAttributeTools(s *mcpserver.MCPServer) {
	s.AddTool(h.talentTool(), h.handleTalent)
	s.AddTool(h.personalityTool(), h.handlePersonality)
	s.AddTool(h.writingStyleTool(), h.handleWritingStyle)
}

// --- Tool Definitions ---

func (h *Handler) talentTool() mcplib.Tool {
	return mcplib.NewTool("talent",
		mcplib.WithDescription("Manage talents. Methods: create, list, show, delete, add (to identity), remove (from identity)."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("create", "list", "show", "delete", "add", "remove"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("slug",
			mcplib.Description("Skill slug. Required for create, show, delete, add, remove."),
		),
		mcplib.WithString("content",
			mcplib.Description("Markdown content. Required for create."),
		),
		mcplib.WithString("handle",
			mcplib.Description("Identity handle. Required for add, remove."),
		),
	)
}

func (h *Handler) personalityTool() mcplib.Tool {
	return mcplib.NewTool("personality",
		mcplib.WithDescription("Manage personalities. Methods: create, list, show, delete, set (on identity)."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("create", "list", "show", "delete", "set"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("slug",
			mcplib.Description("Personality slug. Required for create, show, delete, set."),
		),
		mcplib.WithString("content",
			mcplib.Description("Markdown content. Required for create."),
		),
		mcplib.WithString("handle",
			mcplib.Description("Identity handle. Required for set."),
		),
	)
}

func (h *Handler) writingStyleTool() mcplib.Tool {
	return mcplib.NewTool("writing_style",
		mcplib.WithDescription("Manage writing styles. Methods: create, list, show, delete, set (on identity)."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("create", "list", "show", "delete", "set"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("slug",
			mcplib.Description("Writing style slug. Required for create, show, delete, set."),
		),
		mcplib.WithString("content",
			mcplib.Description("Markdown content. Required for create."),
		),
		mcplib.WithString("handle",
			mcplib.Description("Identity handle. Required for set."),
		),
	)
}

// --- Handlers ---

func (h *Handler) handleTalent(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	method := stringArg(req, "method", "")
	switch method {
	case "create":
		return h.handleCreateAttribute(h.talents, "talent", req)
	case "list":
		return h.handleListAttribute(h.talents, "talents")
	case "show":
		return h.handleGetAttribute(h.talents, "talent", req)
	case "delete":
		return h.handleDeleteAttribute(h.talents, "talent", req)
	case "add":
		return h.handleAddTalent(req)
	case "remove":
		return h.handleRemoveTalent(req)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

func (h *Handler) handlePersonality(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	method := stringArg(req, "method", "")
	switch method {
	case "create":
		return h.handleCreateAttribute(h.personalities, "personality", req)
	case "list":
		return h.handleListAttribute(h.personalities, "personalities")
	case "show":
		return h.handleGetAttribute(h.personalities, "personality", req)
	case "delete":
		return h.handleDeleteAttribute(h.personalities, "personality", req)
	case "set":
		return h.handleSetAttribute(h.store, "personality", req)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

func (h *Handler) handleWritingStyle(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	method := stringArg(req, "method", "")
	switch method {
	case "create":
		return h.handleCreateAttribute(h.writingStyles, "writing style", req)
	case "list":
		return h.handleListAttribute(h.writingStyles, "writing styles")
	case "show":
		return h.handleGetAttribute(h.writingStyles, "writing style", req)
	case "delete":
		return h.handleDeleteAttribute(h.writingStyles, "writing style", req)
	case "set":
		return h.handleSetAttribute(h.store, "writing style", req)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

// --- Shared Implementations ---

func (h *Handler) handleCreateAttribute(store *attribute.Store, display string, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	slug := stringArg(req, "slug", "")
	content := stringArg(req, "content", "")
	if slug == "" {
		return mcplib.NewToolResultError("slug is required"), nil
	}
	if content == "" {
		return mcplib.NewToolResultError("content is required"), nil
	}
	a := &attribute.Attribute{Slug: slug, Content: content}
	if err := store.Save(a); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to create %s: %v", display, err)), nil
	}
	return jsonResult(a)
}

func (h *Handler) handleGetAttribute(store *attribute.Store, display string, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	slug := stringArg(req, "slug", "")
	if slug == "" {
		return mcplib.NewToolResultError("slug is required"), nil
	}
	a, err := store.Load(slug)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("%s not found: %v", display, err)), nil
	}
	return jsonResult(a)
}

func (h *Handler) handleListAttribute(store *attribute.Store, display string) (*mcplib.CallToolResult, error) {
	result, err := store.List()
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to list %s: %v", display, err)), nil
	}
	type attrListResponse struct {
		Attributes []*attribute.Attribute `json:"attributes"`
		Warnings   []string               `json:"warnings,omitempty"`
	}
	return jsonResult(attrListResponse{Attributes: result.Attributes, Warnings: result.Warnings})
}

func (h *Handler) handleDeleteAttribute(store *attribute.Store, display string, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	slug := stringArg(req, "slug", "")
	if slug == "" {
		return mcplib.NewToolResultError("slug is required"), nil
	}
	if err := store.Delete(slug); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to delete %s: %v", display, err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Deleted %s %q", display, slug)), nil
}

func (h *Handler) handleSetAttribute(store identity.IdentityStore, display string, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")
	slug := stringArg(req, "slug", "")
	if handle == "" || slug == "" {
		return mcplib.NewToolResultError("handle and slug are required"), nil
	}
	if err := store.Update(handle, func(id *identity.Identity) error {
		switch display {
		case "personality":
			id.Personality = slug
		case "writing style":
			id.WritingStyle = slug
		default:
			return fmt.Errorf("unknown attribute type %q", display)
		}
		return nil
	}); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to set %s: %v", display, err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Set %s %q on %q", display, slug, handle)), nil
}

func (h *Handler) handleAddTalent(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")
	slug := stringArg(req, "slug", "")
	if handle == "" || slug == "" {
		return mcplib.NewToolResultError("handle and slug are required"), nil
	}
	if err := h.store.Update(handle, func(id *identity.Identity) error {
		for _, s := range id.Talents {
			if s == slug {
				return fmt.Errorf("talent %q already on %q", slug, handle)
			}
		}
		id.Talents = append(id.Talents, slug)
		return nil
	}); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to add talent: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Added talent %q to %q", slug, handle)), nil
}

func (h *Handler) handleRemoveTalent(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")
	slug := stringArg(req, "slug", "")
	if handle == "" || slug == "" {
		return mcplib.NewToolResultError("handle and slug are required"), nil
	}
	if err := h.store.Update(handle, func(id *identity.Identity) error {
		found := false
		filtered := make([]string, 0, len(id.Talents))
		for _, s := range id.Talents {
			if s == slug {
				found = true
			} else {
				filtered = append(filtered, s)
			}
		}
		if !found {
			return fmt.Errorf("talent %q not found on %q", slug, handle)
		}
		id.Talents = filtered
		return nil
	}); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to remove talent: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Removed talent %q from %q", slug, handle)), nil
}
