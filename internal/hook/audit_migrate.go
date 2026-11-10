package hook

import (
	"fmt"
	"io"
	"os"
)

// MigrateAudit copies associated legacy global files into the shared local
// archive. Sources remain in place for late legacy writers and explicit operator
// cleanup; no migration implicitly deletes history or grants publication consent.
func MigrateAudit(globalSessionsDir, repoRoot string, dryRun bool, out io.Writer) error {
	p, err := auditPolicy(repoRoot)
	if err != nil {
		return err
	}
	return MigrateAuditWithPolicy(p, globalSessionsDir, dryRun, out)
}

// appendRawAuditLines appends each JSONL line (newline-terminated) to path and
// fsyncs once, and returns the guard's notices for the caller to print. Used
// by migration. Each line is written in its canonical
// guarded form (guardAuditLineNote), not byte for byte: it is decoded into a
// generic object and re-marshalled, so unknown fields survive as values while
// the input's own syntax does not.
func appendRawAuditLines(path string, lines [][]byte) ([]string, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	// A torn fragment at the destination tail must not fuse onto the first
	// migrated line (R3-2) — separate it with a newline first.
	if err := ensureNewlineBoundary(f); err != nil {
		return nil, fmt.Errorf("checking tail of %s: %w", path, err)
	}
	var notes []string
	for _, l := range lines {
		// Write the line and its terminator in a single call so a crash cannot
		// land a complete JSON object with no trailing newline. Such a tail
		// reads as a torn fragment (skipped) and a re-run's newline boundary
		// plus re-append would duplicate it. One Write keeps line-and-newline
		// atomic against interruption (R3-1).
		g, note := guardAuditLineNote(l)
		notes = append(notes, note)
		line := append(append([]byte(nil), g...), '\n')
		if _, err := f.Write(line); err != nil {
			return nil, fmt.Errorf("writing %s: %w", path, err)
		}
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("syncing %s: %w", path, err)
	}
	return notes, nil
}
