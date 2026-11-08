// Package hook provides Claude Code hook handlers for ethos.
package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// hookInputTimeout bounds how long a hook waits for Claude Code's payload.
// Claude Code serializes a large payload (an Edit of a big file) in pieces and
// can pause for more than a second between them while it is busy. A complete
// JSON value ends the wait at once, so the bound is paid only when the payload
// never completes: a hook that is given no input and an open stdin.
const hookInputTimeout = 10 * time.Second

// ErrInputTimeout is the error ReadInput wraps when the payload is not a
// complete JSON value by the time the deadline fires. The caller cannot tell a
// stalled writer from an empty payload except by this error, so no timeout is
// ever read as "no input".
var ErrInputTimeout = errors.New("hook input timed out")

// maxHookInput bounds how many bytes a hook buffers from stdin. An Edit payload
// carries the old and new contents of a file, so a real payload can be several
// megabytes; 64 MiB is far above any Claude Code has sent and still bounds the
// memory a runaway or hostile writer can make a hook allocate. It is a variable
// only so tests can lower it.
var maxHookInput = 64 << 20

// ErrInputTooLarge is the error the readers wrap when the payload grows past
// maxHookInput. The bytes read before the limit are returned with it.
var ErrInputTooLarge = errors.New("hook input too large")

func inputTooLarge() error {
	return fmt.Errorf("%w: hook input exceeds %d bytes", ErrInputTooLarge, maxHookInput)
}

func inputTimeout(timeout time.Duration, n int) error {
	return fmt.Errorf("%w after %s (%d bytes read)", ErrInputTimeout, timeout, n)
}

// ReadInput reads a JSON object from r with a timeout. It returns an empty map
// on empty input. It returns an error wrapping ErrInputTimeout when the payload
// is not complete when timeout expires, an error wrapping the cause when a read
// fails for any reason other than EOF, and an error on malformed JSON. Callers
// log the error; hooks must be resilient. It does not block when the pipe
// remains open without EOF.
func ReadInput(r io.Reader, timeout time.Duration) (map[string]any, error) {
	data, err := readRaw(r, timeout)
	if err != nil {
		return map[string]any{}, err
	}
	return parseJSON(data)
}

// readRaw is ReadInput without the parse: the bytes read, and the read error
// if any. On error the bytes are what arrived before it, so a caller can still
// look inside a payload that did not complete.
func readRaw(r io.Reader, timeout time.Duration) ([]byte, error) {
	// For regular readers (bytes.Reader, strings.Reader), read directly.
	// For pipes/files, use a deadline if available.
	if f, ok := r.(*os.File); ok {
		return readFromFile(f, timeout)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return data, fmt.Errorf("reading input: %w", err)
	}
	return data, nil
}

// ReadFromFile reads a hook payload from an open file, typically os.Stdin,
// with a timeout and returns the raw bytes. It accumulates reads until the
// buffer holds one complete JSON object, the file reaches EOF, or the timeout
// fires, so a payload larger than one 64KiB read is read in full and an open
// pipe the writer never closes does not block the process. On timeout it
// returns the bytes read so far wrapped in ErrInputTimeout. A payload past
// maxHookInput stops the read and returns the bytes so far wrapped in
// ErrInputTooLarge.
func ReadFromFile(f *os.File, timeout time.Duration) ([]byte, error) {
	return readFromFile(f, timeout)
}

