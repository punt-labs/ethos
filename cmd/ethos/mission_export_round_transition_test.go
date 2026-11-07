//go:build linux || darwin

package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestMissionExportFullRoundTransitionFreshClone(t *testing.T) {
	for _, privateAdvance := range []bool{true, false} {
		name := "complete history without round one result"
		if privateAdvance {
			name = "missing private round_advanced"
		}
		t.Run(name, func(t *testing.T) {
			se, s, _, id := terminalPolicyFixture(t, false)
			binary, err := os.ReadFile(ethosBinary)
			require.NoError(t, err)
			t.Logf("CLI binary sha256=%x", sha256.Sum256(binary))
			input := filepath.Join(t.TempDir(), "reflection.yaml")
			require.NoError(t, os.WriteFile(input, []byte(fmt.Sprintf("mission: %s\nround: 1\nauthor: lead\nconverging: true\nsignals: [another round needed]\nrecommendation: continue\n", id)), 0o600))
			_, stderr, exit := runCLI(t, se, "mission", "reflect", id, "--file", input)
			require.Equal(t, 0, exit, stderr)
			reflection := filepath.ToSlash(filepath.Join(".punt-labs", "ethos", "missions", id, "reflections.yaml"))
			require.FileExists(t, filepath.Join(se.repo, filepath.FromSlash(reflection)))
			config := filepath.Join(se.repo, ".punt-labs", "ethos.yaml")
			if privateAdvance {
				require.NoError(t, os.WriteFile(config, []byte("record: blame\nagent: lead\n"), 0o600))
			}
			_, stderr, exit = runCLI(t, se, "mission", "advance", id)
			require.Equal(t, 0, exit, stderr)
			require.NoError(t, os.WriteFile(config, []byte("record: full\nagent: lead\n"), 0o600))
			input = filepath.Join(t.TempDir(), "result.yaml")
			require.NoError(t, os.WriteFile(input, []byte(fmt.Sprintf("mission: %s\nround: 2\nauthor: bwk\nverdict: pass\nconfidence: 0.9\nevidence:\n  - name: focused check\n    status: pass\n", id)), 0o600))
			_, stderr, exit = runCLI(t, se, "mission", "result", id, "--file", input)
			require.Equal(t, 0, exit, stderr)
			_, stderr, exit = runCLI(t, se, "mission", "close", id)
			require.Equal(t, 0, exit, stderr)
			c, err := s.Load(id)
			require.NoError(t, err)
			require.Equal(t, 2, c.CurrentRound)
			require.Len(t, c.RoundDecisions.Entries, 1)
			results, err := s.LoadResults(id)
			require.NoError(t, err)
			require.Len(t, results, 1, "advance without a round-one result remains legal")
			require.Equal(t, 2, results[0].Round)

			// The source's retained local trace is complete even when the advance
			// was private. Export elsewhere so it cannot backfill the clone.
			complete := filepath.Join(t.TempDir(), "complete-export")
			_, stderr, exit = runCLI(t, se, "mission", "export", id, "--full", "--dir", complete)
			require.Equal(t, 0, exit, stderr)
			assert.FileExists(t, filepath.Join(complete, id+".contract.yaml"))
			base := publicationGitCLI(t, se, "rev-parse", "HEAD")
			stdout, stderr, exit := runCLI(t, se, "mission", "publish", id, "--base", base, "--json")
			require.Equal(t, 0, exit, stderr)
			t.Logf("publication receipt: %s", stdout)
			// Full reflection publication writes an eligible copy, not an index
			// entry. Share that copy explicitly, without promoting private detail.
			publicationGitCLI(t, se, "add", "--", ".punt-labs/ethos.yaml", reflection)
			publicationGitCLI(t, se, "commit", "-m", "publish complete shared authority and eligible trace")
			t.Logf("fixture publication commit=%s", publicationGitCLI(t, se, "rev-parse", "HEAD"))
			clone := filepath.Join(t.TempDir(), "fresh-clone")
			publicationGitCLI(t, se, "clone", "--no-local", se.repo, clone)
			fresh := &cliSubprocessEnv{repo: clone, home: t.TempDir()}
			fresh.env = testGitEnv(fresh.home)
			require.NoDirExists(t, filepath.Join(clone, ".punt-labs", "local", "ethos"))
			require.NoDirExists(t, filepath.Join(fresh.home, ".punt-labs", "ethos"))
			require.FileExists(t, filepath.Join(clone, filepath.FromSlash(reflection)))
			chunks, err := filepath.Glob(filepath.Join(clone, ".punt-labs", "ethos", "missions", id, "log-*.jsonl"))
			require.NoError(t, err)
			require.NotEmpty(t, chunks, "must exercise nonempty, otherwise complete published history")
			var history strings.Builder
			for _, chunk := range chunks {
				data, err := os.ReadFile(chunk)
				require.NoError(t, err)
				history.Write(data)
				t.Logf("published chunk %s sha256=%x", filepath.Base(chunk), sha256.Sum256(data))
			}
			for _, event := range []string{"create", "reflect", "result", "close"} {
				require.Contains(t, history.String(), `"event":"`+event+`"`)
			}
			if privateAdvance {
				require.NotContains(t, history.String(), `"event":"round_advanced"`)
			} else {
				require.Contains(t, history.String(), `"event":"round_advanced"`)
			}
			stdout, stderr, exit = runCLI(t, fresh, "mission", "export", id, "--full", "--dir", "clone-export")
			t.Logf("fresh clone full export exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
			if privateAdvance {
				require.NotEqual(t, 0, exit, "full export must refuse missing round_advanced history")
				assert.Contains(t, stderr, "full export incomplete: mission "+id+" round 1 required round_advanced event is unavailable")
				assert.Empty(t, stdout)
				assert.NoDirExists(t, filepath.Join(clone, "clone-export"), "refuse before writing any export output")
			} else {
				require.Equal(t, 0, exit, stderr)
				assert.FileExists(t, filepath.Join(clone, "clone-export", id+".contract.yaml"))
			}
		})
	}
}

func TestMissionExportFullNonleaderAdvance(t *testing.T) {
	se, s, _, id := terminalPolicyFixture(t, false)
	require.NoError(t, s.AppendReflection(id, &mission.Reflection{
		Mission: id, Round: 1, Author: "lead", Signals: []string{"another round needed"},
		Recommendation: mission.RecommendationContinue, Reason: "continue",
	}))
	// MCP's actor override calls this same writer with any nonempty actor.
	// Export must not impose an actor == current leader constraint absent
	// from the supported write contract.
	round, err := s.AdvanceRound(id, "other")
	require.NoError(t, err)
	require.Equal(t, 2, round)
	c, err := s.Load(id)
	require.NoError(t, err)
	require.Equal(t, "lead", c.Leader)
	require.Equal(t, 2, c.CurrentRound)
	stdout, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "nonleader-export")
	t.Logf("nonleader advance full export exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	require.Equal(t, 0, exit, stderr)
	chunks, err := filepath.Glob(filepath.Join(se.repo, "nonleader-export", id, "log-*.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, chunks)
	var history strings.Builder
	for _, chunk := range chunks {
		data, err := os.ReadFile(chunk)
		require.NoError(t, err)
		history.Write(data)
	}
	assert.Contains(t, history.String(), `"event":"round_advanced","actor":"other"`)
}
