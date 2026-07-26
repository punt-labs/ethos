package schema

import (
	"fmt"
	"sort"
	"strings"
)

// requiredLabel renders the required bit for the human tables.
func requiredLabel(required bool) string {
	if required {
		return "yes"
	}
	return "no"
}

// Table returns the headers and rows for the human field table, rendered
// by hook.FormatTable. Only top-level fields appear; nested-object shapes
// are described in the TYPE and DESCRIPTION columns.
func (e Entity) Table() (headers []string, rows [][]string) {
	headers = []string{"FIELD", "REQUIRED", "TYPE", "DESCRIPTION"}
	for _, f := range e.Fields() {
		rows = append(rows, []string{f.Name, requiredLabel(f.Required), f.Type, f.Description})
	}
	return headers, rows
}

// MarkdownTable returns the fields table as a GitHub-flavored markdown
// table: a header row, a separator, and one row per top-level field. The
// seeded READMEs embed this block verbatim and validate-content compares
// against it.
func (e Entity) MarkdownTable() string {
	var b strings.Builder
	b.WriteString("| Field | Required | Type | Description |\n")
	b.WriteString("|-------|----------|------|-------------|\n")
	for _, f := range e.Fields() {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", f.Name, requiredLabel(f.Required), f.Type, f.Description)
	}
	return b.String()
}

// JSONSchema returns a JSON Schema (draft 2020-12) object for the entity,
// suitable for marshaling to JSON. Required fields are listed in
// declaration order; every field becomes a property.
func (e Entity) JSONSchema() map[string]any {
	fields := e.Fields()
	props := make(map[string]any, len(fields))
	var required []string
	for _, f := range fields {
		props[f.Name] = fieldSchema(f)
		if f.Required {
			required = append(required, f.Name)
		}
	}
	obj := map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"title":                e.Name,
		"type":                 "object",
		"additionalProperties": false,
		"properties":           props,
	}
	if len(required) > 0 {
		obj["required"] = required
	}
	return obj
}

// fieldSchema renders one field as a JSON Schema property. A field with an
// enum and a pattern (role.model) is a partial enum: it emits anyOf of the
// closed-enum half and the pattern half, per ValidateModel. A field with
// nested fields is an object; a list wraps it in an array.
func fieldSchema(f Field) map[string]any {
	if len(f.Fields) > 0 {
		obj := objectSchema(f.Fields)
		if f.List {
			return withDescription(map[string]any{"type": "array", "items": obj}, f.Description)
		}
		return withDescription(obj, f.Description)
	}

	if f.List {
		return withDescription(map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string"},
		}, f.Description)
	}

	switch {
	case len(f.Enum) > 0 && f.Pattern != "":
		// Partial enum: closed aliases OR a pattern (role.model).
		return withDescription(map[string]any{
			"anyOf": []any{
				map[string]any{"enum": enumAny(f.Enum)},
				map[string]any{"type": "string", "pattern": f.Pattern},
			},
		}, f.Description)
	case len(f.Enum) > 0:
		return withDescription(map[string]any{"type": "string", "enum": enumAny(f.Enum)}, f.Description)
	case f.Pattern != "":
		return withDescription(map[string]any{"type": "string", "pattern": f.Pattern}, f.Description)
	default:
		return withDescription(map[string]any{"type": "string"}, f.Description)
	}
}

// objectSchema renders a nested object: its properties and its required
// list (fields whose yaml tag lacks ,omitempty).
func objectSchema(fields []Field) map[string]any {
	props := make(map[string]any, len(fields))
	var required []string
	for _, f := range fields {
		props[f.Name] = fieldSchema(f)
		if f.Required {
			required = append(required, f.Name)
		}
	}
	obj := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		obj["required"] = required
	}
	return obj
}

// withDescription attaches a description to a schema fragment when present.
func withDescription(m map[string]any, desc string) map[string]any {
	if desc != "" {
		m["description"] = desc
	}
	return m
}

// enumAny converts a string slice to []any for JSON marshaling.
func enumAny(vals []string) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
	}
	return out
}

// sortedKeys is a small helper for deterministic iteration in tests and
// callers that need overlay keys in order.
func sortedKeys(m map[string]Overlay) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
