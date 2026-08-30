package schema

import (
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/role"
	"github.com/punt-labs/ethos/v4/internal/team"
)

// slugPattern is the handle/name slug regexp shared by identity, role, and
// team name fields.
const slugPattern = `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`

// Identity overlays the persisted identity fields.
var Identity = Entity{
	Name:   "Identity",
	Wire:   "identity",
	Struct: identity.Identity{},
	Overlay: map[string]Overlay{
		"name":          {Type: "string", Description: "Display name."},
		"handle":        {Type: "string (slug)", Pattern: slugPattern, Description: "Stable key; lowercase alphanumeric with hyphens."},
		"kind":          {Type: "enum", Enum: identity.KindValues, Description: "Either human or agent."},
		"email":         {Type: "string", Description: "Email address; must contain @ and no whitespace. Beadle binding."},
		"github":        {Type: "string", Description: "GitHub handle. Biff binding."},
		"agent":         {Type: "string (path)", Description: "Path to the Claude Code agent .md file."},
		"writing_style": {Type: "string (slug)", Description: "Slug referencing writing-styles/; e.g. concise-quantified."},
		"personality":   {Type: "string (slug)", Description: "Slug referencing personalities/; e.g. principal-engineer."},
		"talents":       {Type: "list of string (slug)", Description: "Slugs referencing talents/; e.g. engineering."},
		"skills":        {Type: "list of string (slug)", Description: "Slugs referencing Claude Code skills preloaded into this identity's generated agent frontmatter, on top of baseline-ops; e.g. gstack-plan."},
	},
}

// Role overlays the persisted role fields. model is a partial enum: the
// four aliases plus any claude-* identifier, so Enum holds the aliases and
// Pattern holds the claude-* half.
var Role = Entity{
	Name:   "Role",
	Wire:   "role",
	Struct: role.Role{},
	Overlay: map[string]Overlay{
		"name":             {Type: "string (slug)", Pattern: slugPattern, Description: "Role name; lowercase alphanumeric with hyphens."},
		"model":            {Type: "enum + pattern", Enum: role.ModelAliases, Pattern: "^claude-.+", AllowEmpty: true, Description: "Claude model: opus, sonnet, haiku, inherit, or any claude-* ID. Empty inherits."},
		"responsibilities": {Type: "list of string", Description: "What the role is accountable for."},
		"permissions":      {Type: "list of string", Description: "Permission grants for the role."},
		"tools":            {Type: "list of string", Description: "Tool names available to the role."},
		"safety_constraints": {
			Type:        "list of object",
			Description: "Tool-usage restrictions: {tool, message}.",
			Fields: map[string]Overlay{
				"tool":    {Type: "string", Description: "Tool name or pattern the restriction applies to."},
				"message": {Type: "string", Description: "Human-readable denial message."},
			},
		},
		"output_format": {Type: "string (markdown)", Description: "Body of the generated agent's Output Format section."},
	},
}

// Team overlays the persisted team fields.
var Team = Entity{
	Name:   "Team",
	Wire:   "team",
	Struct: team.Team{},
	Overlay: map[string]Overlay{
		"name":         {Type: "string (slug)", Pattern: slugPattern, Description: "Team name; lowercase alphanumeric with hyphens."},
		"repositories": {Type: "list of string", Description: "Repository paths the team owns."},
		"members": {
			Type:        "list of object",
			Description: "Identity-to-role bindings: {identity, role}. At least one required.",
			Fields: map[string]Overlay{
				"identity": {Type: "string", Description: "Identity handle of the member."},
				"role":     {Type: "string", Description: "Role name the member fills."},
			},
		},
		"collaborations": {
			Type:        "list of object",
			Description: "Directed role relationships: {from, to, type}.",
			Fields: map[string]Overlay{
				"from": {Type: "string", Description: "Source role name."},
				"to":   {Type: "string", Description: "Target role name."},
				"type": {Type: "enum", Enum: team.CollaborationTypes, Description: "One of reports_to, collaborates_with, delegates_to."},
			},
		},
	},
}

// Registry maps wire name to Entity for CLI and MCP dispatch.
var Registry = map[string]Entity{
	Identity.Wire: Identity,
	Role.Wire:     Role,
	Team.Wire:     Team,
}
