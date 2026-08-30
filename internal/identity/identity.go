// Package identity provides the core identity model and CRUD operations.
package identity

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/repomiss"
)

var validHandle = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// KindValues are the legal values of the kind field. Validate and the
// schema registry read this one slice, so the enum has a single source.
var KindValues = []string{"human", "agent"}

// Identity represents a human or agent identity with channel bindings.
type Identity struct {
	Name         string   `yaml:"name" json:"name"`
	Handle       string   `yaml:"handle" json:"handle"`
	Kind         string   `yaml:"kind" json:"kind"`
	Email        string   `yaml:"email,omitempty" json:"email,omitempty"`
	GitHub       string   `yaml:"github,omitempty" json:"github,omitempty"`
	Agent        string   `yaml:"agent,omitempty" json:"agent,omitempty"`
	WritingStyle string   `yaml:"writing_style,omitempty" json:"writing_style,omitempty"`
	Personality  string   `yaml:"personality,omitempty" json:"personality,omitempty"`
	Talents      []string `yaml:"talents,omitempty" json:"talents,omitempty"`
	// Skills lists Claude Code skill slugs to preload into this identity's
	// generated agent frontmatter, on top of the always-present
	// baseline-ops (DES-073). A slug must resolve to a sidecar-seeded or
	// bundle-scoped skill; unresolved slugs are the generator's problem,
	// not validated here.
	Skills []string `yaml:"skills,omitempty" json:"skills,omitempty"`

	// Resolved content — populated by Store.Load, never persisted.
	// Empty when loaded with Reference(true).
	WritingStyleContent string `yaml:"-" json:"writing_style_content,omitempty"`
	PersonalityContent  string `yaml:"-" json:"personality_content,omitempty"`
	// TalentContents is positionally indexed: TalentContents[i] is the resolved
	// content for Talents[i]. Empty string means the .md file was not found
	// (check Warnings for details).
	TalentContents []string `yaml:"-" json:"talent_contents,omitempty"`

	// Warnings from attribute resolution (e.g., missing .md files).
	// Populated by Store.Load, never persisted.
	Warnings []string `yaml:"-" json:"warnings,omitempty"`

	// MissingExt lists extension base files the repo's .vendor.yaml says
	// were vendored but that the source layer does not hold (DES-057).
	// Load records the verdict without failing — a missing namespace must
	// never brick a live session — and each surface decides what to do
	// with it. Always empty in layered mode and when there is no
	// manifest. Never persisted.
	MissingExt []repomiss.MissingRef `yaml:"-" json:"missing_ext,omitempty"`

	// Ext holds tool-scoped extension data, assembled on Load from
	// <handle>.ext/<namespace>.yaml files. Never persisted to the
	// core identity YAML. Keyed by namespace (tool name), then by key.
	Ext map[string]map[string]string `yaml:"-" json:"ext,omitempty"`
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
	if !validKind(id.Kind) {
		return &ValidationError{Field: "kind", Message: "must be 'human' or 'agent'"}
	}
	// Email is optional (an agent identity carries none), but when present it
	// must be a plausible address: resolution matches it verbatim against git
	// user.email, so a malformed value silently fails to resolve at a distant
	// call site. Enforce presence elsewhere (setup's human path); here we only
	// reject a non-empty value that cannot be an address.
	if id.Email != "" {
		if !strings.ContainsRune(id.Email, '@') || strings.ContainsAny(id.Email, " \t\r\n") {
			return &ValidationError{Field: "email", Message: fmt.Sprintf("%q is not a valid address", id.Email)}
		}
	}
	return nil
}

// validKind reports whether kind is one of KindValues.
func validKind(kind string) bool {
	for _, k := range KindValues {
		if kind == k {
			return true
		}
	}
	return false
}

// ValidationError represents a field-level validation failure.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}
