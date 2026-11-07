package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/layout"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

func TestHandleMissionLogMissingCorrectionAuthority(t *testing.T) {
	h := testHandlerWithMissions(t)
	root := t.TempDir()
	p, err := layout.NewRepository(root, root, layout.RecordBlame)
	require.NoError(t, err)
	h.missionStore = mission.NewRepositoryStore(p, h.missionStore.Root()).WithSessionID("s-log-authority")
	id := closeMissionForMCP(t, h)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "correct", "mission_id": id,
		"correction": fmt.Sprintf("mission: %s\nkind: decision\nauthor: djb\ncorrected: accepted annotation before shared authority loss\n", id),
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, resultText(t, result))
	contract, err := h.missionStore.Load(id)
	require.NoError(t, err)
	require.NotNil(t, contract.RoundDecisions)
	require.Equal(t, mission.StatusClosed, contract.Status)
	readLog := func() mission.LogPayload {
		t.Helper()
		result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
			"method": "log", "mission_id": id, "event": "correct",
		}))
		require.NoError(t, err)
		require.False(t, result.IsError, resultText(t, result))
		var payload mission.LogPayload
		require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &payload))
		require.Len(t, payload.Events, 1)
		assert.Equal(t, mission.EventCorrect, payload.Events[0].Event)
		return payload
	}
	accepted := readLog()
	require.Empty(t, accepted.Warnings)
	require.Equal(t, "accepted", accepted.Events[0].Acceptance)
	require.NotEmpty(t, accepted.Events[0].OperationID)
	path := filepath.Join(p.RecordRoot(), "missions", id, "corrections.yaml")
	require.NoError(t, os.Remove(path))
	for range 2 {
		unknown := readLog()
		assert.Equal(t, accepted.Events[0].OperationID, unknown.Events[0].OperationID)
		assert.Equal(t, "unknown", unknown.Events[0].Acceptance)
		assert.Contains(t, strings.Join(unknown.Warnings, "\n"), "correction acceptance unknown: shared authority")
		assert.NoFileExists(t, path, "MCP log reads must not reconstruct correction authority")
	}
}
