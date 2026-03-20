package mcp

import (
	"context"
	"fmt"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// registerAttributeTools adds all attribute CRUD and binding tools.
func (h *Handler) registerAttributeTools(s *mcpserver.MCPServer) {
	// Skill CRUD
	s.AddTool(h.createAttributeTool("create_skill", "skill"), h.handleCreateSkill)
	s.AddTool(h.getAttributeTool("get_skill", "skill"), h.handleGetSkill)
	s.AddTool(h.listAttributeTool("list_skills", "skills"), h.handleListSkills)
	// Personality CRUD
	s.AddTool(h.createAttributeTool("create_personality", "personality"), h.handleCreatePersonality)
	s.AddTool(h.getAttributeTool("get_personality", "personality"), h.handleGetPersonality)
	s.AddTool(h.listAttributeTool("list_personalities", "personalities"), h.handleListPersonalities)
	// Writing style CRUD
	s.AddTool(h.createAttributeTool("create_writing_style", "writing style"), h.handleCreateWritingStyle)
	s.AddTool(h.getAttributeTool("get_writing_style", "writing style"), h.handleGetWritingStyle)
	s.AddTool(h.listAttributeTool("list_writing_styles", "writing styles"), h.handleListWritingStyles)
	// Binding tools
	s.AddTool(h.setAttributeTool("set_personality", "personality"), h.handleSetPersonality)
	s.AddTool(h.setAttributeTool("set_writing_style", "writing style"), h.handleSetWritingStyle)
	s.AddTool(h.addSkillTool(), h.handleAddSkill)
	s.AddTool(h.removeSkillTool(), h.handleRemoveSkill)
}

// --- Tool Definitions ---

func (h *Handler) createAttributeTool(name, display string) mcplib.Tool {
	return mcplib.NewTool(name,
		mcplib.WithDescription(fmt.Sprintf("Create a new %s as a markdown document.", display)),
		mcplib.WithString("slug", mcplib.Required(),
			mcplib.Description("Unique slug (lowercase alphanumeric with hyphens)."),
		),
		mcplib.WithString("content", mcplib.Required(),
			mcplib.Description("Markdown content for the "+display+"."),
		),
	)
}

func (h *Handler) getAttributeTool(name, display string) mcplib.Tool {
	return mcplib.NewTool(name,
		mcplib.WithDescription(fmt.Sprintf("Get the content of a %s by slug.", display)),
		mcplib.WithString("slug", mcplib.Required(),
			mcplib.Description("The "+display+" slug to look up."),
		),
	)
}

func (h *Handler) listAttributeTool(name, display string) mcplib.Tool {
	return mcplib.NewTool(name,
		mcplib.WithDescription(fmt.Sprintf("List all available %s.", display)),
	)
}

func (h *Handler) setAttributeTool(name, display string) mcplib.Tool {
	return mcplib.NewTool(name,
		mcplib.WithDescription(fmt.Sprintf("Set the %s on an identity.", display)),
		mcplib.WithString("handle", mcplib.Required(),
			mcplib.Description("Identity handle to update."),
		),
		mcplib.WithString("slug", mcplib.Required(),
			mcplib.Description(fmt.Sprintf("The %s slug to set.", display)),
		),
	)
}

func (h *Handler) addSkillTool() mcplib.Tool {
	return mcplib.NewTool("add_skill",
		mcplib.WithDescription("Add a skill to an identity's skill list."),
		mcplib.WithString("handle", mcplib.Required(),
			mcplib.Description("Identity handle to update."),
		),
		mcplib.WithString("slug", mcplib.Required(),
			mcplib.Description("The skill slug to add."),
		),
	)
}

func (h *Handler) removeSkillTool() mcplib.Tool {
	return mcplib.NewTool("remove_skill",
		mcplib.WithDescription("Remove a skill from an identity's skill list."),
		mcplib.WithString("handle", mcplib.Required(),
			mcplib.Description("Identity handle to update."),
		),
		mcplib.WithString("slug", mcplib.Required(),
			mcplib.Description("The skill slug to remove."),
		),
	)
}

// --- Handlers ---

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
	type attrEntry struct {
		Slug string `json:"slug"`
	}
	type attrListResponse struct {
		Entries  []attrEntry `json:"entries"`
		Warnings []string    `json:"warnings,omitempty"`
	}
	entries := make([]attrEntry, 0, len(result.Attributes))
	for _, a := range result.Attributes {
		entries = append(entries, attrEntry{Slug: a.Slug})
	}
	return jsonResult(attrListResponse{Entries: entries, Warnings: result.Warnings})
}

