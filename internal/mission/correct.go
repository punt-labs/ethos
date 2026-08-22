package mission

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// EventCorrect is the event type Store.Correct appends. Named for the
// same reason EventWriteSetReleased is: both the writer and any
// future reader (mission log rendering, doctor's hand-edit check)
// need the exact string without copying it by hand.
const EventCorrect = "correct"

// CorrectionKind is a closed enum distinguishing what kind of record
// a Correction is. The three values are different in kind, not just
// in wording: factual and fabrication both quote a Claim that turned
// out wrong (one true when written, one never true); decision has no
// prior claim to quote at all — it records what the leader decided
// after an escalation. Collapsing all three into free-text would make
// "was any mission's result found fabricated" unanswerable by
// anything but grepping prose.
type CorrectionKind string

const (
	// CorrectionFactual marks a claim that was true when written and
	// is false now — a fact discovered afterward, not a lie at close
	// time.
	CorrectionFactual CorrectionKind = "factual"
	// CorrectionFabrication marks a claim that was never true — an
	// integrity finding, not a stale fact.
	CorrectionFabrication CorrectionKind = "fabrication"
	// CorrectionDecision marks a decision record with no prior claim
	// to correct — the leader's ruling after an escalation, filed as
	// a correction rather than a new mission-lifecycle transition.
	CorrectionDecision CorrectionKind = "decision"
)

var validCorrectionKinds = map[CorrectionKind]bool{
	CorrectionFactual:     true,
	CorrectionFabrication: true,
	CorrectionDecision:    true,
}

// Correction is an additive-only annotation on a closed mission,
// recorded as a "correct" event on the mission's existing append-only
// event log — never a new file, never a rewrite of contract.yaml,
// results.yaml, or reflections.yaml. See DES-072.
type Correction struct {
	// Mission is the full mission ID the correction covers. Must
	// match the missionID passed to Store.Correct — the same
	// cross-check AppendResult runs for Result.Mission.
	Mission string `yaml:"mission" json:"mission"`

	// Round is the round being corrected. 0 is the whole-mission
	// sentinel; any other value must be <= the mission's
	// CurrentRound — a correction cannot cite a round that never ran.
	Round int `yaml:"round" json:"round"`

	// Kind is required and closed: factual, fabrication, or decision.
	Kind CorrectionKind `yaml:"kind" json:"kind"`

	// Author is the identity handle of whoever is filing the
	// correction. Unlike Result.Author (a bookkeeping field), WHO
	// says the record on file is wrong is load-bearing here — see
	// ValidateCorrectionAuthor.
	Author string `yaml:"author" json:"author"`

	// Claim quotes or closely paraphrases the thing being corrected.
	// Required for factual and fabrication; empty for decision, which
	// has no prior claim to quote.
	Claim string `yaml:"claim,omitempty" json:"claim,omitempty"`

	// Corrected is what's actually true, or what was decided.
	// Required for every kind — a correction with nothing corrected
	// is not a correction.
	Corrected string `yaml:"corrected" json:"corrected"`

	// Supersedes optionally references a prior correction this one
	// supersedes.
	Supersedes string `yaml:"supersedes,omitempty" json:"supersedes,omitempty"`

	// Evidence is optional, same shape as Result.Evidence.
	Evidence []EvidenceCheck `yaml:"evidence,omitempty" json:"evidence,omitempty"`
}

