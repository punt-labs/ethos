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
	"reports_to":        true,
	"collaborates_with": true,
	"delegates_to":      true,
}

// ValidateStructural checks every invariant on a team that does not
// require cross-package lookups. Store.Load calls this after
// yaml.Unmarshal to reject malformed team files at load time, closing
// the silent-drop class for typo'd collaboration roles that 9ai.1 r3
// surfaced. Callers that also need identity and role existence
// checks — Save, in particular — should call Validate instead.
//
// The checks here are a strict subset of Validate: team name slug
// rules, at-least-one-member, non-empty identity and role per member,
// no duplicate (identity, role) pair, collaboration from/to non-empty,
// no self-collaboration, valid Type, and from/to filled by a team
// member. Error messages are byte-for-byte identical to Validate so
// callers that match on error text still work.
func ValidateStructural(t *Team) error {
	if err := ValidateName(t.Name); err != nil {
		return fmt.Errorf("invalid team name: %w", err)
	}
	if len(t.Members) == 0 {
		return fmt.Errorf("team %q must have at least one member", t.Name)
	}

	// Per-member checks: non-empty fields and no duplicate
	// (identity, role) assignments.
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
	}

	// Build the set of roles filled by members on this team.
	filledRoles := make(map[string]bool)
	for _, m := range t.Members {
		filledRoles[m.Role] = true
	}

	// Collaboration checks: structure, type membership, and
	// referential integrity against filledRoles.
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

// Validate checks all Z-spec invariants on a team, including
// referential integrity against identity and role stores (via
// callbacks). Structural invariants are delegated to ValidateStructural
// so callers that cannot supply the callbacks (Store.Load, for
// example) still have a reachable subset.
func Validate(t *Team, identityExists func(string) bool, roleExists func(string) bool) error {
	if identityExists == nil || roleExists == nil {
		return fmt.Errorf("identityExists and roleExists callbacks must not be nil")
	}
	if err := ValidateStructural(t); err != nil {
		return err
	}
	for i, m := range t.Members {
		if !identityExists(m.Identity) {
			return fmt.Errorf("member %d: identity %q not found", i, m.Identity)
		}
		if !roleExists(m.Role) {
			return fmt.Errorf("member %d: role %q not found", i, m.Role)
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
