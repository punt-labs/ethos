//go:build linux || darwin

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestMissionExportFullMissingCorrectionAuthority(t *testing.T) {
	for _, state := range []string{"missing-v1", "malformed-v1", "initialized-empty-v1", "missing-legacy"} {
		t.Run(state, func(t *testing.T) {
			se, s, id := fullExportFixture(t)
			require.NoError(t, s.AppendResult(id, &mission.Result{
				Mission: id, Round: 1, Author: "bwk", Verdict: mission.VerdictPass, Confidence: 0.8,
				Evidence: []mission.EvidenceCheck{{Name: "focused check", Status: mission.EvidenceStatusPass}},
			}))
			_, err := s.Close(id, mission.StatusClosed)
			require.NoError(t, err)
			p, ok := s.RepositoryPolicy()
			require.True(t, ok)
			corrections := filepath.Join(p.RecordRoot(), "missions", id, "corrections.yaml")
			contract := filepath.Join(p.RecordRoot(), "missions", id, "contract.yaml")
			if state == "missing-v1" {
				// Lose both the shared correction authority and its isolated
				// stream, while retaining create/reflection/result/close history.
				corrector := s.WithSessionID("lost-correction")
				require.NoError(t, corrector.Correct(id, mission.Correction{
					Mission: id, Author: "claude", Kind: mission.CorrectionDecision, Corrected: "accepted annotation",
				}))
				entries, warnings, err := s.LoadCorrections(id)
				require.NoError(t, err)
				require.Empty(t, warnings)
				require.Len(t, entries, 1)
				stream := audit.Stream{Namespace: audit.MissionNS, Mission: id, Session: "lost-correction"}
				require.NoError(t, os.Remove(stream.LivePath(p)))
				remaining, err := audit.ReadStream(p, stream)
				require.NoError(t, err)
				require.Empty(t, remaining.Lines)
			}
			switch state {
			case "missing-v1", "missing-legacy":
				require.NoError(t, os.Remove(corrections))
			case "malformed-v1":
				require.NoError(t, os.WriteFile(corrections, []byte("version: [broken\n"), 0o600))
			}
			if state == "missing-v1" {
				events, warnings, err := s.LoadEvents(id)
				require.NoError(t, err)
				require.Empty(t, warnings)
				counts := map[string]int{}
				for _, event := range events {
					counts[event.Event]++
				}
				assert.Equal(t, map[string]int{"create": 1, "reflect": 1, "result": 1, "close": 1}, counts)
			}
			if state == "missing-legacy" {
				data, err := os.ReadFile(contract)
				require.NoError(t, err)
				var document yaml.Node
				require.NoError(t, yaml.Unmarshal(data, &document))
				mapping := document.Content[0]
				for i := 0; i < len(mapping.Content); i += 2 {
					if mapping.Content[i].Value == "round_decisions" {
						mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
						break
					}
				}
				data, err = yaml.Marshal(&document)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(contract, data, 0o600))
				loaded, err := s.Load(id)
				require.NoError(t, err)
				require.Nil(t, loaded.RoundDecisions)
			}
			// Stamp after the authority is removed: export, not the storage
			// upgrade's decision step, is what this test exercises against it.
			stampCurrent(t, p)
			retained := map[string][]byte{}
			for _, path := range []string{contract, filepath.Join(p.RecordRoot(), "missions", id, "results.yaml"), filepath.Join(se.repo, ".git", "index")} {
				retained[path], err = os.ReadFile(path)
				require.NoError(t, err)
			}
			if state == "malformed-v1" || state == "initialized-empty-v1" {
				retained[corrections], err = os.ReadFile(corrections)
				require.NoError(t, err)
			}
			for range 2 {
				_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", "selected-export")
				if state == "missing-v1" || state == "malformed-v1" || state == "missing-legacy" {
					assert.NotEqual(t, 0, exit, stderr)
					assert.Contains(t, stderr, "full export incomplete")
					if state == "missing-v1" {
						assert.Contains(t, stderr, "corrections authority unavailable")
					} else if state == "missing-legacy" {
						assert.Contains(t, stderr, "legacy_unknown")
					}
					assert.NoDirExists(t, filepath.Join(se.repo, "selected-export"))
				} else {
					require.Equal(t, 0, exit, stderr)
					assert.FileExists(t, filepath.Join(se.repo, "selected-export", id+".contract.yaml"))
				}
				for path, before := range retained {
					after, err := os.ReadFile(path)
					require.NoError(t, err)
					assert.Equal(t, before, after)
				}
				if state == "missing-v1" || state == "missing-legacy" {
					assert.NoFileExists(t, corrections, "export must not reconstruct missing authority")
				}
				if state == "missing-legacy" {
					loaded, err := s.Load(id)
					require.NoError(t, err)
					assert.Nil(t, loaded.RoundDecisions, "export must not initialize legacy authority")
				}
			}
		})
	}
}
