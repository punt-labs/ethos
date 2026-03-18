// Package identity provides the core identity model and CRUD operations.
package identity

// Identity represents a human or agent identity with channel bindings.
// This is the canonical type — cmd/ethos has a parallel struct for CLI
// serialization. When the MCP server is implemented, both will delegate
// to this package.
type Identity struct {
	Name         string   `yaml:"name"`
	Handle       string   `yaml:"handle"`
	Kind         string   `yaml:"kind"` // "human" or "agent"
	Email        string   `yaml:"email,omitempty"`
	GitHub       string   `yaml:"github,omitempty"`
	Voice        Voice    `yaml:"voice,omitempty"`
	Agent        string   `yaml:"agent,omitempty"`
	WritingStyle string   `yaml:"writing_style,omitempty"`
	Personality  string   `yaml:"personality,omitempty"`
	Skills       []string `yaml:"skills,omitempty"`
}

// Voice binds an identity to a Vox voice configuration.
type Voice struct {
	Provider string `yaml:"provider,omitempty"`
	VoiceID  string `yaml:"voice_id,omitempty"`
}

// Validate checks that required fields are present and valid.
func (id *Identity) Validate() error {
	if id.Name == "" {
		return &ValidationError{Field: "name", Message: "required"}
	}
	if id.Handle == "" {
		return &ValidationError{Field: "handle", Message: "required"}
	}
	if id.Kind != "human" && id.Kind != "agent" {
		return &ValidationError{Field: "kind", Message: "must be 'human' or 'agent'"}
	}
	return nil
}

// ValidationError represents a field-level validation failure.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}
