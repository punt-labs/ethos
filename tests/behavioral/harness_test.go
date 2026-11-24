//go:build behavioral

package behavioral

import (
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
  rounds: 1
`)
	require.Equal(t, "implement", f.MissionShow(t, id)["type"])
}

func TestFixtureMCPStartupKeepsWorktreeClean(t *testing.T) {
	f := SetupFixture(t)
	base := f.GitHead(t)
	_, stderr, err := f.runEthos(t, "serve")
	require.NoError(t, err, stderr)
	require.Empty(t, f.GitDiffFiles(t, base))
}
