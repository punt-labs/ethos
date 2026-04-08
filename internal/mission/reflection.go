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

// Reflection is the typed handoff between round N and round N+1.
//
// Phase 3.4 makes Budget.Rounds enforceable: the round-advance gate
// refuses to begin round N+1 until round N's reflection is on disk
// and recommends continue or pivot. The reflection is structured
// data, not free prose — the recommendation is an enum, the signals
// are a list, and round numbers are integers — so the gate can act
// on it without parsing English.
//
// The reflection is append-only by construction: ReflectionStore.Append
// refuses to overwrite an existing round's reflection.
type Reflection struct {
	// Round is the round this reflection covers (the round that just
	// ended). Reflections are 1-indexed: the reflection submitted at
	// the end of round 1 has Round == 1.
	Round int `yaml:"round" json:"round"`

	// CreatedAt is the RFC3339 timestamp at which the leader recorded
	// this reflection. Set by ReflectionStore.Append, not the caller.
	CreatedAt string `yaml:"created_at" json:"created_at"`

	// Author is the handle of the leader recording the reflection.
	Author string `yaml:"author" json:"author"`

	// Converging reports whether the work in round N appears to be
	// approaching success. A reflection that says converging=false
	// with recommendation=continue is the leader explicitly choosing
	// "give it one more try"; the gate still allows the advance, but
	// the field is on the record so a later post-mortem can find it.
	Converging bool `yaml:"converging" json:"converging"`

	// Signals are the observations that drove the recommendation.
	// Each signal is a single short line (e.g. "tests still failing
	// in cmd/ethos/mission_test.go"). At least one signal is required
	// so the reflection cannot degenerate into a checkbox.
	Signals []string `yaml:"signals" json:"signals"`

	// Recommendation is the typed action the next round must respect.
	// The round-advance gate reads this field directly:
	//   continue → advance permitted
	//   pivot    → advance permitted (the worker takes a different
	//              approach in round N+1; the change is captured in
	//              the new round's instructions, not here)
	//   stop     → advance refused; the mission must be closed
	//   escalate → advance refused; the mission must be re-scoped or
	//              escalated to the human operator
	Recommendation string `yaml:"recommendation" json:"recommendation"`

	// Reason is the prose justification for the recommendation. It
	// is surfaced verbatim in the round-advance gate's refusal
	// message when the recommendation is stop or escalate, so the
	// operator sees the leader's own words rather than a generic
	// "advance refused".
	Reason string `yaml:"reason" json:"reason"`
}

// Recommendation enum values. The set is closed; Validate rejects
// anything else. The names are deliberately short — they appear in
// gate error messages and in the event log.
const (
	RecommendationContinue = "continue"
	RecommendationPivot    = "pivot"
	RecommendationStop     = "stop"
	RecommendationEscalate = "escalate"
)

var validRecommendations = map[string]bool{
	RecommendationContinue: true,
	RecommendationPivot:    true,
	RecommendationStop:     true,
	RecommendationEscalate: true,
}

// IsTerminalRecommendation reports whether r is a recommendation that
// the round-advance gate must refuse to advance through. Stop and
// escalate are terminal; continue and pivot are advance-permitting.
//
// Exposed so the store and the CLI can share one definition of
// "blocks the next round" instead of duplicating the enum check.
func IsTerminalRecommendation(r string) bool {
	return r == RecommendationStop || r == RecommendationEscalate
}

// Validate checks that a Reflection is well-formed enough to persist.
// Called by ReflectionStore.Append before any disk write, and
// defensively on every read.
//
// Validation rules (must match the numbered list below exactly):
//  1. round is in [1, maxRounds]
//  2. author is non-empty and contains no control characters
//  3. recommendation is one of {continue, pivot, stop, escalate}
//  4. signals has at least one entry; each entry is non-empty and
//     contains no control characters
//  5. reason is non-empty when recommendation is stop or escalate
//     (the gate surfaces it verbatim; an empty reason produces a
//     refusal message with no actionable content)
//  6. created_at, when set, is parseable as RFC3339
//
// Validate does NOT check that round number relates to a contract's
// budget — that's the round-advance gate's job, where the contract is
// in scope.
func (r *Reflection) Validate() error {
	if r == nil {
		return fmt.Errorf("reflection is nil")
	}
	if r.Round < minRounds || r.Round > maxRounds {
		return fmt.Errorf("invalid round %d: must be in [%d, %d]", r.Round, minRounds, maxRounds)
	}
	if strings.TrimSpace(r.Author) == "" {
		return fmt.Errorf("author is required")
	}
	if containsControlChar(r.Author) {
		return fmt.Errorf("author contains control character")
	}
	if !validRecommendations[r.Recommendation] {
		return fmt.Errorf("invalid recommendation %q: must be one of continue, pivot, stop, escalate", r.Recommendation)
	}
	if len(r.Signals) == 0 {
		return fmt.Errorf("signals must contain at least one entry")
	}
	for i, s := range r.Signals {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return fmt.Errorf("signals[%d]: entry cannot be empty or whitespace", i)
		}
		if containsControlChar(s) {
			return fmt.Errorf("signals[%d]: entry contains control character", i)
		}
	}
	if IsTerminalRecommendation(r.Recommendation) && strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("reason is required when recommendation is %q", r.Recommendation)
	}
	if r.CreatedAt != "" {
		if _, err := time.Parse(time.RFC3339, r.CreatedAt); err != nil {
			return fmt.Errorf("invalid created_at %q: %w", r.CreatedAt, err)
		}
	}
	return nil
}

// DecodeReflectionStrict parses a YAML reflection with strict rules:
// every field must be known to the Reflection struct, and exactly one
// YAML document must be present. Symmetric with DecodeContractStrict.
//
// The label argument is a human-readable identifier (file path) used
// in error messages.
func DecodeReflectionStrict(data []byte, label string) (*Reflection, error) {
	var r Reflection
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("invalid reflection %s: %w", label, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("invalid reflection %s: multiple YAML documents are not allowed", label)
		}
		return nil, fmt.Errorf("invalid reflection %s: trailing content after first document: %w", label, err)
	}
	return &r, nil
}
