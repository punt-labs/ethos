package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"
)

// entities is the set under guard. Names and required-ness come from
// reflection; only the overlay is asserted here.
func entities() []Entity { return []Entity{Identity, Role, Team} }

// TestOverlayCoverage proves every persisted field — top-level and nested —
// has an overlay entry with a non-empty Type and Description. Field names
// and required-ness are read live from the struct by reflection and are not
// asserted as a copy. Description accuracy is not guarded: the test proves a
// description exists, not that it is correct.
func TestOverlayCoverage(t *testing.T) {
	for _, e := range entities() {
		checkCoverage(t, e.Name, e.Fields())
	}
}

func checkCoverage(t *testing.T, entity string, fields []Field) {
	t.Helper()
	for _, f := range fields {
		if f.Type == "" {
			t.Errorf("%s.%s: missing overlay Type", entity, f.Name)
		}
		if f.Description == "" {
			t.Errorf("%s.%s: missing overlay Description", entity, f.Name)
		}
		if len(f.Fields) > 0 {
			checkCoverage(t, entity+"."+f.Name, f.Fields)
		}
	}
}

// TestNoPhantomOverlays proves the overlay names no field the struct lacks.
// A phantom entry — an overlay key with no matching struct field — is a
// rename left behind, so fail on it.
func TestNoPhantomOverlays(t *testing.T) {
	for _, e := range entities() {
		reflected := reflectedWireNames(reflect.TypeOf(e.Struct))
		checkPhantom(t, e.Name, e.Overlay, reflected)
	}
}

func checkPhantom(t *testing.T, entity string, overlay map[string]Overlay, reflected map[string]reflect.Type) {
	t.Helper()
	for _, key := range sortedKeys(overlay) {
		elem, ok := reflected[key]
		if !ok {
			t.Errorf("%s: phantom overlay %q names no struct field", entity, key)
			continue
		}
		if nested := overlay[key].Fields; len(nested) > 0 {
			if elem == nil {
				t.Errorf("%s.%s: overlay has nested Fields but the struct field is not an object", entity, key)
				continue
			}
			checkPhantom(t, entity+"."+key, nested, reflectedWireNames(elem))
		}
	}
}

// reflectedWireNames maps each persisted wire name of a struct to its
// nested struct element type (nil for scalar or list-of-scalar fields).
func reflectedWireNames(t reflect.Type) map[string]reflect.Type {
	out := make(map[string]reflect.Type)
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		wire, ok := wireName(sf)
		if !ok {
			continue
		}
		elem := sf.Type
		if elem.Kind() == reflect.Slice {
			elem = elem.Elem()
		}
		if elem.Kind() == reflect.Struct {
			out[wire] = elem
		} else {
			out[wire] = nil
		}
	}
	return out
}

// TestClosedEnumsMatchPackages proves each closed-enum overlay equals the
// exported package slice it mirrors. Add a value in one place and forget the
// other and this fails. role.model is a partial enum: only its alias half is
// guarded; the ^claude- pattern is authored and documented as a known partial.
func TestClosedEnumsMatchPackages(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"identity.kind", Identity.Overlay["kind"].Enum, identity.KindValues},
		{"role.model (alias half)", Role.Overlay["model"].Enum, role.ModelAliases},
		{"team.collaborations.type", Team.Overlay["collaborations"].Fields["type"].Enum, team.CollaborationTypes},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: overlay enum %v != package slice %v", c.name, c.got, c.want)
		}
	}
}

func TestFieldsReflectRequiredness(t *testing.T) {
	required := map[string]map[string]bool{
		"identity": {"name": true, "handle": true, "kind": true, "email": false, "talents": false},
		"role":     {"name": true, "model": false, "safety_constraints": false},
		"team":     {"name": true, "members": true, "repositories": false, "collaborations": false},
	}
	for wire, want := range required {
		got := make(map[string]bool)
		for _, f := range Registry[wire].Fields() {
			got[f.Name] = f.Required
		}
		for name, req := range want {
			if got[name] != req {
				t.Errorf("%s.%s: required=%v, want %v", wire, name, got[name], req)
			}
		}
	}
}

