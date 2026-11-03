// Package hook provides Claude Code hook handlers for ethos.
package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// ReadInput reads a JSON object from r with a timeout. Returns an empty
// map on empty input or timeout. Returns an error on read failures or
// malformed JSON (callers should log but continue — hooks must be
// resilient). Does not block when the pipe remains open without EOF —
// uses poll-style reads with a deadline.
func ReadInput(r io.Reader, timeout time.Duration) (map[string]any, error) {
	// For regular readers (bytes.Reader, strings.Reader), read directly.
	// For pipes/files, use a deadline if available.
	if f, ok := r.(*os.File); ok {
		return readFromFile(f, timeout)
	}
	return readDirect(r)
}

// readDirect reads from an io.Reader that will return EOF (buffers, etc).
func readDirect(r io.Reader) (map[string]any, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return map[string]any{}, fmt.Errorf("reading input: %w", err)
	}
	return parseJSON(data)
}

// readFromFile reads from an *os.File using SetReadDeadline to avoid
// blocking forever when Claude Code leaves the pipe open.
func readFromFile(f *os.File, timeout time.Duration) (map[string]any, error) {
	if err := f.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		// Deadline not supported (Linux pipes). Race a goroutine
		// against a timer so we don't block forever.
		return readWithTimeout(f, timeout)
	}
	defer f.SetReadDeadline(time.Time{}) //nolint:errcheck

	var buf []byte
	chunk := make([]byte, 65536)
	for {
		n, err := f.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			// Use a fraction of the initial timeout for inter-chunk
			// gaps. Large enough for OS-level buffering delays, small
			// enough to detect an open pipe without EOF promptly.
			interChunk := timeout / 10
			if interChunk < 20*time.Millisecond {
				interChunk = 20 * time.Millisecond
			}
			if sdErr := f.SetReadDeadline(time.Now().Add(interChunk)); sdErr != nil {
				break // stop reading rather than risk hanging
			}
		}
		if err != nil {
			break // EOF, timeout, or error — all fine
		}
	}

	return parseJSON(buf)
}

// readWithTimeout reads from f in a goroutine and returns what was read when
// the payload is complete or timeout expires. Used when SetReadDeadline is not
// supported (e.g., Linux pipes inherited from a parent process).
//
// It cannot use io.ReadAll: on an open pipe EOF never arrives, so ReadAll
// consumes the data and then blocks, and the data is lost when the timer fires.
// Nor can it stop after one Read: a payload larger than one pipe write (an
// Edit's old and new strings, a tool response) arrives in several, and parsing
// the first fragment loses the audit entry. So the goroutine accumulates reads
// until EOF, an error, or until what it holds is one complete JSON value, which
// ends the payload even when the writer keeps the pipe open.
//
// On timeout the data so far is parsed: none is the empty map, a fragment is
// the parse error, as it was. The goroutine may outlive the call if the writer
// stalls: acceptable for a short-lived hook process.
func readWithTimeout(f *os.File, timeout time.Duration) (map[string]any, error) {
	var mu sync.Mutex
	var buf []byte
	snapshot := func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return append([]byte(nil), buf...)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		chunk := make([]byte, 65536)
		for {
			n, err := f.Read(chunk)
			if n > 0 {
				mu.Lock()
				buf = append(buf, chunk[:n]...)
				whole := json.Valid(bytes.TrimSpace(buf))
				mu.Unlock()
				if whole {
					return
				}
			}
			if err != nil {
				return // EOF or a failed read: what was read is all there is
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
	return parseJSON(snapshot())
}

// parseJSON attempts to parse data as a JSON object. Returns an empty
// map for empty input. Returns an error for malformed JSON or non-object
// types (arrays, scalars).
func parseJSON(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return map[string]any{}, nil
	}

	var result map[string]any
	if err := json.Unmarshal(trimmed, &result); err != nil {
		return map[string]any{}, fmt.Errorf("invalid JSON input (%d bytes): %w", len(data), err)
	}
	if result == nil {
		return map[string]any{}, nil
	}
	return result, nil
}
