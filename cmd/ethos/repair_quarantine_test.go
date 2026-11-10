//go:build linux || darwin

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestCLIRepairRetainsActualQuarantinePublications(t *testing.T) {
	require.NotEmpty(t, ethosBinary, "real CLI must build")
	for _, kind := range []string{"current mission full publication", "current mission full export under blame", "full session publication"} {
		t.Run(kind, func(t *testing.T) {
			var se *cliSubprocessEnv
			var chunks []string
			if kind == "current mission full publication" {
				se = setupCLISubprocessEnv(t)
				repairPut(t, filepath.Join(se.repo, ".punt-labs", "ethos.yaml"), "record: full\n")
				p, err := layout.NewRepository(se.repo, se.repo, layout.RecordFull)
				require.NoError(t, err)
				s := mission.NewRepositoryStore(p, filepath.Join(se.home, ".punt-labs", "ethos")).WithSessionID("repair-session")
				id, stamp := "m-2026-11-04-812", "2026-11-04T10:00:00Z"
				require.NoError(t, s.Create(&mission.Contract{
					MissionID: id, Status: mission.StatusOpen, CreatedAt: stamp, UpdatedAt: stamp,
					Leader: "claude", Worker: "bwk", Evaluator: mission.Evaluator{Handle: "djb", PinnedAt: stamp},
					WriteSet: []string{"source.txt"}, SuccessCriteria: []string{"checked"}, Budget: mission.Budget{Rounds: 2}, CurrentRound: 1,
				}))
				assert.Contains(t, string(repairRead(t, filepath.Join(p.RecordRoot(), "missions", id, "contract.yaml"))), "round_decisions:")
				_, stderr, exit := runCLI(t, se, "audit", "seal")
				require.Equal(t, 0, exit, stderr)
				chunks, err = filepath.Glob(filepath.Join(p.RecordRoot(), "missions", id, "log-*.jsonl"))
				require.NoError(t, err)
				require.NotEmpty(t, chunks, "exercise actual current mission publication without a flat export")
				assert.Contains(t, string(repairRead(t, chunks[0])), `"record":"full"`)
				assert.NoFileExists(t, filepath.Join(p.RecordRoot(), "missions", id+".contract.yaml"))
			} else if kind == "current mission full export under blame" {
				var id string
				se, _, id = fullExportFixture(t)
				root := filepath.Join(se.repo, ".punt-labs", "ethos", "missions")
				_, stderr, exit := runCLI(t, se, "mission", "export", id, "--full", "--dir", root)
				require.Equal(t, 0, exit, stderr)
				var err error
				chunks, err = filepath.Glob(filepath.Join(root, id, "log-*.jsonl"))
				require.NoError(t, err)
				require.NotEmpty(t, chunks, "exercise actual exported mission events")
				assert.Contains(t, string(repairRead(t, chunks[0])), `"record":"blame"`)
			} else {
				se = setupCLISubprocessEnv(t)
				repairPut(t, filepath.Join(se.repo, ".punt-labs", "ethos.yaml"), "record: full\n")
				p, err := layout.NewRepository(se.repo, se.repo, layout.RecordFull)
				require.NoError(t, err)
				s := audit.Stream{Namespace: audit.SessionNS, Session: "repair-session"}
				ts, err := audit.ParseLineTS("2026-11-04T10:00:00Z")
				require.NoError(t, err)
				line, err := audit.StampEligibility(p, s, ts, []byte(`{"ts":"2026-11-04T10:00:00Z","session":"repair-session","tool":"Read"}`))
				require.NoError(t, err)
				require.NoError(t, audit.WriteLocalAtomic(p, s.LivePath(p), append(line, '\n')))
				_, stderr, exit := runCLI(t, se, "audit", "seal")
				require.Equal(t, 0, exit, stderr)
				chunks, err = filepath.Glob(filepath.Join(p.RecordRoot(), "sessions", "*-repair-session", "audit-*.jsonl"))
				require.NoError(t, err)
				require.Len(t, chunks, 1, "exercise actual full-session publication")
				assert.Contains(t, string(repairRead(t, chunks[0])), `"record":"full"`)
			}
			// Keep an unrelated staged path and the source chunks in the real index.
			repairPut(t, filepath.Join(se.repo, "sentinel"), "staged sentinel\n")
			gitRunEnv(t, se.repo, se.env, "add", "--", "sentinel", ".punt-labs/ethos")
			index := filepath.Join(se.repo, ".git", "index")
			beforeIndex := repairRead(t, index)
			alternate := filepath.Join(t.TempDir(), "index")
			require.NoError(t, os.WriteFile(alternate, beforeIndex, 0o600))
			se.env = append(se.env, "GIT_INDEX_FILE="+alternate)
			repairPut(t, chunks[0], "damaged published chunk\n")
			_, stderr, exit := runCLI(t, se, "audit", "quarantine", chunks[0])
			require.Equal(t, 0, exit, stderr)
			assert.Contains(t, stderr, "retired damaged shared chunk")
			assert.NoFileExists(t, chunks[0])
			markerPath := strings.TrimSuffix(chunks[0], ".jsonl") + ".quarantine"
			beforeMarker := repairRead(t, markerPath)
			var marker audit.Marker
			require.NoError(t, json.Unmarshal(beforeMarker, &marker))
			assert.True(t, marker.HasGap(), "shared marker must retain the published availability gap")
			assert.NotContains(t, string(beforeMarker), `"record"`, "marker cannot depend on full-consent field protection")
			assert.Equal(t, beforeIndex, repairRead(t, index))
			assert.Equal(t, beforeIndex, repairRead(t, alternate))
			obsolete := filepath.Join(se.repo, ".punt-labs", "ethos", "missions", "m-obsolete", "log.jsonl")
			repairPut(t, obsolete, "obsolete legacy trace\n")
			repairPut(t, filepath.Join(filepath.Dir(obsolete), "contract.yaml"), "mission_id: m-obsolete\n")
			stdout, stderr, exit := runCLI(t, se, "repair", "--dry-run")
			require.Equal(t, 0, exit, stderr)
			assert.Contains(t, stdout, obsolete)
			assert.NotContains(t, stdout, markerPath)
			assert.Equal(t, beforeMarker, repairRead(t, markerPath))
			assert.FileExists(t, obsolete, "inventory is read-only")
			stdout, stderr, exit = runCLI(t, se, "repair", "--yes")
			require.Equal(t, 0, exit, stderr)
			assert.Contains(t, stdout, "permanently deletes")
			assert.Contains(t, stdout, "Stop ALL old/new writers")
			assert.Contains(t, stdout, "Deleted 1 files")
			assert.NoFileExists(t, obsolete)
			assert.Equal(t, beforeMarker, repairRead(t, markerPath))
			assert.Equal(t, beforeIndex, repairRead(t, index))
			assert.Equal(t, beforeIndex, repairRead(t, alternate))
			stdout, stderr, exit = runCLI(t, se, "repair", "--yes")
			require.Equal(t, 0, exit, stderr)
			assert.Contains(t, stdout, "Deleted 0 files")
			assert.Equal(t, beforeMarker, repairRead(t, markerPath))
			assert.Equal(t, beforeIndex, repairRead(t, index))
			assert.Equal(t, beforeIndex, repairRead(t, alternate))
		})
	}
}