// Skill handlers
func (h *Handler) handleCreateSkill(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return h.handleCreateAttribute(h.skills, "skill", req)
}
func (h *Handler) handleGetSkill(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return h.handleGetAttribute(h.skills, "skill", req)
}
func (h *Handler) handleListSkills(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return h.handleListAttribute(h.skills, "skills")
}

// Personality handlers
func (h *Handler) handleCreatePersonality(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return h.handleCreateAttribute(h.personalities, "personality", req)
}
func (h *Handler) handleGetPersonality(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return h.handleGetAttribute(h.personalities, "personality", req)
}
func (h *Handler) handleListPersonalities(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return h.handleListAttribute(h.personalities, "personalities")
}

// Writing style handlers
func (h *Handler) handleCreateWritingStyle(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return h.handleCreateAttribute(h.writingStyles, "writing style", req)
}
func (h *Handler) handleGetWritingStyle(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return h.handleGetAttribute(h.writingStyles, "writing style", req)
}
func (h *Handler) handleListWritingStyles(_ context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return h.handleListAttribute(h.writingStyles, "writing styles")
}

// Binding handlers

func (h *Handler) handleSetPersonality(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")
	slug := stringArg(req, "slug", "")
	if handle == "" || slug == "" {
		return mcplib.NewToolResultError("handle and slug are required"), nil
	}
	if err := h.store.Update(handle, func(id *identity.Identity) error {
		id.Personality = slug
		return nil
	}); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to set personality: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Set personality %q on %q", slug, handle)), nil
}

func (h *Handler) handleSetWritingStyle(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")
	slug := stringArg(req, "slug", "")
	if handle == "" || slug == "" {
		return mcplib.NewToolResultError("handle and slug are required"), nil
	}
	if err := h.store.Update(handle, func(id *identity.Identity) error {
		id.WritingStyle = slug
		return nil
	}); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to set writing style: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Set writing style %q on %q", slug, handle)), nil
}

func (h *Handler) handleAddSkill(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")
	slug := stringArg(req, "slug", "")
	if handle == "" || slug == "" {
		return mcplib.NewToolResultError("handle and slug are required"), nil
	}
	if !h.skills.Exists(slug) {
		return mcplib.NewToolResultError(fmt.Sprintf("skill %q not found", slug)), nil
	}
	if err := h.store.Update(handle, func(id *identity.Identity) error {
		for _, s := range id.Skills {
			if s == slug {
				return fmt.Errorf("skill %q already on %q", slug, handle)
			}
		}
		id.Skills = append(id.Skills, slug)
		return nil
	}); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to add skill: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Added skill %q to %q", slug, handle)), nil
}

func (h *Handler) handleRemoveSkill(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	handle := stringArg(req, "handle", "")
	slug := stringArg(req, "slug", "")
	if handle == "" || slug == "" {
		return mcplib.NewToolResultError("handle and slug are required"), nil
	}
	if err := h.store.Update(handle, func(id *identity.Identity) error {
		found := false
		filtered := make([]string, 0, len(id.Skills))
		for _, s := range id.Skills {
			if s == slug {
				found = true
			} else {
				filtered = append(filtered, s)
			}
		}
		if !found {
			return fmt.Errorf("skill %q not found on %q", slug, handle)
		}
		id.Skills = filtered
		return nil
	}); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to remove skill: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Removed skill %q from %q", slug, handle)), nil
}
