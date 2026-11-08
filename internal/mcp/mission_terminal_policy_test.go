//go:build linux || darwin

package mcp_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/gitenv"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestMCPMissionCorrectCapturedPolicy(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "regression requires the actual stdio server")
	for _, failPublication := range []bool{false, true} {
		t.Run(fmt.Sprintf("publication-interrupted=%t", failPublication), func(t *testing.T) {
			env := setupMCPTestEnv(t)
			storeRoot := env.repo
			git := func(args ...string) string {
				t.Helper()
				out, err := gitenv.OutputEnv(env.repo, "git", env.env, args...)
				require.NoError(t, err, "git %v: %s", args, out)
				return strings.TrimSpace(string(out))
			}
			git("config", "user.name", "Fixture")
			git("config", "user.email", "fixture@example.invalid")
			require.NoError(t, os.WriteFile(filepath.Join(storeRoot, "source.txt"), []byte("base\n"), 0o600))
			git("add", "source.txt")
			git("commit", "-m", "base")
			checkout := filepath.Join(t.TempDir(), "linked")
			git("worktree", "add", "-b", "feature", checkout)
			env.repo = checkout
			require.NoError(t, os.WriteFile(filepath.Join(storeRoot, ".punt-labs", "ethos.yaml"), []byte("record: full\nagent: test-agent\n"), 0o600))
			require.NoError(t, os.MkdirAll(filepath.Join(checkout, ".punt-labs"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(checkout, ".punt-labs", "ethos.yaml"), []byte("record: blame\nagent: conflicting-agent\n"), 0o600))
			p, err := layout.NewRepository(storeRoot, checkout, layout.RecordFull)
			require.NoError(t, err)
			s := mission.NewRepositoryStore(p, filepath.Join(env.home, ".punt-labs", "ethos")).WithSessionID("mcp-terminal-policy")
			id := "m-2026-11-04-813"
			stamp := "2026-11-04T10:00:00Z"
			require.NoError(t, s.Create(&mission.Contract{MissionID: id, Status: mission.StatusOpen, CreatedAt: stamp, UpdatedAt: stamp,
				Leader: "test-agent", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "djb", PinnedAt: stamp},
				WriteSet: []string{"source.txt"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 1}, CurrentRound: 1}))
			require.NoError(t, s.AppendResult(id, &mission.Result{Mission: id, Round: 1, Author: "bwk", Verdict: mission.VerdictPass,
				Confidence: 0.9, Evidence: []mission.EvidenceCheck{{Name: "focused check", Status: "pass"}}}))
			_, err = s.Close(id, mission.StatusClosed)
			require.NoError(t, err)
			c := startMCPClient(t, env)
			index := git("rev-parse", "--path-format=absolute", "--git-path", "index")
			before, err := os.ReadFile(index)
			require.NoError(t, err)
			if failPublication {
				require.NoError(t, os.WriteFile(index+".lock", []byte("other writer\n"), 0o600))
			}
			result := callTool(t, c, "mission", map[string]interface{}{
				"method": "correct", "mission_id": id,
				"correction": fmt.Sprintf("mission: %s\nround: 0\nkind: fabrication\nauthor: test-agent\nclaim: old claim\ncorrected: corrected claim\n", id),
			})
			require.False(t, result.IsError, resultText(t, result))
			var payload struct {
				Warnings []string `json:"warnings"`
			}
			require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &payload))
			if failPublication {
				require.NotEmpty(t, payload.Warnings)
				assert.Contains(t, strings.Join(payload.Warnings, "\n"), "sealing mission log")
				after, err := os.ReadFile(index)
				require.NoError(t, err)
				assert.Equal(t, before, after)
			} else {
				assert.Empty(t, payload.Warnings)
				assert.Contains(t, git("diff", "--cached", "--name-only"), ".punt-labs/ethos/missions/"+id+"/log-")
			}
			corrections, warnings, err := s.LoadCorrections(id)
			require.NoError(t, err)
			assert.Empty(t, warnings)
			assert.Len(t, corrections, 1, "a post-acceptance seal failure must not erase authority")
			chunks, err := filepath.Glob(filepath.Join(p.LocalRoot(), "trace", "missions", id, "log-*.jsonl"))
			require.NoError(t, err)
			require.NotEmpty(t, chunks)
			var history strings.Builder
			for _, chunk := range chunks {
				data, err := os.ReadFile(chunk)
				require.NoError(t, err)
				history.Write(data)
				published := filepath.Join(p.CheckoutRecordRoot(), "missions", id, filepath.Base(chunk))
				copy, err := os.ReadFile(published)
				require.NoError(t, err)
				assert.Equal(t, data, copy)
			}
			assert.Contains(t, history.String(), `"event":"correct"`)
			assert.NoDirExists(t, filepath.Join(checkout, ".punt-labs", "local", "ethos"))
		})
	}
}
