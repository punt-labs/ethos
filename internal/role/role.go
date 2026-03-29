// Package role provides CRUD for role YAML files.
package role

import "github.com/punt-labs/ethos/internal/attribute"

// Role defines a named set of responsibilities and permissions.
type Role struct {
	Name             string   `yaml:"name" json:"name"`
	Responsibilities []string `yaml:"responsibilities,omitempty" json:"responsibilities,omitempty"`
	Permissions      []string `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Tools            []string `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// ValidateName checks that a role name follows the same slug rules as attributes.
func ValidateName(name string) error {
	return attribute.ValidateSlug(name)
}
