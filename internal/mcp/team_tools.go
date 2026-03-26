package mcp

import (
	"context"
	"fmt"

	"github.com/punt-labs/ethos/internal/team"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

func (h *Handler) teamTool() mcplib.Tool {
	return mcplib.NewTool("team",
		mcplib.WithDescription("Manage teams. Methods: create, list, show, delete, add_member, remove_member, add_collab."),
		mcplib.WithString("method", mcplib.Required(),
			mcplib.Enum("create", "list", "show", "delete", "add_member", "remove_member", "add_collab"),
			mcplib.Description("Operation to perform."),
		),
		mcplib.WithString("name",
			mcplib.Description("Team name (lowercase alphanumeric with hyphens). Required for create, show, delete, add_member, remove_member, add_collab."),
		),
		mcplib.WithArray("repositories",
			mcplib.Description("List of repository paths. For create."),
			mcplib.WithStringItems(),
		),
		mcplib.WithString("identity",
			mcplib.Description("Identity handle. Required for add_member, remove_member."),
		),
		mcplib.WithString("role",
			mcplib.Description("Role name. Required for add_member, remove_member."),
		),
		mcplib.WithString("from",
			mcplib.Description("Source role name. Required for add_collab."),
		),
		mcplib.WithString("to",
			mcplib.Description("Target role name. Required for add_collab."),
		),
		mcplib.WithString("collab_type",
			mcplib.Description("Collaboration type: reports_to, collaborates_with, delegates_to. Required for add_collab."),
		),
		mcplib.WithArray("members",
			mcplib.Description("List of {identity, role} objects. Required for create."),
		),
	)
}

func (h *Handler) handleTeam(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	method := stringArg(req, "method", "")
	switch method {
	case "create":
		return h.handleCreateTeam(req)
	case "list":
		return h.handleListTeams()
	case "show":
		return h.handleShowTeam(req)
	case "delete":
		return h.handleDeleteTeam(req)
	case "add_member":
		return h.handleTeamAddMember(req)
	case "remove_member":
		return h.handleTeamRemoveMember(req)
	case "add_collab":
		return h.handleTeamAddCollab(req)
	default:
		return mcplib.NewToolResultError(fmt.Sprintf("unknown method %q", method)), nil
	}
}

func (h *Handler) handleCreateTeam(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	name := stringArg(req, "name", "")
	if name == "" {
		return mcplib.NewToolResultError("name is required for create"), nil
	}

	t := &team.Team{
		Name:         name,
		Repositories: stringArrayArg(req, "repositories"),
	}

	// Parse members from the raw arguments.
	members, err := parseMembersArg(req)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("invalid members: %v", err)), nil
	}
	if len(members) == 0 {
		return mcplib.NewToolResultError("at least one member is required for create"), nil
	}
	t.Members = members

	identityExists := func(handle string) bool { return h.store.Exists(handle) }
	roleExists := func(name string) bool { return h.roles != nil && h.roles.Exists(name) }

	if err := h.teams.Save(t, identityExists, roleExists); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to create team: %v", err)), nil
	}
	return jsonResult(t)
}

func (h *Handler) handleListTeams() (*mcplib.CallToolResult, error) {
	names, err := h.teams.List()
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to list teams: %v", err)), nil
	}
	if names == nil {
		names = []string{}
	}
	return jsonResult(names)
}

func (h *Handler) handleShowTeam(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	name := stringArg(req, "name", "")
	if name == "" {
		return mcplib.NewToolResultError("name is required for show"), nil
	}
	t, err := h.teams.Load(name)
	if err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("team not found: %v", err)), nil
	}
	return jsonResult(t)
}

func (h *Handler) handleDeleteTeam(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	name := stringArg(req, "name", "")
	if name == "" {
		return mcplib.NewToolResultError("name is required for delete"), nil
	}
	if err := h.teams.Delete(name); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to delete team: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Deleted team %q", name)), nil
}

func (h *Handler) handleTeamAddMember(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	name := stringArg(req, "name", "")
	if name == "" {
		return mcplib.NewToolResultError("name is required for add_member"), nil
	}
	ident := stringArg(req, "identity", "")
	if ident == "" {
		return mcplib.NewToolResultError("identity is required for add_member"), nil
	}
	r := stringArg(req, "role", "")
	if r == "" {
		return mcplib.NewToolResultError("role is required for add_member"), nil
	}

	identityExists := func(handle string) bool { return h.store.Exists(handle) }
	roleExists := func(n string) bool { return h.roles != nil && h.roles.Exists(n) }

	m := team.Member{Identity: ident, Role: r}
	if err := h.teams.AddMember(name, m, identityExists, roleExists); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to add member: %v", err)), nil
	}
	return jsonResult(m)
}

func (h *Handler) handleTeamRemoveMember(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	name := stringArg(req, "name", "")
	if name == "" {
		return mcplib.NewToolResultError("name is required for remove_member"), nil
	}
	ident := stringArg(req, "identity", "")
	if ident == "" {
		return mcplib.NewToolResultError("identity is required for remove_member"), nil
	}
	r := stringArg(req, "role", "")
	if r == "" {
		return mcplib.NewToolResultError("role is required for remove_member"), nil
	}

	if err := h.teams.RemoveMember(name, ident, r); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to remove member: %v", err)), nil
	}
	return mcplib.NewToolResultText(fmt.Sprintf("Removed %s (%s) from team %q", ident, r, name)), nil
}

func (h *Handler) handleTeamAddCollab(req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	name := stringArg(req, "name", "")
	if name == "" {
		return mcplib.NewToolResultError("name is required for add_collab"), nil
	}
	from := stringArg(req, "from", "")
	if from == "" {
		return mcplib.NewToolResultError("from is required for add_collab"), nil
	}
	to := stringArg(req, "to", "")
	if to == "" {
		return mcplib.NewToolResultError("to is required for add_collab"), nil
	}
	ct := stringArg(req, "collab_type", "")
	if ct == "" {
		return mcplib.NewToolResultError("collab_type is required for add_collab"), nil
	}

	c := team.Collaboration{From: from, To: to, Type: ct}
	if err := h.teams.AddCollaboration(name, c); err != nil {
		return mcplib.NewToolResultError(fmt.Sprintf("failed to add collaboration: %v", err)), nil
	}
	return jsonResult(c)
}

// parseMembersArg extracts members from the raw "members" argument.
// Expects an array of objects with "identity" and "role" string fields.
func parseMembersArg(req mcplib.CallToolRequest) ([]team.Member, error) {
	args := req.GetArguments()
	rawVal, exists := args["members"]
	if !exists {
		return nil, nil
	}
	raw, ok := rawVal.([]interface{})
	if !ok {
		return nil, fmt.Errorf("members must be an array, got %T", rawVal)
	}
	var members []team.Member
	for i, v := range raw {
		m, ok := v.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("member %d: expected object", i)
		}
		ident, _ := m["identity"].(string)
		role, _ := m["role"].(string)
		if ident == "" || role == "" {
			return nil, fmt.Errorf("member %d: identity and role are required", i)
		}
		members = append(members, team.Member{Identity: ident, Role: role})
	}
	return members, nil
}
