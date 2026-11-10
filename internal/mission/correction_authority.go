package mission

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"gopkg.in/yaml.v3"
)

func strictJSON(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON content")
	}
	return nil
}

func (s *Store) reconcileCorrection(id string) error {
	in, err := s.readCorrectionIntent(id)
	if err != nil || in == nil {
		return err
	}
	c, err := s.Load(id)
	if err != nil {
		return fmt.Errorf("%w: %w", errAuthorityRead, err)
	}
	a, err := s.readCorrections(c)
	if err != nil {
		return fmt.Errorf("%w: %w", errAuthorityRead, err)
	}
	if a != nil {
		for _, entry := range a.Entries {
			if entry.ID != in.Entry.ID {
				continue
			}
			if !reflect.DeepEqual(entry, in.Entry) {
				return fmt.Errorf("correction receipt conflicts with shared authority")
			}
			if err := s.confirmAcceptedCorrection(*in); err != nil {
				return err
			}
			return s.removeCorrectionIntent(id)
		}
	}
	return fmt.Errorf("mission %q has a pending correction preparation that is not yet in shared authority; no other mutation is accepted until it is resolved", id)
}

// An installed correction is already accepted. Recovery only confirms its
// original required delivery and authority durability before discarding the
// receipt; it must never append a missing event or regenerate accepted payload.
// The caller holds the mission lock, then acquires only the original stream lock.
func (s *Store) confirmAcceptedCorrection(in correctionIntent) error {
	line, err := audit.FindOperation(s.policy, in.Mission, in.Entry.ID)
	if err != nil {
		return fmt.Errorf("accepted correction required event verification failed; receipt retained: %w", err)
	}
	if line == nil {
		return fmt.Errorf("accepted correction required event unavailable; receipt retained")
	}
	matching, err := matchesPreparedEvent(line.Raw, in.Event)
	if err != nil {
		return fmt.Errorf("accepted correction required event verification failed; receipt retained: %w", err)
	}
	if line.Session != in.EventSession || !matching {
		return fmt.Errorf("accepted correction required event conflicts with receipt; receipt retained")
	}
	stream := audit.Stream{Namespace: audit.MissionNS, Mission: in.Mission, Session: in.EventSession}
	if err := audit.WithStreamLock(s.policy, stream, func() error { return confirmStreamDurability(s.policy, stream) }); err != nil {
		return fmt.Errorf("accepted correction required event durability unconfirmed; receipt retained: %w", err)
	}
	if err := syncOwnedAncestors(s.policy, s.correctionsPath(in.Mission)); err != nil {
		return fmt.Errorf("accepted correction authority durability unconfirmed; receipt retained: %w", err)
	}
	return nil
}

type CorrectionEntry struct {
	ID         string     `yaml:"id" json:"id" redact:"keep"`
	CreatedAt  string     `yaml:"created_at" json:"created_at" redact:"keep"`
	Correction Correction `yaml:"correction" json:"correction"`
}

type correctionsAuthority struct {
	Version int               `yaml:"version"`
	Mission string            `yaml:"mission" redact:"keep"`
	Entries []CorrectionEntry `yaml:"entries"`
}

type correctionIntent struct {
	Version      int             `yaml:"version"`
	Mission      string          `yaml:"mission" redact:"keep"`
	Entry        CorrectionEntry `yaml:"entry"`
	Event        Event           `yaml:"event"`
	EventSession string          `yaml:"event_session" redact:"session"`
}

func (s *Store) correctionsPath(id string) string {
	return filepath.Join(s.policy.RecordRoot(), "missions", id, "corrections.yaml")
}
func (s *Store) correctionIntentPath(id string) string {
	return filepath.Join(s.policy.LocalRoot(), "missions", id, "correction-intent.yaml")
}

func (s *Store) readCorrections(c *Contract) (*correctionsAuthority, error) {
	path := s.correctionsPath(c.MissionID)
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var a correctionsAuthority
	if err := strictYAML(b, &a); err != nil {
		return nil, err
	}
	if a.Version != 1 || a.Mission != c.MissionID || a.Entries == nil {
		return nil, fmt.Errorf("invalid corrections authority version, mission or null/missing entries")
	}
	seen := map[string]bool{}
	for _, e := range a.Entries {
		if !operationIDPattern.MatchString(e.ID) || seen[e.ID] {
			return nil, fmt.Errorf("invalid/duplicate correction id")
		}
		seen[e.ID] = true
		if _, err := time.Parse(time.RFC3339, e.CreatedAt); err != nil {
			return nil, err
		}
		if err := e.Correction.Validate(); err != nil {
			return nil, err
		}
		if e.Correction.Mission != c.MissionID || e.Correction.Round > c.CurrentRound {
			return nil, fmt.Errorf("correction refers to another mission or future round")
		}
	}
	return &a, nil
}

