package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/punt-labs/ethos/v4/internal/attribute"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func recoveryWarningAuthor(t *testing.T, s *identity.Store) {
	t.Helper()
	for _, kind := range []attribute.Kind{attribute.Personalities, attribute.WritingStyles} {
		require.NoError(t, attribute.NewStore(s.Root(), kind).Save(&attribute.Attribute{Slug: "author", Content: "Author content"}))
	}
	require.NoError(t, s.Save(&identity.Identity{Name: "Author", Handle: "author", Kind: "agent", Personality: "author", WritingStyle: "author"}))
}

func recoveryWarningRequest(t *testing.T, s *mission.Store, p layout.Policy, id, method string, warning, refuse bool) mcplib.CallToolRequest {
	t.Helper()
	mutationWarningMission(t, s, id)
	if warning {
		mutationWarningReceipt(t, s, p, id)
	} else {
		out, err := s.ApplyReflection(id, &mission.Reflection{Mission: id, Round: 1, Author: "lead", Converging: true,
			Signals: []string{"checked"}, Recommendation: mission.RecommendationContinue, Reason: "accepted"})
		require.NoError(t, err)
		require.Equal(t, "complete", out.Trace)
	}
	args := map[string]interface{}{"method": method, "mission_id": id}
	if method == "reflect" {
		_, _, err := s.AdvanceRoundWithOutcome(id, "lead")
		require.NoError(t, err)
		round := 2
		if refuse {
			round = 3
		}
		body, err := yaml.Marshal(mission.Reflection{Mission: id, Round: round, Author: "lead", Converging: true,
			Signals: []string{"new signal"}, Recommendation: mission.RecommendationContinue, Reason: "new round"})
		require.NoError(t, err)
		args["reflection"] = string(body)
	} else {
		if !refuse {
			_, _, err := s.AbandonWithOutcome(id, "cancelled")
			require.NoError(t, err)
		}
		body, err := yaml.Marshal(mission.Correction{Mission: id, Author: "author", Kind: mission.CorrectionDecision, Corrected: "new annotation"})
		require.NoError(t, err)
		args["correction"] = string(body)
	}
	return callTool(args)
}

func TestRegisteredMissionRecoveryWarnings(t *testing.T) {
	for _, method := range []string{"reflect", "correct"} {
		for _, refuse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/refuse=%t", method, refuse), func(t *testing.T) {
				s, p := mutationWarningStore(t)
				srv, identities := fullServer(t, WithMissionStore(s))
				recoveryWarningAuthor(t, identities)
				id := "m-2026-11-04-961"
				req := recoveryWarningRequest(t, s, p, id, method, true, refuse)
				r, err := srv.GetTool("mission").Handler(context.Background(), req)
				require.NoError(t, err)
				require.Equal(t, refuse, r.IsError, resultText(t, r))
				var payload struct {
					Error    string                  `json:"error"`
					Warnings []string                `json:"warnings"`
					Outcome  mission.OperationResult `json:"outcome"`
				}
				require.NoError(t, json.Unmarshal([]byte(resultText(t, r)), &payload))
				require.Len(t, payload.Warnings, 1)
				assert.Contains(t, payload.Warnings[0], "round 1 accepted; optional reflection trace pending")
				assert.Contains(t, payload.Warnings[0], "optional reflection detail conflict")
				if refuse {
					assert.Contains(t, payload.Error, "failed to record")
				} else {
					assert.Empty(t, payload.Error)
				}
				if method == "reflect" {
					if refuse {
						assert.Equal(t, "not_applied", payload.Outcome.Application)
					} else {
						assert.Equal(t, "applied", payload.Outcome.Application)
						assert.Equal(t, "complete", payload.Outcome.Trace)
					}
				} else {
					corrections, _, err := s.LoadCorrections(id)
					require.NoError(t, err)
					if refuse {
						assert.Empty(t, corrections)
					} else {
						assert.Len(t, corrections, 1)
					}
				}
			})
		}
	}
}

func TestRegisteredMissionRecoveryWarningsConcurrent(t *testing.T) {
	s, p := mutationWarningStore(t)
	srv, identities := fullServer(t, WithMissionStore(s))
	recoveryWarningAuthor(t, identities)
	tool := srv.GetTool("mission")
	const count = 16
	requests := make([]mcplib.CallToolRequest, count)
	for i := range count {
		method := "reflect"
		if i&2 != 0 {
			method = "correct"
		}
		requests[i] = recoveryWarningRequest(t, s, p, fmt.Sprintf("m-2026-11-04-%03d", 970+i), method, i&1 == 0, i&4 != 0)
	}
	type answer struct {
		index  int
		result *mcplib.CallToolResult
		err    error
	}
	answers := make(chan answer, count)
	start := make(chan struct{})
	for i, req := range requests {
		go func() { <-start; r, err := tool.Handler(context.Background(), req); answers <- answer{i, r, err} }()
	}
	close(start)
	for range count {
		a := <-answers
		require.NoError(t, a.err)
		require.Equal(t, a.index&4 != 0, a.result.IsError, resultText(t, a.result))
		text := resultText(t, a.result)
		if a.index&1 != 0 {
			assert.NotContains(t, text, "optional reflection")
			assert.NotContains(t, text, "warnings")
		} else {
			var payload struct {
				Warnings []string `json:"warnings"`
			}
			require.NoError(t, json.Unmarshal([]byte(text), &payload))
			require.Len(t, payload.Warnings, 1)
			assert.Contains(t, payload.Warnings[0], fmt.Sprintf("m-2026-11-04-%03d", 970+a.index))
		}
	}
}
