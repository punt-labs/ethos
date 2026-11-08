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

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/redact"
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

// stagedSuffix recognizes sources left by older interrupted migrations. New
// migrations never rename or delete these global sources.
const stagedSuffix = ".ethos-tmp"

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
		return nil, fmt.Errorf("reading %s: %w", globalSessionsDir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Enumerate a staged leftover under its legacy name. Readers include
		// both sources without creating any new global staging.
		name := strings.TrimSuffix(e.Name(), stagedSuffix)
		path := filepath.Join(globalSessionsDir, name)
		if !strings.HasSuffix(name, ".audit.jsonl") || slices.Contains(out, path) {
			continue
		}
		out = append(out, path)
	}
	return out, nil
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
	key, err := audit.LegacyIdentity(audit.SessionNS, raw)
	if err == nil {
		return key
	}
	// Global legacy corruption is converted to a loss marker before migration.
	sum := sha256.Sum256(raw)
	return e.Ts + "|" + e.Tool + "|invalid:" + hex.EncodeToString(sum[:])
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