// Validate checks that a Correction is well-formed enough to persist.
// Called by Store.Correct before any disk I/O.
//
// Validate does NOT check Round against a specific contract's
// CurrentRound (the contract is not in scope here) and does NOT
// resolve Author to a real identity (that needs a live identity
// store — see ValidateCorrectionAuthor, which CLI and MCP callers run
// before Store.Correct). Those are the store's and the caller's job,
// respectively; Validate enforces only the per-field structural rules
// that are independent of both.
func (c *Correction) Validate() error {
	if c == nil {
		return fmt.Errorf("correction is nil")
	}
	if !missionIDPattern.MatchString(c.Mission) {
		return fmt.Errorf("invalid mission %q: must match m-YYYY-MM-DD-NNN", c.Mission)
	}
	if c.Round < 0 {
		return fmt.Errorf("invalid round %d: must be >= 0 (0 = whole-mission)", c.Round)
	}
	if !validCorrectionKinds[c.Kind] {
		return fmt.Errorf("invalid kind %q: must be one of factual, fabrication, decision", c.Kind)
	}
	if strings.TrimSpace(c.Author) == "" {
		return fmt.Errorf("author is required")
	}
	if containsControlChar(c.Author) {
		return fmt.Errorf("author contains control character")
	}
	if c.Kind != CorrectionDecision && strings.TrimSpace(c.Claim) == "" {
		return fmt.Errorf("claim is required unless kind is decision")
	}
	if c.Claim != "" && containsProseControlChar(c.Claim) {
		return fmt.Errorf("claim contains control character")
	}
	if strings.TrimSpace(c.Corrected) == "" {
		return fmt.Errorf("corrected is required")
	}
	if containsProseControlChar(c.Corrected) {
		return fmt.Errorf("corrected contains control character")
	}
	if c.Supersedes != "" && containsControlChar(c.Supersedes) {
		return fmt.Errorf("supersedes contains control character")
	}
	for i, e := range c.Evidence {
		if err := validateEvidenceCheck(e); err != nil {
			return fmt.Errorf("evidence[%d]: %w", i, err)
		}
	}
	return nil
}

// ValidateCorrectionAuthor resolves a correction's Author against a
// live identity store and returns an error unless it names a real,
// fully-resolved identity. Store.Correct cannot do this itself — the
// Store carries no identity-store dependency, the same reason
// Store.ApplyServerFields takes HashSources as an explicit parameter
// rather than a field. CLI and MCP callers run this before
// Store.Correct, exactly the division of labor ApplyServerFields uses
// for evaluator resolution at Create time.
func ValidateCorrectionAuthor(author string, loader IdentityLoader) error {
	author = strings.TrimSpace(author)
	if loader == nil {
		return fmt.Errorf("identity loader not configured; cannot resolve correction author %q", author)
	}
	if _, err := loader.LoadEvaluator(author); err != nil {
		return fmt.Errorf("correction author %q does not resolve to a valid identity: %w", author, err)
	}
	return nil
}

// Correct records a correction against a closed mission. The guard is
// the inverse of every other write path in this package: Correct
// refuses an OPEN mission (its story isn't finished — use
// Update/Reflect instead) and accepts every terminal status. Correct
// never touches contract.yaml, results.yaml, reflections.yaml, or any
// of the Mission schema's fields — the operation's entire effect is
// one appendEventLocked call with Event: "correct". This is why
// Correct cannot falsify the TerminalIsFinal theorem: there is no
// Mission value in scope for it to mutate.
//
// Layer-consistent or refused, never silently mismatched: when this
// session writes to the per-repo two-tree layout, Correct refuses a
// mission that resolves to the legacy global layer rather than
// writing an event LoadEvents would never read back.
//
// Correct does NOT seal its own write — the mission package cannot
// import internal/hook (internal/hook already imports internal/mission,
// so the reverse would cycle). Callers seal via hook.SealMission after
// a successful Correct, mirroring the existing Close/Abandon pattern in
// cmd/ethos/mission.go. LoadEvents already reads the union of sealed
// chunks and each session's live tail, so a correction is readable
// immediately regardless of when the next seal runs — sealing only
// affects whether the correction has reached git yet, not whether a
// fresh Store can read it back.
func (s *Store) Correct(missionID string, c Correction) error {
	staged := c
	staged.Author = strings.TrimSpace(staged.Author)
	if err := staged.Validate(); err != nil {
		return fmt.Errorf("invalid correction: %w", err)
	}
	return s.withLock(missionID, func() error {
		if s.twoTreeStorage && s.repoRoot != "" {
			layer, err := s.resolveLayer(missionID)
			if err != nil {
				return err
			}
			if layer != layerRepo {
				return fmt.Errorf("cannot correct a global-layer mission from a repo-layer session")
			}
		}
		mc, _, err := s.loadLocked(missionID)
		if err != nil {
			return err
		}
		if mc.Status == StatusOpen {
			return fmt.Errorf(
				"mission %q is open; corrections apply only to closed missions", missionID)
		}
		if staged.Mission != missionID {
			return fmt.Errorf(
				"correction mission %q does not match target mission %q",
				staged.Mission, missionID)
		}
		if staged.Round > mc.CurrentRound {
			return fmt.Errorf(
				"correction round %d exceeds mission %q current round %d",
				staged.Round, missionID, mc.CurrentRound)
		}
		return s.appendEventLocked(missionID, Event{
			TS:      time.Now().UTC().Format(time.RFC3339),
			Event:   EventCorrect,
			Actor:   staged.Author,
			Details: correctionDetails(staged),
		})
	})
}

