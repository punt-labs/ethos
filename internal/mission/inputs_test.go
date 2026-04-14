package mission

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestInputs_YAML_Ticket(t *testing.T) {
	data := []byte("ticket: ethos-42\nfiles:\n  - foo.go\n")
	var in Inputs
	require.NoError(t, yaml.Unmarshal(data, &in))
	assert.Equal(t, "ethos-42", in.Ticket)
	assert.Equal(t, []string{"foo.go"}, in.Files)
}

func TestInputs_YAML_Bead_BackCompat(t *testing.T) {
	data := []byte("bead: ethos-42\n")
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = old
		_ = r.Close()
	})
	var in Inputs
	require.NoError(t, yaml.Unmarshal(data, &in))
	require.NoError(t, w.Close())
	captured, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "ethos-42", in.Ticket)
	assert.Contains(t, string(captured), "deprecation warning")
	assert.Contains(t, string(captured), "inputs.bead")
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
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = old
		_ = r.Close()
	})
	var in Inputs
	require.NoError(t, json.Unmarshal(data, &in))
	require.NoError(t, w.Close())
	captured, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "ethos-42", in.Ticket)
	assert.Contains(t, string(captured), "deprecation warning")
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
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = old
		_ = r.Close()
	})
	var c Contract
	require.NoError(t, yaml.Unmarshal(data, &c))
	require.NoError(t, w.Close())
	captured, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "ethos-old", c.Inputs.Ticket)
	assert.Contains(t, string(captured), "deprecation warning")
}

// Explicit empty string in `ticket:` is treated as absent by omitempty
// semantics — both in YAML and JSON. If `bead:` is also set, the bead
// alias applies (with the usual deprecation warning) rather than
// triggering a "both set" error. This test documents the invariant so
// a future refactor doesn't silently break it.

func TestInputs_YAML_EmptyTicketWithBead_PromotesBead(t *testing.T) {
	data := []byte("ticket: \"\"\nbead: ethos-123\n")
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = old
		_ = r.Close()
	})
	var in Inputs
	require.NoError(t, yaml.Unmarshal(data, &in))
	require.NoError(t, w.Close())
	captured, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "ethos-123", in.Ticket)
	assert.Contains(t, string(captured), "deprecation warning")
	assert.Contains(t, string(captured), "inputs.bead")
}

func TestInputs_JSON_EmptyTicketWithBead_PromotesBead(t *testing.T) {
	data := []byte(`{"ticket":"","bead":"ethos-123"}`)
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = old
		_ = r.Close()
	})
	var in Inputs
	require.NoError(t, json.Unmarshal(data, &in))
	require.NoError(t, w.Close())
	captured, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "ethos-123", in.Ticket)
	assert.Contains(t, string(captured), "deprecation warning")
	assert.Contains(t, string(captured), "inputs.bead")
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
	r, w, err := os.Pipe()
	require.NoError(t, err)
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = old
		_ = r.Close()
	})
	c, err := DecodeContractStrict(data, "test")
	require.NoError(t, w.Close())
	captured, readErr := io.ReadAll(r)
	require.NoError(t, readErr)
	require.NoError(t, err)
	assert.Equal(t, "ethos-strict", c.Inputs.Ticket)
	assert.Contains(t, string(captured), "deprecation warning")
}
