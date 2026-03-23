// Package hook provides Claude Code hook handlers for ethos.
package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
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
		// Deadline not supported on this fd; fall back to direct read.
		return readDirect(f)
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

// parseJSON attempts to parse data as a JSON object. Returns an empty
// map for empty input. Returns an error for malformed JSON or non-object
// types (arrays, scalars).
func parseJSON(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return map[string]any{}, nil
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]any{}, fmt.Errorf("invalid JSON input (%d bytes): %w", len(data), err)
	}
	if result == nil {
		return map[string]any{}, nil
	}
	return result, nil
}
