package mission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
)

// sessionlessID separates ad-hoc CLI events from real session streams without
// putting their live file or lock in the shared record tree.
const sessionlessID = "no-session"

func (s *Store) appendRepositoryEvent(id string, e Event) error {
	session := s.resolveSessionID()
	if session == "" {
		session = sessionlessID
	}
	stream := audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: session}
	e.Record, e.Acceptance = s.recordMode(), ""
	return audit.WithStreamLock(s.policy, stream, func() error {
		data, err := audit.ReadStream(s.policy, stream)
		if err != nil {
			return err
		}
		floor := data.Floor
		for _, l := range append(data.Lines, data.Legacy...) {
			if l.TS > floor {
				floor = l.TS
			}
		}
		live := stream.LivePath(s.policy)
		if err := layout.EnsureLocalDir(s.policy, filepath.Dir(live)); err != nil {
			return err
		}
		if err := rejectSymlink(live); err != nil {
			return err
		}
		// Establish the live name (including a newly created empty file) and
		// its ancestors before appending. A directory-sync failure must not
		// leave an event behind when the caller rolls back ordinary authority.
		// Keep this under the stream lock so a seal cannot remove the name
		// between confirmation and AppendMonotonic's truncate-and-sync append.
		f, err := os.OpenFile(live, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return fmt.Errorf("opening required event stream: %w", err)
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			return fmt.Errorf("confirming required event file durability: %w", err)
		}
		if err := syncOwnedAncestors(s.policy, live); err != nil {
			return fmt.Errorf("confirming required event directory durability: %w", err)
		}
		_, err = audit.AppendMonotonic(live, floor, time.Now().UTC(), func(ts int64) ([]byte, error) {
			e.TS = audit.FormatLineTS(ts)
			b, err := json.Marshal(e)
			if err != nil {
				return nil, err
			}
			return guardWrite(live, b)
		})
		return err
	})
}

func (s *Store) missionStreams(id string) ([]audit.Stream, error) {
	seen := map[string]bool{sessionlessID: true}
	for _, root := range []string{filepath.Join(s.policy.LocalRoot(), "trace"), s.policy.RecordRoot(), s.policy.CheckoutRecordRoot()} {
		scan, err := audit.ScanSealedDir(filepath.Join(root, "missions", id), audit.MissionNS, "")
		if err != nil {
			return nil, err
		}
		for _, c := range scan.Chunks {
			seen[c.Session] = true
		}
	}
	for _, root := range []string{s.policy.LocalRoot(), filepath.Join(s.checkoutRoot, ".punt-labs", "local", "ethos")} {
		ids, err := audit.ListLiveLogSessions(filepath.Join(root, "missions", id))
		if err != nil {
			return nil, err
		}
		for _, sid := range ids {
			seen[sid] = true
		}
	}
	var out []audit.Stream
	for session := range seen {
		stream := audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: session}
		if err := stream.Validate(); err != nil {
			return nil, err
		}
		out = append(out, stream)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Session < out[j].Session })
	return out, nil
}