func (s *Store) initializeCorrections(id string) error {
	a := correctionsAuthority{Version: 1, Mission: id, Entries: []CorrectionEntry{}}
	b, err := yaml.Marshal(a)
	if err != nil {
		return err
	}
	return s.writeBytes(s.correctionsPath(id), b, true)
}

func (s *Store) importLegacyCorrections(c *Contract) (*correctionsAuthority, error) {
	// A tolerant partial log is never blessed as complete shared history.
	if _, err := s.checkRepositoryEventSources(c.MissionID, true); err != nil {
		return nil, err
	}
	events, warnings, err := s.LoadEvents(c.MissionID)
	if err != nil {
		return nil, err
	}
	if len(warnings) > 0 {
		return nil, fmt.Errorf("cannot import incomplete correction history: %v", warnings)
	}
	streams, err := s.missionStreams(c.MissionID)
	if err != nil {
		return nil, err
	}
	for _, stream := range streams {
		b, err := os.ReadFile(stream.LivePath(s.policy))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(b) > 0 && b[len(b)-1] != '\n' {
			return nil, fmt.Errorf("cannot import correction history with a torn live tail")
		}
		_, warnings, err := decodeEventLog(b)
		if err != nil || len(warnings) > 0 {
			return nil, fmt.Errorf("cannot import malformed correction history: %v %v", err, warnings)
		}
	}
	a := &correctionsAuthority{Version: 1, Mission: c.MissionID, Entries: []CorrectionEntry{}}
	for i, e := range events {
		if e.Event != EventCorrect {
			continue
		}
		if e.OperationID != "" {
			return nil, fmt.Errorf("correction event without shared authority; resolve its retained receipt, do not infer acceptance")
		}
		b, err := json.Marshal(e.Details)
		if err != nil {
			return nil, err
		}
		// Decode into the existing content schema with unknown fields refused.
		var content struct {
			Kind       CorrectionKind  `json:"kind"`
			Round      int             `json:"round"`
			Claim      string          `json:"claim"`
			Corrected  string          `json:"corrected"`
			Supersedes string          `json:"supersedes"`
			Evidence   []EvidenceCheck `json:"evidence"`
		}
		if err := strictJSON(b, &content); err != nil {
			return nil, err
		}
		correction := Correction{Mission: c.MissionID, Round: content.Round, Kind: content.Kind, Author: e.Actor, Claim: content.Claim, Corrected: content.Corrected, Supersedes: content.Supersedes, Evidence: content.Evidence}
		if err := correction.Validate(); err != nil {
			return nil, err
		}
		if correction.Round > c.CurrentRound {
			return nil, fmt.Errorf("legacy correction names future round")
		}
		hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", c.MissionID, i, b)))
		a.Entries = append(a.Entries, CorrectionEntry{hex.EncodeToString(hash[:16]), e.TS, correction})
	}
	return a, nil
}

func (in correctionIntent) validate(id string) error {
	if in.Version != 1 || in.Mission != id || in.Entry.Correction.Mission != id || !operationIDPattern.MatchString(in.Entry.ID) {
		return fmt.Errorf("invalid correction receipt identity/version")
	}
	if _, err := time.Parse(time.RFC3339, in.Entry.CreatedAt); err != nil {
		return err
	}
	if err := in.Entry.Correction.Validate(); err != nil {
		return err
	}
	if err := (audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: in.EventSession}).Validate(); err != nil {
		return err
	}
	expected := Event{TS: in.Event.TS, Event: EventCorrect, Actor: in.Entry.Correction.Author, Details: correctionDetails(in.Entry.Correction), OperationID: in.Entry.ID, Record: in.Event.Record}
	if in.Event.Record != "blame" && in.Event.Record != "full" {
		return fmt.Errorf("invalid correction receipt recording consent")
	}
	if _, err := audit.ParseLineTS(in.Event.TS); err != nil {
		return err
	}
	if !sameJSON(in.Event, expected) {
		return fmt.Errorf("correction receipt event disagrees with payload")
	}
	return nil
}

