package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readWithTimeoutJSON is readWithTimeout followed by the parse ReadInput does.
func readWithTimeoutJSON(f *os.File, timeout time.Duration) (map[string]any, error) {
	b, err := readWithTimeout(f, timeout)
	if err != nil {
		return map[string]any{}, err
	}
	return parseJSON(b)
}

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
	data, err := readWithTimeoutJSON(rFd, timeout)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "timeout-fallback", data["source"])
	assert.Less(t, elapsed, timeout/2, "should return immediately, not wait for timeout")
}

func TestReadWithTimeout_EmptyPipe(t *testing.T) {
	// Open pipe with no data and no EOF: the deadline fires, and that is an
	// error, never an empty payload (DES-074: a stall is not "no input").
	rFd, wFd, err := os.Pipe()
	require.NoError(t, err)
	defer rFd.Close()
	defer wFd.Close()

	start := time.Now()
	data, err := readWithTimeoutJSON(rFd, 100*time.Millisecond)
	elapsed := time.Since(start)

	require.ErrorIs(t, err, ErrInputTimeout)
	assert.Contains(t, err.Error(), "0 bytes read")
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
	data, err := readWithTimeoutJSON(rFd, timeout)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.Equal(t, "linux-open-pipe", data["session_id"],
		"must return data even when pipe stays open")
	assert.Less(t, elapsed, timeout/2,
		"should return as soon as data is read, not wait for timeout")
}

// ReadFromFile is what drainAuditStdin delegates to. A payload past one 64KiB
// read, written in pieces to a pipe left open, must come back whole: the single
// Read it replaced truncated at 65536 bytes and the entry parsed as invalid.
func TestReadFromFile_LargePayloadNotTruncated(t *testing.T) {
	rFd, wFd, err := os.Pipe()
	require.NoError(t, err)
	defer rFd.Close()
	defer wFd.Close()
	payload := bigPayload(t)
	require.Greater(t, len(payload), 65536)
	go writeSlowly(wFd, payload)

	const timeout = 10 * time.Second
	start := time.Now()
	data, err := ReadFromFile(rFd, timeout)
	require.NoError(t, err)
	assert.Equal(t, len(payload), len(data), "the whole payload is read, not truncated at 65536 bytes")
	parsed, err := parseJSON(data)
	require.NoError(t, err, "the full payload parses as JSON")
	assert.Equal(t, "Edit", parsed["tool_name"])
	assert.Less(t, time.Since(start), timeout/2, "returns when the payload is whole, not at the timeout")
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

	data, err := readWithTimeoutJSON(rFd, 10*time.Second)
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
	data, err := readWithTimeoutJSON(rFd, timeout)
	require.NoError(t, err)
	assert.Equal(t, "Edit", data["tool_name"])
	assert.Less(t, time.Since(start), timeout/2, "must return when the payload is whole, not at the timeout")
}

// A fragment that never completes is the timeout error at the deadline, with
// the count of bytes that did arrive.
func TestReadWithTimeout_PartialPayloadIsATimeout(t *testing.T) {
	rFd, wFd, err := os.Pipe()
	require.NoError(t, err)
	defer rFd.Close()
	defer wFd.Close()
	_, err = wFd.Write([]byte(`{"tool_name": "Ed`))
	require.NoError(t, err)

	_, err = readWithTimeoutJSON(rFd, 200*time.Millisecond)
	require.ErrorIs(t, err, ErrInputTimeout)
	assert.Contains(t, err.Error(), "17 bytes read")
}

// A deadline-capable fd reports the same timeout, and a failed read is
// wrapped, not swallowed as if the payload had ended.
func TestReadInput_TimeoutAndReadErrors(t *testing.T) {
	t.Run("deadline on a pollable pipe", func(t *testing.T) {
		rFd, wFd, err := os.Pipe()
		require.NoError(t, err)
		defer rFd.Close()
		defer wFd.Close()
		_, err = wFd.Write([]byte(`{"tool_name": "Ed`))
		require.NoError(t, err)

		data, err := ReadInput(rFd, 100*time.Millisecond)
		require.ErrorIs(t, err, ErrInputTimeout)
		assert.Contains(t, err.Error(), "17 bytes read")
		assert.Empty(t, data)
	})
	t.Run("failed read", func(t *testing.T) {
		boom := errors.New("boom")
		_, err := ReadInput(iotest.ErrReader(boom), 100*time.Millisecond)
		require.ErrorIs(t, err, boom)
		assert.NotErrorIs(t, err, ErrInputTimeout)
		assert.Contains(t, err.Error(), "reading input")
	})
	t.Run("closed descriptor", func(t *testing.T) {
		rFd, wFd, err := os.Pipe()
		require.NoError(t, err)
		defer wFd.Close()
		require.NoError(t, rFd.Close())

		_, err = ReadInput(rFd, 100*time.Millisecond)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrInputTimeout)
		assert.Contains(t, err.Error(), "reading input")
	})
}

// writeThenHold writes b to a fresh pipe and keeps the write end open, as
// Claude Code does, then returns the read end.
func writeThenHold(t *testing.T, b []byte) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { r.Close(); w.Close() })
	_, err = w.Write(b)
	require.NoError(t, err)
	return r
}

// No proper prefix of a payload is accepted as the payload: on an open pipe
// each ends at the deadline with the timeout error, never in a map of whatever
// parsed.
func TestReadInput_TruncatedPayloadIsNeverComplete(t *testing.T) {
	payload := `{"tool_name":"mcp__plugin_ethos_self__identity","tool_input":{"a":[1,2,{"b":"c"}]},"n":12}`
	for i := 1; i < len(payload); i++ {
		assert.False(t, completeObject([]byte(payload[:i])), "prefix of %d bytes", i)
	}
	assert.True(t, completeObject([]byte(payload+"\n")))
	for _, name := range []string{"pollable pipe", "blocking inherited fd"} {
		t.Run(name, func(t *testing.T) {
			r := writeThenHold(t, []byte(payload[:len(payload)-1]))
			var data map[string]any
			var err error
			if name == "pollable pipe" {
				data, err = ReadInput(r, 150*time.Millisecond)
			} else {
				data, err = readWithTimeoutJSON(r, 150*time.Millisecond)
			}
			require.ErrorIs(t, err, ErrInputTimeout)
			assert.Empty(t, data)
		})
	}
}

// A complete object followed by more bytes in the same read is not a payload:
// the read goes on to the end of input and refuses it, rather than returning
// the first value. A top-level scalar never ends a read early either.
func TestReadInput_TrailingBytesAndScalarsAreRefused(t *testing.T) {
	for name, in := range map[string]string{
		"object then garbage":     `{"a":1}garbage`,
		"two objects":             `{"a":1}{"b":2}`,
		"scalar":                  `123`,
		"object then array":       `{"a":1}[2]`,
		"object then open object": `{"a":1}{"b":`,
	} {
		t.Run(name+"/reader", func(t *testing.T) {
			data, err := ReadInput(strings.NewReader(in), time.Second)
			require.Error(t, err)
			assert.Empty(t, data)
		})
		t.Run(name+"/open pipe", func(t *testing.T) {
			data, err := ReadInput(writeThenHold(t, []byte(in)), 150*time.Millisecond)
			require.Error(t, err)
			assert.Empty(t, data)
		})
	}
}
