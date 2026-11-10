// Package auditimport reads the global legacy session audit files (v3.0-v3.11),
// redacts them, and merges them into a repository's local archive. It is a
// leaf below internal/hook and internal/autorepair, which both import it; it
// imports neither, so the engine can be wired into the session-start hook
// without a cycle.
package auditimport

import (
	"bufio"
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
	"github.com/punt-labs/ethos/v4/internal/lineguard"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/redact"
)

// StagedSuffix recognizes sources left by older interrupted migrations. New
// migrations never rename or delete these global sources.
const StagedSuffix = ".ethos-tmp"

// EnumerateLegacyFiles returns the absolute paths of every
// <globalSessionsDir>/*.audit.jsonl file. A missing or read-only
// directory yields an empty list with no error so the migrate command
// is no-op on a clean install and fail-soft on a permission failure.
func EnumerateLegacyFiles(globalSessionsDir string) ([]string, error) {
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
		name := strings.TrimSuffix(e.Name(), StagedSuffix)
		path := filepath.Join(globalSessionsDir, name)
		if !strings.HasSuffix(name, ".audit.jsonl") || slices.Contains(out, path) {
			continue
		}
		out = append(out, path)
	}
	return out, nil
}

// Line is one JSONL line kept as its exact on-disk bytes plus its
// dedupe key. Migration and the loss-marker scan use it so unknown fields the
// entry struct drops — notably the audit_error sentinel key — survive.
type Line struct {
	Key string
	Raw []byte
	// N is the line's number in the file it was read from, counting every
	// physical line; 0 when the reader does not record it.
	N int
	// Note says what migration did to the line, for the notice printed when
	// the line is appended. It is nil for a line left as it was.
	Note *Note
}

// Note is what migration did to legacy line N, as the kinds of rewrite
// that applied: never a value.
type Note struct {
	N     int
	Kinds []string
}

// ReadLegacyLines reads every non-blank physical line of a legacy audit
// file, including one that does not decode as an entry and a torn trailing
// fragment, so that migration can route each through the guard instead of
// losing it. A line that does not decode has an empty key, which migration
// replaces once the guard has made it a line that does. Missing file → nil.
func ReadLegacyLines(path string) ([]Line, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	var out []Line
	br := bufio.NewReader(f)
	for n := 1; ; n++ {
		line, readErr := br.ReadString('\n')
		body := strings.TrimRight(strings.TrimRight(line, "\n"), "\r")
		if strings.TrimSpace(body) != "" {
			raw := []byte(body)
			l := Line{Raw: raw, N: n}
			if e, err := DecodeLine(raw); err == nil {
				l.Key = RawLineKey(e, raw)
			}
			out = append(out, l)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return out, nil
			}
			return nil, fmt.Errorf("reading %s: %w", path, readErr)
		}
	}
}

// DecodeLine decodes one JSONL line into an entry. The
// decode is permissive — unknown fields are accepted so a v3.11.0
// reader handles a v3.12.0 line, and a v3.12.0 reader handles a
// v3.11.0 line (the new fields decode to zero-value).
func DecodeLine(raw []byte) (lineguard.Entry, error) {
	var e lineguard.Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return lineguard.Entry{}, fmt.Errorf("decoding: %w", err)
	}
	return e, nil
}

