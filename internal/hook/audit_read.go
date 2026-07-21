package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
)

// readSessionAudit reconstructs a session's full audit stream as the union of
// its sealed chunks and the live tail past the sealed watermark
// (docs/audit-seal.md §`ethos audit show`, I12-merge):
//
//	return stable_sort_by_ts( dedup_by_ts(Sm ++ tail) ++ Sl )
//
// Sm is the post-discipline sealed chunk pool, tail is the live lines past the
// watermark, and Sl is the frozen legacy pool (the oldest chunk, read once,
// never deduped). A near-miss chunk name, an orphan .corrupt, or a corrupt
// chunk returns an error naming the offender.
func readSessionAudit(repoRoot, sessionID string, now time.Time) ([]auditEntry, error) {
	sealedDir, err := resolveRepoSessionDir(repoRoot, sessionID, now)
	if err != nil {
		return nil, fmt.Errorf("resolving session dir: %w", err)
	}
	legacyPath := sessionLegacyPath(sealedDir)
	livePath := liveAuditPath(repoRoot, sessionID)

	sc, err := audit.ScanSealedDir(sealedDir, audit.SessionNS, "")
	if err != nil {
		return nil, err
	}

	// Sm: read every valid chunk, verifying content against its name.
	var monotonic []audit.Line
	for _, c := range audit.SortChunks(sc.Chunks) {
		lines, verr := audit.ReadChunkVerified(filepath.Join(sealedDir, c.ChunkFile()), c.Last)
		if verr != nil {
			return nil, verr
		}
		monotonic = append(monotonic, lines...)
	}

	watermark, err := audit.Watermark(sealedDir, audit.SessionNS, "", legacyPath)
	if err != nil {
		return nil, err
	}

	// Live tail: lines with ts strictly past the watermark.
	tail, err := audit.LiveLinesPastWatermark(livePath, "", watermark)
	if err != nil {
		return nil, err
	}

	post := audit.DedupByIdentity(append(monotonic, tail...))

	// Sl: frozen legacy pool, read once, kept undeduped.
	legacy, err := readAuditEntries(legacyPath)
	if err != nil {
		return nil, fmt.Errorf("reading legacy %s: %w", legacyPath, err)
	}
	for _, e := range legacy {
		ts, perr := audit.ParseLineTS(e.Ts)
		if perr != nil {
			continue
		}
		raw, mErr := json.Marshal(e)
		if mErr != nil {
			return nil, fmt.Errorf("re-marshaling legacy line: %w", mErr)
		}
		post = append(post, audit.Line{TS: ts, Raw: raw})
	}
	sort.SliceStable(post, func(i, j int) bool { return post[i].TS < post[j].TS })

	out := make([]auditEntry, 0, len(post))
	for _, l := range post {
		e, derr := decodeAuditLine(l.Raw)
		if derr != nil {
			return nil, fmt.Errorf("decoding union line: %w", derr)
		}
		out = append(out, e)
	}
	return out, nil
}

// listRepoSessions returns the union of session ids present in the tracked
// sealed tree (dated directory names) and the live zone (flat file names). A
// missing tree is not an error.
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
		if id, ok := stripSuffix(f.Name(), ".audit.jsonl"); ok {
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
