package auditimport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/lineguard"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/session"
)

// MigrateWithPolicy consolidates associated legacy sessions in the shared
// local archive, never in a tracked file, in either recording mode. Old global
// files are retained: an older process can still have one open for append, so a
// successful copy is not proof that deleting it is safe. Queries multiset-union
// those sources with the archive; retries do not duplicate legacy losses.
func MigrateWithPolicy(p layout.Policy, globalSessionsDir string, dryRun bool, out io.Writer) error {
	files, err := EnumerateLegacyFiles(globalSessionsDir)
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

// AuditImport is one session ImportGlobalAudit copied into the local archive.
// Deleted is whether the global file was removed, which requires the
// session's roster to name this repository and no other; a session
// associated only by a repository stream is imported but kept global.
// NoSignal is a session with no roster checkout and no repository stream:
// nothing names its owner, so the repository that upgrades first takes a
// redacted copy and the global file is kept untouched for any other owner.
type AuditImport struct {
	Session  string
	Lines    int  // entries added to the archive; 0 when it already held them
	Noop     bool // the archive already held exactly this content
	Deleted  bool
	NoSignal bool
}

// AuditImportFailure is a session ImportGlobalAudit left in place. Reason
// names the cause, never a line of the file.
type AuditImportFailure struct {
	Session string
	Reason  string
}

// AuditImportResult is what ImportGlobalAudit did. Unowned lists sessions
// whose global roster names another checkout; they are another repository's
// and are left alone.
type AuditImportResult struct {
	Done    []AuditImport
	Failed  []AuditImportFailure
	Unowned []string
	// Remaining is the number of files not attempted because the context was
	// done; the next run takes them.
	Remaining int
}

// ReasonNoAuditOwner is the residual reason for a global audit file nothing
// associates with a repository: imported here, kept global.
const ReasonNoAuditOwner = "no owner signal; global copy kept"

// ImportGlobalAudit imports every global session audit file
// (<globalSessionsDir>/*.audit.jsonl, v3.0-v3.11) that may belong to this
// repository into the shared local archive, redacted. A session's global
// roster (<globalSessionsDir>/<id>.yaml) naming one of roots as its checkout
// is the exclusive signal: the global file is deleted once the archive has
// been read back equal to what was written. A repository stream for the
// session, or no signal at all (every roster before v4.13.0 has no checkout
// and the audit lines carry no repository), imports a copy and keeps the
// global file untouched: the import is lossless and a later run finds the
// copy. Only a roster naming another checkout is skipped. It prints nothing
// and keeps going past a session it cannot import. When ctx is done it
// returns after the file in progress and counts the rest in Remaining.
func ImportGlobalAudit(ctx context.Context, p layout.Policy, globalSessionsDir string, roots ...string) (AuditImportResult, error) {
	var res AuditImportResult
	files, err := EnumerateLegacyFiles(globalSessionsDir)
	if err != nil {
		return res, err
	}
	if len(files) == 0 {
		return res, nil
	}
	streams, err := associatedSessions(p)
	if err != nil {
		return res, err
	}
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	for i, file := range files {
		if ctx.Err() != nil {
			res.Remaining = len(files) - i
			break
		}
		id := strings.TrimSuffix(filepath.Base(file), ".audit.jsonl")
		owner, named := rosterCheckout(globalSessionsDir, id)
		if named && !namesAny(owner, roots) {
			res.Unowned = append(res.Unowned, id)
			continue
		}
		r, err := migrateAuditSession(p, checkout, id, file, false, true)
		if err != nil {
			res.Failed = append(res.Failed, AuditImportFailure{Session: id, Reason: err.Error()})
			continue
		}
		imp := AuditImport{Session: id, Lines: r.added, Noop: r.noop, NoSignal: !named && !streams[id]}
		// The import is the redacted archive of this file; a session another
		// checkout owns never reaches here (Unowned above). Pre-redaction
		// bytes are not kept once archived, whether or not a roster names
		// this checkout: an unnamed session (no roster at all, as v3.11
		// wrote) is still this repository's to retire.
		if err := removeGlobalAudit(file); err != nil {
			res.Failed = append(res.Failed, AuditImportFailure{Session: id, Reason: err.Error()})
			continue
		}
		imp.Deleted = true
		res.Done = append(res.Done, imp)
	}
	return res, nil
}

// rosterCheckout reads the checkout the global roster of session id names.
// named is false when there is no roster or it carries no checkout.
func rosterCheckout(globalSessionsDir, id string) (checkout string, named bool) {
	roster, err := session.NewStore(filepath.Dir(globalSessionsDir)).Load(id)
	if err != nil || roster.Checkout == "" {
		return "", false
	}
	return filepath.Clean(roster.Checkout), true
}

// namesAny reports whether checkout is one of roots under the path rule.
func namesAny(checkout string, roots []string) bool {
	for _, root := range roots {
		if root != "" && mission.SamePath(checkout, filepath.Clean(root)) {
			return true
		}
	}
	return false
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
	for _, path := range []string{file, file + StagedSuffix} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing imported global audit file: %w", err)
		}
	}
	return nil
}

// sessionGuard builds the guard migrateAuditSession applies; a test replaces it.
var sessionGuard = lineguard.SessionLineGuardFor

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
		var input []Line
		for _, path := range []string{file, file + StagedSuffix} {
			lines, err := ReadLegacyLines(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			input = append(input, lines...)
		}
		lines, err := RedactLegacyLines(input, checkout)
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
		var existing []Line
		payloads := map[string]string{}
		guard := sessionGuard(checkout)
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
			existing = append(existing, Line{Key: key, Raw: clean})
		}
		counts := map[string]int{}
		var body []byte
		for _, l := range existing {
			counts[l.Key]++
			body = append(body, l.Raw...)
			body = append(body, '\n')
		}
		added := 0
		var notes []*Note
		for _, l := range lines {
			// Final byte guard covers all added payload fields, including fields
			// the older structured migration pass did not recognize.
			l.Raw, _, _ = guard(l.Raw)
			l.Key, err = audit.CheckLegacyIdentity(audit.SessionNS, l.Raw, payloads)
			if err != nil {
				return err
			}
			if counts[l.Key] > 0 {
				counts[l.Key]--
				continue
			}
			// Legacy eligibility is always private, including a raw input
			// that happens to carry a newer record field.
			body = append(body, l.Raw...)
			body = append(body, '\n')
			added++
			notes = append(notes, l.Note)
		}
		if dryRun {
			if !quiet {
				NoticeMigratedFile(id, notes, added, true)
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
			NoticeMigratedFile(id, notes, added, false)
		}
		res.added = added
		return nil
	})
	return res, err
}
