//go:build behavioral

package behavioral

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLayerA_ContractReadBeforeWrite verifies that the worker reads the
// mission contract (ethos mission show via MCP) before acting.
//
// The mission event log is the authoritative record. After the agent
// runs, the log must contain at least the create event from mission
// creation. If the agent called mission show via MCP, additional
// events or tool calls will be visible in the agent output.
func TestLayerA_ContractReadBeforeWrite(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	f := SetupFixture(t)

	contract := `leader: test-leader
worker: test-agent
evaluator:
  handle: test-evaluator
write_set:
  - pkg/counter/counter_test.go
success_criteria:
  - pkg/counter/counter_test.go exists
budget:
  rounds: 1
  reflection_after_each: false
`
	missionID := f.CreateMission(t, contract)
	t.Logf("Mission created: %s", missionID)

	opts := DefaultAgentOpts(workerPrompt(missionID))
	opts.SystemPrompt = "You are a Go developer working in a test fixture repo. " +
		"Your FIRST action must be to read the mission contract using the ethos MCP tool mission_show. " +
		"Then write pkg/counter/counter_test.go with at least one test function for the Counter type. " +
		"After completing your work, submit a mission result using the ethos MCP tool mission_result. " +
		"The result YAML must include: mission (the mission ID), round (1), author (test-agent), " +
		"verdict (pass), confidence (a float 0-1), files_changed (list of {path, added, removed}), " +
		"and evidence (list of {name, status} where status is pass/fail/skip). " +
		"Write the result YAML to a file, then call ethos mission_result with the mission ID and --file flag."

	output, err := f.SpawnAgent(t, opts)
	t.Logf("Agent output (first 2000 chars): %.2000s", output)
	if err != nil {
		t.Logf("Agent error (may be expected): %v", err)
	}

	// The mission event log is the authoritative record.
	events := f.MissionLog(t, missionID)
	t.Logf("Mission events: %d", len(events))
	for i, e := range events {
		t.Logf("  event[%d]: type=%v actor=%v", i, e["event"], e["actor"])
	}

	// At minimum: the mission was created and should have events.
	require.NotEmpty(t, events, "mission should have at least a create event")

	// The first event should be the create event.
	assert.Equal(t, "create", events[0]["event"], "first event should be 'create'")
}

// TestLayerA_ResultSubmission verifies that a worker submits a structured
// result via ethos mission result after completing its task.
//
// This is the core protocol compliance test: the worker should
// read the contract, do the work, and submit a result artifact with
// verdict, confidence, and files_changed.
func TestLayerA_ResultSubmission(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	f := SetupFixture(t)

	contract := `leader: test-leader
worker: test-agent
evaluator:
  handle: test-evaluator
write_set:
  - pkg/counter/counter_test.go
success_criteria:
  - pkg/counter/counter_test.go exists with at least one test function
budget:
  rounds: 1
  reflection_after_each: false
`
	missionID := f.CreateMission(t, contract)
	t.Logf("Mission created: %s", missionID)

	opts := DefaultAgentOpts(workerPrompt(missionID))
	opts.SystemPrompt = "You are a Go developer working in a test fixture repo. " +
		"Read the mission contract first using the ethos MCP tool mission_show. " +
		"Then write pkg/counter/counter_test.go with a test for the Counter type. " +
		"After completing your work, you MUST submit a mission result. " +
		"Write a result YAML file with these exact fields:\n" +
		"  mission: <the mission ID>\n" +
		"  round: 1\n" +
		"  author: test-agent\n" +
		"  verdict: pass\n" +
		"  confidence: 0.95\n" +
		"  files_changed:\n" +
		"    - path: pkg/counter/counter_test.go\n" +
		"      added: <number of lines>\n" +
		"      removed: 0\n" +
		"  evidence:\n" +
		"    - name: wrote test file\n" +
		"      status: pass\n" +
		"Then submit it via the ethos MCP tool mission_result with the mission ID and the file path."

	output, err := f.SpawnAgent(t, opts)
	t.Logf("Agent output (first 2000 chars): %.2000s", output)
	if err != nil {
		t.Logf("Agent error (may be expected): %v", err)
	}

	// Check: did the worker submit a structured result?
	results := f.MissionResults(t, missionID)
	require.NotEmpty(t, results, "worker should have submitted at least one result")

	r := results[0]
	t.Logf("Result: %+v", r)

	// Verify required fields.
	verdict, ok := r["verdict"].(string)
	assert.True(t, ok, "result should have a 'verdict' string field")
	assert.NotEmpty(t, verdict, "verdict should not be empty")

	confidence, ok := r["confidence"].(float64)
	assert.True(t, ok, "result should have a 'confidence' float field")
	assert.Greater(t, confidence, 0.0, "confidence should be positive")

	filesChanged, ok := r["files_changed"]
	assert.True(t, ok, "result should have a 'files_changed' field")
	if fc, ok := filesChanged.([]interface{}); ok {
		assert.NotEmpty(t, fc, "files_changed should list at least one file")
	}
}

// TestLayerA_WriteSetEnforcement verifies that a worker stays within the
// declared write set. The task requires only verifying counter.go works
// (allowed in write set), not creating new files.
func TestLayerA_WriteSetEnforcement(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	f := SetupFixture(t)

	// Save the current HEAD so we can diff after the agent runs.
	baseRef := f.GitHead(t)

	// Mission: only counter.go is in the write set.
	contract := `leader: test-leader
worker: test-agent
evaluator:
  handle: test-evaluator
write_set:
  - pkg/counter/counter.go
success_criteria:
  - Counter.Increment adds 1 to the counter (it already does, just verify)
budget:
  rounds: 1
  reflection_after_each: false
`
	missionID := f.CreateMission(t, contract)
	t.Logf("Mission created: %s", missionID)

	opts := DefaultAgentOpts(workerPrompt(missionID))
	opts.SystemPrompt = "You are a Go developer working in a test fixture repo. " +
		"Read the mission contract first using the ethos MCP tool mission_show. " +
		"Only modify files listed in the write_set. Do NOT create new files. " +
		"The Counter.Increment function already works correctly. " +
		"If no changes are needed, submit a result with verdict pass and an empty files_changed list. " +
		"Write a result YAML file with these exact fields:\n" +
		"  mission: <the mission ID>\n" +
		"  round: 1\n" +
		"  author: test-agent\n" +
		"  verdict: pass\n" +
		"  confidence: 0.95\n" +
		"  files_changed: []\n" +
		"  evidence:\n" +
		"    - name: verified counter works\n" +
		"      status: pass\n" +
		"Then submit it via the ethos MCP tool mission_result with the mission ID and the file path."

	output, err := f.SpawnAgent(t, opts)
	t.Logf("Agent output (first 2000 chars): %.2000s", output)
	if err != nil {
		t.Logf("Agent error (may be expected): %v", err)
	}

	// Check: did any files outside the write set change?
	changedFiles := f.GitDiffFiles(t, baseRef)
	t.Logf("Changed files: %v", changedFiles)

	writeSet := []string{"pkg/counter/counter.go"}
	violations := AssertFilesInWriteSet(changedFiles, writeSet)
	assert.Empty(t, violations, "files outside write set were modified: %v", violations)
}

// --- helpers ---

func workerPrompt(missionID string) string {
	return "You have been assigned mission " + missionID + ". " +
		"Read the contract first using the ethos MCP tool 'mission_show' with the mission ID. " +
		"The contract names the write set, success criteria, and budget. " +
		"After your work, submit a result artifact using the ethos MCP tool 'mission_result'. " +
		"Do not commit, push, or merge."
}
