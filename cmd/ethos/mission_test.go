package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// missionTestEnv sets HOME to a fresh temp directory and resets the
// global flag state used by mission commands. The returned path is the
// HOME root for the test.
func missionTestEnv(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Reset the package-level flag globals so cross-test contamination
	// (e.g. a leaked --json from a prior test) does not bleed in.
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = false })

	missionCreateFile = ""
	missionCreateLeader = ""
	missionCreateWorker = ""
	missionCreateEvaluator = ""
	missionCreateBead = ""
	missionCreateRounds = 3
	missionListStatus = "open"
	missionCloseStatus = mission.StatusClosed
	t.Cleanup(func() {
		missionCreateFile = ""
		missionCreateLeader = ""
		missionCreateWorker = ""
		missionCreateEvaluator = ""
		missionCreateBead = ""
		missionCreateRounds = 3
		missionListStatus = "open"
		missionCloseStatus = mission.StatusClosed
	})
	return tmp
}

// writeContractFile drops a YAML contract into a temp file and returns
// the path. The contract is valid except for the fields the CLI fills
// in (mission_id, status, created_at, updated_at, evaluator.pinned_at).
func writeContractFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "contract.yaml")
	body := `leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.5
  files:
    - internal/session/store.go
write_set:
  - internal/mission/
  - cmd/ethos/mission.go
tools:
  - Read
  - Write
success_criteria:
  - make check passes
budget:
  rounds: 3
  reflection_after_each: true
context: "smoke test contract"
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestMissionCreate_FromFile(t *testing.T) {
	missionTestEnv(t)

	missionCreateFile = writeContractFile(t)
	out := captureStdout(t, runMissionCreate)
	assert.Contains(t, out, "Created mission ")

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	c, err := ms.Load(ids[0])
	require.NoError(t, err)
	assert.Equal(t, mission.StatusOpen, c.Status)
	assert.Equal(t, "claude", c.Leader)
	assert.Equal(t, "bwk", c.Worker)
	assert.Equal(t, "djb", c.Evaluator.Handle)
	assert.NotEmpty(t, c.CreatedAt)
	assert.NotEmpty(t, c.Evaluator.PinnedAt)
}

func TestMissionCreate_FromFileJSON(t *testing.T) {
	missionTestEnv(t)
	jsonOutput = true

	missionCreateFile = writeContractFile(t)
	out := captureStdout(t, runMissionCreate)

	var c mission.Contract
	require.NoError(t, json.Unmarshal([]byte(out), &c))
	assert.Equal(t, mission.StatusOpen, c.Status)
	assert.NotEmpty(t, c.MissionID)
}

func TestMissionCreate_FromFlags_Defaults(t *testing.T) {
	missionTestEnv(t)
	missionCreateLeader = "claude"
	missionCreateWorker = "bwk"
	missionCreateEvaluator = "djb"
	missionCreateBead = "ethos-07m.5"

	out := captureStdout(t, runMissionCreate)
	assert.Contains(t, out, "Created mission ")

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	c, err := ms.Load(ids[0])
	require.NoError(t, err)
	assert.Equal(t, "claude", c.Leader)
	assert.Equal(t, "bwk", c.Worker)
	assert.Equal(t, "ethos-07m.5", c.Bead)
	assert.Equal(t, 3, c.Budget.Rounds)
}

func TestMissionShow(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	id := ids[0]

	out := captureStdout(t, func() { runMissionShow(id) })
	assert.Contains(t, out, id)
	assert.Contains(t, out, "claude")
	assert.Contains(t, out, "bwk")
	assert.Contains(t, out, "djb")
	assert.Contains(t, out, "internal/mission/")
}

func TestMissionShow_Prefix(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	// First-7 characters should be enough to disambiguate a single mission.
	prefix := ids[0][:8]
	out := captureStdout(t, func() { runMissionShow(prefix) })
	assert.Contains(t, out, ids[0])
}

func TestMissionShow_JSON(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	jsonOutput = true
	out := captureStdout(t, func() { runMissionShow(ids[0]) })

	var c mission.Contract
	require.NoError(t, json.Unmarshal([]byte(out), &c))
	assert.Equal(t, ids[0], c.MissionID)
}

func TestMissionList_Empty(t *testing.T) {
	missionTestEnv(t)
	out := captureStdout(t, func() { runMissionList("open") })
	assert.Contains(t, out, "No missions found")
}

func TestMissionList_FilterByStatus(t *testing.T) {
	missionTestEnv(t)

	// Create three missions.
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)
	captureStdout(t, runMissionCreate)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 3)

	// Close one. The other two stay open.
	require.NoError(t, ms.Close(ids[0], mission.StatusClosed))

	// Default filter "open" returns the two open ones.
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = false })
	out := captureStdout(t, func() { runMissionList("open") })
	var openEntries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &openEntries))
	assert.Len(t, openEntries, 2)

	// "all" returns all three.
	out = captureStdout(t, func() { runMissionList("all") })
	var allEntries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &allEntries))
	assert.Len(t, allEntries, 3)

	// "closed" returns just the one.
	out = captureStdout(t, func() { runMissionList("closed") })
	var closedEntries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &closedEntries))
	assert.Len(t, closedEntries, 1)
}

func TestMissionClose(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	out := captureStdout(t, func() { runMissionClose(ids[0], mission.StatusClosed) })
	assert.Contains(t, out, "Closed mission")

	c, err := ms.Load(ids[0])
	require.NoError(t, err)
	assert.Equal(t, mission.StatusClosed, c.Status)
	assert.NotEmpty(t, c.ClosedAt)
}

func TestMissionClose_PrefixMatch(t *testing.T) {
	missionTestEnv(t)
	missionCreateFile = writeContractFile(t)
	captureStdout(t, runMissionCreate)

	ms := missionStore()
	ids, err := ms.List()
	require.NoError(t, err)
	require.Len(t, ids, 1)

	prefix := ids[0][:9]
	captureStdout(t, func() { runMissionClose(prefix, mission.StatusFailed) })

	c, err := ms.Load(ids[0])
	require.NoError(t, err)
	assert.Equal(t, mission.StatusFailed, c.Status)
}

func TestStatusMatches(t *testing.T) {
	tests := []struct {
		filter string
		status string
		want   bool
	}{
		{"", "open", true},
		{"all", "closed", true},
		{"open", "open", true},
		{"open", "closed", false},
		{"closed", "closed", true},
		{"failed", "closed", false},
	}
	for _, tt := range tests {
		t.Run(tt.filter+"/"+tt.status, func(t *testing.T) {
			assert.Equal(t, tt.want, statusMatches(tt.filter, tt.status))
		})
	}
}
