package hook

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFormatOutput_Mission_Correct asserts the correct method's
// confirmation renders "Corrected <mission_id> (<kind>)" per DES-020
// (every MCP tool needs a formatter before shipping).
func TestFormatOutput_Mission_Correct(t *testing.T) {
	result := `{"mission_id":"m-2026-04-07-001","kind":"fabrication","round":1,"author":"claude"}`
	payload := makeToolPayload("mission", "correct", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "Corrected m-2026-04-07-001 (fabrication)", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

// TestFormatOutput_Mission_Correct_Warnings asserts the correct
// formatter renders a `warnings` array under a Warnings header, the
// same way formatMissionClose and formatMissionAbandon do.
// handleCorrectMission attaches this warning when the post-correction
// seal fails; before this the formatter used emitSimple and dropped
// all context, so the warning reached no rendered surface.
func TestFormatOutput_Mission_Correct_Warnings(t *testing.T) {
	result := `{"mission_id":"m-2026-04-07-001","kind":"fabrication","round":1,"author":"claude",` +
		`"warnings":["sealing mission log: no repo root in scope"]}`
	payload := makeToolPayload("mission", "correct", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "Corrected m-2026-04-07-001 (fabrication)", r.HookSpecificOutput.UpdatedMCPToolOutput)
	assert.Equal(t,
		"Warnings:\n  - sealing mission log: no repo root in scope",
		r.HookSpecificOutput.AdditionalContext)
}

// TestFormatOutput_Mission_Correct_Malformed asserts the early guard
// falls back to a truncated raw render rather than a partial card
// when mission_id or kind is missing.
func TestFormatOutput_Mission_Correct_Malformed(t *testing.T) {
	payload := makeToolPayload("mission", "correct", `{"round":1}`)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, `{"round":1}`, r.HookSpecificOutput.UpdatedMCPToolOutput)
}

// TestFormatOutput_Mission_Results_WithCorrections asserts DES-072's
// wire-shape change — results wrapped in {results, corrections} — is
// rendered: the results bullets as before, plus a trailing
// Corrections section.
func TestFormatOutput_Mission_Results_WithCorrections(t *testing.T) {
	result := `{
  "results": [
    {"mission":"m-2026-04-07-001","round":1,"author":"bwk","verdict":"pass","confidence":0.9,"evidence":[{"name":"make check","status":"pass"}]}
  ],
  "corrections": [
    {"mission":"m-2026-04-07-001","round":1,"kind":"fabrication","author":"claude","claim":"make check (full suite): fail — pre-existing, unrelated","corrected":"make check failed because of a stale worktree base"}
  ]
}`
	payload := makeToolPayload("mission", "results", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "1 result", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "- round 1 (pass) by bwk — confidence=0.90")
	assert.Contains(t, ctx, "Corrections:")
	assert.Contains(t, ctx, "- round 1 (fabrication) by claude")
	assert.Contains(t, ctx, "claim:      make check (full suite): fail — pre-existing, unrelated")
	assert.Contains(t, ctx, "corrected:  make check failed because of a stale worktree base")
}

// TestFormatOutput_Mission_Results_EmptyBothSections asserts an empty
// {results: [], corrections: []} payload renders both sections with
// "(none)" rather than dropping either silently.
func TestFormatOutput_Mission_Results_EmptyBothSections(t *testing.T) {
	payload := makeToolPayload("mission", "results", `{"results":[],"corrections":[]}`)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "0 results", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "(none)")
	assert.Contains(t, ctx, "Corrections:")
}

// TestFormatOutput_Mission_Results_PreDES072Shape asserts a
// pre-DES-072 bare-array payload (the old wire shape, before results
// wrapped in {results, corrections}) falls back to the malformed-input
// path rather than rendering a misleading partial card — a bare JSON
// array cannot unmarshal into the {results, corrections} struct the
// formatter now expects.
func TestFormatOutput_Mission_Results_PreDES072Shape(t *testing.T) {
	payload := makeToolPayload("mission", "results", `[]`)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "[]", r.HookSpecificOutput.UpdatedMCPToolOutput)
}

// TestFormatOutput_Mission_Results_WithWarnings asserts the results
// formatter renders a `warnings` array under a Warnings header, the
// same way formatMissionCorrect does. handleMissionResults attaches
// this warning when LoadCorrections hits a corrupt sidecar; before
// this fix the payload struct had no Warnings field, so the signal
// was silently dropped rather than reaching a rendered surface.
func TestFormatOutput_Mission_Results_WithWarnings(t *testing.T) {
	result := `{"results":[],"corrections":[],` +
		`"warnings":["loading corrections: corrections.jsonl: unexpected EOF"]}`
	payload := makeToolPayload("mission", "results", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "0 results", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Warnings:\n  - loading corrections: corrections.jsonl: unexpected EOF")
}

// TestFormatOutput_Mission_Show_WithCorrections asserts `mission
// show`'s formatter renders a top-level `corrections` array under
// the contract block, the same way it already renders `results`.
func TestFormatOutput_Mission_Show_WithCorrections(t *testing.T) {
	result := `{
  "mission_id": "m-2026-04-07-001",
  "status": "closed",
  "leader": "claude",
  "worker": "bwk",
  "evaluator": {"handle": "djb"},
  "write_set": ["internal/mission/"],
  "success_criteria": ["make check passes"],
  "budget": {"rounds": 3, "reflection_after_each": true},
  "results": [],
  "corrections": [
    {"mission":"m-2026-04-07-001","round":0,"kind":"decision","author":"claude","corrected":"leader ruling: re-scope"}
  ]
}`
	payload := makeToolPayload("mission", "show", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "m-2026-04-07-001 (closed)", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "Corrections:")
	assert.Contains(t, ctx, "- whole mission (decision) by claude")
	assert.Contains(t, ctx, "corrected:  leader ruling: re-scope")
	// A decision-kind correction has no claim — the claim line must
	// not appear at all, not render as an empty "claim:" row.
	assert.NotContains(t, ctx, "claim:")
}

// TestFormatOutput_Mission_Log_CorrectEvent asserts `mission log`
// renders a correct event's kind/round inline, the same one-liner
// summary convention every other event type gets.
func TestFormatOutput_Mission_Log_CorrectEvent(t *testing.T) {
	result := `{"events":[
    {"ts":"2026-04-07T21:30:00Z","event":"correct","actor":"claude","details":{"kind":"fabrication","round":1,"claim":"x","corrected":"y"}}
  ]}`
	payload := makeToolPayload("mission", "log", result)
	out := runFormat(t, payload)

	r := parseFormatResult(t, out)
	assert.Equal(t, "1 event", r.HookSpecificOutput.UpdatedMCPToolOutput)
	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "correct")
	assert.Contains(t, ctx, "by claude")
	assert.Contains(t, ctx, "kind=fabrication")
	assert.Contains(t, ctx, "round=1")
}
