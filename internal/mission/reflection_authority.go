package mission

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"gopkg.in/yaml.v3"
)

// OperationResult distinguishes filesystem acceptance from crash durability and
// optional post-acceptance trace work. The required event is never optional.
type OperationResult struct {
	Application string   `json:"application"`
	Durability  string   `json:"durability"`
	Trace       string   `json:"trace"`
	Diagnostics []string `json:"diagnostics,omitempty"`
}

type reflectionIntent struct {
	Version      int           `yaml:"version"`
	Kind         string        `yaml:"kind" redact:"keep"`
	Mission      string        `yaml:"mission" redact:"keep"`
	Decision     RoundDecision `yaml:"decision"`
	Reflection   Reflection    `yaml:"reflection"`
	Event        Event         `yaml:"event"`
	EventSession string        `yaml:"event_session" redact:"session"`
	Publish      bool          `yaml:"publish"`
}

func newOperationID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("allocating operation id: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

func strictYAML(data []byte, out any) error {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	if err := rejectDuplicateYAMLKeys(&node); err != nil {
		return err
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("multiple documents or trailing YAML content: %v", err)
	}
	return nil
}

func (s *Store) intentPath(id string, round int) string {
	return filepath.Join(s.policy.LocalRoot(), "missions", id, "reflection-intents", fmt.Sprintf("%d.yaml", round))
}

var (
	errOptionalReflectionConflict = errors.New("optional reflection detail conflict")
	errAuthorityRead              = errors.New("mission authority unreadable")
)

func (s *Store) readRoundIntent(id string, round int) (*reflectionIntent, error) {
	in, err := readIntent(s.intentPath(id, round), id)
	if err == nil && in != nil && in.Decision.Round != round {
		return nil, fmt.Errorf("reflection receipt round disagrees with its path")
	}
	return in, err
}

func (s *Store) reflectionArchivePath(id string, round int) string {
	return filepath.Join(s.policy.LocalRoot(), "trace", "missions", id, fmt.Sprintf("reflection-%d.yaml", round))
}

func reflectionEvent(r Reflection, id, record string) Event {
	return Event{Event: "reflect", Actor: r.Author, OperationID: id, Record: record,
		Details: map[string]any{"round": r.Round, "recommendation": r.Recommendation,
			"converging": r.Converging, "signal_count": len(r.Signals)}}
}

func sameJSON(a, b any) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

// matchesPreparedEvent validates the envelope but compares raw canonical JSON,
// not the decoded Event: float rounding and case-insensitive struct keys must
// not make a different required payload equal to the prepared receipt.
func matchesPreparedEvent(raw []byte, prepared Event) (bool, error) {
	if _, err := decodeRepositoryEventLine(raw); err != nil {
		return false, fmt.Errorf("validating required event: %w", err)
	}
	delivered, err := audit.CanonicalPayload(raw)
	if err != nil {
		return false, fmt.Errorf("canonicalizing required event: %w", err)
	}
	data, err := json.Marshal(prepared)
	if err != nil {
		return false, fmt.Errorf("serializing prepared event: %w", err)
	}
	expected, err := audit.CanonicalPayload(data)
	if err != nil {
		return false, fmt.Errorf("canonicalizing prepared event: %w", err)
	}
	return bytes.Equal(delivered, expected), nil
}

func (in reflectionIntent) validate(id string) error {
	r, d := in.Reflection, in.Decision
	if in.Version != 1 || in.Kind != "reflect" || in.Mission != id || r.Mission != id {
		return fmt.Errorf("invalid reflection receipt identity/version")
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if !operationIDPattern.MatchString(d.ID) || d.Round != r.Round || d.Author != r.Author || d.Recommendation != r.Recommendation || d.CreatedAt != r.CreatedAt || r.CreatedAt == "" {
		return fmt.Errorf("reflection receipt and decision disagree")
	}
	if err := (audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: in.EventSession}).Validate(); err != nil {
		return err
	}
	expected := reflectionEvent(r, d.ID, "blame")
	if in.Publish {
		expected.Record = "full"
	}
	expected.TS = in.Event.TS
	if _, err := audit.ParseLineTS(in.Event.TS); err != nil {
		return err
	}
	if !sameJSON(in.Event, expected) {
		return fmt.Errorf("reflection receipt event disagrees with payload")
	}
	return nil
}

func readIntent(path, id string) (*reflectionIntent, error) {
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
	var in reflectionIntent
	if err := strictYAML(b, &in); err != nil {
		return nil, fmt.Errorf("unreadable reflection receipt: %w", err)
	}
	if err := in.validate(id); err != nil {
		return nil, fmt.Errorf("invalid reflection receipt: %w", err)
	}
	return &in, nil
}

func (s *Store) persistIntent(in reflectionIntent) error {
	if err := in.validate(in.Mission); err != nil {
		return err
	}
	path := s.intentPath(in.Mission, in.Decision.Round)
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

// reconcileReflection is a mutation fence, not redo: it never installs a
// decision. A matching accepted receipt may finish optional materialization.
func (s *Store) reconcileReflection(id string) error {
	return s.reconcileReflectionsExcept(id, 0)
}

// Per-round receipts retain the only durable full payload independently of later
// submissions. An accepted receipt's optional archive/publication work is not a
// lifecycle gate; unresolved preparation or conflicting required evidence is.
func (s *Store) reconcileReflectionsExcept(id string, retryRound int) error {
	return s.reconcileReflectionsExceptWithWarnings(id, retryRound, nil)
}

// A caller holding the mission lock may collect optional trace warnings without
// shared Store state. A nil callback preserves the existing stderr diagnostic.
func (s *Store) reconcileReflectionsExceptWithWarnings(id string, retryRound int, warn func(string)) error {
	var c *Contract
	for round := 1; round <= 10; round++ {
		if round == retryRound {
			continue
		}
		in, err := s.readRoundIntent(id, round)
		if err != nil {
			return err
		}
		if in == nil {
			continue
		}
		if c == nil {
			c, err = s.Load(id)
			if err != nil {
				return fmt.Errorf("%w: %w", errAuthorityRead, err)
			}
		}
		d := c.decision(round)
		if d == nil {
			return fmt.Errorf("mission %q has pending reflection preparation for round %d that is not yet in shared authority; no other mutation is accepted until it is resolved", id, round)
		}
		if *d != in.Decision {
			return fmt.Errorf("reflection receipt conflicts with shared decision; evidence retained")
		}
		if err := s.confirmAcceptedReflection(*in); err != nil {
			return err
		}
		if err := s.finishReflection(*in); err != nil {
			message := fmt.Sprintf("mission %s round %d accepted; optional reflection trace pending: %v", id, round, err)
			if warn != nil {
				warn(message)
			} else {
				fmt.Fprintln(os.Stderr, "ethos: "+message)
			}
		}
	}
	return nil
}

func (s *Store) confirmAcceptedReflection(in reflectionIntent) error {
	line, err := audit.FindOperation(s.policy, in.Mission, in.Decision.ID)
	if err != nil {
		return err
	}
	if line == nil {
		return fmt.Errorf("accepted reflection required event unavailable; receipt retained")
	}
	matching, err := matchesPreparedEvent(line.Raw, in.Event)
	if err != nil {
		return err
	}
	if line.Session != in.EventSession || !matching {
		return fmt.Errorf("accepted reflection required event conflicts with receipt")
	}
	stream := audit.Stream{Namespace: audit.MissionNS, Mission: in.Mission, Session: in.EventSession}
	if err := audit.WithStreamLock(s.policy, stream, func() error { return confirmStreamDurability(s.policy, stream) }); err != nil {
		return err
	}
	return s.confirmDecisionDurability(in.Mission)
}

func (s *Store) confirmDecisionDurability(id string) error {
	path, err := s.contractPath(id)
	if err != nil {
		return err
	}
	return syncOwnedAncestors(s.policy, path)
}

// ApplyReflection prepares durable full detail, durably appends the required
// event using its original stream and timestamp, then installs shared authority.
func (s *Store) ApplyReflection(id string, r *Reflection) (OperationResult, error) {
	out := OperationResult{Application: "not_applied", Durability: "not_applicable", Trace: "complete"}
	if !s.twoTreeStorage {
		return out, fmt.Errorf("ApplyReflection requires repository storage")
	}
	if r == nil {
		return out, fmt.Errorf("reflection is nil")
	}
	staged := *r
	staged.Author = strings.TrimSpace(staged.Author)
	if err := staged.Validate(); err != nil {
		return out, fmt.Errorf("invalid reflection: %w", err)
	}
	var err error
	staged, _, err = RedactRecordNotify(s.repoRoot, staged)
	if err != nil {
		return out, err
	}
	if err := staged.Validate(); err != nil {
		return out, fmt.Errorf("invalid redacted reflection: %w", err)
	}
	err = s.withMissionLock(id, true, func() error {
		if err := s.reconcileReflectionsExceptWithWarnings(id, staged.Round, func(message string) {
			out.Diagnostics = append(out.Diagnostics, message)
		}); err != nil {
			return err
		}
		if err := s.reconcileCorrection(id); err != nil {
			return err
		}
		c, _, err := s.loadLocked(id)
		if err != nil {
			return fmt.Errorf("%w: %w", errAuthorityRead, err)
		}
		layer, err := s.resolveLayer(id)
		if err != nil {
			return err
		}
		if layer != layerRepo {
			return fmt.Errorf("reflection authority requires a repository-owned mission; this mission is still in the global store and is fenced from lifecycle writes")
		}
		if staged.Mission != id {
			return fmt.Errorf("reflection mission %q does not match target mission %q", staged.Mission, id)
		}
		in, err := s.readRoundIntent(id, staged.Round)
		if err != nil {
			return err
		}
		if in == nil && c.decision(staged.Round) != nil {
			in, err = readIntent(s.reflectionArchivePath(id, staged.Round), id)
			if err != nil {
				return err
			}
		}
		if in != nil {
			if staged.CreatedAt == "" {
				staged.CreatedAt = in.Reflection.CreatedAt
			}
			if !reflect.DeepEqual(staged, in.Reflection) {
				return fmt.Errorf("different reflection request conflicts with retained preparation/detail; evidence retained")
			}
			if d := c.decision(staged.Round); d != nil {
				if *d != in.Decision {
					return fmt.Errorf("reflection receipt conflicts with shared decision")
				}
				out.Application, out.Durability = "already_applied", "confirmed"
				if err := s.confirmAcceptedReflection(*in); err != nil {
					out.Durability, out.Trace = "unconfirmed", "pending"
					out.Diagnostics = append(out.Diagnostics, err.Error())
					return nil
				}
				if err := s.finishReflection(*in); err != nil {
					out.Trace = "pending"
					out.Diagnostics = append(out.Diagnostics, err.Error())
				}
				r.CreatedAt = staged.CreatedAt
				return nil
			}
		}
		if c.Status != StatusOpen {
			return fmt.Errorf("mission %q is in terminal state %q; reflections are accepted only on open missions", id, c.Status)
		}
		if staged.Round != c.CurrentRound {
			return fmt.Errorf("reflection round %d does not match mission %q current round %d", staged.Round, id, c.CurrentRound)
		}
		if c.decision(staged.Round) != nil {
			return fmt.Errorf("round %d already decided; full reflection detail unavailable here, cannot certify equality or accept replacement", staged.Round)
		}
		if c.RoundDecisions == nil {
			if err := s.importLegacyDecisions(c); err != nil {
				return err
			}
			if c.decision(staged.Round) != nil {
				return fmt.Errorf("reflection for round %d already exists; reflections are append-only", staged.Round)
			}
		}
		if in == nil {
			if staged.CreatedAt == "" {
				staged.CreatedAt = time.Now().UTC().Format(time.RFC3339)
			}
			op, err := newOperationID()
			if err != nil {
				return err
			}
			session := s.resolveSessionID()
			if session == "" {
				session = sessionlessID
			}
			in = &reflectionIntent{Version: 1, Kind: "reflect", Mission: id,
				Decision:   RoundDecision{op, staged.Round, staged.CreatedAt, staged.Author, staged.Recommendation},
				Reflection: staged, Event: reflectionEvent(staged, op, s.recordMode()), EventSession: session, Publish: s.policy.RecordMode() == layout.RecordFull}
		}
		if err := s.deliverReflection(in); err != nil {
			out.Trace = "pending"
			return fmt.Errorf("reflect: required event not confirmed; decision not installed: %w", err)
		}
		if err := s.persistenceCheckpoint("reflect_event_delivered"); err != nil {
			out.Trace = "pending"
			return err
		}
		updated := *c
		updated.RoundDecisions = &RoundDecisions{Version: 1, Entries: append(append([]RoundDecision{}, c.RoundDecisions.Entries...), in.Decision)}
		updated.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := s.validateContract(&updated); err != nil {
			return err
		}
		data, _, err := RedactedYAMLNotify(s.repoRoot, updated, func(red Contract) error {
			if !reflect.DeepEqual(red.RoundDecisions, updated.RoundDecisions) || red.Budget != updated.Budget || red.Evaluator != updated.Evaluator {
				return fmt.Errorf("redaction changed frozen authority")
			}
			return s.validateContract(&red)
		})
		if err != nil {
			return err
		}
		path, err := s.contractPath(id)
		if err != nil {
			return err
		}
		data, err = guardWrite(path, data)
		if err != nil {
			return err
		}
		w, err := localReplace(s.policy, path, data)
		if err != nil {
			out.Trace = "pending"
			return err
		}
		out.Application, out.Durability = "applied", "confirmed"
		r.CreatedAt = in.Reflection.CreatedAt
		w.DurabilityError = errors.Join(w.DurabilityError, s.persistenceCheckpoint("reflect_decision_installed"))
		if w.DurabilityError != nil {
			out.Durability, out.Trace = "unconfirmed", "pending"
			out.Diagnostics = append(out.Diagnostics, "shared decision installed; directory durability unconfirmed: "+w.DurabilityError.Error())
			return nil // Retain intent; do not claim rollback.
		}
		if err := s.finishReflection(*in); err != nil {
			out.Trace = "pending"
			out.Diagnostics = append(out.Diagnostics, err.Error())
		}
		return nil
	})
	if errors.Is(err, errAuthorityRead) {
		out.Application = "unknown"
	}
	return out, err
}

func (s *Store) deliverReflection(in *reflectionIntent) error {
	return s.deliverOperation(in.Mission, in.EventSession, &in.Event, func() error { return s.persistIntent(*in) })
}

func (s *Store) persistenceCheckpoint(phase string) error {
	if s.persistenceCheck != nil {
		return s.persistenceCheck(phase)
	}
	return nil
}

func (s *Store) deliverOperation(id, session string, event *Event, persist func() error) error {
	stream := audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: session}
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
			if _, err := decodeEventLine(l.Raw); err != nil {
				return err
			}
		}
		// The caller holds the mission lock before this stream lock. FindOperation
		// takes no locks: inspect every session/layer before certifying a match,
		// without acquiring another stream lock or accepting the first candidate.
		line, err := audit.FindOperation(s.policy, id, event.OperationID)
		if err != nil {
			return err
		}
		if line != nil {
			matching, err := matchesPreparedEvent(line.Raw, *event)
			if err != nil {
				return err
			}
			if line.Session != session || !matching {
				return fmt.Errorf("operation id has divergent event payload or session")
			}
			return confirmStreamDurability(s.policy, stream)
		}
		live := stream.LivePath(s.policy)
		if err := layout.EnsureLocalDir(s.policy, filepath.Dir(live)); err != nil {
			return err
		}
		if err := rejectSymlink(live); err != nil {
			return err
		}
		now := time.Now().UTC()
		if event.TS != "" {
			now, err = time.Parse(time.RFC3339Nano, event.TS)
			if err != nil {
				return err
			}
		}
		_, err = audit.AppendMonotonic(live, floor, now, func(ts int64) ([]byte, error) {
			allocated := audit.FormatLineTS(ts)
			if event.TS != "" && event.TS != allocated {
				return nil, fmt.Errorf("pending operation timestamp overtaken; refusing reordered replay")
			}
			event.TS = allocated
			if err := persist(); err != nil {
				return nil, err
			}
			if err := s.persistenceCheckpoint("event_prepared"); err != nil {
				return nil, err
			}
			b, err := json.Marshal(*event)
			if err != nil {
				return nil, err
			}
			return guardWrite(live, b)
		})
		if err != nil {
			return err
		}
		return syncOwnedAncestors(s.policy, live)
	})
}

