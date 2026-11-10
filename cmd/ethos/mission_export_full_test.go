//go:build linux || darwin

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/punt-labs/ethos/v4/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fullExportFixture(t *testing.T) (*cliSubprocessEnv, *mission.Store, string) {
	t.Helper()
	require.NotEmpty(t, ethosBinary)
	se := &cliSubprocessEnv{home: t.TempDir(), repo: t.TempDir()}
	se.env = testGitEnv(se.home)
	cmd := exec.Command("git", "init", se.repo)
	cmd.Env = se.env
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	require.NoError(t, os.WriteFile(filepath.Join(se.repo, "sentinel"), []byte("staged\n"), 0o600))
	cmd = exec.Command("git", "-C", se.repo, "add", "sentinel")
	cmd.Env = se.env
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	p, err := layout.NewRepository(se.repo, se.repo, layout.RecordBlame)
	require.NoError(t, err)
	s := mission.NewRepositoryStore(p, filepath.Join(se.home, ".punt-labs", "ethos")).WithSessionID("export-session")
	id := "m-2026-11-04-811"
	stamp := "2026-11-04T10:00:00Z"
	c := &mission.Contract{MissionID: id, Status: mission.StatusOpen, CreatedAt: stamp, UpdatedAt: stamp,
		Leader: "claude", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "djb", PinnedAt: stamp},
		WriteSet: []string{"source.txt"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 2}, CurrentRound: 1}
	require.NoError(t, s.Create(c))
	require.NoError(t, s.AppendReflection(id, &mission.Reflection{Mission: id, Round: 1, Author: "claude",
		Signals: []string{"private-reflection-signal"}, Recommendation: mission.RecommendationContinue, Reason: "continue"}))
	_, err = mission.WriteDelegationSkeletonWithPolicy(p, id, "d-export", mission.DelegationSkeleton{
		Tier: mission.TierB, AgentType: "bwk", PromptHash: "hash", Prompt: []byte("private-prompt-signal read " + se.home + "/notes token=ghp_abcdefghijklmnopqrstuvwxyz0123456789abcd")})
	require.NoError(t, err)
	// Stamp once the mission tree exists: export sees the planted records,
	// not what the storage upgrade made of them.
	stampFixture(t, se.home, se.repo)
	return se, s, id
}

func TestMissionExportFullSealsSelectedTraceWithoutStaging(t *testing.T) {
	for _, mode := range []string{"blame", "full"} {
		t.Run(mode, func(t *testing.T) {
			se, _, id := fullExportFixture(t)
			require.NoError(t, os.WriteFile(filepath.Join(se.repo, ".punt-labs", "ethos.yaml"), []byte("record: "+mode+"\n"), 0o600))
			local := filepath.Join(se.repo, ".punt-labs", "local", "ethos")
			for _, path := range []string{"sessions/s.audit.jsonl", "scratch/artifact", "quarantine/raw", "missions/other/s.log.jsonl"} {
				name := filepath.Join(local, filepath.FromSlash(path))
				require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o700))
				require.NoError(t, os.WriteFile(name, []byte("excluded-detail\n"), 0o600))
			}
			before, err := os.ReadFile(filepath.Join(se.repo, ".git", "index"))
			require.NoError(t, err)
			_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "exported")
			require.Equal(t, 0, exit, stderr)
			after, err := os.ReadFile(filepath.Join(se.repo, ".git", "index"))
			require.NoError(t, err)
			assert.Equal(t, before, after)
			var detail strings.Builder
			chunks := 0
			err = filepath.WalkDir(filepath.Join(se.repo, "exported"), func(path string, entry os.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				assert.False(t, strings.HasSuffix(path, ".log.jsonl") || strings.HasSuffix(path, ".audit.jsonl"))
				if strings.HasPrefix(entry.Name(), "log-") {
					chunks++
					assert.FileExists(t, filepath.Join(local, "trace", "missions", id, entry.Name()))
				}
				b, err := os.ReadFile(path)
				detail.Write(b)
				return err
			})
			require.NoError(t, err)
			assert.Positive(t, chunks)
			assert.Contains(t, detail.String(), "private-prompt-signal")
			assert.Contains(t, detail.String(), "private-reflection-signal")
			assert.NotContains(t, detail.String(), "excluded-detail")
			assert.NotContains(t, detail.String(), se.home)
			assert.NotContains(t, detail.String(), "ghp_abcdefghijklmnopqrstuvwxyz0123456789abcd")
			// The second export must not rewrite any immutable event chunks.
			_, stderr, exit = runCLI(t, se, "mission", "export", id, "--full", "--dir", "exported")
			require.Equal(t, 0, exit, stderr)
		})
	}
}

func TestMissionExportFullReportsMissingHistory(t *testing.T) {
	for _, kind := range []string{"prompt", "reflection", "events", "malformed-authority"} {
		t.Run(kind, func(t *testing.T) {
			se, s, id := fullExportFixture(t)
			p, ok := s.RepositoryPolicy()
			require.True(t, ok)
			switch kind {
			case "prompt":
				require.NoError(t, os.Remove(filepath.Join(p.LocalRoot(), "trace", "missions", id, "delegations", "d-export", "prompt.md")))
			case "reflection":
				require.NoError(t, os.Remove(filepath.Join(p.LocalRoot(), "trace", "missions", id, "reflection-1.yaml")))
			case "events":
				require.NoError(t, os.Remove((audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: "export-session"}).LivePath(p)))
			case "malformed-authority":
				require.NoError(t, os.WriteFile(filepath.Join(p.RecordRoot(), "missions", id, "contract.yaml"), []byte("round_decisions: null\n"), 0o600))
			}
			_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "incomplete")
			require.NotEqual(t, 0, exit, stderr)
			if kind != "malformed-authority" {
				assert.Contains(t, stderr, "incomplete")
			}
			assert.NoDirExists(t, filepath.Join(se.repo, "incomplete"))
		})
	}
}

