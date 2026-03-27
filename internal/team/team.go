// Package team provides CRUD for team YAML files.
package team

import (
	"fmt"

	"github.com/punt-labs/ethos/internal/attribute"
)

// ErrNotFound is returned when a team is not found.
var ErrNotFound = fmt.Errorf("team not found")

// Member binds an identity to a role on a team.
type Member struct {
	Identity string `yaml:"identity" json:"identity"`
	Role     string `yaml:"role" json:"role"`
}

// Collaboration is a directed relationship between two roles on a team.
type Collaboration struct {
	From string `yaml:"from" json:"from"`
	To   string `yaml:"to" json:"to"`
	Type string `yaml:"type" json:"type"` // reports_to, collaborates_with, delegates_to
}

// Team is a named collection that binds identities to roles for a set
// of repositories.
type Team struct {
	Name           string          `yaml:"name" json:"name"`
	Repositories   []string        `yaml:"repositories,omitempty" json:"repositories,omitempty"`
	Members        []Member        `yaml:"members" json:"members"`
	Collaborations []Collaboration `yaml:"collaborations,omitempty" json:"collaborations,omitempty"`
}

// ValidateName checks that a team name follows slug rules.
func ValidateName(name string) error {
	return attribute.ValidateSlug(name)
}

// validCollabTypes enumerates the allowed collaboration types.
var validCollabTypes = map[string]bool{
	"reports_to":       true,
	"collaborates_with": true,
	"delegates_to":     true,
}

// Validate checks all Z-spec invariants on a team.
// The identityExists and roleExists callbacks check referential integrity
// without importing other packages.
func Validate(t *Team, identityExists func(string) bool, roleExists func(string) bool) error {
	if identityExists == nil || roleExists == nil {
		return fmt.Errorf("identityExists and roleExists callbacks must not be nil")
	}
	if err := ValidateName(t.Name); err != nil {
		return fmt.Errorf("invalid team name: %w", err)
	}
	if len(t.Members) == 0 {
		return fmt.Errorf("team %q must have at least one member", t.Name)
	}

	// Check each member references a valid identity and role, no duplicates.
	seen := make(map[string]bool)
	for i, m := range t.Members {
		if m.Identity == "" {
			return fmt.Errorf("member %d: identity is required", i)
		}
		if m.Role == "" {
			return fmt.Errorf("member %d: role is required", i)
		}
		key := m.Identity + "/" + m.Role
		if seen[key] {
			return fmt.Errorf("member %d: duplicate assignment (%s, %s)", i, m.Identity, m.Role)
		}
		seen[key] = true
		if !identityExists(m.Identity) {
			return fmt.Errorf("member %d: identity %q not found", i, m.Identity)
		}
		if !roleExists(m.Role) {
			return fmt.Errorf("member %d: role %q not found", i, m.Role)
		}
	}

	// Build set of roles filled by members on this team.
	filledRoles := make(map[string]bool)
	for _, m := range t.Members {
		filledRoles[m.Role] = true
	}

	// Validate collaborations.
	for i, c := range t.Collaborations {
		if c.From == "" || c.To == "" {
			return fmt.Errorf("collaboration %d: from and to are required", i)
		}
		if c.From == c.To {
			return fmt.Errorf("collaboration %d: self-collaboration not allowed (%s)", i, c.From)
		}
		if !validCollabTypes[c.Type] {
			return fmt.Errorf("collaboration %d: invalid type %q", i, c.Type)
		}
		if !filledRoles[c.From] {
			return fmt.Errorf("collaboration %d: role %q not filled by any member", i, c.From)
		}
		if !filledRoles[c.To] {
			return fmt.Errorf("collaboration %d: role %q not filled by any member", i, c.To)
		}
	}

	return nil
}

// hasMember reports whether the team has a member with the given identity and role.
func hasMember(t *Team, identity, role string) bool {
	for _, m := range t.Members {
		if m.Identity == identity && m.Role == role {
			return true
		}
	}
	return false
}
