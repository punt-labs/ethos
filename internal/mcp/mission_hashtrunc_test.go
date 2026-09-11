package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The MCP create handler is the one place the contract truncation guard
// is wired by hand: results, reflections, and corrections carry it
// inside their strict decoder, but DecodeContractStrict deliberately
// does not, because it also reads contracts back off disk. That makes
// handleCreateMission's call the only thing standing between an MCP
// caller and a persisted contract whose success criterion was cut short
// at an unquoted '#' — and a criterion shortened to "PR" is still a
// non-empty string, so nothing downstream objects.
//
// Without this test, deleting that call leaves every internal/mission
// and cmd/ethos test green. A guard nothing proves is wired is the
// defect class this whole cluster is about.
func TestHandleMission_CreateRefusesHashTruncatedCriterion(t *testing.T) {
	h := testHandlerWithMissions(t)

	const body = `leader: claude
worker: bwk
evaluator:
  handle: djb
write_set:
  - internal/mission/
success_criteria:
  - PR #515 must merge with 6/6 checks green
budget:
  rounds: 3
  reflection_after_each: true
`

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": body,
	}))
	require.NoError(t, err)
	require.True(t, result.IsError,
		"create accepted a criterion cut short at an unquoted '#'")

	text := resultText(t, result)
	assert.Contains(t, text, "success_criteria[0]")
	assert.Contains(t, text, `"PR"`)

	ids, listErr := h.missionStore.List()
	require.NoError(t, listErr)
	assert.Empty(t, ids, "a refused contract must not be persisted")
}

// TestHandleMission_CreateRefusesAlias covers the other half of the MCP
// trust boundary. This one rides inside DecodeContractStrict's sibling
// path rather than the hand-wired call, but an MCP caller reaches it
// through a different argument shape than the CLI does — the contract
// arrives as a JSON string rather than a file — so the surface is worth
// exercising where callers actually use it.
func TestHandleMission_CreateRefusesAlias(t *testing.T) {
	h := testHandlerWithMissions(t)

	const body = `leader: claude
worker: bwk
evaluator:
  handle: djb
write_set:
  - internal/mission/
context: &note follow-up to PR #516
success_criteria:
  - *note
budget:
  rounds: 3
  reflection_after_each: true
`

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": body,
	}))
	require.NoError(t, err)
	require.True(t, result.IsError, "create accepted a YAML alias")
	assert.Contains(t, resultText(t, result), "alias")

	ids, listErr := h.missionStore.List()
	require.NoError(t, listErr)
	assert.Empty(t, ids, "a refused contract must not be persisted")
}

// TestHandleMission_CreateAcceptsQuotedHash keeps the guard narrow at
// the MCP surface too: a quoted criterion citing a PR number is
// ordinary content and must still create the mission.
func TestHandleMission_CreateAcceptsQuotedHash(t *testing.T) {
	h := testHandlerWithMissions(t)

	const body = `leader: claude
worker: bwk
evaluator:
  handle: djb
write_set:
  - internal/mission/
success_criteria:
  - "PR #515 must merge with 6/6 checks green"
budget:
  rounds: 3
  reflection_after_each: true
`

	result, err := h.handleMission(context.Background(), callTool(map[string]interface{}{
		"method":   "create",
		"contract": body,
	}))
	require.NoError(t, err)
	require.False(t, result.IsError, "create must succeed: %s", resultText(t, result))

	ids, listErr := h.missionStore.List()
	require.NoError(t, listErr)
	assert.Len(t, ids, 1)
}