func (s *Store) loadRepositoryEvents(id string) ([]Event, []string, error) {
	streams, err := s.missionStreams(id)
	if err != nil {
		return nil, nil, err
	}
	var lines []audit.Line
	var legacy []audit.Line
	legacyCounts := map[string]int{}
	warnings, err := s.checkRepositoryEventSources(id, false)
	if err != nil {
		return nil, nil, err
	}
	for _, stream := range streams {
		data, err := audit.ReadStream(s.policy, stream)
		if err != nil {
			return nil, nil, err
		}
		lines = append(lines, data.Lines...)
		for range data.Gaps {
			warnings = append(warnings, "mission event history has a recorded gap")
		}
		counts := map[string]int{}
		for _, l := range data.Legacy {
			var v any
			dec := json.NewDecoder(bytes.NewReader(l.Raw))
			dec.UseNumber()
			if err := dec.Decode(&v); err != nil {
				return nil, nil, err
			}
			b, err := json.Marshal(v)
			if err != nil {
				return nil, nil, err
			}
			key := string(b)
			counts[key]++
			if counts[key] > legacyCounts[key] {
				legacy = append(legacy, l)
			}
		}
		for k, n := range counts {
			if n > legacyCounts[k] {
				legacyCounts[k] = n
			}
		}
	}
	lines, err = audit.MergeLines(lines)
	if err != nil {
		return nil, nil, err
	}
	// Supported pre-stream residue is session-tagged and must be filtered by
	// that session's sealed watermark, not the greatest timestamp in the mission.
	watermarks := map[string]int64{}
	for _, root := range []string{filepath.Join(s.policy.LocalRoot(), "trace"), s.policy.RecordRoot(), s.policy.CheckoutRecordRoot()} {
		scan, err := audit.ScanSealedDir(filepath.Join(root, "missions", id), audit.MissionNS, "")
		if err != nil {
			return nil, nil, err
		}
		for session, ts := range audit.MaxLastBySession(scan.Chunks) {
			if ts > watermarks[session] {
				watermarks[session] = ts
			}
		}
	}
	residueCounts := map[string]int{}
	seenPaths := map[string]bool{}
	for _, root := range []string{s.repoRoot, s.auditRoot()} {
		path := audit.MissionResiduePath(root, id)
		if seenPaths[path] {
			continue
		}
		seenPaths[path] = true
		residue, err := audit.ResidueLinesFiltered(path, watermarks)
		if err != nil {
			return nil, nil, err
		}
		counts := map[string]int{}
		for _, l := range residue {
			e, err := decodeResidueEventLine(l.Raw)
			if err != nil {
				warnings = append(warnings, err.Error())
				continue
			}
			l.Raw, err = json.Marshal(e)
			if err != nil {
				return nil, nil, err
			}
			key := string(l.Raw)
			counts[key]++
			if counts[key] > residueCounts[key] {
				legacy = append(legacy, l)
			}
		}
		for key, n := range counts {
			if n > residueCounts[key] {
				residueCounts[key] = n
			}
		}
	}
	lines = append(legacy, lines...)
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].TS < lines[j].TS })
	c, authorityErr := s.Load(id)
	var corrections *correctionsAuthority
	correctionsErr := authorityErr
	if authorityErr == nil {
		corrections, correctionsErr = s.readCorrections(c)
		if correctionsErr == nil && c.RoundDecisions != nil && corrections == nil {
			correctionsErr = errors.New("corrections authority unavailable; missing is not an initialized empty history")
		}
	}
	events := []Event{}
	for _, l := range lines {
		e, err := decodeRepositoryEventLine(l.Raw)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		e.Acceptance = ""
		if e.Event == "reflect" && e.OperationID != "" {
			e.Acceptance = "uncommitted"
			if authorityErr != nil {
				e.Acceptance = "unknown"
				warnings = append(warnings, "reflection acceptance unknown: shared authority unreadable")
			} else if c.RoundDecisions != nil {
				for _, decision := range c.RoundDecisions.Entries {
					if matchesReflectionDecision(e, decision) {
						e.Acceptance = "accepted"
						break
					}
				}
			}
		}
		if e.Event == EventCorrect && e.OperationID != "" {
			e.Acceptance = "uncommitted"
			if correctionsErr != nil {
				e.Acceptance = "unknown"
				warnings = append(warnings, "correction acceptance unknown: shared authority unreadable")
			} else if corrections != nil {
				for _, entry := range corrections.Entries {
					if matchesCorrectionEntry(e, entry) {
						e.Acceptance = "accepted"
						break
					}
				}
			}
		}
		events = append(events, e)
	}
	return events, warnings, nil
}

// These joins are shared by log acceptance and full-export completeness. Event
// timestamps are monotonic append times, not the authority's CreatedAt values.
func matchesReflectionDecision(event Event, decision RoundDecision) bool {
	if event.Event != "reflect" || event.OperationID == "" || event.OperationID != decision.ID || event.Actor != decision.Author {
		return false
	}
	recommendation, ok := event.Details["recommendation"].(string)
	if !ok || recommendation != decision.Recommendation {
		return false
	}
	// Select the exact protocol key before decoding; struct decoding of the
	// whole map would also accept case-insensitive aliases such as "ROUND".
	// Repository decoders retain json.Number so this rejects fractional tokens
	// even when their float64 approximation would equal an accepted round.
	data, err := json.Marshal(event.Details["round"])
	if err != nil {
		return false
	}
	var round int
	if err := json.Unmarshal(data, &round); err != nil {
		return false // Fractional, overflowing or otherwise mistyped rounds cannot match.
	}
	return round == decision.Round
}

func matchesCorrectionEntry(event Event, entry CorrectionEntry) bool {
	return event.Event == EventCorrect && event.OperationID != "" && event.OperationID == entry.ID &&
		event.Actor == entry.Correction.Author && sameJSON(correctionDetails(entry.Correction), event.Details)
}

// confirmStreamDurability is used on receipt retries too: an identical readable
// event after a failed fsync/rollback is delivery only after its source has
// actually been synced. Missing live files may mean a completed archive seal.
func confirmStreamDurability(p layout.Policy, stream audit.Stream) error {
	dirs, err := audit.StreamDirs(p, stream)
	if err != nil {
		return err
	}
	paths := []string{stream.LivePath(p)}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			_, kind := audit.Classify(entry.Name(), audit.MissionNS)
			if kind == audit.KindValid || entry.Name() == "log.jsonl" {
				paths = append(paths, filepath.Join(dir, entry.Name()))
			}
		}
	}
	for _, path := range paths {
		if err := rejectSymlink(path); err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		err = errors.Join(f.Sync(), f.Close())
		if err != nil {
			return fmt.Errorf("confirming required event durability: %w", err)
		}
		if err := syncOwnedAncestors(p, path); err != nil {
			return err
		}
	}
	return nil
}
