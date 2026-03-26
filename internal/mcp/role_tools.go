package mcp

import (
	"context"
	"fmt"

	"github.com/punt-labs/ethos/internal/role"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) roleTool() mcplib.Tool {
	return mcplib.NewTool("role",
		mcplib.WithDescription("Manage roles. Methods: create, list, show, delete."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("create", "list", "show", "delete"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("name",
			mcplib.Description("Role name (lowercase alphanumeric with hyphens). Required for create, show, delete."),
		),
		mcplib.WithArray("responsibilities",
			mcplib.Description("List of responsibilities. For create."),
			mcplib.WithStringItems(),
		),
		mcplib.WithArray("permissions",
			mcplib.Description("List of permissions. For create."),
			mcplib.WithStringItems(),
		),
	)
}

func (h *Handler) handleRole(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	method := stringArg(req, "method", "")
	switch method {
	case "create":
		return h.handleCreateRole(req)
	case "list":
		return h.handleListRoles()
	case "show":
		return h.handleShowRole(req)
	case "delete":
		return h.handleDeleteRole(req)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

func (h *Handler) handleCreateRole(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	name := stringArg(req, "name", "")
	if name == "" {
		return mcplib.NewToolResultError("name is required for create"), nil
	}

	r := &role.Role{
		Name:             name,
		Responsibilities: stringArrayArg(req, "responsibilities"),
		Permissions:      stringArrayArg(req, "permissions"),
	}

	if err := h.roles.Save(r); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to create role: %v", err)), nil
	}
	return jsonResult(r)
}

func (h *Handler) handleListRoles() (*mcplib.CallToolResult, error) {
	names, err := h.roles.List()
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to list roles: %v", err)), nil
	}
	if names == nil {
		names = []string{}
	}
	return jsonResult(names)
}

func (h *Handler) handleShowRole(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	name := stringArg(req, "name", "")
	if name == "" {
		return mcplib.NewToolResultError("name is required for show"), nil
	}
	r, err := h.roles.Load(name)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("role not found: %v", err)), nil
	}
	return jsonResult(r)
}

func (h *Handler) handleDeleteRole(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	name := stringArg(req, "name", "")
	if name == "" {
		return mcplib.NewToolResultError("name is required for delete"), nil
	}
	if err := h.roles.Delete(name); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to delete role: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Deleted role %q", name)), nil
}
