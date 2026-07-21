package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteAuditEntry_TornTailNotFused is SFH R3-2 for the writer path: when the
// file ends in a torn fragment from a crashed writer, the next entry must not
// glue onto it. The separator newline keeps the fragment its own (skipped) line
// and the new entry decodable.
func TestWriteAuditEntry_TornTailNotFused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	// A prior write crashed mid-line: a fragment with no terminator.
	require.NoError(t, os.WriteFile(path, []byte(`{"ts":"2026-05-22T09:00:00Z","tool":"Rea`), 0o600))

	entry := auditEntry{Ts: "2026-05-22T10:00:00Z", Session: "s", Tool: "Bash"}
	require.NoError(t, writeAuditEntry(path, entry))

	// The new entry decodes as its own line; the fragment is skipped, not fused.
	got, err := readAuditEntries(path)
	require.NoError(t, err)
	require.Len(t, got, 1, "only the well-formed new entry decodes; the fragment is skipped")
	assert.Equal(t, "Bash", got[0].Tool)
	assert.Equal(t, "2026-05-22T10:00:00Z", got[0].Ts)
}

// TestWriteAuditEntry_CleanTailNoExtraNewline confirms the boundary guard does
// not add a blank line to a normally-terminated file.
func TestWriteAuditEntry_CleanTailNoExtraNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	require.NoError(t, writeAuditEntry(path, auditEntry{Ts: "2026-05-22T10:00:00Z", Session: "s", Tool: "Read"}))
	require.NoError(t, writeAuditEntry(path, auditEntry{Ts: "2026-05-22T10:00:01Z", Session: "s", Tool: "Bash"}))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	// Exactly two lines, two newlines — no blank line inserted.
	assert.Equal(t, 2, countByte(data, '\n'))
	got, err := readAuditEntries(path)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func countByte(b []byte, c byte) int {
	n := 0
	for _, x := range b {
		if x == c {
			n++
		}
	}
	return n
}
