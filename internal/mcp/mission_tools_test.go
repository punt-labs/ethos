package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/attribute"
	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/mission"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"

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

// contractYAMLWithWriteSet returns a valid contract body with a custom
// write_set entry. Tests that create more than one mission in the same
// store must use disjoint write_sets to bypass the Phase 3.2
// cross-mission conflict check.
func contractYAMLWithWriteSet(path string) string {
	return `leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.5
write_set:
  - ` + path + `
success_criteria:
  - make check passes
budget:
  rounds: 3
  reflection_after_each: true
`
}

func testHandlerWithMissions(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	s := identity.NewStore(dir)
	root := s.Root()
	ms := mission.NewStore(root)

	// Phase 3.3 (DES-033) requires the evaluator handle to resolve to
	// real identity content at create time so the contract's evaluator
	// hash is non-empty. Seed the canonical `djb` identity that every
	// test contract names as evaluator. The personality and writing
	// style files exist with placeholder content; the hash function
	// only requires that resolution succeed.
	personalities := attribute.NewStore(root, attribute.Personalities)
	require.NoError(t, personalities.Save(&attribute.Attribute{
		Slug:    "bernstein",
		Content: "# Bernstein\n\nFrozen-evaluator placeholder.\n",
	}))
	writingStyles := attribute.NewStore(root, attribute.WritingStyles)
	require.NoError(t, writingStyles.Save(&attribute.Attribute{
		Slug:    "bernstein-prose",
		Content: "# Bernstein Prose\n\nPlaceholder.\n",
	}))
	talents := attribute.NewStore(root, attribute.Talents)
	require.NoError(t, talents.Save(&attribute.Attribute{
		Slug:    "security",
		Content: "# Security\n",
	}))
	require.NoError(t, s.Save(&identity.Identity{
		Name:         "Dan B",
		Handle:       "djb",
		Kind:         "agent",
		Personality:  "bernstein",
		WritingStyle: "bernstein-prose",
		Talents:      []string{"security"},
	}))

	// Role and team stores are wired even though no team binds djb to
	// a role in these fixtures — the MCP handler's mission create path
	// calls NewLiveHashSources which rejects nil role/team stores. A
	// handler built without the full set cannot create missions. This
	// is the Phase 3.3 parity invariant: MCP and CLI must produce
	// identical hashes for the same evaluator, which requires both
	// sides to wire the same stores.
	rs := role.NewLayeredStore("", root)
	ts := team.NewLayeredStore("", root)

	return NewHandlerWithOptions(s,
		talents,
		personalities,
		writingStyles,
		WithRoleStore(rs),
		WithTeamStore(ts),
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
	assert.Contains(t, resultText(t, result), "invalid contract")
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
	assert.Contains(t, resultText(t, result), "invalid contract")
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
	// Each gets a disjoint write_set so Phase 3.2's conflict check
	// does not collapse them.
	for i := 0; i < 3; i++ {
		_, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
			"method":   "create",
			"contract": contractYAMLWithWriteSet(fmt.Sprintf("tests/list-%d/", i)),
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

	// Disjoint write_sets so Phase 3.2's cross-mission conflict check
	// does not collapse the second create.
	for i := 0; i < 2; i++ {
		_, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
			"method":   "create",
			"contract": contractYAMLWithWriteSet(fmt.Sprintf("tests/list-filter-%d/", i)),
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

	// Close one. The close gate requires a result first.
	firstID, _ := openEntries[0]["mission_id"].(string)
	require.NotEmpty(t, firstID)
	submitResultForMCP(t, h, firstID)
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

// TestHandleMission_ListRejectsUnknownStatus asserts that an invalid
// status filter produces a structured tool error rather than silently
// returning an empty list. The MCP schema also constrains the enum,
// but the handler defends at the boundary in case a caller bypasses it.
func TestHandleMission_ListRejectsUnknownStatus(t *testing.T) {
	h := testHandlerWithMissions(t)

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "list",
		"status": "bogus",
	}))
	require.NoError(t, err)
	require.True(t, result.IsError, "expected tool error result")
	assert.Contains(t, resultText(t, result), `invalid status filter "bogus"`)
}

// validResultYAMLFor returns a minimal valid result body for the given
// mission, matching the contract's default write_set. Tests use it
// to drive Close through the Phase 3.6 gate.
func validResultYAMLFor(missionID string) string {
	return fmt.Sprintf(`mission: %s
round: 1
author: bwk
verdict: pass
confidence: 0.9
evidence:
  - name: make check
    status: pass
`, missionID)
}

// submitResultForMCP is a helper that submits a valid result for the
// mission's current round via the MCP result method. It keeps each
// close-gate test to the operation under test, not the scaffolding.
func submitResultForMCP(t *testing.T, h *Handler, missionID string) {
	t.Helper()
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "result",
		"mission_id": missionID,
		"result":     validResultYAMLFor(missionID),
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, "result submission must succeed: %s", resultText(t, result))
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
	submitResultForMCP(t, h, created.MissionID)

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
			submitResultForMCP(t, h, c.MissionID)

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

// TestHandleMission_CreateRejectsCrossMissionConflict asserts that
// the MCP create handler surfaces the Phase 3.2 conflict error as a
// structured tool error. The first create succeeds; the second create
// with an overlapping write_set must fail with content matching the
// existing mission's ID, the worker handle, and the overlapping path.
func TestHandleMission_CreateRejectsCrossMissionConflict(t *testing.T) {
	h := testHandlerWithMissions(t)

	first, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": contractYAMLWithWriteSet("internal/foo/"),
	}))
	require.NoError(t, err)
	require.False(t, first.IsError)

	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, first)), &created))

	second, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": contractYAMLWithWriteSet("internal/foo/bar.go"),
	}))
	require.NoError(t, err)
	require.True(t, second.IsError, "overlapping create must produce a tool error")

	msg := resultText(t, second)
	assert.Contains(t, msg, "failed to create mission:")
	assert.Contains(t, msg, "write_set conflict with mission")
	assert.Contains(t, msg, created.MissionID)
	assert.Contains(t, msg, "worker: bwk")
	assert.Contains(t, msg, "internal/foo/bar.go")
}

