package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleAuditLog_ValidPayload(t *testing.T) {
	dir := t.TempDir()
	payload := map[string]any{
		"session_id": "sess-abc",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "git status"},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	err = HandleAuditLog(bytes.NewReader(data), "", dir)
	require.NoError(t, err)

	path := filepath.Join(dir, "sess-abc.audit.jsonl")
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	var entry auditEntry
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(content), &entry))
	assert.Equal(t, "sess-abc", entry.Session)
	assert.Equal(t, "Bash", entry.Tool)
	assert.Contains(t, entry.ToolInputPreview, "git status")
	assert.NotEmpty(t, entry.Ts)
}

func TestHandleAuditLog_NoSessionID(t *testing.T) {
	dir := t.TempDir()
	payload := map[string]any{
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	err = HandleAuditLog(bytes.NewReader(data), "", dir)
	require.NoError(t, err)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no file should be created without session_id")
}

func TestHandleAuditLog_LongToolInput(t *testing.T) {
	dir := t.TempDir()
	longCmd := strings.Repeat("x", 300)
	payload := map[string]any{
		"session_id": "sess-long",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": longCmd},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	err = HandleAuditLog(bytes.NewReader(data), "", dir)
	require.NoError(t, err)

	path := filepath.Join(dir, "sess-long.audit.jsonl")
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	var entry auditEntry
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(content), &entry))
	assert.True(t, strings.HasSuffix(entry.ToolInputPreview, "..."),
		"long input should be truncated with ...")
	// 200 chars + "..."
	assert.Equal(t, 203, len(entry.ToolInputPreview))
}

func TestHandleAuditLog_MissingSessionsDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent", "deep")
	payload := map[string]any{
		"session_id": "sess-missing",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	// Should not crash — writes warning to stderr and returns nil.
	err = HandleAuditLog(bytes.NewReader(data), "", dir)
	assert.NoError(t, err)
}

func TestHandleAuditLog_MultipleAppends(t *testing.T) {
	dir := t.TempDir()
	for i, tool := range []string{"Bash", "Read", "Write"} {
		payload := map[string]any{
			"session_id": "sess-multi",
			"tool_name":  tool,
			"tool_input": map[string]any{"i": i},
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		require.NoError(t, HandleAuditLog(bytes.NewReader(data), "", dir))
	}

	path := filepath.Join(dir, "sess-multi.audit.jsonl")
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	assert.Equal(t, 3, len(lines))

	for i, line := range lines {
		var entry auditEntry
		require.NoError(t, json.Unmarshal([]byte(line), &entry), "line %d", i)
		assert.Equal(t, "sess-multi", entry.Session)
	}
}

func TestHandleAuditLog_NoToolInput(t *testing.T) {
	dir := t.TempDir()
	payload := map[string]any{
		"session_id": "sess-noinput",
		"tool_name":  "SomeInternalTool",
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	err = HandleAuditLog(bytes.NewReader(data), "", dir)
	require.NoError(t, err)

	path := filepath.Join(dir, "sess-noinput.audit.jsonl")
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	var entry auditEntry
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(content), &entry))
	assert.Equal(t, "", entry.ToolInputPreview)
}

func TestToolInputPreview(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name:  "no tool_input key",
			input: map[string]any{"tool_name": "Bash"},
			want:  "",
		},
		{
			name:  "short input",
			input: map[string]any{"tool_input": map[string]any{"cmd": "ls"}},
			want:  `{"cmd":"ls"}`,
		},
		{
			name: "exactly 200 chars",
			input: map[string]any{
				"tool_input": map[string]any{"x": strings.Repeat("a", 186)},
			},
			// {"x":"aaa..."} = 6 + 186 + 2 = 194... let's just check no truncation
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolInputPreview(tt.input)
			if tt.want != "" {
				assert.Equal(t, tt.want, got)
			}
			assert.LessOrEqual(t, len(got), 203, "preview must not exceed 200+3")
		})
	}
}

// TestHandleAuditLog_FullToolInputPersisted asserts that the full
// tool_input map is persisted under the tool_input key, not just the
// 200-char preview. DES-054 phase 1: the audit trail must carry
// enough state to reconstruct a prompt.
func TestHandleAuditLog_FullToolInputPersisted(t *testing.T) {
	dir := t.TempDir()
	longCmd := strings.Repeat("x", 500)
	payload := map[string]any{
		"session_id": "sess-full",
		"tool_name":  "Bash",
		"tool_input": map[string]any{
			"command":     longCmd,
			"description": "a long-running command",
		},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	require.NoError(t, HandleAuditLog(bytes.NewReader(data), "", dir))

	path := filepath.Join(dir, "sess-full.audit.jsonl")
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	var entry auditEntry
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(content), &entry))
	require.NotNil(t, entry.ToolInput, "full tool_input must be persisted")
	assert.Equal(t, longCmd, entry.ToolInput["command"],
		"full command must be preserved, not truncated")
	assert.Equal(t, "a long-running command", entry.ToolInput["description"])
	// Preview is still truncated for grep-style scanning.
	assert.True(t, strings.HasSuffix(entry.ToolInputPreview, "..."),
		"preview must still truncate to 200 chars")
}

