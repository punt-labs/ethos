package mission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/punt-labs/ethos/v4/internal/audit"
)

// SnapshotFullTrace returns only the selected mission's portable forensic
// detail. Paths are relative to the export directory. The caller first seals
// the mission locally; a concurrently extended tail requires another attempt.
// This is not a blame snapshot, a directory copy, or a legacy backfill.
func (s *Store) SnapshotFullTrace(id string) ([]TrackedFile, error) {
	if _, ok := s.RepositoryPolicy(); !ok {
		return nil, fmt.Errorf("full export requires a valid repository policy")
	}
	var files []TrackedFile
	err := s.withMissionLock(id, true, func() error {
		c, err := s.Load(id)
		if err != nil {
			return err
		}
		corrections, err := s.readCorrections(c)
		if err != nil {
			return fmt.Errorf("full export incomplete: reading corrections authority: %w", err)
		}
		if c.RoundDecisions != nil && corrections == nil {
			return fmt.Errorf("full export incomplete: mission %s corrections authority unavailable; missing is not an initialized empty history", id)
		}
		reflections, err := s.acceptedReflections(id)
		if err != nil {
			return fmt.Errorf("full export incomplete: %w", err)
		}
		if len(reflections) > 0 {
			data, err := RedactedYAML(s.repoRoot, reflectionsFile{Reflections: reflections})
			if err != nil {
				return err
			}
			files = append(files, TrackedFile{Path: filepath.Join(id, "reflections.yaml"), Data: data, Perm: 0o644})
		}
		release, err := AcquireMissionLockExclusive(s.repoRoot, id)
		if err != nil {
			return err
		}
		defer release()
		prompts, err := s.snapshotExportPrompts(id)
		if err != nil {
			return err
		}
		files = append(files, prompts...)
		chunks, events, err := s.snapshotExportEvents(id)
		if err != nil {
			return err
		}
		created := false
		operations := map[string][]Event{}
		for _, event := range events {
			created = created || event.Event == "create"
			if event.OperationID != "" {
				operations[event.OperationID] = append(operations[event.OperationID], event)
			}
		}
		// A matching event cannot mask another candidate reusing its ID. Use
		// the same all-session, all-layer conflict check as prepared-event replay;
		// equal copies in the original stream remain valid.
		for operation := range operations {
			if _, err := audit.FindOperation(s.policy, id, operation); err != nil {
				return fmt.Errorf("full export incomplete: %w", err)
			}
		}
		if !created {
			return fmt.Errorf("full export incomplete: mission %s create history is unavailable", id)
		}
		results, err := s.loadResultsLocked(id)
		if err != nil {
			return fmt.Errorf("full export: loading accepted results: %w", err)
		}
		if err := checkFullExportHistory(c, results, events); err != nil {
			return err
		}
		if c.RoundDecisions != nil {
			for _, decision := range c.RoundDecisions.Entries {
				matched := false
				for _, event := range operations[decision.ID] {
					if matchesReflectionDecision(event, decision) {
						matched = true
						break
					}
				}
				if !matched {
					return fmt.Errorf("full export incomplete: mission %s round %d required reflection event is unavailable", id, decision.Round)
				}
			}
		}
		if corrections != nil {
			for _, entry := range corrections.Entries {
				matched := false
				for _, event := range operations[entry.ID] {
					if matchesCorrectionEntry(event, entry) {
						matched = true
						break
					}
				}
				if !matched {
					return fmt.Errorf("full export incomplete: mission %s required correction event is unavailable", id)
				}
			}
		}
		files = append(files, chunks...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// checkFullExportHistory checks accepted authority, including transitions for
// every crossed round; advancing without a result is legal. Decode integers without
// truncating arbitrary JSON numbers or trusting untyped map assertions.
func checkFullExportHistory(c *Contract, results []Result, events []Event) error {
	resultHistory := make(map[int]bool)
	terminal := c.Status == StatusOpen
	for _, event := range events {
		if event.Event != "result" && event.Event != "close" && event.Event != "abandon" {
			continue
		}
		var detail struct {
			Round   int    `json:"round"`
			Verdict string `json:"verdict"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
		}
		data, err := json.Marshal(event.Details)
		if err != nil {
			return fmt.Errorf("full export: encoding event details: %w", err)
		}
		if err := json.Unmarshal(data, &detail); err != nil {
			continue // A malformed candidate cannot establish required history.
		}
		if event.Event == "result" {
			for _, result := range results {
				if detail.Round == result.Round && detail.Verdict == result.Verdict && event.Actor == result.Author {
					resultHistory[result.Round] = true
				}
			}
			continue
		}
		// Repository event timestamps are monotonic append times, not the
		// contract/result timestamps; equality would reject genuine history.
		if event.Actor != c.Leader {
			continue
		}
		if c.Status == StatusAbandoned {
			terminal = terminal || (event.Event == "abandon" && detail.Reason != "")
		} else if event.Event == "close" && detail.Status == c.Status && detail.Round == c.CurrentRound {
			for _, result := range results {
				if result.Round == detail.Round && result.Verdict == detail.Verdict {
					terminal = true
				}
			}
		}
	}
	for _, result := range results {
		if !resultHistory[result.Round] {
			return fmt.Errorf("full export incomplete: mission %s round %d accepted result event is unavailable", c.MissionID, result.Round)
		}
	}
	if !terminal {
		return fmt.Errorf("full export incomplete: mission %s terminal %s event is unavailable", c.MissionID, c.Status)
	}
	return checkFullExportRoundTransitions(c, events)
}

func checkFullExportRoundTransitions(c *Contract, events []Event) error {
	if c.RoundDecisions == nil {
		// Legacy completeness is established (or refused) by the existing
		// reflection/event readers. Do not infer or backfill v1 authority here.
		return nil
	}
	transitions := make(map[int]bool)
	for _, event := range events {
		if event.Event != "round_advanced" {
			continue
		}
		for _, field := range []string{"from_round", "to_round", "recommendation"} {
			if _, ok := event.Details[field]; !ok {
				return fmt.Errorf("full export incomplete: mission %s round_advanced event is missing %s", c.MissionID, field)
			}
		}
		var detail struct {
			FromRound      int    `json:"from_round"`
			ToRound        int    `json:"to_round"`
			Recommendation string `json:"recommendation"`
		}
		data, err := json.Marshal(event.Details)
		if err != nil {
			return fmt.Errorf("full export incomplete: mission %s encoding round_advanced details: %w", c.MissionID, err)
		}
		if err := json.Unmarshal(data, &detail); err != nil {
			return fmt.Errorf("full export incomplete: mission %s malformed round_advanced details: %w", c.MissionID, err)
		}
		decision := c.decision(detail.FromRound)
		if detail.FromRound < 1 || detail.FromRound >= c.CurrentRound || detail.ToRound != detail.FromRound+1 ||
			decision == nil || detail.Recommendation != decision.Recommendation || strings.TrimSpace(event.Actor) == "" {
			return fmt.Errorf("full export incomplete: mission %s invalid or conflicting round_advanced event", c.MissionID)
		}
		// AdvanceRound accepts a caller-supplied nonempty actor (including
		// MCP's override), not necessarily the current leader or decision author.
		// Inspect every candidate, even after a match. A valid event cannot
		// conceal another event contradicting the accepted transition.
		transitions[detail.FromRound] = true
	}
	for round := 1; round < c.CurrentRound; round++ {
		if !transitions[round] {
			return fmt.Errorf("full export incomplete: mission %s round %d required round_advanced event is unavailable", c.MissionID, round)
		}
	}
	return nil
}

func (s *Store) snapshotExportPrompts(id string) ([]TrackedFile, error) {
	dir := filepath.Join(RepoStatePath(s.repoRoot, "missions", id), "delegations")
	if err := rejectSymlink(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r, err := NewPathRedactor(s.repoRoot)
	if err != nil {
		return nil, err
	}
	var files []TrackedFile
	for _, entry := range entries {
		if !entry.IsDir() {
			return nil, fmt.Errorf("invalid delegation directory in full export")
		}
		delegation := entry.Name()
		release, err := s.AcquireDelegationLock(delegation)
		if err != nil {
			return nil, err
		}
		data, err := func() ([]byte, error) {
			record, err := LoadDelegation(filepath.Join(dir, delegation, "record.yaml"))
			if err != nil {
				return nil, err
			}
			if record.ID != delegation || record.Mission != id {
				return nil, fmt.Errorf("delegation identity mismatch in full export")
			}
			prompt, err := s.LoadDelegationPrompt(id, delegation)
			if errors.Is(err, os.ErrNotExist) && record.PromptHash == "" {
				return nil, nil
			}
			if err != nil {
				return nil, fmt.Errorf("full export incomplete: mission %s delegation %s prompt unavailable: %w", id, delegation, err)
			}
			return []byte(r.Content(string(prompt))), nil
		}()
		release()
		if err != nil {
			return nil, err
		}
		if data != nil {
			files = append(files, TrackedFile{Path: filepath.Join(id, "delegations", delegation, "prompt.md"), Data: data, Perm: 0o644})
		}
	}
	return files, nil
}

func (s *Store) snapshotExportEvents(id string) ([]TrackedFile, []Event, error) {
	streams, err := audit.ListStreams(s.policy)
	if err != nil {
		return nil, nil, err
	}
	files := map[string][]byte{}
	var events []Event
	for _, stream := range streams {
		if stream.Namespace != audit.MissionNS || stream.Mission != id {
			continue
		}
		err := audit.WithStreamLock(s.policy, stream, func() error {
			data, err := audit.ReadStream(s.policy, stream)
			if err != nil {
				return err
			}
			if len(data.Gaps) > 0 {
				return fmt.Errorf("full export incomplete: mission %s has quarantined history", id)
			}
			pending, err := audit.UnarchivedCount(s.policy, stream)
			if err != nil {
				return err
			}
			if pending != 0 {
				return fmt.Errorf("full export incomplete: mission %s tail changed after sealing; retry export", id)
			}
			// Build deterministic portable chunks from the verified union. This
			// never rewrites retained chunks, and never copies a live file as a file.
			if len(data.Legacy) > 0 {
				return fmt.Errorf("full export incomplete: legacy mission events require explicit operator handling")
			}
			if len(data.Lines) == 0 {
				return nil
			}
			// Automatic publication can omit private entries between eligible
			// endpoints: full A, blame B, full C publishes AC under A-C. Never
			// reuse that immutable name for ABC. Split at captured eligibility
			// boundaries, so no mixed run shares a sparse publication's range.
			// Equal overlaps remain safe for the canonical union reader.
			for start := 0; start < len(data.Lines); {
				eligible := audit.Eligible(stream, data.Lines[start])
				end := start + 1
				for end < len(data.Lines) && audit.Eligible(stream, data.Lines[end]) == eligible {
					end++
				}
				lines := data.Lines[start:end]
				var body []byte
				for _, line := range lines {
					event, err := decodeRepositoryEventLine(line.Raw)
					if err != nil {
						return fmt.Errorf("invalid mission event in full export: %w", err)
					}
					event, err = RedactRecord(s.repoRoot, event)
					if err != nil {
						return err
					}
					events = append(events, event)
					encoded, err := json.Marshal(event)
					if err != nil {
						return err
					}
					body = append(body, append(encoded, '\n')...)
				}
				name := audit.MissionChunkFile(stream.Session, lines[0].TS, lines[len(lines)-1].TS)
				path := filepath.Join(id, name)
				body, err = guardWrite(path, body)
				if err != nil {
					return err
				}
				if old, ok := files[path]; ok && !bytes.Equal(old, body) {
					return fmt.Errorf("conflicting export event chunks")
				}
				files[path] = body
				start = end
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	var out []TrackedFile
	for path, data := range files {
		out = append(out, TrackedFile{Path: path, Data: data, Perm: 0o644})
	}
	return out, events, nil
}