// --- 3.4: reflect, reflections, advance ---

// validReflectionYAML is a minimal continue-recommendation reflection
// the MCP reflect handler accepts. Tests parameterize it via
// reflectionYAMLForRound when they need other rounds or
// recommendations.
const validReflectionYAML = `round: 1
author: claude
converging: true
signals:
  - tests passing
recommendation: continue
reason: round 1 went well
`

func reflectionYAMLForRound(round int, rec, reason string) string {
	return fmt.Sprintf(`round: %d
author: claude
converging: true
signals:
  - tests passing
recommendation: %s
reason: %q
`, round, rec, reason)
}

func TestHandleMission_Reflect_RoundTrip(t *testing.T) {
	h := testHandlerWithMissions(t)

	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	reflectResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "reflect",
		"mission_id": created.MissionID,
		"reflection": validReflectionYAML,
	}))
	require.NoError(t, err)
	require.False(t, reflectResult.IsError, "reflect must succeed: %s", resultText(t, reflectResult))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(resultText(t, reflectResult)), &got))
	assert.Equal(t, created.MissionID, got["mission_id"])
	assert.Equal(t, float64(1), got["round"])
	assert.Equal(t, "continue", got["recommendation"])
}

func TestHandleMission_Reflect_RequiresMissionID(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "reflect",
		"reflection": validReflectionYAML,
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "mission_id is required")
}

func TestHandleMission_Reflect_RequiresBody(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "reflect",
		"mission_id": "m-2026-04-08-001",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "reflection YAML body is required")
}

func TestHandleMission_Reflect_RejectsUnknownField(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	body := validReflectionYAML + "bogus: smuggled\n"
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "reflect",
		"mission_id": created.MissionID,
		"reflection": body,
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "field bogus not found")
}

func TestHandleMission_Reflections_EmptyReturnsArray(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "reflections",
		"mission_id": created.MissionID,
	}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, "[]", resultText(t, result))
}

func TestHandleMission_Reflections_ReturnsAfterReflect(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	_, err = h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "reflect",
		"mission_id": created.MissionID,
		"reflection": validReflectionYAML,
	}))
	require.NoError(t, err)

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "reflections",
		"mission_id": created.MissionID,
	}))
	require.NoError(t, err)
	var rs []mission.Reflection
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &rs))
	require.Len(t, rs, 1)
	assert.Equal(t, 1, rs[0].Round)
	assert.Equal(t, "continue", rs[0].Recommendation)
}

func TestHandleMission_Advance_RequiresReflection(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "advance",
		"mission_id": created.MissionID,
	}))
	require.NoError(t, err)
	require.True(t, result.IsError, "advance without reflection must fail")
	msg := resultText(t, result)
	assert.Contains(t, msg, "no reflection for round 1")
	assert.Contains(t, msg, created.MissionID)
}

