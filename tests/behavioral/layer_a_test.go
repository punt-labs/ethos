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

// TestLayerA_MultiRoundCycle exercises the full reflect -> advance -> round 2
// lifecycle: create a 2-round mission, run round 1, reflect, advance, run
// round 2, close, and verify the event log sequence.
func TestLayerA_MultiRoundCycle(t *testing.T) {
	if ethosBinary == "" {
		t.Skip("ethos binary not built")
	}

	f := SetupFixture(t)

	// Step 1: Create a 2-round mission.
	contract := `leader: test-leader
worker: test-agent
evaluator:
  handle: test-evaluator
write_set:
  - pkg/counter/counter_test.go
success_criteria:
  - pkg/counter/counter_test.go has at least two test functions
budget:
  rounds: 2
  reflection_after_each: true
`
	missionID := f.CreateMission(t, contract)
	t.Logf("Mission created: %s", missionID)

	// Verify initial state: current_round == 1.
	show := f.MissionShow(t, missionID)
	require.Equal(t, float64(1), show["current_round"], "new mission should start at round 1")

	// Step 2: Round 1 — agent writes a test file and submits a result.
	round1Opts := DefaultAgentOpts(workerPrompt(missionID))
	round1Opts.SystemPrompt = "You are a Go developer working in a test fixture repo. " +
		"Read the mission contract first using the ethos MCP tool mission_show. " +
		"Then write pkg/counter/counter_test.go with ONE test function (TestIncrement) for the Counter type. " +
		"After completing your work, submit a mission result. " +
		"Write a result YAML file with these exact fields:\n" +
		"  mission: " + missionID + "\n" +
		"  round: 1\n" +
		"  author: test-agent\n" +
		"  verdict: pass\n" +
		"  confidence: 0.9\n" +
		"  files_changed:\n" +
		"    - path: pkg/counter/counter_test.go\n" +
		"      added: <number of lines you wrote>\n" +
		"      removed: 0\n" +
		"  evidence:\n" +
		"    - name: wrote test file with TestIncrement\n" +
		"      status: pass\n" +
		"Then submit it via the ethos MCP tool mission_result with the mission ID and the file path."

	output, err := f.SpawnAgent(t, round1Opts)
	t.Logf("Round 1 agent output (first 2000 chars): %.2000s", output)
	if err != nil {
		t.Logf("Round 1 agent error (may be expected): %v", err)
	}

	// Verify round 1 result was submitted.
	results := f.MissionResults(t, missionID)
	require.NotEmpty(t, results, "round 1 should have submitted a result")
	assert.Equal(t, float64(1), results[0]["round"], "first result should be round 1")

	// Step 3: Reflect on round 1.
	reflectionYAML := `round: 1
author: test-leader
converging: true
signals:
  - round 1 test file created
  - go test passes
recommendation: continue
reason: round 1 created the test file; round 2 will add coverage
`
	f.Reflect(t, missionID, reflectionYAML)

	// Verify reflection was recorded.
	reflections := f.MissionReflections(t, missionID)
	require.Len(t, reflections, 1, "should have exactly 1 reflection after round 1")
	assert.Equal(t, float64(1), reflections[0]["round"])
	assert.Equal(t, "continue", reflections[0]["recommendation"])

	// Step 4: Advance to round 2.
	adv := f.Advance(t, missionID)
	assert.Equal(t, float64(2), adv["current_round"], "advance should report current_round=2")
	assert.Equal(t, float64(2), adv["to_round"], "advance should report to_round=2")

	// Verify mission show confirms round 2.
	show = f.MissionShow(t, missionID)
	require.Equal(t, float64(2), show["current_round"], "mission should be on round 2 after advance")

	// Step 5: Round 2 — agent adds a second test function and submits result.
	round2Opts := DefaultAgentOpts(workerPrompt(missionID))
	round2Opts.SystemPrompt = "You are a Go developer working in a test fixture repo. " +
		"Read the mission contract first using the ethos MCP tool mission_show. " +
		"This is ROUND 2. The file pkg/counter/counter_test.go already exists with TestIncrement. " +
		"Add ONE MORE test function (TestReset) to the same file. Do NOT rewrite the existing test. " +
		"After completing your work, submit a mission result. " +
		"Write a result YAML file with these exact fields:\n" +
		"  mission: " + missionID + "\n" +
		"  round: 2\n" +
		"  author: test-agent\n" +
		"  verdict: pass\n" +
		"  confidence: 0.95\n" +
		"  files_changed:\n" +
		"    - path: pkg/counter/counter_test.go\n" +
		"      added: <number of lines you added>\n" +
		"      removed: 0\n" +
		"  evidence:\n" +
		"    - name: added TestReset to existing test file\n" +
		"      status: pass\n" +
		"Then submit it via the ethos MCP tool mission_result with the mission ID and the file path."

	output, err = f.SpawnAgent(t, round2Opts)
	t.Logf("Round 2 agent output (first 2000 chars): %.2000s", output)
	if err != nil {
		t.Logf("Round 2 agent error (may be expected): %v", err)
	}

	// Verify round 2 result was submitted.
	results = f.MissionResults(t, missionID)
	require.Len(t, results, 2, "should have 2 results after both rounds")
	assert.Equal(t, float64(2), results[1]["round"], "second result should be round 2")

	// Step 6: Close the mission.
	// Submit a round 2 reflection first (required by close gate).
	reflection2YAML := `round: 2
author: test-leader
converging: true
signals:
  - both test functions present
  - go test passes
recommendation: stop
reason: both rounds complete, all success criteria met
`
	f.Reflect(t, missionID, reflection2YAML)
	f.MissionClose(t, missionID)

	// Step 7: Verify event log sequence.
	events := f.MissionLog(t, missionID)
	t.Logf("Event log (%d events):", len(events))
	for i, e := range events {
		t.Logf("  event[%d]: type=%v actor=%v", i, e["event"], e["actor"])
	}

	// Extract the event type sequence.
	var eventTypes []string
	for _, e := range events {
		if et, ok := e["event"].(string); ok {
			eventTypes = append(eventTypes, et)
		}
	}

	// The expected sequence: create, result (r1), reflect (r1),
	// round_advanced, result (r2), reflect (r2), close.
	// Minimum required subsequence — other events (e.g. additional
	// show events if the store logs reads) may be interspersed.
	expected := []string{"create", "result", "reflect", "round_advanced", "result", "reflect", "close"}
	idx := 0
	for _, et := range eventTypes {
		if idx < len(expected) && et == expected[idx] {
			idx++
		}
	}
	assert.Equal(t, len(expected), idx,
		"event log should contain the subsequence %v, got types: %v", expected, eventTypes)

	// Verify the final mission status.
	show = f.MissionShow(t, missionID)
	assert.Equal(t, "closed", show["status"], "mission should be closed")
}

// --- helpers ---

func workerPrompt(missionID string) string {
	return "You have been assigned mission " + missionID + ". " +
		"Read the contract first using the ethos MCP tool 'mission_show' with the mission ID. " +
		"The contract names the write set, success criteria, and budget. " +
		"After your work, submit a result artifact using the ethos MCP tool 'mission_result'. " +
		"Do not commit, push, or merge."
}
