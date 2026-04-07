//go:build !windows

package mission

import (
	"encoding/json"
	"fmt"
	"os"
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

	// The io.Writer contract says a short write (n < len(line))
	// must be accompanied by a non-nil error, but defensive code
	// should not trust implementations to honor the contract —
	// a silently truncated line corrupts the append-only JSONL log.
	n, err := f.Write(line)
	if err != nil {
		return fmt.Errorf("writing event: %w", err)
	}
	if n != len(line) {
		return fmt.Errorf("writing event: short write %d of %d bytes", n, len(line))
	}
	return nil
}
