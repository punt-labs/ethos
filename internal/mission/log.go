//go:build !windows

package mission

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Event is a single line in a mission's append-only event log.
//
// 3.1 only writes create, update, and close events. Phases 3.4–3.7 will
// add reflect, verify, and other event types. The schema is intentionally
// open: Details is a free-form map so future event types do not require
// a schema migration.
type Event struct {
	TS      string         `json:"ts"`              // RFC3339
	Event   string         `json:"event"`           // create|update|close|...
	Actor   string         `json:"actor"`           // identity handle
	Details map[string]any `json:"details,omitempty"`
}

// Event log writes:
//
// Each event is encoded as one complete JSON line via json.Marshal
// and written via a single Write call to an O_APPEND file. Production
// callers (Create/Update/Close) hold the per-mission flock and call
// appendEventLocked directly — the flock is what serializes writers
// across cooperating processes, not PIPE_BUF atomicity (which applies
// to pipes/FIFOs, not regular files). A short-write check defends
// against partial writes regardless.
//
// There is no public appendEvent wrapper that acquires the flock.
// Earlier drafts had one, but it was only used by tests and became
// a deadlock footgun for any future caller invoking it from inside
// an existing locked block. 3.7's log reader API will add a public
// read path when external consumers genuinely need it; writes stay
// internal to the store.

// appendEventLocked writes a single event without acquiring the flock.
// The caller must hold the lock for the given missionID.
//
// Atomicity: the pre-write file size is captured, and on ANY write
// failure (partial or full) the file is truncated back to its
// original length. This prevents a partially-written JSONL line from
// corrupting the log. If the truncation-rollback itself fails, the
// returned error notes both failures.
func (s *Store) appendEventLocked(missionID string, e Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshaling event: %w", err)
	}
	// Append a single newline so the file stays JSON-lines compliant.
	line := append(data, '\n')

	f, err := os.OpenFile(s.logPath(missionID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening event log: %w", err)
	}
	defer f.Close()

	// Capture the pre-write length so we can truncate back on any
	// partial write. Stat on the open fd returns the current size.
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat event log: %w", err)
	}
	origSize := info.Size()

	// The io.Writer contract says a short write (n < len(line))
	// must be accompanied by a non-nil error, but defensive code
	// should not trust implementations to honor the contract —
	// a silently truncated line corrupts the append-only JSONL log.
	n, writeErr := f.Write(line)
	if writeErr != nil || n != len(line) {
		// Roll back any partial write by truncating to the original
		// size. Without this, a partial write leaves a truncated
		// JSONL line at EOF, breaking the append-only invariant for
		// any future reader.
		if tErr := f.Truncate(origSize); tErr != nil {
			if writeErr != nil {
				return fmt.Errorf("writing event: %w; truncating partial line failed: %v", writeErr, tErr)
			}
			return fmt.Errorf("writing event: short write %d of %d bytes; truncating partial line failed: %v", n, len(line), tErr)
		}
		if writeErr != nil {
			return fmt.Errorf("writing event: %w", writeErr)
		}
		return fmt.Errorf("writing event: short write %d of %d bytes", n, len(line))
	}
	return nil
}

// --- Phase 3.7: public reader API ---
//
// Everything below is the READ path. It does not modify anything
// above: appendEventLocked and the writer invariants (flock, truncate
// rollback, single Write call) are frozen. Phase 3.7 adds LoadEvents
// and FilterEvents so external consumers — CLI `mission log`, MCP
// `mission log`, post-mortem tooling — can walk the audit trail
// without hand-parsing JSONL.
//
// The split is deliberate: earlier drafts exported an AppendEvent
// wrapper, but DES-031 round 3 unexported it as a deadlock footgun
// for callers holding the store lock. Phase 3.7 does not
// re-introduce a public writer path.

// LoadEvents returns every parseable event in a mission's JSONL log,
// in the on-disk order the writer appended them, plus a warnings
// slice naming any lines that could not be decoded.
//
// The reader is a post-mortem tool: one corrupt line must not erase
// the rest of the log. Each line is decoded independently with
// DisallowUnknownFields — symmetric with the reflection and result
// decoders — and a failing line produces a warning identifying the
// line number. A hand-edited file that plants an unknown top-level
// field, an empty required field, or garbage bytes on a single line
// degrades to "that line is missing from the output, the rest of
// the file is still readable."
//
// Missing file → empty slice, nil warnings, nil error. Symmetric
// with LoadResults and LoadReflections: the absence of any event is
// the normal state for a brand-new mission whose Store.Create has
// not yet been called. Empty file (zero bytes) → same shape.
//
// Permission-denied and other I/O failures on the log file itself
// are distinguishable from "missing file": the reader returns a
// typed error, nil events, nil warnings. The caller can
// errors.Is(err, os.ErrPermission) or inspect the wrapped chain.
//
// Mission identity is enforced via the file path, not a per-line
// field. The Event schema has no mission_id — the log file IS
// scoped to one mission by construction (see logPath, which runs
// the ID through filepath.Base as defense in depth against
// traversal-laced callers). A caller-supplied "mission" key inside
// the free-form Details map is opaque payload, not identity; the
// reader preserves it untouched.
//
// The returned events slice is never nil when err is nil — it is
// an empty slice at minimum — so callers that marshal the result
// to JSON see `[]` rather than `null`.
func (s *Store) LoadEvents(missionID string) ([]Event, []string, error) {
	if strings.TrimSpace(missionID) == "" {
		return nil, nil, fmt.Errorf("missionID is required")
	}
	data, err := os.ReadFile(s.logPath(missionID))
	if err != nil {
		if os.IsNotExist(err) {
			return []Event{}, nil, nil
		}
		return nil, nil, fmt.Errorf("reading events for %q: %w", missionID, err)
	}
	return decodeEventLog(data)
}

