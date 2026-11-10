package hook

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// MigrateAuditWithPolicy consolidates associated legacy sessions in the shared
// local archive, never in a tracked file, in either recording mode. Old global
// files are retained: an older process can still have one open for append, so a
// successful copy is not proof that deleting it is safe. Queries multiset-union
// those sources with the archive; retries do not duplicate legacy losses.
func MigrateAuditWithPolicy(p layout.Policy, globalSessionsDir string, dryRun bool, out io.Writer) error {
	files, err := enumerateLegacyAuditFiles(globalSessionsDir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Fprintln(out, "nothing to migrate")
		return nil
	}
	associated, err := associatedSessions(p)
	if err != nil {
		return err
	}
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	for _, file := range files {
		id := strings.TrimSuffix(filepath.Base(file), ".audit.jsonl")
		if !associated[id] {
			fmt.Fprintf(out, "skip %s: no repo-tree session\n", id)
			continue
		}
		r, err := migrateAuditSession(p, checkout, id, file, dryRun, false)
		if err != nil {
			return fmt.Errorf("migrating session %s (legacy source retained): %w", id, err)
		}
		switch {
		case r.noop:
			fmt.Fprintf(out, "noop %s: already migrated; source retained\n", id)
		case dryRun:
			fmt.Fprintf(out, "migrate %s: %d new entries (dry-run); source retained\n", id, r.added)
		default:
			fmt.Fprintf(out, "migrate %s: %d new entries; source retained\n", id, r.added)
		}
	}
	return nil
}

// AuditImport is one session ImportGlobalAudit moved into the local archive.
type AuditImport struct {
	Session string
	Lines   int // entries added to the archive; 0 when it already held them
}

// AuditImportFailure is a session ImportGlobalAudit left in place. Reason
// names the cause, never a line of the file.
type AuditImportFailure struct {
	Session string
	Reason  string
}

// ImportGlobalAudit imports every global session audit file
// (<globalSessionsDir>/*.audit.jsonl, v3.0-v3.11) whose session this
// repository holds into the shared local archive, redacted, and deletes the
// global file once the archive has been read back equal to what was written.
// It prints nothing and keeps going past a session it cannot import. Sessions
// the repository does not hold stay global.
func ImportGlobalAudit(p layout.Policy, globalSessionsDir string) ([]AuditImport, []AuditImportFailure, error) {
	files, err := enumerateLegacyAuditFiles(globalSessionsDir)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, nil
	}
	associated, err := associatedSessions(p)
	if err != nil {
		return nil, nil, err
	}
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	var done []AuditImport
	var failed []AuditImportFailure
	for _, file := range files {
		id := strings.TrimSuffix(filepath.Base(file), ".audit.jsonl")
		if !associated[id] {
			continue
		}
		r, err := migrateAuditSession(p, checkout, id, file, false, true)
		if err != nil {
			failed = append(failed, AuditImportFailure{Session: id, Reason: err.Error()})
			continue
		}
		if err := removeGlobalAudit(file); err != nil {
			failed = append(failed, AuditImportFailure{Session: id, Reason: err.Error()})
			continue
		}
		done = append(done, AuditImport{Session: id, Lines: r.added})
	}
	return done, failed, nil
}

// associatedSessions is the set of session IDs the repository holds a stream
// for, in any layer: the ownership signal for a global audit file.
func associatedSessions(p layout.Policy) (map[string]bool, error) {
	streams, err := audit.ListStreams(p)
	if err != nil {
		return nil, err
	}
	associated := map[string]bool{}
	for _, s := range streams {
		if s.Namespace == audit.SessionNS {
			associated[s.Session] = true
		}
	}
	return associated, nil
}

// removeGlobalAudit deletes a global audit file and its crash-staged twin.
func removeGlobalAudit(file string) error {
	for _, path := range []string{file, file + stagedSuffix} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing imported global audit file: %w", err)
		}
	}
	return nil
}

type auditSessionResult struct {
	added int  // entries newly written to the archive
	noop  bool // the archive already held exactly this content
}

// migrateAuditSession merges one global session file into the session's local
// archive under the stream lock. quiet suppresses the stderr notice. A write
// is read back and compared before it returns.
func migrateAuditSession(p layout.Policy, checkout, id, file string, dryRun, quiet bool) (auditSessionResult, error) {
	var res auditSessionResult
	s := audit.Stream{Namespace: audit.SessionNS, Session: id}
	err := audit.WithStreamLock(p, s, func() error {
		// Include a crash-staged old global file without creating more
		// staging there. New staging belongs exclusively to S.LocalRoot.
		var input []rawAuditLine
		for _, path := range []string{file, file + stagedSuffix} {
			lines, err := readLegacyAuditLines(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			input = append(input, lines...)
		}
		lines, err := redactLegacyLines(input, checkout)
		if err != nil {
			return err
		}
		dir, err := audit.ArchiveDir(p, s, time.Now().UTC().Format(audit.SessionDateFormat))
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "audit.jsonl")
		data, err := audit.ReadStream(p, s)
		if err != nil {
			return err
		}
		var existing []rawAuditLine
		payloads := map[string]string{}
		guard := sessionLineGuard(checkout)
		for _, l := range data.Legacy {
			// Existing frozen identities cannot be rewritten to make them safe.
			clean, _, _ := guard(l.Raw)
			before, err := audit.CanonicalPayload(l.Raw)
			if err != nil {
				return err
			}
			after, err := audit.CanonicalPayload(clean)
			if err != nil || !bytes.Equal(before, after) {
				return fmt.Errorf("unsafe immutable legacy audit source %s: original retained", l.Source)
			}
			key, err := audit.CheckLegacyIdentity(audit.SessionNS, clean, payloads)
			if err != nil {
				return err
			}
			existing = append(existing, rawAuditLine{key: key, raw: clean})
		}
		counts := map[string]int{}
		var body []byte
		for _, l := range existing {
			counts[l.key]++
			body = append(body, l.raw...)
			body = append(body, '\n')
		}
		added := 0
		var notes []*migrateNote
		for _, l := range lines {
			// Final byte guard covers all added payload fields, including fields
			// the older structured migration pass did not recognize.
			l.raw, _, _ = guard(l.raw)
			l.key, err = audit.CheckLegacyIdentity(audit.SessionNS, l.raw, payloads)
			if err != nil {
				return err
			}
			if counts[l.key] > 0 {
				counts[l.key]--
				continue
			}
			// Legacy eligibility is always private, including a raw input
			// that happens to carry a newer record field.
			body = append(body, l.raw...)
			body = append(body, '\n')
			added++
			notes = append(notes, l.note)
		}
		if dryRun {
			if !quiet {
				noticeMigratedFile(id, notes, added, true)
			}
			res.added = added
			return nil
		}
		prior, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && bytes.Equal(prior, body) {
			res.noop = true
			return nil
		}
		if err := audit.ReplaceLocalFile(p, path, body); err != nil {
			return err
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading back the archive: %w", err)
		}
		if !bytes.Equal(got, body) {
			return fmt.Errorf("the archive differs from what was written")
		}
		if !quiet {
			noticeMigratedFile(id, notes, added, false)
		}
		res.added = added
		return nil
	})
	return res, err
}
