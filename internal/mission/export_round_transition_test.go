package mission

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func exportTransitionContract(round int) *Contract {
	c := &Contract{MissionID: "m-2026-11-04-001", Status: StatusOpen, CurrentRound: round, Leader: "lead",
		Budget:         Budget{Rounds: round},
		RoundDecisions: &RoundDecisions{Version: 1, Entries: []RoundDecision{}}}
	for r := 1; r < round; r++ {
		c.RoundDecisions.Entries = append(c.RoundDecisions.Entries, RoundDecision{
			ID: fmt.Sprintf("%032x", r), Round: r, CreatedAt: "2026-11-04T10:00:00Z", Author: "lead", Recommendation: RecommendationContinue,
		})
	}
	return c
}

func exportTransitionEvent(from int, recommendation string) Event {
	return Event{Event: "round_advanced", Actor: "lead", Details: map[string]any{
		"from_round": from, "to_round": from + 1, "recommendation": recommendation,
	}}
}

func TestFullExportRoundTransitionHistory(t *testing.T) {
	first := exportTransitionEvent(1, RecommendationContinue)
	second := exportTransitionEvent(2, RecommendationContinue)
	third := exportTransitionEvent(3, RecommendationContinue)
	for _, tt := range []struct {
		name   string
		round  int
		events []Event
		want   string
	}{
		{"no crossed round", 1, nil, ""},
		{"missing transition", 2, nil, "round 1 required round_advanced event is unavailable"},
		{"crossed round without result", 2, []Event{first}, ""},
		{"all crossed rounds without results", 4, []Event{first, second, third}, ""},
		{"equal transition copies", 2, []Event{first, first}, ""},
		{"missing middle transition", 4, []Event{first, third}, "round 2 required round_advanced event is unavailable"},
		{"missing first transition", 4, []Event{second, third}, "round 1 required round_advanced event is unavailable"},
		{"missing last transition", 4, []Event{first, second}, "round 3 required round_advanced event is unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := checkFullExportHistory(exportTransitionContract(tt.round), nil, tt.events)
			if tt.want == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.want)
			}
		})
	}
	for _, recommendation := range []string{RecommendationContinue, RecommendationAdvance, RecommendationPivot} {
		t.Run("accepted recommendation "+recommendation, func(t *testing.T) {
			c := exportTransitionContract(2)
			c.RoundDecisions.Entries[0].Recommendation = recommendation
			assert.NoError(t, checkFullExportHistory(c, nil, []Event{exportTransitionEvent(1, recommendation)}))
		})
	}
	t.Run("current round decision is not a crossed round", func(t *testing.T) {
		c := exportTransitionContract(2)
		d := c.RoundDecisions.Entries[0]
		d.ID, d.Round, d.Recommendation = fmt.Sprintf("%032x", 2), 2, RecommendationStop
		c.RoundDecisions.Entries = append(c.RoundDecisions.Entries, d)
		assert.NoError(t, checkFullExportHistory(c, nil, []Event{first}))
	})
	t.Run("advance actor need not be reflection author", func(t *testing.T) {
		c := exportTransitionContract(2)
		c.RoundDecisions.Entries[0].Author = "reviewer"
		assert.NoError(t, checkFullExportHistory(c, nil, []Event{first}))
	})
	for _, actor := range []string{"other", "reviewer", " lead "} {
		t.Run("caller supplied advance actor "+actor, func(t *testing.T) {
			c := exportTransitionContract(2)
			c.RoundDecisions.Entries[0].Author = "reviewer"
			event := exportTransitionEvent(1, RecommendationContinue)
			event.Actor = actor
			assert.NoError(t, checkFullExportHistory(c, nil, []Event{event}))
		})
	}
}

func TestFullExportRoundTransitionInvalidCandidates(t *testing.T) {
	type candidate struct {
		name  string
		value any
	}
	for _, field := range []string{"from_round", "to_round", "recommendation", "actor"} {
		values := []candidate{
			{"missing", nil}, {"null", nil}, {"string", "1"}, {"fractional", 1.5},
			{"overflow", json.Number("9999999999999999999999999")},
			{"object", map[string]int{"round": 1}}, {"array", []int{1}}, {"boolean", true},
			{"zero", 0}, {"negative", -1}, {"wrong round", 9},
			{"precise fractional", json.Number("1.0000000000000001")},
			{"precise fractional below one", json.Number("0.99999999999999999")},
			{"decimal", json.Number("1.0")}, {"exponent", json.Number("1e0")},
			{"alias", nil},
		}
		if field == "to_round" {
			values[3].value = 2.5 // Truncation to 2 must not count as an advance.
			values[11].value = json.Number("2.0000000000000001")
			values[12].value = json.Number("1.99999999999999999")
			values[13].value = json.Number("2.0")
			values[14].value = json.Number("2e0")
		}
		if field == "recommendation" {
			values = append(values, candidate{"mismatched permitting recommendation", RecommendationPivot})
			values = append(values, candidate{"synonym is not accepted recommendation", RecommendationAdvance})
			for _, value := range []string{"", RecommendationStop, RecommendationEscalate, "unknown"} {
				values = append(values, candidate{"invalid " + value, value})
			}
		}
		if field == "actor" {
			values = []candidate{{"empty", ""}, {"whitespace only", " \t\n"}}
		}
		for _, value := range values {
			for _, order := range []string{"alone", "before valid", "after valid"} {
				t.Run(field+"/"+value.name+"/"+order, func(t *testing.T) {
					c := exportTransitionContract(2)
					// The reflection author does not select the advance actor.
					c.RoundDecisions.Entries[0].Author = "reviewer"
					bad := exportTransitionEvent(1, RecommendationContinue)
					if field == "actor" {
						bad.Actor = value.value.(string)
					} else if value.name == "missing" {
						delete(bad.Details, field)
					} else if value.name == "alias" {
						bad.Details[strings.ToUpper(field)] = bad.Details[field]
						delete(bad.Details, field)
					} else {
						bad.Details[field] = value.value
					}
					events := []Event{bad}
					if order == "before valid" {
						events = append(events, exportTransitionEvent(1, RecommendationContinue))
					} else if order == "after valid" {
						events = append([]Event{exportTransitionEvent(1, RecommendationContinue)}, events...)
					}
					assert.ErrorContains(t, checkFullExportHistory(c, nil, events), "round_advanced")
				})
			}
		}
	}
}

func TestFullExportRoundTransitionMalformedDetails(t *testing.T) {
	for _, tt := range []struct {
		name    string
		details map[string]any
	}{
		{"nil details", nil},
		{"unencodable details", map[string]any{"from_round": make(chan int), "to_round": 2, "recommendation": RecommendationContinue}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			event := exportTransitionEvent(1, RecommendationContinue)
			event.Details = tt.details
			assert.ErrorContains(t, checkFullExportHistory(exportTransitionContract(2), nil, []Event{event}), "round_advanced")
		})
	}
}
