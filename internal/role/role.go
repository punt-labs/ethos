// Package role provides CRUD for role YAML files.
package role

import (
	"fmt"
	"strings"

	"github.com/punt-labs/ethos/internal/attribute"
)

// SafetyConstraint is a tool-usage restriction emitted as a body
// section in generated agent files. Each constraint names a tool (or
// tool pattern) and a human-readable denial message.
type SafetyConstraint struct {
	Tool    string `yaml:"tool" json:"tool"`
	Message string `yaml:"message" json:"message"`
}

// Role defines a named set of responsibilities and permissions.
//
// OutputFormat is a free-form markdown body emitted as the final
// section of the generated agent file. The generator owns the
// `## Output Format` heading; the role provides only the body. When
// empty, no section is emitted at all. The field is trusted source —
// role YAML is git-tracked and human-reviewed — so no validation runs
// against it, matching the trust boundary already in place for
// Responsibilities and the other string fields.
type Role struct {
	Name              string             `yaml:"name" json:"name"`
	Model             string             `yaml:"model,omitempty" json:"model,omitempty"`
	Responsibilities  []string           `yaml:"responsibilities,omitempty" json:"responsibilities,omitempty"`
	Permissions       []string           `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Tools             []string           `yaml:"tools,omitempty" json:"tools,omitempty"`
	SafetyConstraints []SafetyConstraint `yaml:"safety_constraints,omitempty" json:"safety_constraints,omitempty"`
	OutputFormat      string             `yaml:"output_format,omitempty" json:"output_format,omitempty"`
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
	shortAliases := map[string]bool{
		"opus": true, "sonnet": true, "haiku": true, "inherit": true,
	}
	if shortAliases[model] {
		return nil
	}
	if strings.HasPrefix(model, "claude-") && len(model) > len("claude-") {
		return nil
	}
	return fmt.Errorf("unrecognized model %q: must be opus, sonnet, haiku, inherit, or a full claude-* model ID", model)
}