func (s *Store) finishReflection(in reflectionIntent) error {
	path := s.intentPath(in.Mission, in.Decision.Round)
	// Refuse redirected recovery evidence before any optional materialization.
	if _, err := ownedSyncRoot(s.policy, path); err != nil {
		return err
	}
	if err := s.persistenceCheckpoint("reflect_archive"); err != nil {
		return err
	}
	archive := s.reflectionArchivePath(in.Mission, in.Decision.Round)
	if err := layout.EnsureLocalDir(s.policy, filepath.Dir(archive)); err != nil {
		return err
	}
	if old, err := readIntent(archive, in.Mission); err != nil {
		return err
	} else if old != nil && !sameJSON(old, in) {
		return fmt.Errorf("%w: reflection archive conflicts with receipt", errOptionalReflectionConflict)
	}
	b, err := yaml.Marshal(in)
	if err != nil {
		return err
	}
	b, err = guardWrite(archive, b)
	if err != nil {
		return err
	}
	w, err := localReplace(s.policy, archive, b)
	if err := errors.Join(err, w.DurabilityError); err != nil {
		return err
	}
	if in.Publish {
		if err := s.persistenceCheckpoint("reflect_publish"); err != nil {
			return err
		}
		path := filepath.Join(s.policy.CheckoutRecordRoot(), "missions", in.Mission, "reflections.yaml")
		var rs []Reflection
		b, err := os.ReadFile(path)
		if err == nil {
			rs, err = decodeReflectionsFile(b, in.Mission)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		found := false
		for _, r := range rs {
			if r.Round == in.Reflection.Round {
				if !reflect.DeepEqual(r, in.Reflection) {
					return fmt.Errorf("%w: published reflection conflicts with accepted detail", errOptionalReflectionConflict)
				}
				found = true
			}
		}
		if !found {
			rs = append(rs, in.Reflection)
		}
		sort.Slice(rs, func(i, j int) bool { return rs[i].Round < rs[j].Round })
		b, err = yaml.Marshal(reflectionsFile{Reflections: rs})
		if err != nil {
			return err
		}
		b, err = guardWrite(path, b)
		if err != nil {
			return err
		}
		w, err = localReplace(s.policy, path, b)
		if err := errors.Join(err, w.DurabilityError); err != nil {
			return err
		}
	}
	// Validate the deletion precondition before removal, not only during sync.
	if _, err := ownedSyncRoot(s.policy, path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncOwnedAncestors(s.policy, path)
}

// Legacy import is strict and complete for the lifecycle prefix. Absence of
// crossed-round detail is unknown history, not permission to initialize empty.
//
// A decision ID is LegacyDecisionID of the reflection as it stood when the
// decision was imported. It is stored in the contract. The scrub-records step
// leaves reflections.yaml alone once decisions exist, so the file still hashes
// to the stored IDs.
func (s *Store) importLegacyDecisions(c *Contract) error {
	rs, _, err := s.loadReflectionsBytes(c.MissionID)
	if err != nil {
		return err
	}
	d := &RoundDecisions{Version: 1, Entries: []RoundDecision{}}
	for _, r := range rs {
		id := LegacyDecisionID(r)
		if id == "" {
			return fmt.Errorf("deriving decision id for reflection round %d", r.Round)
		}
		d.Entries = append(d.Entries, RoundDecision{id, r.Round, r.CreatedAt, r.Author, r.Recommendation})
	}
	c.RoundDecisions = d
	if err := c.validateDecisions(); err != nil {
		return fmt.Errorf("legacy reflection history incomplete or inconsistent: %w", err)
	}
	return nil
}
