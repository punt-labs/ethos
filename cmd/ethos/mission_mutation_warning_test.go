//go:build linux || darwin

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func cliMutationWarningFixture(t *testing.T, method string) (*cliSubprocessEnv, *mission.Store, string, []string) {
	t.Helper()
	require.NotEmpty(t, ethosBinary, "warning checks require the actual CLI")
	se := setupCLISubprocessEnv(t)
	p, err := layout.NewRepository(se.repo, se.repo, layout.RecordBlame)
	require.NoError(t, err)
	s := mission.NewRepositoryStore(p, filepath.Join(se.home, ".punt-labs", "ethos")).WithSessionID("mutation-warning")
	id := "m-2026-11-04-851"
	stamp := "2026-11-04T10:00:00Z"
	require.NoError(t, s.Create(&mission.Contract{MissionID: id, Status: mission.StatusOpen,
		CreatedAt: stamp, UpdatedAt: stamp, Leader: "test-agent", Worker: "bwk",
		Evaluator: mission.Evaluator{Handle: "djb", PinnedAt: stamp}, WriteSet: []string{"source.go"},
		SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 3}, CurrentRound: 1}))
	result := &mission.Result{Mission: id, Round: 1, Author: "bwk", Verdict: mission.VerdictPass,
		Confidence: 0.9, Evidence: []mission.EvidenceCheck{{Name: "focused check", Status: "pass"}}}
	if method == "close" {
		require.NoError(t, s.AppendResult(id, result))
	}
	out, err := s.ApplyReflection(id, &mission.Reflection{Mission: id, Round: 1, Author: "test-agent",
		Converging: true, Signals: []string{"checked"}, Recommendation: mission.RecommendationContinue,
		Reason: "accepted reflection"})
	require.NoError(t, err)
	require.Equal(t, "complete", out.Trace)
	archive := filepath.Join(p.LocalRoot(), "trace", "missions", id, "reflection-1.yaml")
	data, err := os.ReadFile(archive)
	require.NoError(t, err)
	receipt := filepath.Join(p.LocalRoot(), "missions", id, "reflection-intents", "1.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(receipt), 0o700))
	require.NoError(t, os.WriteFile(receipt, data, 0o600))
	var detail map[string]any
	require.NoError(t, yaml.Unmarshal(data, &detail))
	detail["reflection"].(map[string]any)["reason"] = "divergent optional prose"
	data, err = yaml.Marshal(detail)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(archive, data, 0o600))
	args := []string{"mission", method, id}
	switch method {
	case "result":
		file := filepath.Join(t.TempDir(), "result.yaml")
		data, err := yaml.Marshal(result)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(file, data, 0o600))
		args = append(args, "--file", file)
	case "abandon":
		args = append(args, "--reason", "cancelled")
	}
	return se, s, receipt, args
}

func TestCLIMissionMutationWarnings(t *testing.T) {
	for _, method := range []string{"result", "advance", "close", "abandon"} {
		for _, jsonMode := range []bool{false, true} {
			name := method + "/human"
			if jsonMode {
				name = method + "/json"
			}
			t.Run(name, func(t *testing.T) {
				se, s, receipt, args := cliMutationWarningFixture(t, method)
				before, err := os.ReadFile(receipt)
				require.NoError(t, err)
				if jsonMode {
					args = append(args, "--json")
				}
				stdout, stderr, exit := runCLI(t, se, args...)
				require.Equal(t, 0, exit, stderr)
				if jsonMode {
					var payload struct {
						Mission  string   `json:"mission_id"`
						Warnings []string `json:"warnings"`
					}
					require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
					assert.Equal(t, args[2], payload.Mission)
					require.Len(t, payload.Warnings, 1)
					assert.Contains(t, payload.Warnings[0], "accepted; optional reflection trace pending")
					assert.Contains(t, payload.Warnings[0], "optional reflection detail conflict")
					assert.NotContains(t, stderr, "optional reflection trace pending", "JSON owns the warning")
				} else {
					assert.Equal(t, 1, strings.Count(stderr, "accepted; optional reflection trace pending"))
					assert.Contains(t, stderr, "optional reflection detail conflict")
					assert.NotContains(t, stdout, "optional reflection trace pending")
				}
				after, err := os.ReadFile(receipt)
				require.NoError(t, err)
				assert.Equal(t, before, after, "optional conflict retains recovery evidence")
				c, err := s.Load(args[2])
				require.NoError(t, err)
				switch method {
				case "result":
					r, err := s.LoadResult(args[2], 1)
					require.NoError(t, err)
					require.NotNil(t, r)
				case "advance":
					assert.Equal(t, 2, c.CurrentRound)
				case "close":
					assert.Equal(t, mission.StatusClosed, c.Status)
				case "abandon":
					assert.Equal(t, mission.StatusAbandoned, c.Status)
				}
			})
		}
	}
}

