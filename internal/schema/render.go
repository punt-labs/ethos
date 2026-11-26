package schema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/checkset"
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
	if e.Wire == "checks" {
		items := props["checks"].(map[string]any)["items"].(map[string]any)
		items["if"] = map[string]any{"properties": map[string]any{"stage": map[string]any{"const": "edit"}}}
		items["then"] = map[string]any{"properties": map[string]any{"files": map[string]any{"type": "array"}}}
		timeout := items["properties"].(map[string]any)["timeout"].(map[string]any)
		timeout["allOf"] = []any{map[string]any{"pattern": "[1-9]"}}
		adapterSchema(items)
		snapshotSchema(items)
	}
	return obj
}

func snapshotSchema(items map[string]any) {
	snapshot := items["properties"].(map[string]any)["snapshot"].(map[string]any)
	fields := snapshot["properties"].(map[string]any)
	inputs := fields["inputs"].(map[string]any)
	inputs["minItems"] = 1
	input := inputs["items"].(map[string]any)
	input["pattern"] = `^[^/\\:*?\[\]{}\x00]+(/[^/\\:*?\[\]{}\x00]+)*$`
	input["not"] = map[string]any{"pattern": `(^|/)[.](\.?|git|tmp)(/|$)|^[.]punt-labs/(local|ethos/sessions)(/|$)`}
	epoch := fields["epoch"].(map[string]any)
	epoch["minLength"] = 1
	epoch["maxLength"] = 128
	epoch["pattern"] = `\S`
	items["allOf"] = []any{map[string]any{
		"if": map[string]any{"required": []string{"snapshot"}},
		"then": map[string]any{
			"required": []string{"run"},
			"properties": map[string]any{
				"stage": map[string]any{"const": "handoff"},
				"run":   map[string]any{"prefixItems": []any{map[string]any{"const": "/bin/sh"}}},
			},
		},
	}}
}

func adapterSchema(items map[string]any) {
	props := items["properties"].(map[string]any)
	argv := func(file bool) map[string]any {
		placeholders := "files|root|package_dir"
		if file {
			placeholders = "file|" + placeholders
		}
		pattern := `^([^{}]*|\{(` + placeholders + `)\})$`
		return map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "pattern": pattern}, "prefixItems": []any{map[string]any{"type": "string", "pattern": pattern, "allOf": []any{map[string]any{"pattern": `\S`}}}}}
	}
	props["run"] = argv(true)
	props["uv"] = argv(true)
	props["vars"] = map[string]any{"type": "object", "propertyNames": map[string]any{"pattern": checkset.MakeVariablePattern}, "additionalProperties": map[string]any{"type": "string", "pattern": `^[^${}]*$`}}
	props["script"] = map[string]any{"type": "string", "pattern": `^[^{}-][^{}]*$`, "allOf": []any{map[string]any{"pattern": `\S`}}}
	items["else"] = map[string]any{"properties": map[string]any{"run": argv(false), "uv": argv(false)}}
	var choices []any
	for _, adapter := range []string{"exec", "make", "go", "uv", "npm"} {
		own := map[string][]string{"exec": {"run"}, "make": {"target", "vars"}, "go": {"go"}, "uv": {"uv"}, "npm": {"script"}}[adapter]
		p := map[string]any{}
		required := []string{own[0]}
		if adapter == "exec" {
			p["adapter"] = map[string]any{"enum": []string{"", "exec"}}
		} else {
			p["adapter"] = map[string]any{"const": adapter}
			required = append(required, "adapter")
		}
		for _, field := range []string{"run", "target", "vars", "go", "uv", "script"} {
			allowed := false
			for _, v := range own {
				if v == field {
					allowed = true
				}
			}
			if !allowed {
				p[field] = false
			}
		}
		choices = append(choices, map[string]any{"properties": p, "required": required})
	}
	items["oneOf"] = choices
}

// fieldSchema renders one field as a JSON Schema property. A field with an
// enum and a pattern (role.model) is a partial enum: it emits anyOf of the
// closed-enum half and the pattern half, per ValidateModel. A field with
// nested fields is an object; a list wraps it in an array.
func fieldSchema(f Field) map[string]any {
	if f.Type == "glob list or all" {
		return withDescription(map[string]any{"anyOf": []any{
			map[string]any{"const": "all"},
			map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1},
		}}, f.Description)
	}
	if f.Type == "boolean" || f.Type == "integer" {
		return withDescription(map[string]any{"type": f.Type}, f.Description)
	}
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
		// Partial enum: closed aliases OR a pattern (role.model). When empty
		// is a distinct legal value (inherit), add a const "" branch so the
		// published schema accepts exactly what ValidateModel does.
		branches := []any{
			map[string]any{"enum": enumAny(f.Enum)},
			map[string]any{"type": "string", "pattern": f.Pattern},
		}
		if f.AllowEmpty {
			branches = append(branches, map[string]any{"const": ""})
		}
		return withDescription(map[string]any{"anyOf": branches}, f.Description)
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
	obj := map[string]any{"type": "object", "additionalProperties": false, "properties": props}
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
