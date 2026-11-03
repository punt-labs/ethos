package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
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
	assert.Error(t, err, "malformed JSON should return an error")
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
	// Arrays are valid JSON but not maps — should return error.
	r := bytes.NewReader([]byte(`[1, 2, 3]`))
	data, err := ReadInput(r, 100*time.Millisecond)
	assert.Error(t, err, "JSON array should return an error (not a map)")
	assert.Empty(t, data)
}

func TestReadWithTimeout_PipeClosed(t *testing.T) {
	// Data written and pipe closed — readWithTimeout returns the data
	// promptly (the normal case when Claude Code sends input).
	rFd, wFd, err := os.Pipe()
	require.NoError(t, err)
	defer rFd.Close()

	_, err = wFd.Write([]byte(`{"source": "timeout-fallback"}`))
	require.NoError(t, err)
	wFd.Close() // EOF after data

	start := time.Now()
	const timeout = 5 * time.Second // far above any scheduling noise
	data, err := readWithTimeout(rFd, timeout)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "timeout-fallback", data["source"])
	assert.Less(t, elapsed, timeout/2, "should return immediately, not wait for timeout")
}

func TestReadWithTimeout_EmptyPipe(t *testing.T) {
	// Open pipe with no data and no EOF — must return empty map on timeout.
	rFd, wFd, err := os.Pipe()
	require.NoError(t, err)
	defer rFd.Close()
	defer wFd.Close()

	start := time.Now()
	data, err := readWithTimeout(rFd, 100*time.Millisecond)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Empty(t, data)
	assert.GreaterOrEqual(t, elapsed, 80*time.Millisecond, "should wait near timeout duration")
	assert.Less(t, elapsed, 500*time.Millisecond, "must not block beyond timeout")
}

func TestReadWithTimeout_OpenPipeWithData(t *testing.T) {
	// The critical case: data in the pipe, write end NOT closed.
	// This is what Claude Code does for SessionStart hooks.
	// The old io.ReadAll approach consumed the data into its buffer
	// but blocked on the second Read waiting for EOF — the timer
	// fired and the data was lost. Single f.Read returns immediately.
	rFd, wFd, err := os.Pipe()
	require.NoError(t, err)
	defer rFd.Close()
	defer wFd.Close()

	_, err = wFd.Write([]byte(`{"session_id": "linux-open-pipe"}`))
	require.NoError(t, err)
	// Deliberately NOT closing wFd.

	start := time.Now()
	const timeout = 5 * time.Second // far above any scheduling noise
	data, err := readWithTimeout(rFd, timeout)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "linux-open-pipe", data["session_id"],
		"must return data even when pipe stays open")
	assert.Less(t, elapsed, timeout/2,
		"should return as soon as data is read, not wait for timeout")
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

// bigPayload is an Edit-sized PostToolUse input: well past one pipe write.
func bigPayload(t *testing.T) []byte {
	t.Helper()
	p, err := json.Marshal(map[string]any{
		"tool_name": "Edit",
		"tool_input": map[string]any{
			"old_string": strings.Repeat("old line of a large markdown file\n", 3000),
			"new_string": strings.Repeat("new line of a large markdown file\n", 3000),
		},
	})
	require.NoError(t, err)
	require.Greater(t, len(p), 200_000)
	return p
}

// writeSlowly sends p in pieces with small pauses, as a busy writer does.
func writeSlowly(w *os.File, p []byte) {
	const piece = 40_000
	for len(p) > 0 {
		n := min(piece, len(p))
		_, _ = w.Write(p[:n])
		p = p[n:]
		time.Sleep(5 * time.Millisecond)
	}
}

// A payload larger than one read, written in several pieces and then closed.
func TestReadWithTimeout_LargePayloadInSeveralWrites(t *testing.T) {
	rFd, wFd, err := os.Pipe()
	require.NoError(t, err)
	defer rFd.Close()
	payload := bigPayload(t)
	go func() {
		defer wFd.Close()
		writeSlowly(wFd, payload)
	}()

	data, err := readWithTimeout(rFd, 10*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "Edit", data["tool_name"])
}

// The same payload with the write end left open: the payload is complete when
// it parses, and the read must not wait for an EOF that never comes.
func TestReadWithTimeout_LargePayloadPipeLeftOpen(t *testing.T) {
	rFd, wFd, err := os.Pipe()
	require.NoError(t, err)
	defer rFd.Close()
	defer wFd.Close()
	payload := bigPayload(t)
	go writeSlowly(wFd, payload)

	const timeout = 10 * time.Second
	start := time.Now()
	data, err := readWithTimeout(rFd, timeout)
	require.NoError(t, err)
	assert.Equal(t, "Edit", data["tool_name"])
	assert.Less(t, time.Since(start), timeout/2, "must return when the payload is whole, not at the timeout")
}

// A fragment that never completes is still a loud parse error at the timeout.
func TestReadWithTimeout_PartialPayloadIsAnError(t *testing.T) {
	rFd, wFd, err := os.Pipe()
	require.NoError(t, err)
	defer rFd.Close()
	defer wFd.Close()
	_, err = wFd.Write([]byte(`{"tool_name": "Ed`))
	require.NoError(t, err)

	_, err = readWithTimeout(rFd, 200*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid JSON input")
}
