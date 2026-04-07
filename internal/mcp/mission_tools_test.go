package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/mission"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validContractYAML is a minimal valid contract body the MCP create
// handler accepts. It omits server-controlled fields (mission_id,
// status, timestamps) which the handler fills in.
const validContractYAML = `leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.5
write_set:
  - internal/mission/
success_criteria:
  - make check passes
budget:
  rounds: 3
  reflection_after_each: true
`

func testHandlerWithMissions(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	s := identity.NewStore(dir)
	root := s.Root()
	ms := mission.NewStore(root)
	return NewHandlerWithOptions(s,
		attribute.NewStore(root, attribute.Talents),
		attribute.NewStore(root, attribute.Personalities),
		attribute.NewStore(root, attribute.WritingStyles),
		WithMissionStore(ms),
	)
}

func TestHandleMission_NoStoreConfigured(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	root := s.Root()
	h := NewHandlerWithOptions(s,
		attribute.NewStore(root, attribute.Talents),
		attribute.NewStore(root, attribute.Personalities),
		attribute.NewStore(root, attribute.WritingStyles),
	)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "mission store not configured")
}

func TestHandleMission_Create(t *testing.T) {
	h := testHandlerWithMissions(t)

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	var c mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &c))
	assert.NotEmpty(t, c.MissionID)
	assert.Equal(t, mission.StatusOpen, c.Status)
	assert.Equal(t, "claude", c.Leader)
	assert.Equal(t, "bwk", c.Worker)
	assert.Equal(t, "djb", c.Evaluator.Handle)
}

func TestHandleMission_CreateMissingContract(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "create",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "contract YAML body is required")
}

func TestHandleMission_CreateMalformedYAML(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": "leader: [unterminated",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "parsing contract")
}

func TestHandleMission_CreateRejectsUnknownField(t *testing.T) {
	// KnownFields(true) must reject typo'd or smuggled keys.
	h := testHandlerWithMissions(t)
	body := validContractYAML + "\nunknown_field: gotcha\n"
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": body,
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "parsing contract")
}

func TestHandleMission_CreateRejectsBadWriteSet(t *testing.T) {
	h := testHandlerWithMissions(t)
	body := `leader: claude
worker: bwk
evaluator:
  handle: djb
write_set:
  - "../etc/passwd"
success_criteria:
  - make check passes
budget:
  rounds: 3
`
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": body,
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "path traversal")
}

func TestHandleMission_Show(t *testing.T) {
	h := testHandlerWithMissions(t)

	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	require.False(t, createResult.IsError)

	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	showResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "show",
		"mission_id": created.MissionID,
	}))
	require.NoError(t, err)
	assert.False(t, showResult.IsError)

	var loaded mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, showResult)), &loaded))
	assert.Equal(t, created.MissionID, loaded.MissionID)
	assert.Equal(t, mission.StatusOpen, loaded.Status)
}

func TestHandleMission_ShowByPrefix(t *testing.T) {
	h := testHandlerWithMissions(t)

	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)

	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	prefix := created.MissionID[:8]
	showResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "show",
		"mission_id": prefix,
	}))
	require.NoError(t, err)
	assert.False(t, showResult.IsError)
}

func TestHandleMission_ShowMissingID(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "show",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "mission_id is required")
}

func TestHandleMission_ShowNotFound(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "show",
		"mission_id": "m-9999-12-31-001",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestHandleMission_List(t *testing.T) {
	h := testHandlerWithMissions(t)

	// Create three missions back to back; counter rolls 001 → 003.
	for i := 0; i < 3; i++ {
		_, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
			"method":   "create",
			"contract": validContractYAML,
		}))
		require.NoError(t, err)
	}

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)

	var entries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &entries))
	assert.Len(t, entries, 3)
}

func TestHandleMission_ListEmptyReturnsArray(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
	require.NoError(t, err)
	assert.False(t, result.IsError)
	assert.Equal(t, "[]", resultText(t, result))
}

func TestHandleMission_ListFilterStatus(t *testing.T) {
	h := testHandlerWithMissions(t)

	for i := 0; i < 2; i++ {
		_, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
			"method":   "create",
			"contract": validContractYAML,
		}))
		require.NoError(t, err)
	}

	listResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "list",
	}))
	require.NoError(t, err)
	var openEntries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(resultText(t, listResult)), &openEntries))
	require.Len(t, openEntries, 2)

	// Close one.
	firstID, _ := openEntries[0]["mission_id"].(string)
	require.NotEmpty(t, firstID)
	_, err = h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "close",
		"mission_id": firstID,
	}))
	require.NoError(t, err)

	// Status=open returns one.
	listResult, err = h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "list",
		"status": "open",
	}))
	require.NoError(t, err)
	var afterClose []map[string]any
	require.NoError(t, json.Unmarshal([]byte(resultText(t, listResult)), &afterClose))
	assert.Len(t, afterClose, 1)

	// Status=all returns both.
	listResult, err = h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "list",
		"status": "all",
	}))
	require.NoError(t, err)
	var all []map[string]any
	require.NoError(t, json.Unmarshal([]byte(resultText(t, listResult)), &all))
	assert.Len(t, all, 2)
}

func TestHandleMission_Close(t *testing.T) {
	h := testHandlerWithMissions(t)

	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)

	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	closeResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "close",
		"mission_id": created.MissionID,
	}))
	require.NoError(t, err)
	assert.False(t, closeResult.IsError)

	// Verify via show.
	showResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "show",
		"mission_id": created.MissionID,
	}))
	require.NoError(t, err)
	var loaded mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, showResult)), &loaded))
	assert.Equal(t, mission.StatusClosed, loaded.Status)
}

func TestHandleMission_CloseFailedAndEscalated(t *testing.T) {
	for _, st := range []string{mission.StatusFailed, mission.StatusEscalated} {
		t.Run(st, func(t *testing.T) {
			h := testHandlerWithMissions(t)

			createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
				"method":   "create",
				"contract": validContractYAML,
			}))
			require.NoError(t, err)
			var c mission.Contract
			require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &c))

			closeResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
				"method":     "close",
				"mission_id": c.MissionID,
				"status":     st,
			}))
			require.NoError(t, err)
			assert.False(t, closeResult.IsError)

			showResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
				"method":     "show",
				"mission_id": c.MissionID,
			}))
			require.NoError(t, err)
			var loaded mission.Contract
			require.NoError(t, json.Unmarshal([]byte(resultText(t, showResult)), &loaded))
			assert.Equal(t, st, loaded.Status)
		})
	}
}

func TestHandleMission_CloseMissingID(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "close",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "mission_id is required")
}

func TestHandleMission_UnknownMethod(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "bogus",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "unknown method")
}

