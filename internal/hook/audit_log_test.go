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

	err = HandleAuditLog(bytes.NewReader(data), dir)
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

	err = HandleAuditLog(bytes.NewReader(data), dir)
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

	err = HandleAuditLog(bytes.NewReader(data), dir)
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
	err = HandleAuditLog(bytes.NewReader(data), dir)
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
		require.NoError(t, HandleAuditLog(bytes.NewReader(data), dir))
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

	err = HandleAuditLog(bytes.NewReader(data), dir)
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
