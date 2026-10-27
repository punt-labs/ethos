package hook

import (
	"fmt"
	"os"
	"time"
)

// appendLiveAudit is appendLiveAuditReport without the guard's report.
func appendLiveAudit(livePath, sealedDir, legacyPath string, entry auditEntry, now time.Time) (auditEntry, error) {
	e, _, err := appendLiveAuditReport(livePath, sealedDir, legacyPath, entry, now)
	return e, err
}

// writeAuditEntry is writeAuditEntryReport without the guard's report.
func writeAuditEntry(path string, entry auditEntry) error {
	_, err := writeAuditEntryReport(path, entry)
	return err
}

// guardAuditLine is guardAuditLineNote printing its notice at once.
func guardAuditLine(line []byte) []byte {
	out, note := guardAuditLineNote(line)
	if note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	return out
}

// sayMigrateNotes prints the migration notice of a file's lines, as
// migrateOneSession does for the lines it appends.
func sayMigrateNotes(lines []rawAuditLine) {
	var notes []*migrateNote
	for _, l := range lines {
		notes = append(notes, l.note)
	}
	noticeMigratedFile("file", notes, len(lines), false)
}
