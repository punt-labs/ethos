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
	streams, err := audit.ListStreams(p)
	if err != nil {
		return err
	}
	associated := map[string]bool{}
	for _, s := range streams {
		if s.Namespace == audit.SessionNS {
			associated[s.Session] = true
		}
	}
	checkout := filepath.Dir(filepath.Dir(p.CheckoutRecordRoot()))
	for _, file := range files {
		id := strings.TrimSuffix(filepath.Base(file), ".audit.jsonl")
		if !associated[id] {
			fmt.Fprintf(out, "skip %s: no repo-tree session\n", id)
			continue
		}
		s := audit.Stream{Namespace: audit.SessionNS, Session: id}
		if err := audit.WithStreamLock(p, s, func() error {
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
				noticeMigratedFile(id, notes, added, true)
				fmt.Fprintf(out, "migrate %s: %d new entries (dry-run); source retained\n", id, added)
				return nil
			}
			prior, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if err == nil && bytes.Equal(prior, body) {
				fmt.Fprintf(out, "noop %s: already migrated; source retained\n", id)
				return nil
			}
			if err := audit.ReplaceLocalFile(p, path, body); err != nil {
				return err
			}
			noticeMigratedFile(id, notes, added, false)
			fmt.Fprintf(out, "migrate %s: %d new entries; source retained\n", id, added)
			return nil
		}); err != nil {
			return fmt.Errorf("migrating session %s (legacy source retained): %w", id, err)
		}
	}
	return nil
}
