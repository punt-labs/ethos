package mcp

import (
	"context"
	"fmt"

	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/schema"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) roleTool() mcplib.Tool {
	// method is dispatch-level; every role field (name, model,
	// responsibilities, permissions, tools, safety_constraints,
	// output_format) is generated from the schema registry.
	fixed := []mcplib.ToolOption{
		mcplib.WithDescription("Manage roles. Methods: create, list, show, delete."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("create", "list", "show", "delete"),
			mcplib.Description("Operation to perform."),
		),
	}
	return mcplib.NewTool("role", withOptions(fixed, schema.Role)...)
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
	name, err := stringArgStrict(req, "name")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if name == "" {
		return mcplib.NewToolResultError("name is required for create"), nil
	}

	model, err := stringArgStrict(req, "model")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	if err := role.ValidateModel(model); err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	responsibilities, err := stringListArg(req, "responsibilities")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	permissions, err := stringListArg(req, "permissions")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	tools, err := stringListArg(req, "tools")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}
	constraints, err := parseSafetyConstraintsArg(req)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("invalid safety_constraints: %v", err)), nil
	}
	outputFormat, err := stringArgStrict(req, "output_format")
	if err != nil {
		return mcplib.NewToolResultError(err.Error()), nil
	}

	r := &role.Role{
		Name:              name,
		Model:             model,
		Responsibilities:  responsibilities,
		Permissions:       permissions,
		Tools:             tools,
		SafetyConstraints: constraints,
		OutputFormat:      outputFormat,
	}

	if err := h.roles.Save(r); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to create role: %v", err)), nil
	}
	return jsonResult(r)
}

// parseSafetyConstraintsArg extracts safety constraints from the raw
// "safety_constraints" argument: an array of objects with "tool" and
// "message" string fields, mirroring parseMembersArg.
func parseSafetyConstraintsArg(req mcplib.CallToolRequest) ([]role.SafetyConstraint, error) {
	rawVal, exists := req.GetArguments()["safety_constraints"]
	if !exists {
		return nil, nil
	}
	raw, ok := rawVal.([]interface{})
	if !ok {
		return nil, fmt.Errorf("safety_constraints must be an array, got %T", rawVal)
	}
	var out []role.SafetyConstraint
	for i, v := range raw {
		m, ok := v.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("constraint %d: expected object", i)
		}
		tool, _ := m["tool"].(string)
		msg, _ := m["message"].(string)
		if tool == "" || msg == "" {
			return nil, fmt.Errorf("constraint %d: tool and message must be non-empty strings", i)
		}
		out = append(out, role.SafetyConstraint{Tool: tool, Message: msg})
	}
	return out, nil
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
		return mcplib.NewToolResultError(fmt.Sprintf("failed to load role: %v", err)), nil
	}
	return jsonResult(r)
}

func (h *Handler) handleDeleteRole(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	name := stringArg(req, "name", "")
	if name == "" {
		return mcplib.NewToolResultError("name is required for delete"), nil
	}
	// Check referential integrity: no team should reference this role.
	// Fail closed — if we can't verify, don't delete.
	if h.teams != nil {
		teamNames, err := h.teams.List()
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("cannot verify role %q is unused: %v", name, err)), nil
		}
		for _, tn := range teamNames {
			t, err := h.teams.Load(tn)
			if err != nil {
				return mcplib.NewToolResultError(fmt.Sprintf("cannot verify role %q is unused: failed to load team %q: %v", name, tn, err)), nil
			}
			for _, m := range t.Members {
				if m.Role == name {
					return mcplib.NewToolResultError(fmt.Sprintf(
						"cannot delete role %q: referenced by team %q (member %s)", name, tn, m.Identity)), nil
				}
			}
		}
	}
	if err := h.roles.Delete(name); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to delete role: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Deleted role %q", name)), nil
}