// readFromFile reads from an *os.File using SetReadDeadline to avoid
// blocking forever when Claude Code leaves the pipe open.
func readFromFile(f *os.File, timeout time.Duration) ([]byte, error) {
	if err := f.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		// Deadline not supported (Linux pipes). Race a goroutine
		// against a timer so we don't block forever.
		return readWithTimeout(f, timeout)
	}
	defer f.SetReadDeadline(time.Time{}) //nolint:errcheck

	var buf []byte
	var frame objectFrame
	chunk := make([]byte, 65536)
	for {
		n, err := f.Read(chunk)
		if n > 0 {
			if len(buf)+n > maxHookInput {
				return buf, inputTooLarge()
			}
			buf = append(buf, chunk[:n]...)
			// A complete JSON object ends the payload even when the writer
			// keeps the pipe open. Until then the one deadline set above
			// bounds the wait: a writer may pause between the pieces of a
			// large payload for longer than any fixed gap.
			if frame.feed(chunk[:n]) && frame.confirm(buf) {
				return buf, nil
			}
		}
		switch {
		case err == nil:
		case errors.Is(err, io.EOF):
			return buf, nil
		case errors.Is(err, os.ErrDeadlineExceeded):
			return buf, inputTimeout(timeout, len(buf))
		default:
			return buf, fmt.Errorf("reading input: %w", err)
		}
	}
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
// until EOF, an error, the maxHookInput bound, or until what it holds is one
// complete JSON object, which ends the payload even when the writer keeps the
// pipe open. Completeness is tracked by an objectFrame over each new chunk, so
// the buffer is not rescanned per read.
//
// On timeout the bytes so far are returned with an ErrInputTimeout error. The
// goroutine may outlive the call if the writer stalls: acceptable for a
// short-lived hook process.
func readWithTimeout(f *os.File, timeout time.Duration) ([]byte, error) {
	var mu sync.Mutex
	var buf []byte
	var readErr error
	snapshot := func() ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		return append([]byte(nil), buf...), readErr
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var frame objectFrame
		chunk := make([]byte, 65536)
		for {
			n, err := f.Read(chunk)
			if n > 0 {
				mu.Lock()
				if len(buf)+n > maxHookInput {
					readErr = inputTooLarge()
					mu.Unlock()
					return
				}
				buf = append(buf, chunk[:n]...)
				whole := frame.feed(chunk[:n]) && frame.confirm(buf)
				mu.Unlock()
				if whole {
					return
				}
			}
			if err != nil {
				mu.Lock()
				readErr = err
				mu.Unlock()
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		data, _ := snapshot()
		return data, inputTimeout(timeout, len(data))
	}
	data, err := snapshot()
	if errors.Is(err, ErrInputTooLarge) {
		return data, err
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return data, fmt.Errorf("reading input: %w", err)
	}
	return data, nil
}

// objectFrame tracks, one chunk at a time, whether the bytes fed so far are
// exactly one JSON object: it began with '{', its braces and brackets have
// balanced, and only whitespace follows. Only an object ends a read early: a
// proper prefix of an object is never valid JSON, so a truncated payload cannot
// pass, whereas a scalar prefix can ("12" is a prefix of "123"). Any other
// non-whitespace byte after the object, or a first byte that is not '{',
// rejects the frame for good, so the read continues to EOF or the deadline and
// the payload is refused, not trimmed to its first value.
//
// The frame checks structure only. confirm settles validity once, when the
// structure is complete, so each byte is scanned a bounded number of times.
type objectFrame struct {
	started  bool // a non-whitespace byte has been seen
	depth    int  // nesting across {} and []
	inString bool
	escaped  bool
	rejected bool
}

// feed scans chunk and reports whether the frame is now complete.
func (f *objectFrame) feed(chunk []byte) bool {
	for _, c := range chunk {
		if f.rejected {
			return false
		}
		if f.inString {
			switch {
			case f.escaped:
				f.escaped = false
			case c == '\\':
				f.escaped = true
			case c == '"':
				f.inString = false
			}
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if !f.started {
			f.started = true
			if c != '{' {
				f.rejected = true
				return false
			}
		} else if f.depth == 0 {
			f.rejected = true
			return false
		}
		switch c {
		case '{', '[':
			f.depth++
		case '}', ']':
			f.depth--
		case '"':
			f.inString = true
		}
	}
	return f.started && f.depth == 0 && !f.inString && !f.rejected
}

// confirm runs json.Valid over buf, the final authority on a structurally
// complete frame. A balanced but invalid payload rejects the frame, so it is
// not validated again and the read goes on to EOF or the deadline.
func (f *objectFrame) confirm(buf []byte) bool {
	if json.Valid(buf) {
		return true
	}
	f.rejected = true
	return false
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
