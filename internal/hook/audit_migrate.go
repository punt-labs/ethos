package hook

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/redact"
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
// same on-disk state as running once. A real run renames the legacy file to
// its .ethos-tmp name first, so on an error mid-merge the legacy content is
// under that name in the same directory and the next run picks it up; the
// staged file is deleted only after a successful append of all new entries to
// the repo-tree file.
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

// stagedSuffix names the file a migration works from: the legacy file renamed
// in place, in the same directory (a machine-local class 4 temp). A writer that
// appends meanwhile creates a fresh legacy file, which the next run picks up.
// An appender that already holds the old file open can still lose a line to
// the deletion, as it could before staging; removeStaged narrows that window by
// keeping a file that grew since it was read.
const stagedSuffix = ".ethos-tmp"

// stageLegacy renames legacyPath to its staging name and returns that name. A
// staging file left by an earlier run is taken as it is, and the legacy file, if
// any, waits for the next run. It returns "" when there is nothing to migrate.
func stageLegacy(legacyPath string) (string, error) {
	staged := legacyPath + stagedSuffix
	if _, err := os.Stat(staged); err == nil {
		return staged, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("stat %s: %w", staged, err)
	}
	if err := os.Rename(legacyPath, staged); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("staging %s: %w", legacyPath, err)
	}
	return staged, nil
}

