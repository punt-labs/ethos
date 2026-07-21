//go:build !windows

package mission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/punt-labs/ethos/internal/audit"
)

// sessionlessID is the reserved session token for a mission event append made
// with no resolvable session (an ad-hoc CLI invocation inside a repo). It keeps
// the append in the machine-local live zone — under its own per-(mission,
// session) live log and log-no-session-* chunk namespace — instead of the
// tracked log.jsonl, so the sealed-record invariant holds even without a
// session. It is a normal session id everywhere downstream (seal, read, dedup);
// a real session id never collides with it.
const sessionlessID = "no-session"

// appendLiveEventLocked appends one event to the DES-058 per-(mission,
// session) live log with a strictly-monotonic per-(mission, session)
// timestamp (docs/audit-seal.md §Mission-tree churn). The caller already
// holds the per-mission flock; this method additionally takes the live-zone
// flock beside the live log so the timestamp allocation and append are
// atomic against a concurrent seal.
//
// The timestamp floor is the monotonic floor — the max <last> over this
// session's own sealed chunks plus the frozen legacy log.jsonl's max ts — so a
// post-upgrade event sorts strictly after frozen history. A sessionless append
// lands under the reserved sessionlessID.
func (s *Store) appendLiveEventLocked(missionID string, e Event) error {
	repoRoot := s.repoRoot
	sessionID := s.sessionID
	if sessionID == "" {
		sessionID = sessionlessID
	}
	sealedDir := audit.SealedMissionDir(repoRoot, missionID)
	legacy := audit.MissionLegacySources(repoRoot, missionID)
	livePath := audit.LiveMissionLogPath(repoRoot, missionID, sessionID)
	lockPath := audit.LiveMissionLockPath(repoRoot, missionID, sessionID)

	floor, err := audit.MonotonicFloor(sealedDir, audit.MissionNS, sessionID, legacy...)
	if err != nil {
		return fmt.Errorf("computing mission monotonic floor for %s: %w", missionID, err)
	}
	return audit.WithLock(lockPath, func() error {
		_, aErr := audit.AppendMonotonic(livePath, floor, time.Now().UTC(), func(ts int64) ([]byte, error) {
			e.TS = audit.FormatLineTS(ts)
			data, mErr := json.Marshal(e)
			if mErr != nil {
				return nil, fmt.Errorf("marshaling event: %w", mErr)
			}
			return data, nil
		})
		return aErr
	})
}

