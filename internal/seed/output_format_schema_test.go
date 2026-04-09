package seed

import (
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/role"
	"gopkg.in/yaml.v3"
)

// TestWriterRoleOutputFormat_DecodesAsResult is the tripwire that
// keeps the implementer and test-engineer output_format template
// bodies aligned with the mission.Result strict schema.
//
// The worker "writer" roles (implementer, test-engineer) are the two
// sidecar roles whose templates describe a handoff a worker would
// submit as a mission.Result. Reviewer-style roles (reviewer,
// architect, security-reviewer) and researcher use different output
// shapes (FINDINGS, RESEARCH) and are deliberately excluded from this
// round-trip — aligning them with Result would semantically distort
// them.
//
// The test loads each role through the embedded Roles FS (the same
// bytes seeded to disk by Seed), extracts the raw YAML body from the
// output_format field, and runs it through the real
// mission.DecodeResultStrict + Validate path. Strict decode rejects
// unknown fields, so a future edit that renames author back to worker
// or adds an ad-hoc key fails here.
func TestWriterRoleOutputFormat_DecodesAsResult(t *testing.T) {
	cases := []struct {
		role string
	}{
		{role: "implementer"},
		{role: "test-engineer"},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			r := loadEmbeddedRole(t, tc.role)
			if strings.TrimSpace(r.OutputFormat) == "" {
				t.Fatalf("role %q has empty output_format", tc.role)
			}
			body := []byte(r.OutputFormat)
			result, err := mission.DecodeResultStrict(body, tc.role+"-template")
			if err != nil {
				t.Fatalf("strict decode %s: %v", tc.role, err)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("validate %s: %v", tc.role, err)
			}
		})
	}
}

// TestWriterRoleOutputFormat_RejectsUnknownField proves the tripwire
// fires. It loads the implementer template, appends an unknown
// `worker:` key (the old schema's field name, the exact regression we
// want to catch), and asserts DecodeResultStrict rejects it. If a
// future edit reverts the template to the old `worker:` key, CI
// fails here with a clear message.
func TestWriterRoleOutputFormat_RejectsUnknownField(t *testing.T) {
	r := loadEmbeddedRole(t, "implementer")
	body := []byte(r.OutputFormat)
	corrupted := append(append([]byte{}, body...), []byte("\nworker: test\n")...)
	_, err := mission.DecodeResultStrict(corrupted, "implementer-corrupted")
	if err == nil {
		t.Fatal("expected strict decode to reject unknown field worker")
	}
	msg := err.Error()
	if !strings.Contains(msg, "field worker not found") {
		t.Fatalf("expected rejection message to name the unknown field worker; got: %v", err)
	}
}

// loadEmbeddedRole reads a role YAML from the embedded Roles FS and
// unmarshals it into role.Role. Mirrors the loader pattern already in
// seed_test.go so both tests see identical bytes.
func loadEmbeddedRole(t *testing.T, name string) role.Role {
	t.Helper()
	path := "sidecar/roles/" + name + ".yaml"
	data, err := Roles.ReadFile(path)
	if err != nil {
		t.Fatalf("reading embedded role %s: %v", path, err)
	}
	var r role.Role
	if err := yaml.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshaling embedded role %s: %v", path, err)
	}
	return r
}
