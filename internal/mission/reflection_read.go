package mission

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

// ReflectionRead separates shared authority from optional forensic detail.
// Pending preparations never appear as accepted reflections.
type ReflectionRead struct {
	Round         int            `json:"round"`
	DecisionState string         `json:"decision_state"`
	Decision      *RoundDecision `json:"decision,omitempty"`
	DetailState   string         `json:"detail_state"`
	Reflection    *Reflection    `json:"reflection,omitempty"`
	Pending       bool           `json:"pending"`
	Diagnostics   []string       `json:"diagnostics,omitempty"`
}

// ReadReflections reports authority and detail availability for each round
// through the mission's current round, without initializing legacy authority.
func (s *Store) ReadReflections(id string) ([]ReflectionRead, error) {
	c, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	if c.RoundDecisions == nil {
		rs, _, err := s.loadReflectionsBytes(id)
		if err != nil {
			return nil, fmt.Errorf("reading legacy reflections for %q: %w", id, err)
		}
		// Prose cannot establish non-submission when shared authority was
		// never initialized. Include every relevant round, even when the
		// optional legacy file is missing, empty, or only partly retained.
		out := make([]ReflectionRead, c.CurrentRound)
		for i := range out {
			out[i] = ReflectionRead{Round: i + 1, DecisionState: "legacy_unknown", DetailState: "absent"}
		}
		for i := range rs {
			r := rs[i]
			if r.Round > c.CurrentRound {
				return nil, fmt.Errorf("legacy reflections for %q: round %d exceeds current round %d", id, r.Round, c.CurrentRound)
			}
			out[r.Round-1].DetailState = "present"
			out[r.Round-1].Reflection = &r
		}
		return out, nil
	}
	// Read each supported aggregate once. A corrupt optional detail source
	// never erases compact authority and never permits a replacement decision.
	var copies []Reflection
	var detailErrors []string
	paths := []string{filepath.Join(s.policy.RecordRoot(), "missions", id, "reflections.yaml")}
	if s.policy.CheckoutRecordRoot() != s.policy.RecordRoot() {
		paths = append(paths, filepath.Join(s.policy.CheckoutRecordRoot(), "missions", id, "reflections.yaml"))
	}
	for _, path := range paths {
		if err := rejectSymlink(path); err != nil {
			detailErrors = append(detailErrors, err.Error())
			continue
		}
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			detailErrors = append(detailErrors, err.Error())
			continue
		}
		rs, err := decodeReflectionsFile(b, id)
		if err != nil {
			detailErrors = append(detailErrors, err.Error())
			continue
		}
		copies = append(copies, rs...)
	}
	out := make([]ReflectionRead, 0, c.CurrentRound)
	for round := 1; round <= c.CurrentRound; round++ {
		pending, pendingErr := s.readRoundIntent(id, round)
		v := ReflectionRead{Round: round, DecisionState: "absent", DetailState: "absent"}
		v.Diagnostics = append(v.Diagnostics, detailErrors...)
		d := c.decision(round)
		v.Pending = pending != nil && pending.Decision.Round == round
		if pendingErr != nil {
			v.Diagnostics = append(v.Diagnostics, pendingErr.Error())
		}
		if d == nil {
			if len(v.Diagnostics) > 0 {
				v.DetailState = "unreadable"
			}
			out = append(out, v)
			continue
		}
		v.DecisionState, v.Decision, v.DetailState = "present", d, "unavailable_here"
		var candidates []Reflection
		for _, r := range copies {
			if r.Round == round {
				candidates = append(candidates, r)
			}
		}
		archivePath := s.reflectionArchivePath(id, round)
		archive, err := readIntent(archivePath, id)
		if err != nil {
			v.Diagnostics = append(v.Diagnostics, err.Error())
		}
		if archive != nil && archive.Decision.Round != round {
			v.Diagnostics = append(v.Diagnostics, fmt.Sprintf("reflection archive %q: requested round %d, actual round %d", archivePath, round, archive.Decision.Round))
			archive = nil
		}
		for _, receipt := range []*reflectionIntent{archive, pending} {
			if receipt == nil || receipt.Decision.Round != round {
				continue
			}
			if receipt.Decision != *d {
				v.Diagnostics = append(v.Diagnostics, "reflection receipt conflicts with shared decision")
				continue
			}
			candidates = append(candidates, receipt.Reflection)
		}
		for _, r := range candidates {
			if r.Round != d.Round || r.Author != d.Author || r.Recommendation != d.Recommendation || r.CreatedAt != d.CreatedAt {
				v.Diagnostics = append(v.Diagnostics, "reflection detail conflicts with shared decision")
				continue
			}
			if v.Reflection != nil && !reflect.DeepEqual(*v.Reflection, r) {
				v.Diagnostics = append(v.Diagnostics, "unequal reflection detail copies")
				continue
			}
			copy := r
			v.Reflection = &copy
			v.DetailState = "present"
		}
		if len(v.Diagnostics) > 0 {
			v.DetailState = "unreadable"
			v.Reflection = nil
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Store) acceptedReflections(id string) ([]Reflection, error) {
	states, err := s.ReadReflections(id)
	if err != nil {
		return nil, err
	}
	out := []Reflection{}
	for _, v := range states {
		if v.DecisionState == "legacy_unknown" && v.Reflection == nil {
			return nil, fmt.Errorf("mission %q round %d decision legacy_unknown; reflection detail %s cannot establish complete submission history", id, v.Round, v.DetailState)
		}
		if v.DetailState == "unreadable" || v.DetailState == "unavailable_here" {
			return nil, fmt.Errorf("mission %q round %d decision %s; reflection detail %s", id, v.Round, v.DecisionState, v.DetailState)
		}
		if v.Reflection != nil {
			out = append(out, *v.Reflection)
		}
	}
	return out, nil
}
