//go:build linux || darwin

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func cliLegacyUnknownFixture(t *testing.T, round int, detail string) (*cliSubprocessEnv, *mission.Store, string) {
	t.Helper()
	require.NotEmpty(t, ethosBinary, "legacy reader acceptance requires the real CLI")
	se := &cliSubprocessEnv{home: t.TempDir(), repo: t.TempDir()}
	se.env = testGitEnv(se.home)
	gitRunEnv(t, se.repo, se.env, "init", "-q")
	stampFixture(t, se.home, se.repo)
	id, stamp := "m-2026-11-05-162", "2026-11-05T10:00:00Z"
	c := &mission.Contract{MissionID: id, Status: mission.StatusOpen, CreatedAt: stamp, UpdatedAt: stamp,
		Leader: "claude", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "djb", PinnedAt: stamp},
		WriteSet: []string{"source.go"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 3}, CurrentRound: round}
	// Do not call repository Create: it initializes the decision block.
	body, err := yaml.Marshal(c)
	require.NoError(t, err)
	require.NotContains(t, string(body), "round_decisions")
	path := mission.RepoStatePath(se.repo, "missions", id, "contract.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, body, 0o600))
	if detail != "missing" {
		var reflections []mission.Reflection
		if detail == "prefix" || detail == "later" {
			n := 1
			if detail == "later" {
				n = round
			}
			reflections = []mission.Reflection{{Mission: id, Round: n, CreatedAt: stamp, Author: "claude",
				Signals: []string{"retained legacy signal"}, Recommendation: mission.RecommendationContinue, Reason: "retained legacy reason"}}
		}
		body, err = yaml.Marshal(struct {
			Reflections []mission.Reflection `yaml:"reflections"`
		}{reflections})
		require.NoError(t, err)
		if detail == "empty" {
			body = nil
		}
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), "reflections.yaml"), body, 0o600))
	}
	p, err := layout.NewRepository(se.repo, se.repo, layout.RecordBlame)
	require.NoError(t, err)
	s := mission.NewRepositoryStore(p, filepath.Join(se.home, ".punt-labs", "ethos"))
	loaded, err := s.Load(id)
	require.NoError(t, err)
	require.Nil(t, loaded.RoundDecisions)
	return se, s, id
}

func TestCLIMissionLegacyUnknown(t *testing.T) {
	for _, tc := range []struct {
		round  int
		detail string
	}{{1, "missing"}, {1, "empty"}, {3, "missing"}, {3, "empty"}, {3, "prefix"}, {3, "later"}} {
		t.Run(fmt.Sprintf("round=%d/%s", tc.round, tc.detail), func(t *testing.T) {
			se, s, id := cliLegacyUnknownFixture(t, tc.round, tc.detail)
			path := mission.RepoStatePath(se.repo, "missions", id, "contract.yaml")
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			stdout, stderr, exit := runCLI(t, se, "mission", "reflections", id, "--json")
			require.Equal(t, 0, exit, stderr)
			t.Logf("JSON: %s", stdout)
			var states []mission.ReflectionRead
			require.NoError(t, json.Unmarshal([]byte(stdout), &states))
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
			for _, command := range []string{"reflections", "show"} {
				stdout, stderr, exit = runCLI(t, se, "mission", command, id)
				require.Equal(t, 0, exit, stderr)
				t.Logf("%s human: %s", command, stdout)
				for _, state := range states {
					assert.Contains(t, stdout, fmt.Sprintf("round %d: decision=legacy_unknown detail=%s", state.Round, state.DetailState))
				}
				if tc.detail == "prefix" || tc.detail == "later" {
					assert.Contains(t, stdout, "retained legacy reason")
				}
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			loaded, err := s.Load(id)
			require.NoError(t, err)
			assert.Nil(t, loaded.RoundDecisions)
			assert.NoDirExists(t, filepath.Join(se.repo, ".punt-labs", "local"), "read-only entry points must not initialize local state")
			assert.NoDirExists(t, filepath.Join(se.home, ".punt-labs"), "read-only entry points must not initialize global state")
		})
	}
}

func TestCLIMissionLegacyUnknownFullExport(t *testing.T) {
	for _, detail := range []string{"missing", "empty", "prefix", "later"} {
		t.Run(detail, func(t *testing.T) {
			se, s, id := cliLegacyUnknownFixture(t, 3, detail)
			// Supply the independent create-history gate. Before the reader
			// repair, these fixtures could export successfully as empty or
			// partial history, rather than just failing for another artifact.
			p, ok := s.RepositoryPolicy()
			require.True(t, ok)
			live := (audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: "legacy-history"}).LivePath(p)
			event, err := json.Marshal(mission.Event{TS: "2026-11-05T10:00:00Z", Event: "create", Actor: "claude"})
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
			require.NoError(t, os.WriteFile(live, append(event, '\n'), 0o600))
			events, warnings, err := s.LoadEvents(id)
			require.NoError(t, err)
			require.Empty(t, warnings)
			require.Len(t, events, 1)
			require.Equal(t, "create", events[0].Event)
			for range 2 {
				_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "incomplete-export")
				require.NotEqual(t, 0, exit, stderr)
				assert.Contains(t, stderr, "full export incomplete")
				assert.Contains(t, stderr, "legacy_unknown")
				assert.NoDirExists(t, filepath.Join(se.repo, "incomplete-export"))
				loaded, err := s.Load(id)
				require.NoError(t, err)
				assert.Nil(t, loaded.RoundDecisions)
			}
		})
	}
}
