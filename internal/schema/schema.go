// Package schema publishes the field shape of the typed entities —
// identity, role, team — from one registry. Field names and
// required-ness are read live from the Go structs by reflection; the
// registry overlays only what a struct tag cannot carry: descriptions,
// enums, human type labels, and patterns. Every schema-shaped consumer
// (the CLI schema subcommands, the MCP inputSchema, validate-content,
// the seeded READMEs) reads from here, so the field set cannot drift.
package schema

import (
	"reflect"
	"strings"
)

// Overlay is the metadata a struct tag cannot hold, keyed by wire name.
// Field names and required-ness come from the struct; this supplies the
// rest. Description prose is authored and unguarded.
type Overlay struct {
	Type        string             // human type label: "string (slug)", "list of object"
	Enum        []string           // closed-enum values, when the field is a closed enum
	Pattern     string             // regexp for slug/handle fields
	Description string             // one sentence, human-facing
	Fields      map[string]Overlay // nested-object overlays (Member, Collaboration, SafetyConstraint)
}

// Entity binds a Go struct to its overlay. The struct is reflected for
// field names and required-ness (required when the yaml tag lacks
// ,omitempty); Overlay is merged in by wire name. Struct is a zero value
// used only for its type — it is never persisted.
type Entity struct {
	Name    string // "Identity"
	Wire    string // "identity" — the subcommand and JSON title stem
	Struct  any
	Overlay map[string]Overlay
}

// Field is the merged view of one field: name and required from
// reflection, the rest from the overlay. Produced on demand by Fields,
// never stored.
type Field struct {
	Name        string
	Type        string
	Pattern     string
	Description string
	Required    bool
	List        bool     // the Go field is a slice
	Enum        []string // closed-enum values, when applicable
	Fields      []Field  // nested-object fields (when the element is a struct)
}

// Fields walks the entity's struct once and returns the merged field set
// in declaration order. A field with no yaml tag, or a tag of "-", is
// skipped: it is not persisted, so it has no schema.
func (e Entity) Fields() []Field {
	return reflectFields(reflect.TypeOf(e.Struct), e.Overlay)
}

// reflectFields merges reflection (name, required, list, nesting) with the
// overlay (type, enum, pattern, description) for one struct type.
func reflectFields(t reflect.Type, overlays map[string]Overlay) []Field {
	var fields []Field
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		wire, ok := wireName(sf)
		if !ok {
			continue
		}
		ov := overlays[wire]
		f := Field{
			Name:        wire,
			Type:        ov.Type,
			Pattern:     ov.Pattern,
			Description: ov.Description,
			Required:    isRequired(sf),
			Enum:        ov.Enum,
		}

		// A slice of structs (or a bare struct) is a nested object; recurse
		// into its element type, merging the overlay's Fields.
		elem := sf.Type
		if elem.Kind() == reflect.Slice {
			f.List = true
			elem = elem.Elem()
		}
		if elem.Kind() == reflect.Struct {
			f.Fields = reflectFields(elem, ov.Fields)
		}
		fields = append(fields, f)
	}
	return fields
}

// wireName returns the yaml wire name of a struct field and whether it is
// persisted. A field with no yaml tag, an empty name, or a name of "-" is
// not persisted.
func wireName(sf reflect.StructField) (string, bool) {
	tag := sf.Tag.Get("yaml")
	if tag == "" {
		return "", false
	}
	name := strings.Split(tag, ",")[0]
	if name == "" || name == "-" {
		return "", false
	}
	return name, true
}

// isRequired reports whether a field is required: its yaml tag lacks
// ,omitempty.
func isRequired(sf reflect.StructField) bool {
	parts := strings.Split(sf.Tag.Get("yaml"), ",")
	for _, p := range parts[1:] {
		if p == "omitempty" {
			return false
		}
	}
	return true
}