func TestCLIMissionMutationAuthorityFailure(t *testing.T) {
	se, s, receipt, args := cliMutationWarningFixture(t, "result")
	require.NoError(t, os.WriteFile(receipt, []byte("broken: [yaml"), 0o600))
	stdout, stderr, exit := runCLI(t, se, append(args, "--json")...)
	require.NotEqual(t, 0, exit)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "unreadable reflection receipt")
	assert.NotContains(t, stderr, "optional reflection trace pending")
	r, err := s.LoadResult(args[2], 1)
	require.NoError(t, err)
	assert.Nil(t, r)
}

func TestCLIMissionMutationSealWarnings(t *testing.T) {
	for _, method := range []string{"close", "abandon"} {
		for _, jsonMode := range []bool{false, true} {
			name := method + "/human"
			if jsonMode {
				name = method + "/json"
			}
			t.Run(name, func(t *testing.T) {
				se, s, _, id := terminalPolicyFixture(t, false)
				args := []string{"mission", method, id}
				if method == "close" {
					terminalPolicyResult(t, s, id)
				} else {
					args = append(args, "--reason", "cancelled")
				}
				if jsonMode {
					args = append(args, "--json")
				}
				index := publicationGitCLI(t, se, "rev-parse", "--path-format=absolute", "--git-path", "index")
				require.NoError(t, os.WriteFile(index+".lock", []byte("other writer\n"), 0o600))
				stdout, stderr, exit := runCLI(t, se, args...)
				require.Equal(t, 0, exit, stderr)
				assert.Equal(t, 1, strings.Count(stderr, "sealing mission log"), "preserve legacy stderr once")
				if jsonMode {
					var payload struct {
						Warnings []string `json:"warnings"`
					}
					require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
					require.Len(t, payload.Warnings, 1)
					assert.Contains(t, payload.Warnings[0], "sealing mission log")
				}
			})
		}
	}
}

func TestCLIMissionMutationWarningsRefused(t *testing.T) {
	for _, method := range []string{"close", "abandon", "advance", "result"} {
		for _, jsonMode := range []bool{false, true} {
			t.Run(method+"/json="+fmt.Sprint(jsonMode), func(t *testing.T) {
				se, s, receipt, args := cliMutationWarningFixture(t, "result")
				id := args[2]
				switch method {
				case "close":
					args = []string{"mission", method, id}
				case "abandon":
					_, err := s.AppendResultWithOutcome(id, &mission.Result{Mission: id, Round: 1, Author: "bwk", Verdict: mission.VerdictPass,
						Confidence: 0.9, Evidence: []mission.EvidenceCheck{{Name: "check", Status: "pass"}}})
					require.NoError(t, err)
					args = []string{"mission", method, id, "--reason", "cancelled"}
				case "advance":
					data, err := os.ReadFile(args[4])
					require.NoError(t, err)
					r, err := mission.DecodeResultStrict(data, args[4])
					require.NoError(t, err)
					_, err = s.AppendResultWithOutcome(id, r)
					require.NoError(t, err)
					_, _, err = s.CloseWithOutcome(id, mission.StatusClosed)
					require.NoError(t, err)
					args = []string{"mission", method, id}
				case "result":
					data, err := os.ReadFile(args[4])
					require.NoError(t, err)
					data = []byte(strings.Replace(string(data), "round: 1", "round: 2", 1))
					require.NoError(t, os.WriteFile(args[4], data, 0o600))
				}
				if jsonMode {
					args = append(args, "--json")
				}
				path, err := s.ContractPath(id)
				require.NoError(t, err)
				before, err := os.ReadFile(path)
				require.NoError(t, err)
				stdout, stderr, exit := runCLI(t, se, args...)
				require.Equal(t, 1, exit, stderr)
				assert.Contains(t, stderr, "ethos: mission "+method+":")
				want := map[string]string{"close": "no result artifact", "abandon": "result artifact(s) exist", "advance": "terminal state", "result": "result round 2 does not match"}
				assert.Contains(t, stderr, want[method])
				if jsonMode {
					var payload struct {
						Error    string   `json:"error"`
						Warnings []string `json:"warnings"`
					}
					require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
					assert.Contains(t, payload.Error, "mission "+method+":")
					require.Len(t, payload.Warnings, 1)
					assert.Contains(t, payload.Warnings[0], "accepted; optional reflection trace pending")
					assert.NotContains(t, stderr, "optional reflection trace pending")
				} else {
					assert.Empty(t, stdout)
					assert.Equal(t, 1, strings.Count(stderr, "accepted; optional reflection trace pending"))
				}
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				assert.FileExists(t, receipt)
			})
		}
	}
}