// RedactLegacyLines applies the audit path's redaction to legacy lines and
// recomputes each dedupe key. Every line is written in its canonical form, the
// decoded line re-marshalled by the line guard, never its original bytes;
// unknown fields and sentinels survive as decoded values.
func RedactLegacyLines(lines []Line, repoRoot string) ([]Line, error) {
	r, err := mission.NewPathRedactor(repoRoot)
	if err != nil {
		return nil, err
	}
	out := make([]Line, len(lines))
	for i, l := range lines {
		// Invalid text and a duplicate key are looked for in the original
		// bytes: once the line is decoded the surrogate is U+FFFD and the
		// first value is gone. why collects what was done, for the one
		// notice the line gets.
		src := l.Raw
		var kinds []string
		add := func(k string) {
			if !slices.Contains(kinds, k) {
				kinds = append(kinds, k)
			}
		}
		if mission.InvalidText(l.Raw) != "" {
			src, _, _ = lineguard.GuardAuditLineFull(l.Raw)
			add(KindMarker)
		} else if _, _, dup := redact.DuplicateJSONKey(l.Raw); dup {
			// The last value is kept and the path pass still runs on it, so
			// this machine's home becomes ~/x as on any other line.
			var ok bool
			if src, ok = lineguard.DedupeLine(l.Raw); ok {
				add(kindDuplicate)
			} else {
				src, _, _ = lineguard.GuardAuditLineFull(l.Raw)
				add(KindMarker)
			}
		}
		n := l.N
		if n == 0 {
			n = i + 1
		}
		var raw []byte
		var changed bool
		if lineguard.IsJSONObject(src) {
			var err error
			raw, changed, err = lineguard.RedactLegacyLine(src, repoRoot, r)
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
			raw, _, _ = lineguard.GuardAuditLineFull(src)
			add(KindMarker)
			changed = true
		}
		if !bytes.Equal(src, l.Raw) {
			changed = true
		}
		// The line-level check is the sink guard every appender runs: it covers
		// what redactLegacyLine does not look at (unknown fields, a tool_input
		// that is not a map). A line only re-encoded in canonical form holds the
		// same values and is not counted.
		if g, rule, m := lineguard.GuardAuditLineFull(raw); !bytes.Equal(g, raw) {
			raw, changed = g, true
			switch {
			case m:
				add(KindMarker)
			case rule != "":
				add(kindReencoded)
			}
		}
		var note *Note
		if len(kinds) > 0 {
			note = &Note{N: n, Kinds: kinds}
		}
		if !changed {
			out[i] = l
			continue
		}
		e, err := DecodeLine(raw)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out[i] = Line{Key: RawLineKey(e, raw), Raw: raw, N: n, Note: note}
	}
	return out, nil
}

// The kinds of rewrite migration counts, in the order a notice lists them.
const (
	kindContent   = "home path or content"
	kindDuplicate = "duplicate key"
	kindReencoded = "re-encoded"
	// KindMarker is a line rewritten into a loss marker.
	KindMarker = "loss marker"
)

var migrateKinds = []string{kindContent, kindDuplicate, kindReencoded, KindMarker}

// maxNoticeLines is how many line numbers a migration notice lists per kind.
const maxNoticeLines = 10

// NoticeMigratedFile reports, once for a migrated file (one write), how many of
// its total lines migration rewrote, or in a dry run would rewrite, by kind,
// with the first ten line numbers of each kind. It names no value. A file with
// no rewritten line prints nothing.
func NoticeMigratedFile(session string, notes []*Note, total int, dry bool) {
	byKind := map[string][]int{}
	count := 0
	for _, n := range notes {
		if n == nil {
			continue
		}
		count++
		for _, k := range n.Kinds {
			byKind[k] = append(byKind[k], n.N)
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
		fmt.Fprintf(os.Stderr, "ethos: audit import: %s: %s %d of %d line(s) %s: %s\n",
			lineguard.QuoteUnlessLeak(session), lead, count, total, verb, strings.Join(parts, "; "))
		return
	}
	fmt.Fprintf(os.Stderr, "ethos: audit import: %s: %d of %d line(s) %s: %s\n",
		lineguard.QuoteUnlessLeak(session), count, total, verb, strings.Join(parts, "; "))
}

// RawLineKey returns the dedupe key for one audit line, computed from both
// its decoded entry and its raw bytes. A hashed entry keys on ts + tool +
// tool_input_hash, so two byte-different encodings of the same logical
// tool_input dedupe. A hashless line (older v3.11.0 entries, and sentinels,
// whose ts + empty tool + empty preview is degenerate — a burst of same-second
// sentinels would collapse to one) keys on ts + tool + a sha256 of the line
// re-marshalled (the form the guard writes), so only identical lines dedupe
// and distinct losses each survive.
func RawLineKey(e lineguard.Entry, raw []byte) string {
	key, err := audit.LegacyIdentity(audit.SessionNS, raw)
	if err == nil {
		return key
	}
	// Global legacy corruption is converted to a loss marker before migration.
	sum := sha256.Sum256(raw)
	return e.Ts + "|" + e.Tool + "|invalid:" + hex.EncodeToString(sum[:])
}