func TestTable(t *testing.T) {
	headers, rows := Role.Table()
	if want := []string{"FIELD", "REQUIRED", "TYPE", "DESCRIPTION"}; !reflect.DeepEqual(headers, want) {
		t.Fatalf("headers = %v, want %v", headers, want)
	}
	if len(rows) != 7 {
		t.Fatalf("role table has %d rows, want 7", len(rows))
	}
	// name is required; model is not.
	byName := make(map[string][]string)
	for _, r := range rows {
		byName[r[0]] = r
	}
	if byName["name"][1] != "yes" {
		t.Errorf("name required = %q, want yes", byName["name"][1])
	}
	if byName["model"][1] != "no" {
		t.Errorf("model required = %q, want no", byName["model"][1])
	}
	if byName["safety_constraints"][2] != "list of object" {
		t.Errorf("safety_constraints type = %q, want list of object", byName["safety_constraints"][2])
	}
}

func TestMarkdownTable(t *testing.T) {
	md := Identity.MarkdownTable()
	if !strings.HasPrefix(md, "| Field | Required | Type | Description |\n|") {
		t.Fatalf("markdown table missing header:\n%s", md)
	}
	if !strings.Contains(md, "| `handle` | yes | string (slug) |") {
		t.Errorf("markdown table missing handle row:\n%s", md)
	}
	// Nested-object fields do not appear as their own rows.
	if strings.Contains(md, "| `tool` |") {
		t.Errorf("markdown table leaked a nested field")
	}
}

func TestJSONSchema(t *testing.T) {
	for _, e := range entities() {
		obj := e.JSONSchema()
		// Round-trips to valid JSON.
		if _, err := json.Marshal(obj); err != nil {
			t.Fatalf("%s: marshal: %v", e.Name, err)
		}
		if obj["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
			t.Errorf("%s: wrong $schema %v", e.Name, obj["$schema"])
		}
		if obj["title"] != e.Name {
			t.Errorf("%s: title = %v", e.Name, obj["title"])
		}
		if obj["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties should be false", e.Name)
		}
	}
}

func TestJSONSchemaRoleModelPartialEnum(t *testing.T) {
	props := Role.JSONSchema()["properties"].(map[string]any)
	model := props["model"].(map[string]any)
	anyOf, ok := model["anyOf"].([]any)
	// aliases OR claude-* pattern OR empty (inherit) — three legal shapes,
	// matching ValidateModel which accepts "".
	if !ok || len(anyOf) != 3 {
		t.Fatalf("model should be anyOf of three branches, got %v", model)
	}
	var hasEnum, hasPattern, hasEmpty bool
	for _, b := range anyOf {
		m := b.(map[string]any)
		if _, ok := m["enum"]; ok {
			hasEnum = true
		}
		if m["pattern"] == "^claude-" {
			hasPattern = true
		}
		if c, ok := m["const"]; ok && c == "" {
			hasEmpty = true
		}
	}
	if !hasEnum || !hasPattern || !hasEmpty {
		t.Errorf("model anyOf missing a branch: enum=%v pattern=%v empty=%v (%v)", hasEnum, hasPattern, hasEmpty, anyOf)
	}
}

func TestJSONSchemaNestedObjectArray(t *testing.T) {
	props := Team.JSONSchema()["properties"].(map[string]any)
	members := props["members"].(map[string]any)
	if members["type"] != "array" {
		t.Fatalf("members should be an array: %v", members)
	}
	items := members["items"].(map[string]any)
	if items["type"] != "object" {
		t.Fatalf("members items should be an object: %v", items)
	}
	if items["additionalProperties"] != false {
		t.Errorf("nested object should set additionalProperties:false: %v", items)
	}
	req := items["required"].([]string)
	if !reflect.DeepEqual(req, []string{"identity", "role"}) {
		t.Errorf("member required = %v, want [identity role]", req)
	}
}

// TestRoleModelSchemaMatchesValidator proves the published model schema
// accepts exactly the values ValidateModel accepts: the aliases, any
// claude-* id, and the empty string (inherit) — the drift-match promise
// the registry makes to its consumers.
func TestRoleModelSchemaMatchesValidator(t *testing.T) {
	accepted := []string{"", "opus", "sonnet", "haiku", "inherit", "claude-opus-4-8"}
	for _, v := range accepted {
		if err := role.ValidateModel(v); err != nil {
			t.Fatalf("ValidateModel(%q) unexpectedly rejected: %v", v, err)
		}
	}
	// The schema's model field must offer a branch for the empty string, or a
	// valid `model: ""` would fail the published schema while passing the
	// validator.
	model := Role.JSONSchema()["properties"].(map[string]any)["model"].(map[string]any)
	var hasEmpty bool
	for _, b := range model["anyOf"].([]any) {
		if c, ok := b.(map[string]any)["const"]; ok && c == "" {
			hasEmpty = true
		}
	}
	if !hasEmpty {
		t.Errorf("model schema rejects the empty string but ValidateModel accepts it")
	}
}