func TestHandleMission_Advance_HappyPath(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	_, err = h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "reflect",
		"mission_id": created.MissionID,
		"reflection": validReflectionYAML,
	}))
	require.NoError(t, err)

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "advance",
		"mission_id": created.MissionID,
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, "advance must succeed: %s", resultText(t, result))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &got))
	assert.Equal(t, created.MissionID, got["mission_id"])
	assert.Equal(t, float64(2), got["current_round"])
}

func TestHandleMission_Advance_StopBlocks(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	stopBody := reflectionYAMLForRound(1, "stop", "fixture is broken; close")
	_, err = h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "reflect",
		"mission_id": created.MissionID,
		"reflection": stopBody,
	}))
	require.NoError(t, err)

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "advance",
		"mission_id": created.MissionID,
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	msg := resultText(t, result)
	assert.Contains(t, msg, `recommends "stop"`)
	assert.Contains(t, msg, "fixture is broken")
}

// TestHandleMission_CreateMatchesCLIHashWithRoles is the round 4
// Bugbot regression test. It proves the Phase 3.3 parity invariant:
// the MCP create path and the CLI create path produce identical
// evaluator hashes for the same evaluator content, including
// team-based role assignments.
//
// Before round 4's fix, the MCP handler's NewLiveHashSources call
// could silently receive nil role/team stores (the options are
// individually optional), producing a hash that omitted role content.
// The CLI and verifier hook always wire both stores, so the CLI-
// computed hash would include roles. Divergent hashes permanently
// block every verifier spawn for missions created via the broken MCP
// handler.
//
// The fix makes NewLiveHashSources reject nil stores at construction
// and makes the test fixture wire both. This test proves the parity
// holds under the richest possible content — a team that binds the
// evaluator to a role whose content participates in the hash.
func TestHandleMission_CreateMatchesCLIHashWithRoles(t *testing.T) {
	dir := t.TempDir()
	s := identity.NewStore(dir)
	root := s.Root()
	ms := mission.NewStore(root)

	// Seed djb with personality and writing style so the test path
	// exercises every hash input, not just the role section.
	personalities := attribute.NewStore(root, attribute.Personalities)
	require.NoError(t, personalities.Save(&attribute.Attribute{
		Slug:    "bernstein",
		Content: "# Bernstein\n",
	}))
	writingStyles := attribute.NewStore(root, attribute.WritingStyles)
	require.NoError(t, writingStyles.Save(&attribute.Attribute{
		Slug:    "bernstein-prose",
		Content: "# Bernstein Prose\n",
	}))
	talents := attribute.NewStore(root, attribute.Talents)
	require.NoError(t, s.Save(&identity.Identity{
		Name:         "Dan B",
		Handle:       "djb",
		Kind:         "agent",
		Personality:  "bernstein",
		WritingStyle: "bernstein-prose",
	}))

	// Seed a role and bind djb to it via a team. Without this the
	// test would silently pass because the hash would have no role
	// section to disagree on — defeating the purpose of the
	// regression.
	rs := role.NewLayeredStore("", root)
	require.NoError(t, rs.Save(&role.Role{
		Name:             "verifier",
		Responsibilities: []string{"review changes"},
	}))
	ts := team.NewLayeredStore("", root)
	identityExists := func(h string) bool { return h == "djb" }
	roleExists := func(n string) bool { return rs.Exists(n) }
	require.NoError(t, ts.Save(&team.Team{
		Name: "frozen-verifier",
		Members: []team.Member{
			{Identity: "djb", Role: "verifier"},
		},
	}, identityExists, roleExists))

	h := NewHandlerWithOptions(s,
		talents,
		personalities,
		writingStyles,
		WithRoleStore(rs),
		WithTeamStore(ts),
		WithMissionStore(ms),
	)

	// Create the mission via the MCP handler.
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, "MCP create must succeed: %s", resultText(t, result))

	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &created))
	require.NotEmpty(t, created.Evaluator.Hash, "MCP create must populate the hash")

	// Simulate the CLI path by building a fresh HashSources against
	// the same stores and computing the hash the same way CLI and the
	// verifier hook do. A byte-mismatch here is exactly the Bugbot
	// finding this test exists to catch.
	cliSources, err := mission.NewLiveHashSources(s, rs, ts)
	require.NoError(t, err)
	cliHash, err := mission.ComputeEvaluatorHash("djb", cliSources)
	require.NoError(t, err)
	assert.Equal(t, created.Evaluator.Hash, cliHash,
		"MCP-pinned hash must equal the hash CLI would compute for the same evaluator content")

	// And a belt-and-braces parity check: create a "parallel" mission
	// via the same ApplyServerFields entry point the CLI uses, and
	// assert the two pinned hashes match byte-for-byte. The mission
	// IDs and timestamps will differ (counter advances, write_set
	// must be disjoint to bypass Phase 3.2 conflict detection), but
	// the evaluator hash is independent of both.
	parallel := mission.Contract{
		Leader:          "claude",
		Worker:          "bwk",
		Evaluator:       mission.Evaluator{Handle: "djb"},
		Inputs:          mission.Inputs{Bead: "ethos-07m.5"},
		WriteSet:        []string{"tests/parity-cli/"},
		SuccessCriteria: []string{"make check passes"},
		Budget:          mission.Budget{Rounds: 3, ReflectionAfterEach: true},
	}
	require.NoError(t, ms.ApplyServerFields(&parallel, time.Now(), cliSources))
	assert.Equal(t, created.Evaluator.Hash, parallel.Evaluator.Hash,
		"MCP-created and CLI-created missions with identical evaluator content must have identical pinned hashes")
}

