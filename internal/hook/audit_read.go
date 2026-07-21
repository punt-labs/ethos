package hook

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// readSessionAudit reconstructs a session's full audit stream as the union
// of its sealed chunks and the live tail past the sealed watermark
// (docs/audit-seal.md §`ethos audit show`, I12-merge):
//
//	return stable_sort_by_ts( dedup_by_ts(Sm ++ tail) ++ Sl )
//
// Sm is the post-discipline sealed chunk pool, tail is the live lines with
// ts past the watermark, and Sl is the frozen legacy pool (the oldest
// chunk, read once, never deduped). Post-discipline lines dedup on ts —
// loss-free, since equal ts means a byte-identical line from the same
// append-only live file. Legacy lines pass through undeduped and, because
// every legacy ts sits below every post-discipline ts, sort first.
//
// A near-miss chunk name, an orphan .corrupt, or a corrupt chunk (does not
// parse whole, or last ts != its filename <last>) returns an error naming
// the offender — corruption is surfaced, never dropped.
func readSessionAudit(repoRoot, sessionID string, now time.Time) ([]auditEntry, error) {
	sealedDir, err := resolveRepoSessionDir(repoRoot, sessionID, now)
	if err != nil {
		return nil, fmt.Errorf("resolving session dir: %w", err)
	}
	legacyPath := filepath.Join(sealedDir, "audit.jsonl")
	livePath := liveAuditPath(repoRoot, sessionID)

	sc, err := scanSealedDir(sealedDir, sessionNamespace, "")
	if err != nil {
		return nil, err
	}

	// Sm: read every valid chunk, verifying content against its name.
	var monotonic []tsEntry
	for _, c := range sortChunks(sc.chunks) {
		name := sessionChunkFile(c.First, c.Last)
		entries, verr := readChunkVerified(filepath.Join(sealedDir, name), c.Last)
		if verr != nil {
			return nil, verr
		}
		for _, e := range entries {
			ts, perr := parseLineTS(e.Ts)
			if perr != nil {
				return nil, fmt.Errorf("chunk %s: %w", name, perr)
			}
			monotonic = append(monotonic, tsEntry{ts: ts, e: e})
		}
	}

	// Sl: frozen legacy pool, read once, kept undeduped.
	legacy, err := readAuditEntries(legacyPath)
	if err != nil {
		return nil, fmt.Errorf("reading legacy %s: %w", legacyPath, err)
	}

	// Watermark: the max ts already sealed (chunks + legacy + markers).
	watermark, err := sessionWatermark(sealedDir, sessionNamespace, "", legacyPath)
	if err != nil {
		return nil, err
	}

	// Live tail: lines with ts strictly past the watermark.
	live, err := readAuditEntries(livePath)
	if err != nil {
		return nil, fmt.Errorf("reading live %s: %w", livePath, err)
	}
	var tail []tsEntry
	for _, e := range live {
		ts, perr := parseLineTS(e.Ts)
		if perr != nil {
			// A terminated-unparseable live line is already counted on
			// stderr by readAuditEntries' decode path; drop it here.
			continue
		}
		if ts > watermark {
			tail = append(tail, tsEntry{ts: ts, e: e})
		}
	}

	post := dedupByTS(append(monotonic, tail...))
	for _, e := range legacy {
		ts, perr := parseLineTS(e.Ts)
		if perr != nil {
			continue
		}
		post = append(post, tsEntry{ts: ts, e: e})
	}
	sort.SliceStable(post, func(i, j int) bool { return post[i].ts < post[j].ts })

	out := make([]auditEntry, len(post))
	for i, te := range post {
		out[i] = te.e
	}
	return out, nil
}

// tsEntry pairs a parsed timestamp with its entry so the union can sort
// and dedup without reparsing the ts string.
type tsEntry struct {
	ts int64
	e  auditEntry
}

// sortChunks returns chunks ordered by First so a concatenated read is in
// time order before the stable sort.
func sortChunks(chunks []chunkName) []chunkName {
	out := make([]chunkName, len(chunks))
	copy(out, chunks)
	sort.Slice(out, func(i, j int) bool { return out[i].First < out[j].First })
	return out
}

// dedupByTS collapses post-discipline lines sharing a ts to one entry.
// Equal ts implies a byte-identical line from the same append-only live
// file (a cross-branch re-seal duplicate), so keeping the first is
// loss-free. Input order is preserved for the survivors.
func dedupByTS(in []tsEntry) []tsEntry {
	seen := make(map[int64]struct{}, len(in))
	out := in[:0:0]
	for _, te := range in {
		if _, ok := seen[te.ts]; ok {
			continue
		}
		seen[te.ts] = struct{}{}
		out = append(out, te)
	}
	return out
}

// readChunkVerified reads a sealed chunk and verifies its content matches
// its name: it must parse to completion and its last line's ts must equal
// the <last> in its filename. I11-chunk writes chunks whole, so a torn or
// mismatched chunk is corruption, not a recoverable partial write — the
// caller surfaces it as an error (exit 2), the escape being
// `ethos audit quarantine`.
func readChunkVerified(path string, expectedLast int64) ([]auditEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading chunk %s: %w", path, err)
	}
	entries, err := decodeChunkStrict(data, path)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("corrupt chunk %s: empty", path)
	}
	lastTS, err := parseLineTS(entries[len(entries)-1].Ts)
	if err != nil {
		return nil, fmt.Errorf("corrupt chunk %s: %w", path, err)
	}
	if lastTS != expectedLast {
		return nil, fmt.Errorf(
			"corrupt chunk %s: last ts %d != filename <last> %d", path, lastTS, expectedLast)
	}
	return entries, nil
}

// decodeChunkStrict parses every line of a sealed chunk, tolerating no
// torn tail and no unparseable line. Unlike the live/legacy reader, a
// chunk is written whole by the seal (temp + fsync + rename), so any
// deviation is corruption. A final line without a newline terminator, or
// any line that fails to decode, is an error naming the chunk.
func decodeChunkStrict(data []byte, path string) ([]auditEntry, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("corrupt chunk %s: missing final newline (torn write)", path)
	}
	var entries []auditEntry
	lineNo := 0
	for _, raw := range splitLines(data) {
		lineNo++
		e, err := decodeAuditLine(raw)
		if err != nil {
			return nil, fmt.Errorf("corrupt chunk %s: line %d: %w", path, lineNo, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// splitLines splits chunk bytes on newline, dropping the empty run after
// the trailing terminator. Every returned slice is a non-empty line.
func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// listRepoSessions returns the union of session ids present in the tracked
// sealed tree (dated directory names) and the live zone (flat file names).
// A session appears once even when it has both a sealed directory and a
// live file. A missing tree is not an error.
func listRepoSessions(repoRoot string) ([]string, error) {
	seen := make(map[string]struct{})
	var ids []string
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	sealed, err := os.ReadDir(sealedSessionsBase(repoRoot))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", sealedSessionsBase(repoRoot), err)
	}
	for _, d := range sealed {
		if d.IsDir() {
			add(sessionIDFromDir(d.Name()))
		}
	}

	live, err := os.ReadDir(liveSessionsDir(repoRoot))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", liveSessionsDir(repoRoot), err)
	}
	for _, f := range live {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		if id, ok := stripSuffix(name, ".audit.jsonl"); ok {
			add(id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// stripSuffix returns s without suffix and true when s ends with it.
func stripSuffix(s, suffix string) (string, bool) {
	if len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)], true
	}
	return "", false
}
