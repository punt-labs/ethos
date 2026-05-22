package hook

import (
	"encoding/json"
	"fmt"
	"os"
)

// auditSentinel is the minimal JSONL line emitted when the audit
// pipeline cannot persist a full entry. The shape is deliberately
// narrow — three fields only — so a permission, ENOSPC, or fsync
// failure that defeated the regular write has the best chance of
// admitting the sentinel write. Readers (`ethos audit show`, vox)
// see it through the same permissive decoder as a normal entry: the
// audit_error key falls into auditEntry's Unmarshal as an unknown
// field and is preserved at the JSONL line level.
//
// The struct exists separately from auditEntry because the sentinel
// is not a tool invocation. Carrying the auditEntry struct here would
// either inject empty tool/tool_input fields (noisy in audit show) or
// require omitempty on every field of auditEntry — a wider change
// than the sentinel warrants.
type auditSentinel struct {
	Ts         string `json:"ts"`
	Session    string `json:"session"`
	AuditError string `json:"audit_error"`
}

// emitAuditSentinel appends a sentinel JSONL line to path so a later
// `ethos audit show` reveals that an entry was lost. Returns the
// sentinel's own write error so the caller can fall back to stderr
// when even the sentinel cannot land. Reason is the operator-facing
// description of what defeated the original entry write.
//
// Defensive shape: a single canonical-JSON line plus newline, opened
// O_APPEND, written in one call, then fsynced and closed. Same
// atomicity contract as writeAuditEntry, but a much smaller payload
// — if the directory is writable at all, this line gets through.
func emitAuditSentinel(path, sessionID, ts, reason string) error {
	s := auditSentinel{
		Ts:         ts,
		Session:    sessionID,
		AuditError: reason,
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshaling sentinel: %w", err)
	}
	line := append(data, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("writing sentinel to %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing sentinel %s: %w", path, err)
	}
	return nil
}
