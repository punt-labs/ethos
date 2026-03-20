// Package attribute provides CRUD for named markdown attribute files
// (skills, personalities, writing styles).
package attribute

import "regexp"

var validSlug = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Kind configures a Store for one attribute type.
type Kind struct {
	DirName     string // directory under ethos root: "skills", "personalities", "writing-styles"
	CmdName     string // CLI command name: "skill", "personality", "writing-style"
	DisplayName string // human-readable singular: "skill", "personality", "writing style"
	PluralName  string // human-readable plural: "skills", "personalities", "writing styles"
}

// Pre-defined kinds.
var (
	Skills        = Kind{DirName: "skills", CmdName: "skill", DisplayName: "skill", PluralName: "skills"}
	Personalities = Kind{DirName: "personalities", CmdName: "personality", DisplayName: "personality", PluralName: "personalities"}
	WritingStyles = Kind{DirName: "writing-styles", CmdName: "writing-style", DisplayName: "writing style", PluralName: "writing styles"}
)

// Attribute is a named markdown document.
type Attribute struct {
	Slug    string `json:"slug"`
	Content string `json:"content"`
}

// ListResult holds attribute listing results with warnings for unreadable files.
type ListResult struct {
	Attributes []*Attribute
	Warnings   []string
}

// ValidateSlug checks that a slug is valid.
func ValidateSlug(slug string) error {
	if slug == "" {
		return &ValidationError{Field: "slug", Message: "required"}
	}
	if !validSlug.MatchString(slug) {
		return &ValidationError{Field: "slug", Message: "must be lowercase alphanumeric with hyphens"}
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
