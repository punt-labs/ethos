package hook

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadInput_ValidJSON(t *testing.T) {
	r := bytes.NewReader([]byte(`{"session_id": "abc-123", "event": "startup"}`))
	data, err := ReadInput(r, 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "abc-123", data["session_id"])
	assert.Equal(t, "startup", data["event"])
}

func TestReadInput_EmptyInput(t *testing.T) {
	r := bytes.NewReader(nil)
	data, err := ReadInput(r, 100*time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestReadInput_MalformedJSON(t *testing.T) {
	r := bytes.NewReader([]byte(`{not json`))
	data, err := ReadInput(r, 100*time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestReadInput_OpenPipeNoEOF(t *testing.T) {
	// Simulate Claude Code leaving the pipe open without closing it.
	// Write data, do NOT close the write end. ReadInput must return
	// within the timeout, not block forever.
	rFd, wFd, err := os.Pipe()
	require.NoError(t, err)
	defer rFd.Close()
	defer wFd.Close()

	_, err = wFd.Write([]byte(`{"session_id": "open-pipe"}`))
	require.NoError(t, err)
	// Deliberately NOT closing wFd — simulates open pipe without EOF.

	start := time.Now()
	data, err := ReadInput(rFd, 200*time.Millisecond)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "open-pipe", data["session_id"])
	assert.Less(t, elapsed, 500*time.Millisecond, "must return within timeout, not block forever")
}

func TestReadInput_WhitespaceOnly(t *testing.T) {
	r := bytes.NewReader([]byte("   \n  \t  "))
	data, err := ReadInput(r, 100*time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestReadInput_JSONArray(t *testing.T) {
	// Arrays are valid JSON but not maps — should return empty.
	r := bytes.NewReader([]byte(`[1, 2, 3]`))
	data, err := ReadInput(r, 100*time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, data)
}

func TestReadInput_NestedJSON(t *testing.T) {
	r := bytes.NewReader([]byte(`{"tool_input": {"command": "ls"}, "tool_name": "Bash"}`))
	data, err := ReadInput(r, 100*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, "Bash", data["tool_name"])
	inner, ok := data["tool_input"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ls", inner["command"])
}
