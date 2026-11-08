package hook

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
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
	post, err := sessionUnionLines(repoRoot, sessionID, now)
	if err != nil {
		return nil, err
	}
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

func readSessionAuditWithPolicy(p layout.Policy, sessionID string) ([]auditEntry, error) {
	data, err := audit.ReadStream(p, audit.Stream{Namespace: audit.SessionNS, Session: sessionID})
	if err != nil {
		return nil, err
	}
	var out []auditEntry
	for _, l := range append(data.Legacy, data.Lines...) {
		e, err := decodeAuditLine(l.Raw)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// sessionUnionLines returns a session's full audit stream as raw ts-tagged
// lines — sealed chunks + live tail past the watermark + the frozen legacy
// pool, stable-sorted by ts. Keeping the raw bytes (rather than decoded
// auditEntry) lets a caller inspect fields the struct drops, e.g. the
// audit_error sentinel key (see sessionLossMarkers).
func sessionUnionLines(repoRoot, sessionID string, _ time.Time) ([]audit.Line, error) {
	p, err := auditPolicy(repoRoot)
	if err != nil {
		return nil, err
	}
	data, err := audit.ReadStream(p, audit.Stream{Namespace: audit.SessionNS, Session: sessionID})
	if err != nil {
		return nil, err
	}
	post := append(data.Legacy, data.Lines...)
	sort.SliceStable(post, func(i, j int) bool { return post[i].TS < post[j].TS })
	return post, nil
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
