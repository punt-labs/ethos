package hook

import (
	"encoding/json"
	"fmt"
	"os"
)

// writeAuditEntry appends one JSONL line to path, fsyncs the file,
// and closes the fd. Every line is a complete JSON value followed by
// a single newline; readers tolerate a partial trailing line (no
// terminator) when a writer crashed mid-write.
//
// The fsync is per-line. Audit logs are post-mortem evidence — a
// power loss between the write and the fsync would otherwise lose the
// most recent entries, exactly the ones an operator needs after a
// crash. DES-054 I10-audit-atomic names this as the single-store
// append-atomicity property.
//
// Atomicity: a successful return guarantees the entry is on disk; an
// error return leaves the file in its pre-write state up to a partial
// trailing line (which the reader skips with a warning). Audit
// logging is advisory — every error path here writes a warning to
// stderr and returns nil from the caller so a logging failure cannot
// block the tool call being audited.
func writeAuditEntry(path string, entry auditEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling entry: %w", err)
	}
	line := append(data, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// Per-line fsync — see the function comment for why. Sync failure
	// is propagated because the entry is not durable on disk; the
	// caller decides whether to retry or surface to the operator.
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", path, err)
	}
	return nil
}