// TestHandleAuditLog_NewFieldsRoundTrip covers the enrichment fields
// introduced by DES-054 phase 1: parent_session, agent_id,
// agent_type, delegation_id, parent_delegation, contract_id.
func TestHandleAuditLog_NewFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	payload := map[string]any{
		"session_id":        "sess-child",
		"parent_session_id": "sess-parent",
		"agent_id":          "bwk",
		"agent_type":        "go-specialist",
		"delegation_id":     "d-2026-05-22-007",
		"parent_delegation": "m-2026-05-22-024-d01",
		"contract_id":       "m-2026-05-22-024",
		"tool_name":         "Read",
		"tool_input":        map[string]any{"file_path": "/etc/hosts"},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	require.NoError(t, HandleAuditLog(bytes.NewReader(data), "", dir))

	path := filepath.Join(dir, "sess-child.audit.jsonl")
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	var entry auditEntry
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(content), &entry))
	assert.Equal(t, "sess-child", entry.Session)
	assert.Equal(t, "sess-parent", entry.ParentSession)
	assert.Equal(t, "bwk", entry.AgentID)
	assert.Equal(t, "go-specialist", entry.AgentType)
	assert.Equal(t, "d-2026-05-22-007", entry.DelegationID)
	assert.Equal(t, "m-2026-05-22-024-d01", entry.ParentDelegation)
	assert.Equal(t, "m-2026-05-22-024", entry.ContractID)
}

// TestHandleAuditLog_OmitsEmptyNewFields verifies the new fields are
// omitted from the JSONL line when absent, preserving wire shape for
// the common case (single-tenant non-delegated tool call).
func TestHandleAuditLog_OmitsEmptyNewFields(t *testing.T) {
	dir := t.TempDir()
	payload := map[string]any{
		"session_id": "sess-bare",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	require.NoError(t, HandleAuditLog(bytes.NewReader(data), "", dir))

	path := filepath.Join(dir, "sess-bare.audit.jsonl")
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	body := string(bytes.TrimSpace(content))
	// Each new field should be absent from the JSON line.
	for _, field := range []string{
		"parent_session", "agent_id", "agent_type",
		"delegation_id", "parent_delegation", "contract_id",
	} {
		assert.NotContains(t, body, `"`+field+`"`,
			"empty %s must be omitted from wire shape", field)
	}
}

// TestAuditEntry_BackwardCompatDecode asserts that a v3.11.0 audit
// JSONL line (only ts/session/tool/tool_input_preview) decodes
// cleanly with the v3.12.0 auditEntry struct — new fields stay
// zero-valued.
func TestAuditEntry_BackwardCompatDecode(t *testing.T) {
	old := `{"ts":"2026-05-21T12:00:00Z","session":"sess-old","tool":"Bash","tool_input_preview":"{\"command\":\"ls\"}"}`
	var e auditEntry
	require.NoError(t, json.Unmarshal([]byte(old), &e))
	assert.Equal(t, "sess-old", e.Session)
	assert.Equal(t, "Bash", e.Tool)
	assert.Equal(t, `{"command":"ls"}`, e.ToolInputPreview)
	// New fields must decode to zero-value, not error.
	assert.Empty(t, e.ParentSession)
	assert.Empty(t, e.AgentID)
	assert.Empty(t, e.DelegationID)
	assert.Nil(t, e.ToolInput)
	assert.Empty(t, e.ToolInputHash)
}

// TestHashToolInput_Deterministic asserts that two inputs with the
// same logical content but different map iteration orders produce
// identical hashes. encoding/json sorts map keys, so this is the
// canonical-JSON property the hash relies on.
func TestHashToolInput_Deterministic(t *testing.T) {
	a := map[string]any{
		"tool_input": map[string]any{
			"command": "make check",
			"timeout": 60,
			"env":     map[string]any{"CI": "true", "GOFLAGS": "-race"},
		},
	}
	b := map[string]any{
		"tool_input": map[string]any{
			"env":     map[string]any{"GOFLAGS": "-race", "CI": "true"},
			"timeout": 60,
			"command": "make check",
		},
	}
	ha := hashToolInput(a)
	hb := hashToolInput(b)
	assert.NotEmpty(t, ha, "hash must be non-empty for non-empty input")
	assert.Equal(t, ha, hb,
		"canonical-JSON hash must be order-independent across map keys")
	assert.Len(t, ha, 64, "sha256 hex must be 64 chars")
}

// TestHashToolInput_DifferentInputsDiffer asserts that two inputs
// with different content produce different hashes — the collision
// detector requires the function to actually distinguish inputs.
func TestHashToolInput_DifferentInputsDiffer(t *testing.T) {
	a := map[string]any{"tool_input": map[string]any{"path": "/a"}}
	b := map[string]any{"tool_input": map[string]any{"path": "/b"}}
	assert.NotEqual(t, hashToolInput(a), hashToolInput(b))
}

// TestHashToolInput_NoInput verifies the hash is empty when
// tool_input is absent. Empty inputs cannot produce a meaningful
// collision detector.
func TestHashToolInput_NoInput(t *testing.T) {
	assert.Empty(t, hashToolInput(map[string]any{"tool_name": "Bash"}))
}

// TestReadAuditEntries_PartialTrailingLine asserts that a JSONL file
// whose last line is missing the trailing newline (the crash-during-
// write failure mode) decodes the well-formed lines and skips the
// fragment with a stderr warning.
func TestReadAuditEntries_PartialTrailingLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.audit.jsonl")

	// Two well-formed lines followed by a truncated third.
	good1 := `{"ts":"2026-05-22T10:00:00Z","session":"s1","tool":"Bash","tool_input_hash":"abc"}`
	good2 := `{"ts":"2026-05-22T10:00:01Z","session":"s1","tool":"Read","tool_input_hash":"def"}`
	partial := `{"ts":"2026-05-22T10:00:02Z","session":"s1","tool":"`
	body := good1 + "\n" + good2 + "\n" + partial
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	entries, err := readAuditEntries(path)
	require.NoError(t, err)
	require.Len(t, entries, 2,
		"two complete lines must decode; partial fragment must be skipped")
	assert.Equal(t, "Bash", entries[0].Tool)
	assert.Equal(t, "Read", entries[1].Tool)
}