// unstageLegacy puts a staged file back under its legacy name when no new
// legacy file has appeared; otherwise the staged file stays for the next run.
func unstageLegacy(staged, legacyPath string) {
	if _, err := os.Stat(legacyPath); errors.Is(err, fs.ErrNotExist) {
		_ = os.Rename(staged, legacyPath)
	}
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
		// A staged leftover (a run that crashed or found late appends) is
		// migrated under its legacy name, which stageLegacy resolves.
		name := strings.TrimSuffix(e.Name(), stagedSuffix)
		path := filepath.Join(globalSessionsDir, name)
		if !strings.HasSuffix(name, ".audit.jsonl") || slices.Contains(out, path) {
			continue
		}
		out = append(out, path)
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

	// Migrate RAW lines, not decoded entries: re-marshaling through auditEntry
	// strips any field the struct does not name — notably the audit_error
	// sentinel key — silently destroying a loss marker. Copying the exact bytes
	// preserves every unknown field for every line, matching the permissive-
	// decode compatibility posture (docs/audit-seal.md §Migration).
	//
	// A real run first renames the legacy file to its staging name and works
	// from that, so an append that opens the legacy path afterwards creates a
	// fresh file instead of being deleted with this one. An appender that
	// already holds the old file open can still write into the staged file; a
	// line landing after removeStaged's size check is lost, as it was before.
	origPath := legacyPath
	var stagedSize int64
	if !dryRun {
		staged, err := stageLegacy(legacyPath)
		if err != nil {
			return "", err
		}
		if staged == "" {
			return fmt.Sprintf("noop %s: nothing to migrate", sessionID), nil
		}
		legacyPath = staged
	} else if _, err := os.Stat(legacyPath + stagedSuffix); err == nil {
		legacyPath += stagedSuffix
	}
	if fi, err := os.Stat(legacyPath); err == nil {
		stagedSize = fi.Size()
	}
	legacyLines, err := readLegacyAuditLines(legacyPath)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			if legacyPath != origPath && !dryRun {
				unstageLegacy(legacyPath, origPath)
			}
			return fmt.Sprintf("skip %s: read-only", sessionID), nil
		}
		return "", fmt.Errorf("reading legacy %s: %w", legacyPath, err)
	}

	// Legacy lines predate the redaction policy, and repoDir is tracked.
	// Redact them before the dedupe below so its keys are computed over the
	// form that lands on disk; a re-run then finds its own earlier output.
	// A redactor failure refuses the migration and leaves the legacy content
	// under its .ethos-tmp name (queryLegacyAudit reads it there): unlike the
	// audit hook, this is an operator command and may refuse.
	legacyLines, err = redactLegacyLines(legacyLines, filepath.Dir(filepath.Dir(filepath.Dir(sessionsBase))))
	if err != nil {
		return "", fmt.Errorf("redacting legacy %s: %w", legacyPath, err)
	}

	repoPath := filepath.Join(repoDir, "audit.jsonl")
	repoLines, err := readRawAuditLines(repoPath)
	if err != nil {
		return "", fmt.Errorf("reading repo %s: %w", repoPath, err)
	}

	// Dedupe as a MULTISET on rawAuditLineKey. Both sides read raw so the key
	// scheme matches: a hashed entry keys on ts+tool+hash; a hashless line
	// (e.g. a sentinel, which is ts+empty+empty and so degenerate on ts alone)
	// keys on a hash of its raw bytes. Byte-identical legacy lines legitimately
	// recur: an ENOSPC burst emits one same-second, same-text sentinel per
	// failed write (emitLegacySentinel), so N identical loss markers must
	// migrate as N, not collapse to one. Count each key on the repo side and
	// decrement per legacy match — a key already present M times absorbs the
	// first M legacy copies and appends the rest. This keeps crash-re-run
	// idempotency: a run interrupted mid-append tops the repo up to exactly the
	// legacy count; a completed run appends zero and deletes.
	repoCounts := make(map[string]int, len(repoLines))
	for _, rl := range repoLines {
		repoCounts[rl.key]++
	}
	var newLines [][]byte
	var notes []*migrateNote
	for _, rl := range legacyLines {
		if repoCounts[rl.key] > 0 {
			repoCounts[rl.key]--
			continue
		}
		newLines = append(newLines, rl.raw)
		notes = append(notes, rl.note)
	}

	if len(newLines) == 0 {
		if dryRun {
			return fmt.Sprintf("noop %s: already migrated (dry-run)", sessionID), nil
		}
		// Already merged in a prior run. The legacy file is
		// redundant and safe to delete.
		if _, err := removeStaged(legacyPath, stagedSize); err != nil {
			return "", err
		}
		return fmt.Sprintf("noop %s: already migrated", sessionID), nil
	}

	rel, relErr := filepath.Rel(filepath.Dir(filepath.Dir(sessionsBase)), repoDir)
	if relErr != nil {
		rel = repoDir
	}

	if dryRun {
		noticeMigratedFile(sessionID, notes, len(newLines), true)
		return fmt.Sprintf("migrate %s -> %s (%d new entries, dry-run)", sessionID, rel, len(newLines)), nil
	}

	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", repoDir, err)
	}
	guardNotes, writeErr := appendRawAuditLines(repoPath, newLines)
	if writeErr != nil {
		return "", fmt.Errorf("appending to %s: %w", repoPath, writeErr)
	}
	// Only lines that were appended are reported.
	noticeMigratedFile(sessionID, notes, len(newLines), false)
	printNotices(guardNotes)
	// Only delete the staged file after every line has been successfully
	// appended and fsynced. If we crashed during the append, a re-run finds the
	// staged .ethos-tmp file still in place, picks it up, and the dedupe above
	// skips lines that already landed.
	left, err := removeStaged(legacyPath, stagedSize)
	if err != nil {
		return "", err
	}
	if left {
		return fmt.Sprintf("migrate %s -> %s (%d new entries; late appends left staged for the next run)", sessionID, rel, len(newLines)), nil
	}
	return fmt.Sprintf("migrate %s -> %s (%d new entries)", sessionID, rel, len(newLines)), nil
}

// removeStaged deletes the staged file once its lines are written, unless it
// grew after it was read (an appender that had it open when it was renamed):
// then it is left for the next run, whose dedupe skips what already landed. It
// reports whether it left the file.
func removeStaged(path string, readSize int64) (bool, error) {
	if fi, err := os.Stat(path); err == nil && fi.Size() != readSize {
		return true, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("removing legacy %s: %w", path, err)
	}
	return false, nil
}