func TestMissionExportFullMixedEligibilityPreservesPublishedChunk(t *testing.T) {
	se, _, p, id := terminalPolicyFixture(t, false)
	participant := session.Participant{AgentID: "lead"}
	require.NoError(t, session.NewStore(filepath.Join(se.home, ".punt-labs", "ethos")).CreateInCheckout(
		"terminal-policy", participant, participant, se.repo, se.repo, "fixture"))
	se.env = append(se.env, "ETHOS_SESSION=terminal-policy")
	// Create A in full, accept result B in blame, then close C in full.
	// With no intervening seal, close retains ABC but publishes AC using the
	// same endpoint-based filename. Default full export must add B, not try
	// to replace the immutable AC chunk with ABC.
	config := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
	require.NoError(t, os.WriteFile(config, []byte("record: blame\nagent: lead\n"), 0o600))
	input := filepath.Join(t.TempDir(), "result.yaml")
	require.NoError(t, os.WriteFile(input, []byte(fmt.Sprintf("mission: %s\nround: 1\nauthor: bwk\nverdict: pass\nconfidence: 0.9\nevidence:\n  - name: focused check\n    status: pass\n", id)), 0o600))
	_, stderr, exit := runCLI(t, se, "mission", "result", id, "--file", input)
	require.Equal(t, 0, exit, stderr)
	require.NoError(t, os.WriteFile(config, []byte("record: full\nagent: lead\n"), 0o600))
	_, stderr, exit = runCLI(t, se, "mission", "close", id)
	require.Equal(t, 0, exit, stderr)
	require.NotContains(t, stderr, "sealing mission log")

	dir := filepath.Join(p.CheckoutRecordRoot(), "missions", id)
	published, err := filepath.Glob(filepath.Join(dir, "log-*.jsonl"))
	require.NoError(t, err)
	require.Len(t, published, 1)
	original, err := os.ReadFile(published[0])
	require.NoError(t, err)
	require.Contains(t, string(original), `"event":"create"`)
	require.Contains(t, string(original), `"event":"close"`)
	require.NotContains(t, string(original), `"event":"result"`)
	retained, err := os.ReadFile(filepath.Join(p.LocalRoot(), "trace", "missions", id, filepath.Base(published[0])))
	require.NoError(t, err)
	require.Contains(t, string(retained), `"event":"result"`)
	index, err := os.ReadFile(filepath.Join(se.repo, ".git", "index"))
	require.NoError(t, err)

	var firstExport map[string][]byte
	for attempt := 0; attempt < 2; attempt++ {
		stdout, stderr, exit := runCLI(t, se, "mission", "export", id, "--full")
		t.Logf("default full export attempt=%d exit=%d stdout=%q stderr=%q", attempt+1, exit, stdout, stderr)
		require.Equal(t, 0, exit, stderr)
		after, err := os.ReadFile(published[0])
		require.NoError(t, err)
		assert.Equal(t, original, after, "the sparse publication is immutable")
		after, err = os.ReadFile(filepath.Join(se.repo, ".git", "index"))
		require.NoError(t, err)
		assert.Equal(t, index, after, "explicit export never stages")
		chunks, err := filepath.Glob(filepath.Join(dir, "log-*.jsonl"))
		require.NoError(t, err)
		seen := map[string]bool{}
		inventory := map[string][]byte{}
		for _, path := range chunks {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			inventory[path] = data
			for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
				var event mission.Event
				require.NoError(t, json.Unmarshal(line, &event))
				seen[event.Event] = true
			}
		}
		assert.Equal(t, map[string]bool{"create": true, "result": true, "close": true}, seen)
		if attempt == 0 {
			firstExport = inventory
		} else {
			assert.Equal(t, firstExport, inventory, "repeat export neither adds nor rewrites chunks")
		}
		union, err := audit.ReadStream(p, audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: "terminal-policy"})
		require.NoError(t, err)
		assert.Len(t, union.Lines, 3, "equal publication/export/archive overlaps deduplicate")
	}
}

func TestMissionExportFullRefusesDivergentExportChunk(t *testing.T) {
	se, _, id := fullExportFixture(t)
	_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "exported")
	require.Equal(t, 0, exit, stderr)
	chunks, err := filepath.Glob(filepath.Join(se.repo, "exported", id, "log-*.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, chunks)
	old, err := os.ReadFile(chunks[0])
	require.NoError(t, err)
	divergent := bytes.ReplaceAll(old, []byte(`"actor":"claude"`), []byte(`"actor":"other"`))
	require.NotEqual(t, old, divergent)
	require.NoError(t, os.WriteFile(chunks[0], divergent, 0o600))
	_, stderr, exit = runCLI(t, se, "mission", "export", id, "--full", "--dir", "exported")
	require.NotEqual(t, 0, exit)
	assert.Contains(t, stderr, "full export refuses to rewrite immutable chunk")
	after, err := os.ReadFile(chunks[0])
	require.NoError(t, err)
	assert.Equal(t, divergent, after)
}
