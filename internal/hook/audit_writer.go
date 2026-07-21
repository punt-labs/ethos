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
// trailing line (which the reader skips with a warning). This helper
// only marshals, appends, fsyncs, and returns. The advisory policy
// (log a warning to stderr, emit the sentinel, return nil so the
// tool call is not blocked) lives in HandleAuditLog — the helper is
// pure error-bubbling.
func writeAuditEntry(path string, entry auditEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling entry: %w", err)
	}
	line := append(data, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	if err := ensureNewlineBoundary(f); err != nil {
		return fmt.Errorf("checking tail of %s: %w", path, err)
	}
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

// ensureNewlineBoundary writes a '\n' to f when its current tail byte is not a
// newline — a torn fragment left by a crashed writer. Without it an O_APPEND
// write glues the new line onto the fragment, fusing debris and a good line
// into one undecodable line (and, if the good line is a sentinel, destroying
// the loss marker). The separator instead leaves the fragment as its own line,
// which the tolerant reader skips with a warning. The fd must be opened
// O_RDWR so ReadAt can inspect the tail; O_APPEND still forces writes to the
// end. A newly-created or empty file needs no separator.
func ensureNewlineBoundary(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return nil
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, info.Size()-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	if _, err := f.Write([]byte{'\n'}); err != nil {
		return err
	}
	return nil
}
