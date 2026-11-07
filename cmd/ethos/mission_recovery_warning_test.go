//go:build linux || darwin

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCLIMissionRecoveryWarnings(t *testing.T) {
	for _, method := range []string{"reflect", "correct"} {
		for _, refuse := range []bool{false, true} {
			for _, jsonMode := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/refuse=%t/json=%t", method, refuse, jsonMode), func(t *testing.T) {
					se, s, receipt, originalArgs := cliMutationWarningFixture(t, "result")
					id := originalArgs[2]
					var body []byte
					var err error
					wantError := "is open"
					if method == "reflect" {
						_, _, err = s.AdvanceRoundWithOutcome(id, "test-agent")
						require.NoError(t, err)
						round := 2
						if refuse {
							round = 3
						}
						body, err = yaml.Marshal(mission.Reflection{Mission: id, Round: round, Author: "test-agent", Converging: true,
							Signals: []string{"new signal"}, Recommendation: mission.RecommendationContinue, Reason: "new round"})
						wantError = "does not match"
					} else {
						if !refuse {
							_, _, err = s.AbandonWithOutcome(id, "cancelled")
							require.NoError(t, err)
						}
						body, err = yaml.Marshal(mission.Correction{Mission: id, Author: "test-agent", Kind: mission.CorrectionDecision, Corrected: "new annotation"})
					}
					require.NoError(t, err)
					file := filepath.Join(t.TempDir(), method+".yaml")
					require.NoError(t, os.WriteFile(file, body, 0o600))
					args := []string{"mission", method, id, "--file", file}
					if jsonMode {
						args = append(args, "--json")
					}
					path, err := s.ContractPath(id)
					require.NoError(t, err)
					before, err := os.ReadFile(path)
					require.NoError(t, err)
					stdout, stderr, exit := runCLI(t, se, args...)
					if refuse {
						require.Equal(t, 1, exit, stderr)
						assert.Contains(t, stderr, wantError)
						after, err := os.ReadFile(path)
						require.NoError(t, err)
						assert.Equal(t, before, after)
					} else {
						require.Equal(t, 0, exit, stderr)
					}
					if jsonMode {
						var payload struct {
							Error    string                  `json:"error"`
							Warnings []string                `json:"warnings"`
							Outcome  mission.OperationResult `json:"outcome"`
						}
						require.NoError(t, json.Unmarshal([]byte(stdout), &payload), stdout)
						require.Len(t, payload.Warnings, 1)
						assert.Contains(t, payload.Warnings[0], "round 1 accepted; optional reflection trace pending")
						if refuse {
							assert.Contains(t, payload.Error, wantError)
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
						}
						assert.NotContains(t, stderr, "optional reflection trace pending")
					} else {
						assert.Equal(t, 1, strings.Count(stderr, "round 1 accepted; optional reflection trace pending"))
						if refuse {
							assert.Empty(t, stdout)
						} else {
							assert.Contains(t, stdout, map[string]string{"reflect": "reflected:", "correct": "corrected:"}[method])
						}
					}
					assert.FileExists(t, receipt)
					if method == "correct" {
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
}
