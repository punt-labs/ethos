//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/hook"
	"github.com/punt-labs/ethos/v4/internal/identity"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func terminalPolicyFixture(t *testing.T, linked bool) (*cliSubprocessEnv, *mission.Store, layout.Policy, string) {
	t.Helper()
	require.NotEmpty(t, ethosBinary)
	root := t.TempDir()
	se := &cliSubprocessEnv{repo: root, home: t.TempDir()}
	se.env = testGitEnv(se.home)
	publicationGitCLI(t, se, "init", "-b", "main")
	publicationGitCLI(t, se, "config", "user.name", "Fixture")
	publicationGitCLI(t, se, "config", "user.email", "fixture@example.invalid")
	require.NoError(t, os.WriteFile(filepath.Join(root, "source.txt"), []byte("base\n"), 0o600))
	publicationGitCLI(t, se, "add", "source.txt")
	publicationGitCLI(t, se, "commit", "-m", "base")
	if linked {
		checkout := filepath.Join(t.TempDir(), "linked")
		publicationGitCLI(t, se, "worktree", "add", "-b", "feature", checkout)
		se.repo = checkout
		require.NoError(t, os.MkdirAll(filepath.Join(checkout, ".punt-labs"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(checkout, ".punt-labs", "ethos.yaml"), []byte("record: blame\n"), 0o600))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".punt-labs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".punt-labs", "ethos.yaml"), []byte("record: full\nagent: lead\n"), 0o600))
	p, err := layout.NewRepository(root, se.repo, layout.RecordFull)
	require.NoError(t, err)
	global := filepath.Join(se.home, ".punt-labs", "ethos")
	require.NoError(t, identity.NewStore(global).Save(&identity.Identity{Name: "Leader", Handle: "lead", Kind: "human"}))
	s := mission.NewRepositoryStore(p, global).WithSessionID("terminal-policy")
	id := "m-2026-11-04-812"
	stamp := "2026-11-04T10:00:00Z"
	require.NoError(t, s.Create(&mission.Contract{MissionID: id, Status: mission.StatusOpen, CreatedAt: stamp, UpdatedAt: stamp,
		Leader: "lead", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "djb", PinnedAt: stamp},
		WriteSet: []string{"source.txt"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 2}, CurrentRound: 1}))
	return se, s, p, id
}

func terminalPolicyResult(t *testing.T, s *mission.Store, id string) {
	t.Helper()
	require.NoError(t, s.AppendResult(id, &mission.Result{Mission: id, Round: 1, Author: "bwk", Verdict: mission.VerdictPass,
		Confidence: 0.9, Evidence: []mission.EvidenceCheck{{Name: "focused check", Status: "pass"}}}))
}

func TestMissionTerminalSealCapturedPolicy(t *testing.T) {
	for _, method := range []string{"close", "abandon", "correct"} {
		for _, failPublication := range []bool{false, true} {
			name := method
			if failPublication {
				name += " publication interrupted"
			}
			t.Run(name, func(t *testing.T) {
				se, s, p, id := terminalPolicyFixture(t, true)
				args := []string{"mission", method, id, "--json"}
				if method != "abandon" {
					terminalPolicyResult(t, s, id)
				}
				switch method {
				case "abandon":
					args = append(args, "--reason", "cancelled")
				case "correct":
					_, err := s.Close(id, mission.StatusClosed)
					require.NoError(t, err)
					args = append(args, "--kind", "fabrication", "--claim", "old claim", "--corrected", "corrected claim")
				}
				index := publicationGitCLI(t, se, "rev-parse", "--path-format=absolute", "--git-path", "index")
				before, err := os.ReadFile(index)
				require.NoError(t, err)
				if failPublication {
					require.NoError(t, os.WriteFile(index+".lock", []byte("other writer\n"), 0o600))
				}
				stdout, stderr, exit := runCLI(t, se, args...)
				require.Equal(t, 0, exit, stderr)
				c, err := s.Load(id)
				require.NoError(t, err)
				wantStatus := mission.StatusClosed
				if method == "abandon" {
					wantStatus = mission.StatusAbandoned
				}
				assert.Equal(t, wantStatus, c.Status)
				if method == "correct" {
					corrections, warnings, err := s.LoadCorrections(id)
					require.NoError(t, err)
					assert.Empty(t, warnings)
					assert.Len(t, corrections, 1)
				}
				chunks, err := filepath.Glob(filepath.Join(p.LocalRoot(), "trace", "missions", id, "log-*.jsonl"))
				require.NoError(t, err)
				require.NotEmpty(t, chunks, "S must retain the actual tail")
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
				assert.Contains(t, history.String(), `"event":"`+method+`"`)
				assert.NoDirExists(t, filepath.Join(se.repo, ".punt-labs", "local", "ethos"))
				if failPublication {
					assert.Contains(t, stderr, "sealing mission log")
					after, err := os.ReadFile(index)
					require.NoError(t, err)
					assert.Equal(t, before, after)
					if method == "correct" {
						var payload struct {
							Warnings []string `json:"warnings"`
						}
						require.NoError(t, json.Unmarshal([]byte(stdout), &payload))
						assert.NotEmpty(t, payload.Warnings)
					}
				} else {
					assert.NotContains(t, stderr, "sealing mission log")
					staged := publicationGitCLI(t, se, "diff", "--cached", "--name-only")
					assert.Contains(t, staged, ".punt-labs/ethos/missions/"+id+"/log-")
				}
			})
		}
	}
}

func TestMissionExportFullPartialHistoryFreshClone(t *testing.T) {
	for _, missing := range []string{"result", "close", "abandon"} {
		t.Run(missing, func(t *testing.T) {
			se, s, p, id := terminalPolicyFixture(t, false)
			if missing == "close" {
				terminalPolicyResult(t, s, id)
			}
			_, err := hook.SealMissionWithPolicy(p, id, time.Now().UTC(), hook.SealOptions{})
			require.NoError(t, err)
			// The published create survives the clone. Later private history does
			// not: switching modes cannot promote the accepted result or terminal.
			require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"), []byte("record: blame\n"), 0o600))
			private, err := layout.NewRepository(se.repo, se.repo, layout.RecordBlame)
			require.NoError(t, err)
			s = mission.NewRepositoryStore(private, filepath.Join(se.home, ".punt-labs", "ethos")).WithSessionID("terminal-policy")
			method := "close"
			args := []string{"mission", method, id}
			if missing == "result" {
				terminalPolicyResult(t, s, id)
			}
			if missing == "abandon" {
				args = []string{"mission", "abandon", id, "--reason", "cancelled"}
			}
			_, stderr, exit := runCLI(t, se, args...)
			require.Equal(t, 0, exit, stderr)
			// Retained local history is complete. Export it outside the source
			// checkout, so the clone below still carries only eligible history.
			complete := filepath.Join(t.TempDir(), "complete")
			_, stderr, exit = runCLI(t, se, "mission", "export", id, "--full", "--dir", complete)
			require.Equal(t, 0, exit, stderr)
			assert.FileExists(t, filepath.Join(complete, id+".contract.yaml"))
			base := publicationGitCLI(t, se, "rev-parse", "HEAD")
			_, stderr, exit = runCLI(t, se, "mission", "publish", id, "--base", base)
			require.Equal(t, 0, exit, stderr)
			publicationGitCLI(t, se, "commit", "-m", "publish selected mission records and earlier eligible trace")
			clone := filepath.Join(t.TempDir(), "fresh-clone")
			publicationGitCLI(t, se, "clone", "--no-local", se.repo, clone)
			fresh := &cliSubprocessEnv{repo: clone, home: t.TempDir()}
			fresh.env = testGitEnv(fresh.home)
			assert.NoDirExists(t, filepath.Join(clone, ".punt-labs", "local", "ethos"))
			published, err := filepath.Glob(filepath.Join(clone, ".punt-labs", "ethos", "missions", id, "log-*.jsonl"))
			require.NoError(t, err)
			require.NotEmpty(t, published, "regression requires a nonempty but partial history")
			_, stderr, exit = runCLI(t, fresh, "mission", "export", id, "--full", "--dir", "refused-export")
			require.NotEqual(t, 0, exit, stderr)
			want := "round 1 accepted result event is unavailable"
			if missing == "close" {
				want = "terminal closed event is unavailable"
			} else if missing == "abandon" {
				want = "terminal abandoned event is unavailable"
			}
			assert.Contains(t, stderr, "full export incomplete: mission "+id+" "+want)
			assert.NoDirExists(t, filepath.Join(clone, "refused-export"), "refuse before writing even plain export output")
		})
	}
}