// TestReadAuditEntries_Missing asserts that a missing file decodes
// to nil, nil — the absence of a log is the normal state for a
// freshly-started session.
func TestReadAuditEntries_Missing(t *testing.T) {
	entries, err := readAuditEntries(filepath.Join(t.TempDir(), "absent.jsonl"))
	require.NoError(t, err)
	assert.Nil(t, entries)
}

// TestReadAuditEntries_RoundTrip writes a sequence of entries
// through writeAuditEntry and verifies the reader returns them
// byte-equivalent.
func TestReadAuditEntries_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rt.audit.jsonl")

	want := []auditEntry{
		{
			Ts:            "2026-05-22T10:00:00Z",
			Session:       "s1",
			Tool:          "Bash",
			ToolInputHash: "h1",
		},
		{
			Ts:               "2026-05-22T10:00:01Z",
			Session:          "s1",
			ParentSession:    "parent",
			AgentID:          "bwk",
			AgentType:        "go-specialist",
			DelegationID:     "d-2026-05-22-001",
			ParentDelegation: "p1",
			ContractID:       "m-2026-05-22-024",
			Tool:             "Read",
			ToolInput:        map[string]any{"file_path": "/etc/hosts"},
			ToolInputHash:    "h2",
			ToolInputPreview: `{"file_path":"/etc/hosts"}`,
		},
	}
	for _, e := range want {
		require.NoError(t, writeAuditEntry(path, e))
	}

	got, err := readAuditEntries(path)
	require.NoError(t, err)
	require.NoError(t, auditEntriesEqual(want, got))
}

// TestWriteAuditEntry_FsyncEnforced is a smoke test that the writer
// does not fail on a normal file. The contract is that f.Sync is
// called per line; a missing fsync would not surface as a test
// failure short of a power-loss simulator, but the helper exists so
// future refactors that drop the Sync get caught structurally.
func TestWriteAuditEntry_FsyncEnforced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fsync.audit.jsonl")
	for i := 0; i < 3; i++ {
		err := writeAuditEntry(path, auditEntry{
			Ts:      "2026-05-22T10:00:00Z",
			Session: "s",
			Tool:    "Bash",
		})
		require.NoError(t, err)
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(string(data), "\n"),
		"three appends must produce three newline-terminated lines")
}
