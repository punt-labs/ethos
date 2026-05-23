package hook

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MigrateAudit copies legacy global audit JSONL files (the
// <globalSessionsDir>/<id>.audit.jsonl shape used by v3.11) into the
// DES-054 v5 repo-tree layout
// (<repoRoot>/.punt-labs/ethos/sessions/<date>-<id>/audit.jsonl). Each legacy
// file whose session id matches a repo-tree session directory is
// merged in; entries already present (matched on tool_use_id + ts +
// tool_name) are not re-written. Legacy files with no matching
// repo-tree session are left alone — cross-repo policy.
//
// dryRun=true enumerates what would change without writing or
// deleting. The function is idempotent: running twice produces the
// same on-disk state as running once. On any error mid-merge, both
// sources stay intact (the legacy file is deleted only after a
// successful append of all new entries to the repo-tree file).
//
// out receives one human-readable line per session decision:
//
//	migrate sess-abc -> .punt-labs/ethos/sessions/2026-05-23-sess-abc (N new entries)
//	skip sess-xyz: no repo-tree session
//	skip sess-ro: read-only
//	noop sess-dup: already migrated
//
// A successful run with no legacy files prints "nothing to migrate".
func MigrateAudit(globalSessionsDir, repoRoot string, dryRun bool, out io.Writer) error {
	if repoRoot == "" {
		return fmt.Errorf("migrate audit: repoRoot is empty")
	}
	legacyFiles, err := enumerateLegacyAuditFiles(globalSessionsDir)
	if err != nil {
		return fmt.Errorf("enumerating legacy audit files in %s: %w", globalSessionsDir, err)
	}
	if len(legacyFiles) == 0 {
		fmt.Fprintln(out, "nothing to migrate")
		return nil
	}

	sessionsBase := filepath.Join(repoRoot, ".punt-labs", "ethos", "sessions")
	var failures []string
	for _, lf := range legacyFiles {
		sessionID := strings.TrimSuffix(filepath.Base(lf), ".audit.jsonl")
		if sessionID == "" {
			continue
		}
		decision, err := migrateOneSession(lf, sessionsBase, sessionID, dryRun)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"ethos: audit migrate: %s: %v\n", sessionID, err)
			failures = append(failures, sessionID)
			continue
		}
		fmt.Fprintln(out, decision)
	}
	if len(failures) > 0 {
		return fmt.Errorf("audit migrate: %d session(s) failed: %s",
			len(failures), strings.Join(failures, ", "))
	}
	return nil
}

// enumerateLegacyAuditFiles returns the absolute paths of every
// <globalSessionsDir>/*.audit.jsonl file. A missing or read-only
// directory yields an empty list with no error so the migrate command
// is no-op on a clean install and fail-soft on a permission failure.
func enumerateLegacyAuditFiles(globalSessionsDir string) ([]string, error) {
	entries, err := os.ReadDir(globalSessionsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if errors.Is(err, fs.ErrPermission) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", globalSessionsDir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".audit.jsonl") {
			continue
		}
		out = append(out, filepath.Join(globalSessionsDir, name))
	}
	return out, nil
}

// migrateOneSession migrates a single legacy audit file. Returns a
// short status line describing the decision: skip (no repo-tree
// session), noop (already migrated), skip (read-only legacy file),
// or migrate (N new entries copied). On any I/O error the legacy
// file is preserved.
func migrateOneSession(legacyPath, sessionsBase, sessionID string, dryRun bool) (string, error) {
	repoDir, err := findSessionDir(sessionsBase, sessionID)
	if err != nil {
		return "", fmt.Errorf("looking up repo session dir: %w", err)
	}
	if repoDir == "" {
		// Cross-repo policy: session belongs to a different repo or
		// was created outside any repo. Leave the legacy file alone.
		return fmt.Sprintf("skip %s: no repo-tree session", sessionID), nil
	}

	legacyEntries, err := readAuditEntries(legacyPath)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Sprintf("skip %s: read-only", sessionID), nil
		}
		return "", fmt.Errorf("reading legacy %s: %w", legacyPath, err)
	}

	repoPath := filepath.Join(repoDir, "audit.jsonl")
	repoEntries, err := readAuditEntries(repoPath)
	if err != nil {
		return "", fmt.Errorf("reading repo %s: %w", repoPath, err)
	}

	newEntries := diffAuditEntries(legacyEntries, repoEntries)
	if len(newEntries) == 0 {
		if dryRun {
			return fmt.Sprintf("noop %s: already migrated (dry-run)", sessionID), nil
		}
		// Already merged in a prior run. The legacy file is
		// redundant and safe to delete.
		if err := os.Remove(legacyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("removing legacy %s: %w", legacyPath, err)
		}
		return fmt.Sprintf("noop %s: already migrated", sessionID), nil
	}

	rel, relErr := filepath.Rel(filepath.Dir(filepath.Dir(sessionsBase)), repoDir)
	if relErr != nil {
		rel = repoDir
	}

	if dryRun {
		return fmt.Sprintf("migrate %s -> %s (%d new entries, dry-run)", sessionID, rel, len(newEntries)), nil
	}

	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", repoDir, err)
	}
	for _, e := range newEntries {
		if writeErr := writeAuditEntry(repoPath, e); writeErr != nil {
			return "", fmt.Errorf("appending to %s: %w", repoPath, writeErr)
		}
	}
	// Only delete the legacy file after every entry has been
	// successfully appended and fsynced. If we crashed during the
	// loop above, a re-run finds the legacy file still in place and
	// the dedupe in diffAuditEntries skips entries that already
	// landed.
	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("removing legacy %s: %w", legacyPath, err)
	}
	return fmt.Sprintf("migrate %s -> %s (%d new entries)", sessionID, rel, len(newEntries)), nil
}

// diffAuditEntries returns the entries from src that are not already
// present in dst. Two entries match when their ts, tool name, and
// tool_input_hash are equal. The hash is the canonical-JSON sha256 of
// tool_input (see hashToolInput) so logically identical inputs match
// regardless of map iteration order.
//
// Entries with no hash (older v3.11.0 lines that pre-date the field)
// fall back to ts + tool + tool_input_preview for the match key. This
// is best-effort — two truly distinct entries with the same ts to the
// second AND the same tool AND the same 200-char preview are treated
// as duplicates. The fallback is acceptable because the practical
// resolution of a sha256 hash is reached only on v3.12+ logs, and the
// migration is one-shot per machine.
func diffAuditEntries(src, dst []auditEntry) []auditEntry {
	seen := make(map[string]struct{}, len(dst))
	for _, e := range dst {
		seen[auditEntryKey(e)] = struct{}{}
	}
	var out []auditEntry
	for _, e := range src {
		k := auditEntryKey(e)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, e)
	}
	return out
}

// auditEntryKey returns the dedupe key for one audit entry. Prefers
// the sha256 hash when present; falls back to the preview field for
// older lines.
func auditEntryKey(e auditEntry) string {
	if e.ToolInputHash != "" {
		return e.Ts + "|" + e.Tool + "|" + e.ToolInputHash
	}
	return e.Ts + "|" + e.Tool + "|preview:" + e.ToolInputPreview
}