func (s *Store) readCorrectionIntent(id string) (*correctionIntent, error) {
	path := s.correctionIntentPath(id)
	if err := validatePhysicalPath(path, false); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var in correctionIntent
	if err := strictYAML(b, &in); err != nil {
		return nil, err
	}
	if err := in.validate(id); err != nil {
		return nil, err
	}
	return &in, nil
}

func (s *Store) persistCorrectionIntent(in correctionIntent) error {
	if err := in.validate(in.Mission); err != nil {
		return err
	}
	path := s.correctionIntentPath(in.Mission)
	if err := layout.EnsureLocalDir(s.policy, filepath.Dir(path)); err != nil {
		return err
	}
	b, err := yaml.Marshal(in)
	if err != nil {
		return err
	}
	b, err = guardWrite(path, b)
	if err != nil {
		return err
	}
	w, err := localReplace(s.policy, path, b)
	return errors.Join(err, w.DurabilityError)
}

func (s *Store) applySharedCorrection(c *Contract, correction Correction) error {
	if c.RoundDecisions == nil {
		// A mission whose crossed-round history cannot be imported is fenced:
		// no lifecycle write may add authority beside history that is unknown.
		probe := *c
		if err := s.importLegacyDecisions(&probe); err != nil {
			return fmt.Errorf("mission %q is fenced from lifecycle writes: %w", c.MissionID, err)
		}
	}
	a, err := s.readCorrections(c)
	if err != nil {
		return err
	}
	if a == nil {
		if c.RoundDecisions != nil {
			return fmt.Errorf("corrections authority unavailable; missing is not an initialized empty history")
		}
		a, err = s.importLegacyCorrections(c)
		if err != nil {
			return err
		}
	}
	in, err := s.readCorrectionIntent(c.MissionID)
	if err != nil {
		return err
	}
	if in != nil {
		if !reflect.DeepEqual(in.Entry.Correction, correction) {
			return fmt.Errorf("pending correction conflicts with new request; evidence retained")
		}
		for _, entry := range a.Entries {
			if entry.ID != in.Entry.ID {
				continue
			}
			if !reflect.DeepEqual(entry, in.Entry) {
				return fmt.Errorf("correction receipt conflicts with shared authority")
			}
			if err := s.confirmAcceptedCorrection(*in); err != nil {
				return err
			}
			return s.removeCorrectionIntent(c.MissionID)
		}
	} else {
		op, err := newOperationID()
		if err != nil {
			return err
		}
		session := s.resolveSessionID()
		if session == "" {
			session = sessionlessID
		}
		in = &correctionIntent{Version: 1, Mission: c.MissionID, Entry: CorrectionEntry{op, time.Now().UTC().Format(time.RFC3339), correction}, EventSession: session,
			Event: Event{Event: EventCorrect, Actor: correction.Author, Details: correctionDetails(correction), OperationID: op, Record: s.recordMode()}}
	}
	if err := s.deliverOperation(c.MissionID, in.EventSession, &in.Event, func() error { return s.persistCorrectionIntent(*in) }); err != nil {
		return fmt.Errorf("correct: required event failed; correction not installed: %w", err)
	}
	a.Entries = append(a.Entries, in.Entry)
	b, _, err := RedactedYAMLNotify(s.repoRoot, *a)
	if err != nil {
		return err
	}
	b, err = guardWrite(s.correctionsPath(c.MissionID), b)
	if err != nil {
		return err
	}
	w, err := localReplace(s.policy, s.correctionsPath(c.MissionID), b)
	if err != nil {
		return err
	}
	if w.DurabilityError != nil {
		return fmt.Errorf("correction applied but crash durability unconfirmed; receipt retained: %w", w.DurabilityError)
	}
	if err := s.removeCorrectionIntent(c.MissionID); err != nil {
		return fmt.Errorf("correction applied; receipt cleanup pending: %w", err)
	}
	return nil
}

func (s *Store) removeCorrectionIntent(id string) error {
	path := s.correctionIntentPath(id)
	if _, err := ownedSyncRoot(s.policy, path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncOwnedAncestors(s.policy, path)
}
