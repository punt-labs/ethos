package mission

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// the captured output. Restores os.Stderr before returning in all cases.
// Shared by tests across the package that assert stderr diagnostics.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	fn()
	_ = w.Close()
	return string(<-done)
}

func TestInputs_YAML_Ticket(t *testing.T) {
	data := []byte("ticket: ethos-42\nfiles:\n  - foo.go\n")
	var in Inputs
	require.NoError(t, yaml.Unmarshal(data, &in))
	assert.Equal(t, "ethos-42", in.Ticket)
	assert.Equal(t, []string{"foo.go"}, in.Files)
}

// TestInputs_YAML_Bead_BackCompat asserts the deprecated bead alias
// still decodes into Ticket. The decode is silent: the deprecation
// warning moved to the user-submission path (ethos-c0yp), so it no
// longer fires from a plain Unmarshal.
func TestInputs_YAML_Bead_BackCompat(t *testing.T) {
	data := []byte("bead: ethos-42\n")
	var in Inputs
	require.NoError(t, yaml.Unmarshal(data, &in))
	assert.Equal(t, "ethos-42", in.Ticket)
}

func TestInputs_YAML_Both_Error(t *testing.T) {
	data := []byte("ticket: a\nbead: b\n")
	var in Inputs
	err := yaml.Unmarshal(data, &in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both 'ticket' and 'bead' set")
}

func TestInputs_YAML_Marshal_EmitsTicket(t *testing.T) {
	in := Inputs{Ticket: "ethos-42", Files: []string{"a.go"}}
	data, err := yaml.Marshal(&in)
	require.NoError(t, err)
	assert.Contains(t, string(data), "ticket:")
	assert.NotContains(t, string(data), "bead:")
}

func TestInputs_JSON_Ticket(t *testing.T) {
	data := []byte(`{"ticket":"ethos-42","files":["foo.go"]}`)
	var in Inputs
	require.NoError(t, json.Unmarshal(data, &in))
	assert.Equal(t, "ethos-42", in.Ticket)
	assert.Equal(t, []string{"foo.go"}, in.Files)
}

func TestInputs_JSON_Bead_BackCompat(t *testing.T) {
	data := []byte(`{"bead":"ethos-42"}`)
	var in Inputs
	require.NoError(t, json.Unmarshal(data, &in))
	assert.Equal(t, "ethos-42", in.Ticket)
}

func TestInputs_JSON_Both_Error(t *testing.T) {
	data := []byte(`{"ticket":"a","bead":"b"}`)
	var in Inputs
	err := json.Unmarshal(data, &in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both 'ticket' and 'bead' set")
}

func TestInputs_JSON_Marshal_EmitsTicket(t *testing.T) {
	in := Inputs{Ticket: "ethos-42", Files: []string{"a.go"}}
	data, err := json.Marshal(&in)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"ticket"`)
	assert.NotContains(t, string(data), `"bead"`)
}

// TestInputs_YAML_RoundTrip_ViaContract verifies that a full contract
// with ticket round-trips through YAML marshal/unmarshal.
func TestInputs_YAML_RoundTrip_ViaContract(t *testing.T) {
	c := Contract{
		MissionID:       "m-test-001",
		Status:          StatusOpen,
		CreatedAt:       "2026-04-13T00:00:00Z",
		UpdatedAt:       "2026-04-13T00:00:00Z",
		Leader:          "claude",
		Worker:          "bwk",
		Evaluator:       Evaluator{Handle: "djb", PinnedAt: "2026-04-13T00:00:00Z"},
		Inputs:          Inputs{Ticket: "ethos-99", Files: []string{"a.go"}},
		WriteSet:        []string{"internal/"},
		SuccessCriteria: []string{"make check passes"},
		Budget:          Budget{Rounds: 1, ReflectionAfterEach: true},
		CurrentRound:    1,
	}
	data, err := yaml.Marshal(&c)
	require.NoError(t, err)
	assert.Contains(t, string(data), "ticket:")
	assert.NotContains(t, string(data), "bead:")

	var loaded Contract
	require.NoError(t, yaml.Unmarshal(data, &loaded))
	assert.Equal(t, "ethos-99", loaded.Inputs.Ticket)
}

// TestInputs_YAML_OldContract_BeadKey verifies that an old contract
// YAML file with "bead:" key loads into Ticket.
func TestInputs_YAML_OldContract_BeadKey(t *testing.T) {
	data := []byte(`
mission_id: m-test-002
status: open
created_at: "2026-04-13T00:00:00Z"
updated_at: "2026-04-13T00:00:00Z"
leader: claude
worker: bwk
evaluator:
  handle: djb
  pinned_at: "2026-04-13T00:00:00Z"
inputs:
  bead: ethos-old
  files:
    - a.go
write_set:
  - internal/
success_criteria:
  - make check passes
budget:
  rounds: 1
  reflection_after_each: true
current_round: 1
`)
	var c Contract
	require.NoError(t, yaml.Unmarshal(data, &c))
	assert.Equal(t, "ethos-old", c.Inputs.Ticket)
}

// Explicit empty string in `ticket:` is treated as absent by omitempty
// semantics -- both in YAML and JSON. If `bead:` is also set, the bead
// alias applies rather than triggering a "both set" error. This test
// documents the invariant so a future refactor doesn't silently break
// it.

func TestInputs_YAML_EmptyTicketWithBead_PromotesBead(t *testing.T) {
	data := []byte("ticket: \"\"\nbead: ethos-123\n")
	var in Inputs
	require.NoError(t, yaml.Unmarshal(data, &in))
	assert.Equal(t, "ethos-123", in.Ticket)
}

func TestInputs_JSON_EmptyTicketWithBead_PromotesBead(t *testing.T) {
	data := []byte(`{"ticket":"","bead":"ethos-123"}`)
	var in Inputs
	require.NoError(t, json.Unmarshal(data, &in))
	assert.Equal(t, "ethos-123", in.Ticket)
}

// TestInputs_StrictDecoder_AcceptsBead verifies that DecodeContractStrict
// (which uses KnownFields=true) still accepts old "bead:" contracts
// because the custom UnmarshalYAML handles the field internally.
func TestInputs_StrictDecoder_AcceptsBead(t *testing.T) {
	data := []byte(`
mission_id: m-test-003
status: open
created_at: "2026-04-13T00:00:00Z"
updated_at: "2026-04-13T00:00:00Z"
leader: claude
worker: bwk
evaluator:
  handle: djb
  pinned_at: "2026-04-13T00:00:00Z"
inputs:
  bead: ethos-strict
  files:
    - a.go
write_set:
  - internal/
success_criteria:
  - make check passes
budget:
  rounds: 1
  reflection_after_each: true
current_round: 1
`)
	c, err := DecodeContractStrict(data, "test")
	require.NoError(t, err)
	assert.Equal(t, "ethos-strict", c.Inputs.Ticket)
}

// TestInputs_StrictDecoder_RejectsUnknownFieldUnderInputs verifies that
// an unknown key inside inputs: is rejected by DecodeContractStrict.
func TestInputs_StrictDecoder_RejectsUnknownFieldUnderInputs(t *testing.T) {
	data := []byte(`
mission_id: m-test-004
status: open
created_at: "2026-04-13T00:00:00Z"
updated_at: "2026-04-13T00:00:00Z"
leader: claude
worker: bwk
evaluator:
  handle: djb
  pinned_at: "2026-04-13T00:00:00Z"
inputs:
  ticket: ethos-42
  bogus: slipped-through
write_set:
  - internal/
success_criteria:
  - make check passes
budget:
  rounds: 1
  reflection_after_each: true
current_round: 1
`)
	_, err := DecodeContractStrict(data, "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
	assert.Contains(t, err.Error(), "bogus")
}

// TestInputs_YAML_RejectsUnknownField verifies that plain YAML unmarshal
// of Inputs rejects unknown keys.
func TestInputs_YAML_RejectsUnknownField(t *testing.T) {
	data := []byte("ticket: ethos-42\nbogus: oops\n")
	var in Inputs
	err := yaml.Unmarshal(data, &in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
	assert.Contains(t, err.Error(), "bogus")
}

// TestInputs_JSON_RejectsUnknownField verifies that JSON unmarshal of
// Inputs rejects unknown keys.
func TestInputs_JSON_RejectsUnknownField(t *testing.T) {
	data := []byte(`{"ticket":"ethos-42","bogus":"oops"}`)
	var in Inputs
	err := json.Unmarshal(data, &in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

// TestInputs_YAML_AllKnownFields verifies that all known fields decode
// without error.
func TestInputs_YAML_AllKnownFields(t *testing.T) {
	data := []byte("ticket: ethos-42\nfiles:\n  - a.go\nreferences:\n  - ref.md\n")
	var in Inputs
	require.NoError(t, yaml.Unmarshal(data, &in))
	assert.Equal(t, "ethos-42", in.Ticket)
	assert.Equal(t, []string{"a.go"}, in.Files)
	assert.Equal(t, []string{"ref.md"}, in.References)
}

// TestInputs_JSON_AllKnownFields verifies that all known fields decode
// without error.
func TestInputs_JSON_AllKnownFields(t *testing.T) {
	data := []byte(`{"ticket":"ethos-42","files":["a.go"],"references":["ref.md"]}`)
	var in Inputs
	require.NoError(t, json.Unmarshal(data, &in))
	assert.Equal(t, "ethos-42", in.Ticket)
	assert.Equal(t, []string{"a.go"}, in.Files)
	assert.Equal(t, []string{"ref.md"}, in.References)
}

// TestBeadAlias reports the deprecated inputs.bead value a contract
// body sets, and "" for a ticket-only or bead-less body. The
// user-submission paths use it to decide whether to warn (ethos-c0yp);
// an incidental store load that decodes an old bead mission does not
// call it, so the scan stays silent.
func TestBeadAlias(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"bead set", "inputs:\n  bead: punt-labs-6dj\n", "punt-labs-6dj"},
		{"ticket set", "inputs:\n  ticket: ethos-42\n", ""},
		{"no inputs", "leader: claude\n", ""},
		{"blank bead", "inputs:\n  bead: \"  \"\n", ""},
		{"malformed yaml", "inputs: [unterminated\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, BeadAlias([]byte(tt.body)))
		})
	}
}

// TestWarnBeadDeprecated asserts the warning names the field and the
// bead value so the operator can find and migrate it.
func TestWarnBeadDeprecated(t *testing.T) {
	var buf bytes.Buffer
	WarnBeadDeprecated(&buf, "punt-labs-6dj")
	out := buf.String()
	assert.Contains(t, out, "deprecation warning")
	assert.Contains(t, out, "inputs.bead")
	assert.Contains(t, out, "punt-labs-6dj")
}

func TestInputs_TriggerYAML(t *testing.T) {
	data := []byte(`
ticket: ethos-50
trigger:
  type: email
  message_id: "<abc@example.com>"
  from: mal@serenity.ship
  subject: "cargo run"
files:
  - a.go
`)
	var in Inputs
	require.NoError(t, yaml.Unmarshal(data, &in))
	require.NotNil(t, in.Trigger)
	assert.Equal(t, "email", in.Trigger.Type)
	assert.Equal(t, "<abc@example.com>", in.Trigger.MessageID)
	assert.Equal(t, "mal@serenity.ship", in.Trigger.From)
	assert.Equal(t, "cargo run", in.Trigger.Subject)
	assert.Equal(t, "ethos-50", in.Ticket)

	// Round-trip: marshal and re-decode.
	out, err := yaml.Marshal(&in)
	require.NoError(t, err)
	var rt Inputs
	require.NoError(t, yaml.Unmarshal(out, &rt))
	assert.Equal(t, in.Trigger, rt.Trigger)
}

func TestInputs_TriggerJSON(t *testing.T) {
	data := []byte(`{
		"ticket": "ethos-50",
		"trigger": {
			"type": "email",
			"message_id": "<abc@example.com>",
			"from": "mal@serenity.ship",
			"subject": "cargo run"
		},
		"files": ["a.go"]
	}`)
	var in Inputs
	require.NoError(t, json.Unmarshal(data, &in))
	require.NotNil(t, in.Trigger)
	assert.Equal(t, "email", in.Trigger.Type)
	assert.Equal(t, "<abc@example.com>", in.Trigger.MessageID)
	assert.Equal(t, "mal@serenity.ship", in.Trigger.From)
	assert.Equal(t, "cargo run", in.Trigger.Subject)

	// Round-trip: marshal and re-decode.
	out, err := json.Marshal(&in)
	require.NoError(t, err)
	var rt Inputs
	require.NoError(t, json.Unmarshal(out, &rt))
	assert.Equal(t, in.Trigger, rt.Trigger)
}

func TestInputs_TriggerOmittedWhenNil(t *testing.T) {
	in := Inputs{Ticket: "ethos-51", Files: []string{"a.go"}}
	// YAML
	ydata, err := yaml.Marshal(&in)
	require.NoError(t, err)
	assert.NotContains(t, string(ydata), "trigger")
	// JSON
	jdata, err := json.Marshal(&in)
	require.NoError(t, err)
	assert.NotContains(t, string(jdata), "trigger")
}

func TestInputs_TriggerWithTicket(t *testing.T) {
	data := []byte(`
ticket: ethos-52
trigger:
  type: email
  from: wash@serenity.ship
files:
  - b.go
`)
	var in Inputs
	require.NoError(t, yaml.Unmarshal(data, &in))
	assert.Equal(t, "ethos-52", in.Ticket)
	require.NotNil(t, in.Trigger)
	assert.Equal(t, "email", in.Trigger.Type)
	assert.Equal(t, "wash@serenity.ship", in.Trigger.From)
	assert.Equal(t, []string{"b.go"}, in.Files)
}
