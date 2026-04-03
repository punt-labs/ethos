// Package role provides CRUD for role YAML files.
package role

import (
	"fmt"

	"github.com/punt-labs/ethos/internal/attribute"
)

// Role defines a named set of responsibilities and permissions.
type Role struct {
	Name             string   `yaml:"name" json:"name"`
	Model            string   `yaml:"model,omitempty" json:"model,omitempty"`
	Responsibilities []string `yaml:"responsibilities,omitempty" json:"responsibilities,omitempty"`
	Permissions      []string `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Tools            []string `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// ValidateName checks that a role name follows the same slug rules as attributes.
func ValidateName(name string) error {
	return attribute.ValidateSlug(name)
}

// ValidateModel checks that a model value is a known Claude model identifier
// or empty (meaning inherit). Returns an error for unrecognized values.
func ValidateModel(model string) error {
	if model == "" {
		return nil // empty means inherit
	}
	valid := map[string]bool{
		"opus": true, "sonnet": true, "haiku": true, "inherit": true,
		"claude-opus-4-6": true, "claude-sonnet-4-6": true, "claude-haiku-4-5-20251001": true,
	}
	if valid[model] {
		return nil
	}
	return fmt.Errorf("unrecognized model %q: must be one of opus, sonnet, haiku, inherit, or a full model ID", model)
}