// redactLegacyLines applies the audit path's redaction to legacy lines and
// recomputes each dedupe key. Every line is written in its canonical form, the
// decoded line re-marshalled by guardAuditLine, never its original bytes;
// unknown fields and sentinels survive as decoded values.
func redactLegacyLines(lines []rawAuditLine, repoRoot string) ([]rawAuditLine, error) {
	r, err := mission.NewPathRedactor(repoRoot)
	if err != nil {
		return nil, err
	}
	out := make([]rawAuditLine, len(lines))
	for i, l := range lines {
		// Invalid text and a duplicate key are looked for in the original
		// bytes: once the line is decoded the surrogate is U+FFFD and the
		// first value is gone. why collects what was done, for the one
		// notice the line gets.
		src := l.raw
		var kinds []string
		add := func(k string) {
			if !slices.Contains(kinds, k) {
				kinds = append(kinds, k)
			}
		}
		if mission.InvalidText(l.raw) != "" {
			src, _, _ = guardAuditLineFull(l.raw)
			add(kindMarker)
		} else if _, _, dup := redact.DuplicateJSONKey(l.raw); dup {
			// The last value is kept and the path pass still runs on it, so
			// this machine's home becomes ~/x as on any other line.
			var ok bool
			if src, ok = dedupeLine(l.raw); ok {
				add(kindDuplicate)
			} else {
				src, _, _ = guardAuditLineFull(l.raw)
				add(kindMarker)
			}
		}
		n := l.n
		if n == 0 {
			n = i + 1
		}
		var raw []byte
		var changed bool
		if isJSONObject(src) {
			var err error
			raw, changed, err = redactLegacyLine(src, repoRoot, r)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
			if changed {
				add(kindContent)
			}
		} else {
			// Not an object at all (junk, a torn fragment): the guard makes it a
			// loss marker, so the line is kept as a record of the loss rather
			// than dropped with the legacy file.
			raw, _, _ = guardAuditLineFull(src)
			add(kindMarker)
			changed = true
		}
		if !bytes.Equal(src, l.raw) {
			changed = true
		}
		// The line-level check is the sink guard every appender runs: it covers
		// what redactLegacyLine does not look at (unknown fields, a tool_input
		// that is not a map). A line only re-encoded in canonical form holds the
		// same values and is not counted.
		if g, rule, m := guardAuditLineFull(raw); !bytes.Equal(g, raw) {
			raw, changed = g, true
			switch {
			case m:
				add(kindMarker)
			case rule != "":
				add(kindReencoded)
			}
		}
		var note *migrateNote
		if len(kinds) > 0 {
			note = &migrateNote{n: n, kinds: kinds}
		}
		if !changed {
			out[i] = l
			continue
		}
		e, err := decodeAuditLine(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out[i] = rawAuditLine{key: rawAuditLineKey(e, raw), raw: raw, n: n, note: note}
	}
	return out, nil
}

// The kinds of rewrite migration counts, in the order a notice lists them.
const (
	kindContent   = "home path or content"
	kindDuplicate = "duplicate key"
	kindReencoded = "re-encoded"
	kindMarker    = "loss marker"
)

var migrateKinds = []string{kindContent, kindDuplicate, kindReencoded, kindMarker}

// maxNoticeLines is how many line numbers a migration notice lists per kind.
const maxNoticeLines = 10

// noticeMigratedFile reports, once for a migrated file (one write), how many of
// its total lines migration rewrote, or in a dry run would rewrite, by kind,
// with the first ten line numbers of each kind. It names no value. A file with
// no rewritten line prints nothing.
func noticeMigratedFile(session string, notes []*migrateNote, total int, dry bool) {
	byKind := map[string][]int{}
	count := 0
	for _, n := range notes {
		if n == nil {
			continue
		}
		count++
		for _, k := range n.kinds {
			byKind[k] = append(byKind[k], n.n)
		}
	}
	if count == 0 {
		return
	}
	var parts []string
	for _, k := range migrateKinds {
		nums := byKind[k]
		if len(nums) == 0 {
			continue
		}
		var list []string
		for i, n := range nums {
			if i == maxNoticeLines {
				break
			}
			list = append(list, strconv.Itoa(n))
		}
		label := "line"
		if len(list) > 1 {
			label = "lines"
		}
		more := ""
		if len(nums) > maxNoticeLines {
			more = fmt.Sprintf(" and %d more", len(nums)-maxNoticeLines)
		}
		parts = append(parts, fmt.Sprintf("%s %d (%s %s%s)", k, len(nums), label, strings.Join(list, ", "), more))
	}
	verb := "rewritten before they were stored"
	lead := ""
	if dry {
		lead, verb = "would rewrite", "before they are stored"
		fmt.Fprintf(os.Stderr, "ethos: audit migrate: %s: %s %d of %d line(s) %s: %s\n",
			quoteUnlessLeak(session), lead, count, total, verb, strings.Join(parts, "; "))
		return
	}
	fmt.Fprintf(os.Stderr, "ethos: audit migrate: %s: %d of %d line(s) %s: %s\n",
		quoteUnlessLeak(session), count, total, verb, strings.Join(parts, "; "))
}

// isJSONObject reports whether line is exactly one JSON object.
func isJSONObject(line []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(line))
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return false
	}
	_, err := dec.Token()
	return errors.Is(err, io.EOF)
}

