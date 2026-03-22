// Package hook provides Claude Code hook handlers for ethos.
package hook

import (
	"encoding/json"
	"io"
	"os"
	"time"
)

// ReadInput reads a JSON object from r with a timeout. Returns an empty
// map on empty input, malformed JSON, or timeout. Does not block when
// the pipe remains open without EOF — uses poll-style reads with a
// deadline to avoid the stdin hang that affects Claude Code hooks.
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
		return map[string]any{}, nil
	}
	return parseJSON(data), nil
}

// readFromFile reads from an *os.File using SetReadDeadline to avoid
// blocking forever when Claude Code leaves the pipe open.
func readFromFile(f *os.File, timeout time.Duration) (map[string]any, error) {
	_ = f.SetReadDeadline(time.Now().Add(timeout))
	defer f.SetReadDeadline(time.Time{}) //nolint:errcheck

	var buf []byte
	chunk := make([]byte, 65536)
	for {
		n, err := f.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			// Reset deadline after each successful read — give more
			// time for multi-chunk payloads but still catch open pipes.
			_ = f.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		}
		if err != nil {
			break // EOF, timeout, or error — all fine
		}
	}

	return parseJSON(buf), nil
}

// parseJSON attempts to parse data as a JSON object. Returns an empty
// map for empty input, whitespace, arrays, or malformed JSON.
func parseJSON(data []byte) map[string]any {
	if len(data) == 0 {
		return map[string]any{}
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]any{}
	}
	if result == nil {
		return map[string]any{}
	}
	return result
}
