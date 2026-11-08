package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleMissionCorrectUnavailablePolicyWarning(t *testing.T) {
	h := testHandlerWithMissions(t) // Deliberately global-only legacy store.
	id := closeMissionForMCP(t, h)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "correct", "mission_id": id,
		"correction": fmt.Sprintf("mission: %s\nround: 0\nkind: fabrication\nauthor: djb\nclaim: old claim\ncorrected: corrected claim\n", id),
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, resultText(t, result))
	var payload struct {
		Warnings []string `json:"warnings"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &payload))
	assert.Contains(t, payload.Warnings, "sealing mission log: repository policy unavailable")
	corrections, warnings, err := h.missionStore.LoadCorrections(id)
	require.NoError(t, err)
	assert.Empty(t, warnings)
	assert.Len(t, corrections, 1)
}