// --- 3.6: result submission, close gate, CLI+MCP parity ---

// TestHandleMission_Result_RoundTrip asserts success criterion 1 via
// MCP: a well-formed result body persists and comes back through the
// results method.
func TestHandleMission_Result_RoundTrip(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "result",
		"mission_id": created.MissionID,
		"result":     validResultYAMLFor(created.MissionID),
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, "result submission must succeed: %s", resultText(t, result))

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &got))
	assert.Equal(t, created.MissionID, got["mission_id"])
	assert.Equal(t, float64(1), got["round"])
	assert.Equal(t, "pass", got["verdict"])
}

func TestHandleMission_Result_RequiresMissionID(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method": "result",
		"result": validResultYAMLFor("m-2026-04-08-001"),
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "mission_id is required")
}

func TestHandleMission_Result_RequiresBody(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "result",
		"mission_id": "m-2026-04-08-001",
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "result YAML body is required")
}

func TestHandleMission_Result_RejectsUnknownField(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	body := validResultYAMLFor(created.MissionID) + "bogus: smuggled\n"
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "result",
		"mission_id": created.MissionID,
		"result":     body,
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "field bogus not found")
}

// TestHandleMission_Result_AppendOnlyViaMCP asserts success
// criterion 3 through the MCP surface: a duplicate submission via
// MCP for the same round fails with the append-only error. The
// companion test in store_test.go covers the direct store surface;
// this one closes the MCP-specific regression surface.
func TestHandleMission_Result_AppendOnlyViaMCP(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	first, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "result",
		"mission_id": created.MissionID,
		"result":     validResultYAMLFor(created.MissionID),
	}))
	require.NoError(t, err)
	require.False(t, first.IsError)

	second, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "result",
		"mission_id": created.MissionID,
		"result":     validResultYAMLFor(created.MissionID),
	}))
	require.NoError(t, err)
	require.True(t, second.IsError, "duplicate result submission via MCP must fail")
	msg := resultText(t, second)
	assert.Contains(t, msg, "append-only")
	assert.Contains(t, msg, "round 1")
}

// TestHandleMission_Result_RejectsOutOfWriteSet asserts success
// criterion 4 through MCP: a result with a path outside the
// contract's write_set is refused with a clear operator-facing
// error.
func TestHandleMission_Result_RejectsOutOfWriteSet(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	body := fmt.Sprintf(`mission: %s
round: 1
author: bwk
verdict: pass
confidence: 0.9
files_changed:
  - path: /etc/passwd
    added: 1
    removed: 0
evidence:
  - name: make check
    status: pass
`, created.MissionID)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "result",
		"mission_id": created.MissionID,
		"result":     body,
	}))
	require.NoError(t, err)
	require.True(t, result.IsError)
	// The per-entry validator rejects absolute paths before the
	// containment check runs — either message is acceptable, but
	// both cleanly identify the reason.
	msg := resultText(t, result)
	assert.True(t,
		strings.Contains(msg, "relative path") || strings.Contains(msg, "outside mission"),
		"expected path rejection, got: %s", msg)
}

// TestHandleMission_Results_EmptyReturnsArray asserts that the
// results method returns [] (not null) for a mission with no
// results yet, symmetric with the reflections path.
func TestHandleMission_Results_EmptyReturnsArray(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "results",
		"mission_id": created.MissionID,
	}))
	require.NoError(t, err)
	require.False(t, result.IsError)
	assert.Equal(t, "[]", resultText(t, result))
}