// dedupeLine returns line decoded and re-encoded, so a duplicate key keeps its
// last value, with redacted set. It reports false when the line is not a JSON
// object.
func dedupeLine(line []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false
	}
	m["redacted"] = true
	out, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return out, true
}

// redactLegacyLine redacts one legacy audit line. tool_input goes through
// redactToolInput, exactly as a fresh line does, and tool_input_hash and
// tool_input_preview are recomputed over the stored form when it changed
// (DES-054). Free-text fields that carry no tool_input (a preview-only
// line, a sentinel's audit_error) are redacted as text. It never sets
// path_format: a ~ already in a legacy line may be another user's home.
func redactLegacyLine(raw []byte, repoRoot string, r mission.PathRedactor) ([]byte, bool, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, false, fmt.Errorf("decoding: %w", err)
	}
	changed := false
	tool, _ := m["tool"].(string)
	if before, ok := m["tool_input"].(map[string]any); ok {
		// Compare the stored form, not redactToolInput's bool: that bool
		// reports content removed, a leak replaced or a key renamed, and
		// ignores a value the path pass rewrote in place.
		red, removed, _ := redactToolInputQuiet(map[string]any{"tool_input": before}, tool, repoRoot)
		wasJSON, err := json.Marshal(before)
		if err != nil {
			return nil, false, fmt.Errorf("encoding tool_input: %w", err)
		}
		isJSON, err := json.Marshal(red)
		if err != nil {
			return nil, false, fmt.Errorf("encoding redacted tool_input: %w", err)
		}
		if !bytes.Equal(wasJSON, isJSON) {
			m["tool_input"] = red
			env := map[string]any{"tool_input": red}
			if _, ok := m["tool_input_hash"]; ok {
				m["tool_input_hash"] = hashToolInput(env)
			}
			if _, ok := m["tool_input_preview"]; ok {
				m["tool_input_preview"] = toolInputPreview(env)
			}
			if removed {
				m["redacted"] = true
			}
			changed = true
		}
	}
	// A preview is recomputed when tool_input changed, so it is swept only
	// when tool_input did not. audit_error is free text beside tool_input,
	// not derived from it, and is swept either way.
	textKeys := []string{"audit_error"}
	if !changed {
		textKeys = append(textKeys, "tool_input_preview")
	}
	textChanged := false
	for _, k := range textKeys {
		if s, ok := m[k].(string); ok {
			if red := r.Content(s); red != s {
				m[k], textChanged = red, true
			}
		}
	}
	if textChanged {
		m["redacted"] = true
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, false, fmt.Errorf("encoding: %w", err)
	}
	return out, true, nil
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

// rawAuditLineKey returns the dedupe key for one audit line, computed from both
// its decoded entry and its raw bytes. A hashed entry keys on ts + tool +
// tool_input_hash, so two byte-different encodings of the same logical
// tool_input dedupe. A hashless line (older v3.11.0 entries, and sentinels,
// whose ts + empty tool + empty preview is degenerate — a burst of same-second
// sentinels would collapse to one) keys on ts + tool + a sha256 of the line
// re-marshalled (the form the guard writes), so only identical lines dedupe
// and distinct losses each survive.
func rawAuditLineKey(e auditEntry, raw []byte) string {
	if e.ToolInputHash != "" {
		return e.Ts + "|" + e.Tool + "|" + e.ToolInputHash
	}
	sum := sha256.Sum256(remarshalled(raw))
	return e.Ts + "|" + e.Tool + "|raw:" + hex.EncodeToString(sum[:])
}

// remarshalled is raw decoded and encoded again, the form guardAuditLine
// writes, so a line keys the same before and after it went through the guard.
// A line that does not decode as one JSON object keys on its own bytes.
func remarshalled(raw []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}