// loadLiveUnionEvents reconstructs a mission's full event stream as the union
// of every session's sealed chunks and live tails under missions/<id>/, plus
// the frozen legacy log.jsonl, stable-sorted by ts with post-discipline lines
// deduped on (session, ts) (docs/audit-seal.md §Mission-tree churn). Returns
// the decoded events, per-line warnings, and an error only on corruption
// (a near-miss chunk name, an orphan .corrupt, or a content-vs-name mismatch).
//
// Used only in two-tree mode; the legacy single-tree read path stays on
// LoadEvents' tracked-log walk.
func (s *Store) loadLiveUnionEvents(missionID string) ([]Event, []string, error) {
	repoRoot := s.repoRoot
	sealedDir := audit.SealedMissionDir(repoRoot, missionID)

	sc, err := audit.ScanSealedDir(sealedDir, audit.MissionNS, "")
	if err != nil {
		return nil, nil, err
	}

	// Sm: every session's sealed chunks, verified against their names.
	var post []audit.Line
	for _, c := range audit.SortChunks(sc.Chunks) {
		lines, verr := audit.ReadChunkVerified(filepath.Join(sealedDir, c.ChunkFile()), c.Last)
		if verr != nil {
			return nil, nil, verr
		}
		for _, l := range lines {
			l.Session = c.Session
			post = append(post, l)
		}
	}

	// Live tails: each session's own live log past its own sealed watermark
	// (own chunks + markers only — never the shared legacy max, which would
	// strand lines below a sessionless writer's later append). See
	// audit.Watermark.
	sessions, err := s.listLiveMissionSessions(missionID, sc.Chunks)
	if err != nil {
		return nil, nil, err
	}
	for _, sess := range sessions {
		wm, wErr := audit.Watermark(sealedDir, audit.MissionNS, sess)
		if wErr != nil {
			return nil, nil, wErr
		}
		livePath := audit.LiveMissionLogPath(repoRoot, missionID, sess)
		tail, tErr := audit.LiveLinesPastWatermark(livePath, sess, wm)
		if tErr != nil {
			return nil, nil, tErr
		}
		post = append(post, tail...)
	}

	post = audit.DedupByIdentity(post)

	// Decode the post-discipline pool (sealed chunks + live tails) into
	// ts-tagged events. Chunks are strict-verified and live lines are
	// pre-filtered, so a decode failure here is genuine corruption — warn.
	entries := make([]tsEvent, 0, len(post))
	var warnings []string
	for i, l := range post {
		e, derr := decodeEventLine(l.Raw)
		if derr != nil {
			warnings = append(warnings, fmt.Sprintf("union line %d: %v", i+1, derr))
			continue
		}
		entries = append(entries, tsEvent{ts: l.TS, e: e})
	}

	// Sl: the frozen legacy sources, merged as the oldest pool — pre-discipline
	// ts sort before post-discipline ts, and the tracked log.jsonl inserted
	// before the residue so the stable sort keeps their defined order for any
	// equal ts (docs/audit-seal.md §Migration).
	//
	// The tracked log.jsonl is the true frozen legacy — read whole and undeduped
	// through the strict walker so a corrupt line surfaces as a warning.
	trackedLog := filepath.Join(sealedDir, "log.jsonl")
	trackedEvents, trackedWarnings, lErr := loadFrozenLog(trackedLog)
	if lErr != nil {
		return nil, nil, lErr
	}
	warnings = append(warnings, trackedWarnings...)
	for _, e := range trackedEvents {
		ts, perr := audit.ParseLineTS(e.TS)
		if perr != nil {
			ts = 0 // sorts first; a decoded event always has a valid RFC3339 ts
		}
		entries = append(entries, tsEvent{ts: ts, e: e})
	}

	// The superseded shared-live missions/<id>.jsonl residue is drained once
	// with a PER-LINE filter: a line survives only if its ts is past the max
	// <last> of the sealed chunks carrying its own session id, because that
	// design's seal already copied some lines into chunks. Reading it whole
	// would re-add every already-sealed line as a duplicate. Survivors carry no
	// session identity in the new scheme, so they decode through the tolerant
	// residue decoder (the foreign session tag is discarded, not rejected).
	residueLines, rErr := audit.ResidueLinesFiltered(
		audit.MissionResiduePath(repoRoot, missionID), audit.MaxLastBySession(sc.Chunks))
	if rErr != nil {
		return nil, nil, rErr
	}
	for _, l := range residueLines {
		e, derr := decodeResidueEventLine(l.Raw)
		if derr != nil {
			warnings = append(warnings, fmt.Sprintf("residue line: %v", derr))
			continue
		}
		entries = append(entries, tsEvent{ts: l.TS, e: e})
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ts < entries[j].ts })
	events := make([]Event, 0, len(entries))
	for _, en := range entries {
		events = append(events, en.e)
	}
	return events, warnings, nil
}

// tsEvent pairs a parsed timestamp with its decoded event for the stable
// merge in loadLiveUnionEvents.
type tsEvent struct {
	ts int64
	e  Event
}

// loadFrozenLog reads and strictly decodes a frozen legacy log.jsonl, so a
// corrupt line surfaces as a warning rather than being silently dropped. A
// missing file yields no events, no warnings, no error.
func loadFrozenLog(path string) ([]Event, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("reading frozen log %s: %w", path, err)
	}
	return decodeEventLog(data)
}

// listLiveMissionSessions returns the union of session ids that have a live
// log file or a sealed chunk under a mission directory.
func (s *Store) listLiveMissionSessions(missionID string, chunks []audit.ChunkName) ([]string, error) {
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
	for _, c := range chunks {
		add(c.Session)
	}
	liveDir := filepath.Join(audit.LiveMissionsDir(s.repoRoot), filepath.Base(missionID))
	names, err := audit.ListLiveLogSessions(liveDir)
	if err != nil {
		return nil, err
	}
	for _, id := range names {
		add(id)
	}
	sort.Strings(ids)
	return ids, nil
}
