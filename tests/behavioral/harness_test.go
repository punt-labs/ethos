//go:build behavioral

package behavioral

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFixtureMissionCreation(t *testing.T) {
	f := SetupFixture(t)
	id := f.CreateMission(t, `leader: test-leader
worker: test-agent
evaluator:
  handle: test-evaluator
write_set:
  - pkg/counter/counter.go
success_criteria:
  - verify the counter
budget:
  rounds: 2
  reflection_after_each: true
`)
	require.Equal(t, "implement", f.MissionShow(t, id)["type"])
	require.Empty(t, f.MissionResults(t, id))
	base := f.CommitMissionFixture(t, id)

	result := filepath.Join(f.Root, "result.yaml")
	writeFile(t, result, "mission: "+id+"\n"+`round: 1
author: test-agent
verdict: pass
confidence: 1
files_changed: []
evidence:
  - name: verify the counter
    status: pass
`)
	_, stderr, err := f.runEthos(t, "mission", "result", id, "--file", result)
	require.NoError(t, err, stderr)
	results := f.MissionResults(t, id)
	require.Len(t, results, 1)
	require.Equal(t, "pass", results[0]["verdict"])
	require.Equal(t, []string{missionResultPath(id)}, f.GitDiffFiles(t, base))

	f.Reflect(t, id, "mission: "+id+"\n"+`round: 1
author: test-leader
converging: true
signals:
  - verified the counter
recommendation: continue
`)
	reflections := f.MissionReflections(t, id)
	require.Len(t, reflections, 1)
	require.NotNil(t, reflections[0].Decision)
	require.Equal(t, "continue", reflections[0].Decision.Recommendation)
	require.Equal(t, float64(2), f.Advance(t, id)["current_round"])
}

func TestFixtureMCPStartupKeepsWorktreeClean(t *testing.T) {
	f := SetupFixture(t)
	base := f.GitHead(t)
	_, stderr, err := f.runEthos(t, "serve")
	require.NoError(t, err, stderr)
	require.Empty(t, f.GitDiffFiles(t, base))
}
