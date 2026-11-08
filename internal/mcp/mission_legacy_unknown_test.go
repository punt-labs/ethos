package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestRegisteredMissionLegacyUnknown(t *testing.T) {
	for _, tc := range []struct {
		round  int
		detail string
	}{{1, "missing"}, {1, "empty"}, {3, "missing"}, {3, "empty"}, {3, "prefix"}, {3, "later"}} {
		t.Run(fmt.Sprintf("round=%d/%s", tc.round, tc.detail), func(t *testing.T) {
			root, global := t.TempDir(), t.TempDir()
			p, err := layout.NewRepository(root, root, layout.RecordBlame)
			require.NoError(t, err)
			s := mission.NewRepositoryStore(p, global)
			id, stamp := "m-2026-11-05-162", "2026-11-05T10:00:00Z"
			c := &mission.Contract{MissionID: id, Status: mission.StatusOpen, CreatedAt: stamp, UpdatedAt: stamp,
				Leader: "claude", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "djb", PinnedAt: stamp},
				WriteSet: []string{"source.go"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 3}, CurrentRound: tc.round}
			// Direct old-format fixture, never repository Create (which installs v1).
			body, err := yaml.Marshal(c)
			require.NoError(t, err)
			require.NotContains(t, string(body), "round_decisions")
			path := mission.RepoStatePath(root, "missions", id, "contract.yaml")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, body, 0o600))
			before := body
			if tc.detail != "missing" {
				var reflections []mission.Reflection
				if tc.detail == "prefix" || tc.detail == "later" {
					round := 1
					if tc.detail == "later" {
						round = tc.round
					}
					reflections = []mission.Reflection{{Mission: id, Round: round, CreatedAt: stamp, Author: "claude",
						Signals: []string{"retained legacy signal"}, Recommendation: mission.RecommendationContinue, Reason: "retained legacy reason"}}
				}
				body, err = yaml.Marshal(struct {
					Reflections []mission.Reflection `yaml:"reflections"`
				}{reflections})
				require.NoError(t, err)
				if tc.detail == "empty" {
					body = nil
				}
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), "reflections.yaml"), body, 0o600))
			}
			loaded, err := s.Load(id)
			require.NoError(t, err)
			require.Nil(t, loaded.RoundDecisions)
			srv, _ := fullServer(t, WithMissionStore(s))
			tool := srv.GetTool("mission")
			require.NotNil(t, tool)
			for range 2 {
				result, err := tool.Handler(context.Background(), callTool(map[string]interface{}{"method": "reflections", "mission_id": id}))
				require.NoError(t, err)
				require.False(t, result.IsError, resultText(t, result))
				t.Logf("registered MCP reflections: %s", resultText(t, result))
				var states []mission.ReflectionRead
				require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &states))
				require.Len(t, states, tc.round)
				for i, state := range states {
					assert.Equal(t, i+1, state.Round)
					assert.Equal(t, "legacy_unknown", state.DecisionState)
					assert.Nil(t, state.Decision)
					present := (tc.detail == "prefix" && state.Round == 1) || (tc.detail == "later" && state.Round == tc.round)
					if present {
						require.NotNil(t, state.Reflection)
						assert.Equal(t, "present", state.DetailState)
						assert.Equal(t, "retained legacy reason", state.Reflection.Reason)
					} else {
						assert.Equal(t, "absent", state.DetailState)
						assert.Nil(t, state.Reflection)
					}
				}
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.NoDirExists(t, filepath.Join(root, ".punt-labs", "local"))
			entries, err := os.ReadDir(global)
			require.NoError(t, err)
			assert.Empty(t, entries, "registered reads must not create global locks or records")
		})
	}
}
