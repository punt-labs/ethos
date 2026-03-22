// Package identity provides the core identity model and CRUD operations.
package identity

import "regexp"

var validHandle = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Identity represents a human or agent identity with channel bindings.
type Identity struct {
	Name         string   `yaml:"name" json:"name"`
	Handle       string   `yaml:"handle" json:"handle"`
	Kind         string   `yaml:"kind" json:"kind"`
	Email        string   `yaml:"email,omitempty" json:"email,omitempty"`
	GitHub       string   `yaml:"github,omitempty" json:"github,omitempty"`
	Voice        *Voice   `yaml:"voice,omitempty" json:"voice,omitempty"`
	Agent        string   `yaml:"agent,omitempty" json:"agent,omitempty"`
	WritingStyle string   `yaml:"writing_style,omitempty" json:"writing_style,omitempty"`
	Personality  string   `yaml:"personality,omitempty" json:"personality,omitempty"`
	Talents      []string `yaml:"talents,omitempty" json:"talents,omitempty"`

	// Resolved content — populated by Store.Load, never persisted.
	// Empty when loaded with Reference(true).
	WritingStyleContent string   `yaml:"-" json:"writing_style_content,omitempty"`
	PersonalityContent  string   `yaml:"-" json:"personality_content,omitempty"`
	// TalentContents is positionally indexed: TalentContents[i] is the resolved
	// content for Talents[i]. Empty string means the .md file was not found
	// (check Warnings for details).
	TalentContents []string `yaml:"-" json:"talent_contents,omitempty"`

	// Warnings from attribute resolution (e.g., missing .md files).
	// Populated by Store.Load, never persisted.
	Warnings []string `yaml:"-" json:"warnings,omitempty"`

	// Ext holds tool-scoped extension data, assembled on Load from
	// <persona>.ext/<namespace>.yaml files. Never persisted to the
	// core identity YAML. Keyed by namespace (tool name), then by key.
	Ext map[string]map[string]string `yaml:"-" json:"ext,omitempty"`
}

// Voice binds an identity to a Vox voice configuration.
type Voice struct {
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	VoiceID  string `yaml:"voice_id,omitempty" json:"voice_id,omitempty"`
}

// Validate checks that required fields are present and valid.
func (id *Identity) Validate() error {
	if id.Name == "" {
		return &ValidationError{Field: "name", Message: "required"}
	}
	if id.Handle == "" {
		return &ValidationError{Field: "handle", Message: "required"}
	}
	if !validHandle.MatchString(id.Handle) {
		return &ValidationError{Field: "handle", Message: "must be lowercase alphanumeric with hyphens"}
	}
	if id.Kind != "human" && id.Kind != "agent" {
		return &ValidationError{Field: "kind", Message: "must be 'human' or 'agent'"}
	}
	if id.Voice != nil && id.Voice.VoiceID != "" && id.Voice.Provider == "" {
		return &ValidationError{Field: "voice", Message: "voice_id requires voice_provider"}
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
