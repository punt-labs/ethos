// Package hook provides Claude Code hook handlers for ethos.
package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
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

// readWithTimeout reads from f in a goroutine and returns whatever was
// read before timeout expires. Used when SetReadDeadline is not supported
// (e.g., Linux pipes inherited from a parent process).
//
// Uses a single f.Read (not io.ReadAll) because ReadAll blocks until EOF.
// On an open pipe, EOF never arrives — ReadAll consumes the data into its
// internal buffer, then blocks on the second Read waiting for more. The
// timer fires, the goroutine hasn't returned, and the data is lost.
//
// A single Read returns as soon as data is available. Hook payloads are
// small JSON objects (< 4KB, well within PIPE_BUF), delivered in one
// pipe write, so one Read gets the full payload. The goroutine may leak
// if no data ever arrives — acceptable for a short-lived hook process.
func readWithTimeout(f *os.File, timeout time.Duration) (map[string]any, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		buf := make([]byte, 65536)
		n, err := f.Read(buf)
		if n > 0 {
			ch <- result{buf[:n], nil}
			return
		}
		ch <- result{nil, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return map[string]any{}, nil // no data — treat like timeout
		}
		return parseJSON(r.data)
	case <-time.After(timeout):
		return map[string]any{}, nil
	}
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
