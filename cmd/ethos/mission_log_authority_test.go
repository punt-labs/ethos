package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestMissionLogMissingCorrectionAuthority(t *testing.T) {
	home := missionTestEnv(t)
	seedClaudeIdentity(t, filepath.Join(home, ".punt-labs", "ethos"))
	id := closeCLIMission(t)
	missionCorrectKind = "decision"
	missionCorrectCorrected = "accepted annotation before shared authority loss"
	captureStdoutE(t, func() error { return runMissionCorrect(missionCorrectCmd, id) })
	store := missionStore()
	p, ok := store.RepositoryPolicy()
	require.True(t, ok)
	contract, err := store.Load(id)
	require.NoError(t, err)
	require.NotNil(t, contract.RoundDecisions)
	require.Equal(t, mission.StatusClosed, contract.Status)
	t.Cleanup(func() { missionLogEventFilter, missionLogSinceFilter = "", "" })
	readLog := func() mission.LogPayload {
		t.Helper()
		out := captureStdoutE(t, func() error {
			_, _, err := runCobra(t, "mission", "log", id, "--event", "correct", "--json")
			return err
		})
		var payload mission.LogPayload
		require.NoError(t, json.Unmarshal([]byte(out), &payload))
		require.Len(t, payload.Events, 1)
		assert.Equal(t, mission.EventCorrect, payload.Events[0].Event)
		return payload
	}
	accepted := readLog()
	require.Empty(t, accepted.Warnings)
	require.Equal(t, "accepted", accepted.Events[0].Acceptance)
	require.NotEmpty(t, accepted.Events[0].OperationID)
	path := filepath.Join(p.RecordRoot(), "missions", id, "corrections.yaml")
	require.NoError(t, os.Remove(path)) // Remove only authority; retain the accepted event.
	for range 2 {
		unknown := readLog()
		assert.Equal(t, accepted.Events[0].OperationID, unknown.Events[0].OperationID)
		assert.Equal(t, "unknown", unknown.Events[0].Acceptance)
		assert.Contains(t, strings.Join(unknown.Warnings, "\n"), "correction acceptance unknown: shared authority")
		assert.NoFileExists(t, path, "reading logs must not reconstruct correction authority")
	}
}