// TestHandleMission_Results_ReturnsAfterResult asserts that a
// submitted result comes back via the results method with the same
// fields that went in.
func TestHandleMission_Results_ReturnsAfterResult(t *testing.T) {
	h := testHandlerWithMissions(t)
	createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": validContractYAML,
	}))
	require.NoError(t, err)
	var created mission.Contract
	require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

	submitResultForMCP(t, h, created.MissionID)

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "results",
		"mission_id": created.MissionID,
	}))
	require.NoError(t, err)
	var rs []mission.Result
	require.NoError(t, json.Unmarshal([]byte(resultText(t, result)), &rs))
	require.Len(t, rs, 1)
	assert.Equal(t, 1, rs[0].Round)
	assert.Equal(t, mission.VerdictPass, rs[0].Verdict)
	assert.Equal(t, created.MissionID, rs[0].Mission)
}

// TestHandleMission_CloseGate_RefusesViaMCP covers success criterion
// 7 (the load-bearing CLI+MCP parity test): the close method via
// MCP fires the same Phase 3.6 gate as the CLI close path. Without
// this test, a regression that wired the gate into only one surface
// would ship silently.
func TestHandleMission_CloseGate_RefusesViaMCP(t *testing.T) {
	for _, status := range []string{mission.StatusClosed, mission.StatusFailed, mission.StatusEscalated} {
		t.Run(status, func(t *testing.T) {
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
				"status":     status,
			}))
			require.NoError(t, err)
			require.True(t, closeResult.IsError,
				"MCP close must refuse without a result for status %q", status)
			msg := resultText(t, closeResult)
			assert.Contains(t, msg, created.MissionID)
			assert.Contains(t, msg, "no result artifact for round 1")
			assert.Contains(t, msg, "ethos mission result")

			// Mission must still be open.
			showResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
				"method":     "show",
				"mission_id": created.MissionID,
			}))
			require.NoError(t, err)
			var loaded mission.Contract
			require.NoError(t, json.Unmarshal([]byte(resultText(t, showResult)), &loaded))
			assert.Equal(t, mission.StatusOpen, loaded.Status)
		})
	}
}

// TestHandleMission_CloseGate_AcceptsViaMCP asserts the positive
// branch of the MCP close gate: submitting a result via MCP and
// then closing via MCP succeeds. Together with
// TestHandleMission_CloseGate_RefusesViaMCP, this proves the MCP
// close path fires the gate with the same semantics as the direct
// store path.
func TestHandleMission_CloseGate_AcceptsViaMCP(t *testing.T) {
	cases := map[string]string{
		mission.StatusClosed:    mission.VerdictPass,
		mission.StatusFailed:    mission.VerdictFail,
		mission.StatusEscalated: mission.VerdictEscalate,
	}
	for status, verdict := range cases {
		t.Run(status, func(t *testing.T) {
			h := testHandlerWithMissions(t)
			createResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
				"method":   "create",
				"contract": validContractYAML,
			}))
			require.NoError(t, err)
			var created mission.Contract
			require.NoError(t, json.Unmarshal([]byte(resultText(t, createResult)), &created))

			body := fmt.Sprintf(`mission: %s
round: 1
author: bwk
verdict: %s
confidence: 0.8
evidence:
  - name: make check
    status: pass
`, created.MissionID, verdict)
			resultRes, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
				"method":     "result",
				"mission_id": created.MissionID,
				"result":     body,
			}))
			require.NoError(t, err)
			require.False(t, resultRes.IsError, "result must succeed: %s", resultText(t, resultRes))

			closeResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
				"method":     "close",
				"mission_id": created.MissionID,
				"status":     status,
			}))
			require.NoError(t, err)
			require.False(t, closeResult.IsError,
				"MCP close with a valid result must succeed: %s", resultText(t, closeResult))

			// Verify the mission is in the requested terminal state.
			showResult, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
				"method":     "show",
				"mission_id": created.MissionID,
			}))
			require.NoError(t, err)
			var loaded mission.Contract
			require.NoError(t, json.Unmarshal([]byte(resultText(t, showResult)), &loaded))
			assert.Equal(t, status, loaded.Status)
		})
	}
}

// TestHandleMission_Result_UnknownIDReturnsError asserts that
// submitting a result for a mission that does not exist produces
// a structured tool error rather than silently creating one.
func TestHandleMission_Result_UnknownIDReturnsError(t *testing.T) {
	h := testHandlerWithMissions(t)
	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":     "result",
		"mission_id": "m-9999-12-31-001",
		"result":     validResultYAMLFor("m-9999-12-31-001"),
	}))
	require.NoError(t, err)
	assert.True(t, result.IsError)
}

