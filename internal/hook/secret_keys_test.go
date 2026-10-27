package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAudit_CredentialAsAKeyIsRedacted: the credential pass runs on map keys as
// well as values, so a token used as a nested tool_input key does not reach the
// audit log or the chunks sealed from it. Two keys that redact to the same name
// do not merge.
func TestAudit_CredentialAsAKeyIsRedacted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tok1 := "ghp_" + strings.Repeat("a1B2", 9)
	tok2 := "ghp_" + strings.Repeat("c3D4", 9)
	in := map[string]any{"x": map[string]any{tok1: 1, tok2: 2, "ok": 3}}
	out, _ := redactToolInput(map[string]any{"tool_input": in}, "Bash", t.TempDir())
	b, err := json.Marshal(out)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "ghp_")
	x := out["x"].(map[string]any)
	assert.Len(t, x, 3, "no two keys merge")
	assert.Equal(t, 3, x["ok"])

	// Through the writer and a seal of the file it wrote.
	repo := t.TempDir()
	payload, _ := json.Marshal(map[string]any{"session_id": "s1", "tool_name": "Bash", "tool_input": in})
	require.NoError(t, HandleAuditLog(bytes.NewReader(payload), repo, ""))
	live := liveAuditPath(repo, "s1")
	data, err := os.ReadFile(live)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "ghp_")
	sealedLines, _ := guardTail([][]byte{bytes.TrimSpace(data)}, "label")
	assert.NotContains(t, string(sealedLines[0]), "ghp_")
}
