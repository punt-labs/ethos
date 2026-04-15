package mission

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// resetBeadDeprecation resets the once guard so each test can observe
// the warning independently. Must be called before any test that
// expects the deprecation message.
func resetBeadDeprecation() {
	beadDeprecationOnce = sync.Once{}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// the captured output. Restores os.Stderr before returning in all cases.
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

func TestInputs_YAML_Bead_BackCompat(t *testing.T) {
	resetBeadDeprecation()
	data := []byte("bead: ethos-42\n")
	var in Inputs
	captured := captureStderr(t, func() {
		require.NoError(t, yaml.Unmarshal(data, &in))
	})
	assert.Equal(t, "ethos-42", in.Ticket)
	assert.Contains(t, captured, "deprecation warning")
	assert.Contains(t, captured, "inputs.bead")
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
	resetBeadDeprecation()
	data := []byte(`{"bead":"ethos-42"}`)
	var in Inputs
	captured := captureStderr(t, func() {
		require.NoError(t, json.Unmarshal(data, &in))
	})
	assert.Equal(t, "ethos-42", in.Ticket)
	assert.Contains(t, captured, "deprecation warning")
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
	resetBeadDeprecation()
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
	captured := captureStderr(t, func() {
		require.NoError(t, yaml.Unmarshal(data, &c))
	})
	assert.Equal(t, "ethos-old", c.Inputs.Ticket)
	assert.Contains(t, captured, "deprecation warning")
}

// Explicit empty string in `ticket:` is treated as absent by omitempty
// semantics -- both in YAML and JSON. If `bead:` is also set, the bead
// alias applies (with the usual deprecation warning) rather than
// triggering a "both set" error. This test documents the invariant so
// a future refactor doesn't silently break it.

func TestInputs_YAML_EmptyTicketWithBead_PromotesBead(t *testing.T) {
	resetBeadDeprecation()
	data := []byte("ticket: \"\"\nbead: ethos-123\n")
	var in Inputs
	captured := captureStderr(t, func() {
		require.NoError(t, yaml.Unmarshal(data, &in))
	})
	assert.Equal(t, "ethos-123", in.Ticket)
	assert.Contains(t, captured, "deprecation warning")
	assert.Contains(t, captured, "inputs.bead")
}

func TestInputs_JSON_EmptyTicketWithBead_PromotesBead(t *testing.T) {
	resetBeadDeprecation()
	data := []byte(`{"ticket":"","bead":"ethos-123"}`)
	var in Inputs
	captured := captureStderr(t, func() {
		require.NoError(t, json.Unmarshal(data, &in))
	})
	assert.Equal(t, "ethos-123", in.Ticket)
	assert.Contains(t, captured, "deprecation warning")
	assert.Contains(t, captured, "inputs.bead")
}

// TestInputs_StrictDecoder_AcceptsBead verifies that DecodeContractStrict
// (which uses KnownFields=true) still accepts old "bead:" contracts
// because the custom UnmarshalYAML handles the field internally.
func TestInputs_StrictDecoder_AcceptsBead(t *testing.T) {
	resetBeadDeprecation()
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
	var c *Contract
	captured := captureStderr(t, func() {
		var err error
		c, err = DecodeContractStrict(data, "test")
		require.NoError(t, err)
	})
	assert.Equal(t, "ethos-strict", c.Inputs.Ticket)
	assert.Contains(t, captured, "deprecation warning")
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

// TestInputs_BeadDeprecation_EmitsOnce verifies that decoding multiple
// missions with inputs.bead emits the deprecation warning exactly once.
func TestInputs_BeadDeprecation_EmitsOnce(t *testing.T) {
	resetBeadDeprecation()
	captured := captureStderr(t, func() {
		for _, v := range []string{"ethos-1", "ethos-2", "ethos-3"} {
			var in Inputs
			data := []byte("bead: " + v + "\n")
			require.NoError(t, yaml.Unmarshal(data, &in))
			assert.Equal(t, v, in.Ticket)
		}
	})
	n := strings.Count(captured, "deprecation warning")
	assert.Equal(t, 1, n, "expected exactly 1 deprecation warning, got %d in:\n%s", n, captured)
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
