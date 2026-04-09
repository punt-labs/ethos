//go:build !windows

package mission

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// maxLogSize bounds the total bytes LoadEvents will read from a
// single mission log. 16 MiB is well above realistic operational
// usage — the writer appends small events and a long-running mission
// with ten rounds produces on the order of 40–80 lines — but small
// enough to reject a runaway writer or an attacker planting a
// pathological file before it OOMs the ethos process. The cap is
// defensive, not ergonomic; an operator who legitimately hits it is
// already in post-mortem territory.
const maxLogSize = 16 * 1024 * 1024

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
// field, an empty required field, a non-RFC3339 ts value, or garbage
// bytes on a single line degrades to "that line is missing from the
// output, the rest of the file is still readable."
//
// Missing file → empty slice, nil warnings, nil error. Symmetric
// with LoadResults and LoadReflections: the absence of any event is
// the normal state for a brand-new mission whose Store.Create has
// not yet been called. Empty file (zero bytes) → same shape.
//
// The mission must exist: LoadEvents calls os.Stat on the contract
// file first and returns a "mission not found" error when the
// contract is absent, symmetric with LoadReflections and LoadResults.
// A traversal-laced ID that filepath.Base collapses to a legitimate
// filename is still rejected unless the corresponding contract
// exists. This closes the asymmetry where LoadEvents alone would
// return an empty slice for a bogus ID.
//
// The log file must be smaller than 16 MiB. Larger files return an
// error instead of exhausting memory: missions with huge logs are
// operationally pathological, and the cap bounds the blast radius
// of a runaway writer or a pre-attack forensic read.
//
// Permission-denied, directory-at-log-path, and other I/O failures
// on the log file itself are distinguishable from "missing file":
// the reader returns a typed error, nil events, nil warnings. The
// caller can errors.Is(err, os.ErrPermission) or inspect the wrapped
// chain.
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
	// Reject malformed IDs at the API boundary before any stat/open.
	// Matches Contract.Validate (rule 1) and Store.Create: an ID
	// with control bytes, traversal segments, or any other shape
	// outside m-YYYY-MM-DD-NNN cannot name a legitimate mission, and
	// rejecting here keeps attacker-controlled bytes out of any
	// downstream *fs.PathError string that flows to operator
	// terminals via the CLI's stderr path or the MCP warnings slice.
	if !missionIDPattern.MatchString(missionID) {
		return nil, nil, fmt.Errorf(
			"invalid mission id: must match m-YYYY-MM-DD-NNN")
	}
	// Existence check on the contract file first — symmetric with
	// LoadReflections and LoadResults, which implicitly require the
	// mission to exist because they both rely on Store.Load or a
	// sibling path anchored at a known-good contract. os.Stat is the
	// light-weight option: we do not need to parse or validate the
	// contract, only confirm the mission was ever created.
	if _, err := os.Stat(s.ContractPath(missionID)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("mission %q not found", missionID)
		}
		return nil, nil, fmt.Errorf("loading events for %q: %w", missionID, err)
	}
	// Open, stat, and read the log file through a single fd so there
	// is no TOCTOU window between a path-level stat and a path-level
	// read: a concurrent writer growing the file past the cap would
	// otherwise silently bypass the cap. io.LimitReader enforces the
	// cap at read time regardless of any growth after the stat, and
	// the post-read length check turns the `+1` overflow byte into a
	// distinct error naming the race.
	logPath := s.logPath(missionID)
	f, err := os.Open(logPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []Event{}, nil, nil
		}
		return nil, nil, fmt.Errorf("opening event log for %q: %w", missionID, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat event log for %q: %w", missionID, err)
	}
	if info.IsDir() {
		return nil, nil, fmt.Errorf("event log for %q: path is a directory", missionID)
	}
	if info.Size() > maxLogSize {
		return nil, nil, fmt.Errorf(
			"event log for %q: %d bytes exceeds cap %d",
			missionID, info.Size(), maxLogSize)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxLogSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("reading events for %q: %w", missionID, err)
	}
	if int64(len(data)) > maxLogSize {
		return nil, nil, fmt.Errorf(
			"event log for %q: grew past cap %d during read",
			missionID, maxLogSize)
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
// required fields (ts, event, actor) or a non-RFC3339 ts value
// reject the line with a warning.
//
// Uses bufio.Reader.ReadString rather than bufio.Scanner so a single
// line exceeding any fixed buffer does not silently truncate the
// tail of the log. The whole-file 16 MiB cap (enforced in
// LoadEvents) bounds the memory a pathological line can consume;
// there is no per-line cap. A mid-stream read error is a genuine
// I/O failure and surfaces as the returned error, not as a silent
// partial result.
func decodeEventLog(data []byte) ([]Event, []string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return []Event{}, nil, nil
	}
	events := []Event{}
	var warnings []string
	reader := bufio.NewReader(bytes.NewReader(data))
	lineNo := 0
	for {
		line, readErr := reader.ReadString('\n')
		// ReadString returns whatever it has read even when it also
		// returns io.EOF. Process the final non-terminated line (if
		// any) before honoring the EOF so a file with no trailing
		// newline is still fully walked.
		if len(line) > 0 {
			lineNo++
			// Strip the trailing \n and an optional \r (Windows-written
			// logs) so the downstream decoder sees a clean line body.
			line = strings.TrimRight(line, "\n")
			line = strings.TrimRight(line, "\r")
			if strings.TrimSpace(line) != "" {
				e, err := decodeEventLine([]byte(line))
				if err != nil {
					warnings = append(warnings,
						sanitizeWarning(fmt.Sprintf("line %d: %v", lineNo, err)))
				} else {
					events = append(events, e)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			// Any other read failure is an I/O error, not a decode
			// failure. Surface what we decoded before the failure as
			// a warning with the line number just attempted, then
			// return the error so the caller sees the distinction.
			// The underlying reader in decodeEventLog today is
			// bytes.NewReader which only returns io.EOF, so this path
			// is defensive. Sanitize the error string regardless: a
			// future caller wiring a file-backed reader cannot
			// accidentally forward raw bytes from an OS error string
			// through to operator terminals.
			warnings = append(warnings, sanitizeWarning(
				fmt.Sprintf("line %d: reading: %v", lineNo+1, readErr)))
			return events, warnings, errors.New(sanitizeWarning(
				fmt.Sprintf("reading event log: %v", readErr)))
		}
	}
	return events, warnings, nil
}

// sanitizeWarning replaces control characters in a warning string
// with their escaped hex form so that operator terminals and MCP
// consumers cannot be misled by attacker-controlled bytes in line
// contents or JSON field names. The strict JSON decoder echoes
// unknown top-level field names verbatim into its error message;
// an attacker with local write access to a mission log can plant
// a line like `{..., "\u001b[2J...": 1}` whose decode error string
// contains literal ESC sequences. Without sanitization, those
// bytes forward to terminals via the CLI's stderr path and to MCP
// consumers via the warnings slice, letting an attacker clear the
// screen and paint a spoofed "no corruption detected" message at
// the exact moment the post-mortem operator is looking for damage.
//
// Tab and space are preserved so wrapped error messages still read
// naturally. Every other rune < 0x20 and the DEL + C1 control
// block (U+007F–U+009F) are rendered as \xHH so operators can
// still see what was attempted. Runes above U+009F pass through
// unchanged — warning strings are UTF-8 and we do not want to
// fight with legitimate non-ASCII content.
//
// Rune-level iteration is deliberate: byte-level checking would
// mangle legitimate multi-byte UTF-8 because continuation bytes
// overlap the C1 byte range (ß = 0xc3 0x9f, for instance). But
// naive rune iteration also hides malformed UTF-8 behind the
// replacement rune U+FFFD, which an attacker could exploit to
// smuggle control bytes past the sanitizer. The walker uses
// utf8.DecodeRuneInString and detects RuneError + width 1 — the
// signal that the byte at the cursor is not part of a valid
// UTF-8 sequence — and escapes the raw byte directly. A
// legitimate U+FFFD in input (encoded as 0xef 0xbf 0xbd, width 3)
// passes through unchanged.
func sanitizeWarning(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Invalid UTF-8 byte at this position — escape it
			// directly so an attacker cannot hide a control byte
			// behind a RuneError decode.
			fmt.Fprintf(&b, `\x%02x`, s[i])
			i++
			continue
		}
		switch {
		case r == '\t' || r == ' ':
			b.WriteRune(r)
		case r < 0x20, r >= 0x7f && r <= 0x9f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// decodeEventLine strictly decodes a single JSONL line into an
// Event. Unknown top-level fields are rejected, empty mandatory
// fields (ts, event, actor) produce a typed error so decodeEventLog
// can attach the line number, and a non-RFC3339 ts value is
// rejected at decode time — NOT silently dropped at filter time.
// Rejecting bad timestamps at decode closes the silent
// count-mismatch vector where the same audit trail returned N
// events without --since and N-k events with --since, where k was
// the number of lines with unparseable ts values. The Details map
// is free-form: any shape the writer chose is preserved as-is.
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
	if _, err := time.Parse(time.RFC3339, e.TS); err != nil {
		return Event{}, fmt.Errorf("event ts %q is not RFC3339: %w", e.TS, err)
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
// error with a human-readable hint ("expected RFC3339 (e.g.
// 2026-04-08T12:00:00Z)") rather than leaking the Go time layout
// reference string. Events reaching FilterEvents are guaranteed
// to have an RFC3339 ts — decodeEventLine rejects any line whose
// ts cannot be parsed — so the count agrees between --since and
// no-filter states of the same audit trail. A bad-ts line never
// appears in either count.
func FilterEvents(events []Event, types []string, since string) ([]Event, error) {
	var cutoff time.Time
	hasCutoff := false
	if strings.TrimSpace(since) != "" {
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return nil, fmt.Errorf(
				"invalid since %q: expected RFC3339 (e.g. 2026-04-08T12:00:00Z)",
				since)
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
	for i, e := range events {
		if typeSet != nil {
			if _, ok := typeSet[e.Event]; !ok {
				continue
			}
		}
		if hasCutoff {
			// decodeEventLine guarantees a valid RFC3339 ts on every
			// event that flows through LoadEvents, so a parse failure
			// here means the caller constructed Event values directly
			// and skipped the decoder. Return a loud error rather than
			// silently dropping the row: the silent drop was a
			// tripwire for any future in-process caller.
			ts, err := time.Parse(time.RFC3339, e.TS)
			if err != nil {
				return nil, fmt.Errorf(
					"event %d has unparseable ts %q: %w", i, e.TS, err)
			}
			if ts.Before(cutoff) {
				continue
			}
		}
		out = append(out, e)
	}
	return out, nil
}