// decodeEventLog walks a JSONL log body line-by-line, strictly
// decoding each line into an Event. Corrupt lines are reported in
// the warnings slice with their 1-based line number; clean lines
// land in the events slice in on-disk order. The helper is pure so
// it can be exercised directly by tests without a Store.
//
// Strict decode: every line runs through json.Decoder with
// DisallowUnknownFields. Trust-boundary parity with the reflection
// and result loaders is the whole point — an attacker with local
// write access cannot smuggle extra fields past a lax reader.
//
// Blank lines (runs of whitespace or empty) are silently skipped —
// the writer never emits them, but hand-edited files might. Missing
// required fields (ts, event, actor) reject the line with a warning.
func decodeEventLog(data []byte) ([]Event, []string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return []Event{}, nil, nil
	}
	events := []Event{}
	var warnings []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Bump the scanner buffer so a long Details payload does not
	// truncate — the default 64 KiB is small enough to clip a
	// realistic event with a big files_changed map. Ceiling is 1 MiB,
	// matching readLog in log_test.go.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Bytes()
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		e, err := decodeEventLine(raw)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("line %d: %v", lineNo, err))
			continue
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		// A scanner error is a genuine read failure (I/O, buffer
		// overflow). Surface it as a warning on the last line
		// attempted so partial output is still useful — the caller
		// can see what was successfully decoded before the failure.
		warnings = append(warnings, fmt.Sprintf("line %d: scanner: %v", lineNo+1, err))
	}
	return events, warnings, nil
}

// decodeEventLine strictly decodes a single JSONL line into an
// Event. Unknown top-level fields are rejected, and empty mandatory
// fields (ts, event, actor) produce a typed error so
// decodeEventLog can attach the line number. The Details map is
// free-form: any shape the writer chose is preserved as-is.
func decodeEventLine(raw []byte) (Event, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var e Event
	if err := dec.Decode(&e); err != nil {
		return Event{}, fmt.Errorf("decoding event: %w", err)
	}
	// Refuse trailing content after the first document on a single
	// line. A JSONL line must contain exactly one JSON value.
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Event{}, fmt.Errorf("decoding event: multiple JSON values on one line")
		}
		return Event{}, fmt.Errorf("decoding event: trailing content: %w", err)
	}
	if strings.TrimSpace(e.TS) == "" {
		return Event{}, fmt.Errorf("event missing ts")
	}
	if strings.TrimSpace(e.Event) == "" {
		return Event{}, fmt.Errorf("event missing event type")
	}
	if strings.TrimSpace(e.Actor) == "" {
		return Event{}, fmt.Errorf("event missing actor")
	}
	return e, nil
}

// FilterEvents returns the subset of events that match every
// supplied filter. Filters AND-compose: an event is included only
// if it passes type and since.
//
// An empty types slice or nil means "all types". A blank since
// means "no time cutoff". Both filters are optional and both
// compose. An unknown type string is not rejected — event types
// are forward-compatible, so a caller filtering for a type the
// writer has not yet emitted gets an empty slice, not an error.
//
// The returned slice is never nil — it is an empty slice at
// minimum — so the CLI and MCP JSON surfaces see `[]` rather than
// `null`.
//
// since uses RFC3339 parsing; an invalid value produces a typed
// error naming the field. Events whose on-disk ts is not RFC3339
// are dropped when since is set (they cannot satisfy the cutoff)
// and preserved when since is blank (the filter only answers
// "matches the filters", not "is the file well-formed").
func FilterEvents(events []Event, types []string, since string) ([]Event, error) {
	var cutoff time.Time
	hasCutoff := false
	if strings.TrimSpace(since) != "" {
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return nil, fmt.Errorf("invalid since %q: %w", since, err)
		}
		cutoff = t
		hasCutoff = true
	}
	var typeSet map[string]struct{}
	if len(types) > 0 {
		typeSet = make(map[string]struct{}, len(types))
		for _, t := range types {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			typeSet[t] = struct{}{}
		}
	}
	out := []Event{}
	for _, e := range events {
		if typeSet != nil {
			if _, ok := typeSet[e.Event]; !ok {
				continue
			}
		}
		if hasCutoff {
			ts, err := time.Parse(time.RFC3339, e.TS)
			if err != nil {
				// Event with an unparseable ts cannot satisfy the
				// cutoff; drop it. Without --since the same event
				// would be included. See the test
				// TestFilterEvents_EventWithInvalidTSSkippedUnderSince
				// for the documented policy.
				continue
			}
			if ts.Before(cutoff) {
				continue
			}
		}
		out = append(out, e)
	}
	return out, nil
}