// correctionDetails builds the Event.Details map for a correction.
// kind, round, and corrected are always present; claim, supersedes,
// and evidence are included only when set, so a decision-kind
// correction (no claim) does not carry a misleading empty string.
func correctionDetails(c Correction) map[string]any {
	details := map[string]any{
		"kind":      string(c.Kind),
		"round":     c.Round,
		"corrected": c.Corrected,
	}
	if c.Claim != "" {
		details["claim"] = c.Claim
	}
	if c.Supersedes != "" {
		details["supersedes"] = c.Supersedes
	}
	if len(c.Evidence) > 0 {
		ev := make([]map[string]any, len(c.Evidence))
		for i, e := range c.Evidence {
			ev[i] = map[string]any{"name": e.Name, "status": e.Status}
		}
		details["evidence"] = ev
	}
	return details
}

// LoadCorrections returns every correction recorded for missionID, in
// the event log's on-disk (chronological) order. Missing mission or a
// log with no correct events -> empty slice, consistent with
// LoadResults and LoadReflections: the absence of any correction is
// the normal state for most missions.
func (s *Store) LoadCorrections(missionID string) ([]Correction, error) {
	events, _, err := s.LoadEvents(missionID)
	if err != nil {
		return nil, err
	}
	out := []Correction{}
	for _, e := range events {
		if e.Event != EventCorrect {
			continue
		}
		out = append(out, correctionFromEvent(missionID, e))
	}
	return out, nil
}

// DecodeCorrectionStrict parses a YAML correction with strict rules:
// every field must be known to the Correction struct, and exactly
// one YAML document must be present. Symmetric with
// DecodeResultStrict and DecodeReflectionStrict — the `ethos mission
// correct --file` and MCP `mission correct` entry points share it so
// the input trust boundary is enforced identically regardless of how
// the YAML reached the store.
//
// The label argument is a human-readable identifier (file path or
// request label) used in error messages.
func DecodeCorrectionStrict(data []byte, label string) (*Correction, error) {
	var c Correction
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid correction %s: %w", label, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid correction %s: multiple YAML documents are not allowed", label)
		}
		return nil, fmt.Errorf("invalid correction %s: trailing content after first document: %w", label, err)
	}
	return &c, nil
}

// correctionFromEvent reconstructs a Correction from a decoded "correct"
// Event. Details came from json.Marshal/Unmarshal round-trip (LoadEvents
// always decodes through JSON), so numeric fields decode as float64 and
// nested evidence entries decode as []any of map[string]any — the
// reconstruction is defensive: a field of the wrong shape is silently
// left at its zero value rather than erroring, since a rendering read
// path must not fail the whole mission log over one malformed field.
func correctionFromEvent(missionID string, e Event) Correction {
	c := Correction{Mission: missionID, Author: e.Actor}
	if v, ok := e.Details["kind"].(string); ok {
		c.Kind = CorrectionKind(v)
	}
	c.Round = int(detailFloat(e.Details, "round"))
	if v, ok := e.Details["claim"].(string); ok {
		c.Claim = v
	}
	if v, ok := e.Details["corrected"].(string); ok {
		c.Corrected = v
	}
	if v, ok := e.Details["supersedes"].(string); ok {
		c.Supersedes = v
	}
	if raw, ok := e.Details["evidence"].([]any); ok {
		for _, item := range raw {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, _ := m["name"].(string)
			status, _ := m["status"].(string)
			c.Evidence = append(c.Evidence, EvidenceCheck{Name: name, Status: status})
		}
	}
	return c
}

// detailFloat extracts a numeric value from an event Details map.
// JSON decoding produces float64; in-process construction (tests)
// may produce int.
func detailFloat(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return 0
	}
}
