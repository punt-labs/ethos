package hook

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These are supplementary wire-shape tests. Registered handler payloads are
// exercised end to end in internal/mcp/mission_format_warnings_test.go.
func TestFormatOutputMutationWarnings(t *testing.T) {
	for _, method := range []string{"create", "result", "advance", "reflect", "correct", "close", "abandon"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			payload := map[string]any{
				"mission_id": "m-2026-11-05-197", "status": "closed", "round": 1,
				"current_round": 2, "verdict": "pass", "recommendation": "continue",
				"kind": "decision", "reason": "cancelled", "leader": "lead", "worker": "bwk",
				"private_unrendered": "do-not-copy-the-original-payload",
			}
			base, err := json.Marshal(payload)
			require.NoError(t, err)
			var clean bytes.Buffer
			require.NoError(t, HandleFormatOutput(bytes.NewReader(makeToolPayload("mission", method, string(base))), &clean))
			before := parseFormatResult(t, clean.String()).HookSpecificOutput
			warnings := []string{"accepted; trace pending", strings.Repeat("long diagnostic λ ", 40) + "end of diagnostic"}
			payload["warnings"] = warnings
			data, err := json.Marshal(payload)
			require.NoError(t, err)
			var out bytes.Buffer
			require.NoError(t, HandleFormatOutput(bytes.NewReader(makeToolPayload("mission", method, string(data))), &out))
			after := parseFormatResult(t, out.String()).HookSpecificOutput
			assert.Equal(t, before.UpdatedMCPToolOutput, after.UpdatedMCPToolOutput)
			assert.NotContains(t, after.AdditionalContext, "do-not-copy-the-original-payload")
			assert.NotContains(t, after.AdditionalContext, `"warnings"`)
			assert.Equal(t, 1, strings.Count(after.AdditionalContext, "Warnings:"))
			for _, warning := range warnings {
				assert.Contains(t, after.AdditionalContext, "\n  - "+warning)
			}
			if before.AdditionalContext != "" {
				assert.True(t, strings.HasPrefix(after.AdditionalContext, before.AdditionalContext+"\n\nWarnings:"))
			}
		})
	}
}

func TestFormatOutputMutationWarningsEmpty(t *testing.T) {
	for _, method := range []string{"create", "result", "advance", "reflect", "correct", "close", "abandon"} {
		t.Run(method, func(t *testing.T) {
			base := `{"mission_id":"m-2026-11-05-197","status":"closed","round":1,"current_round":2,"verdict":"pass","recommendation":"continue","kind":"decision"`
			var clean bytes.Buffer
			require.NoError(t, HandleFormatOutput(bytes.NewReader(makeToolPayload("mission", method, base+"}")), &clean))
			for _, empty := range []string{"null", "[]"} {
				var out bytes.Buffer
				require.NoError(t, HandleFormatOutput(bytes.NewReader(makeToolPayload("mission", method, base+`,"warnings":`+empty+"}")), &out))
				assert.Equal(t, clean.String(), out.String())
			}
		})
	}
}

func TestFormatOutputReflectWarningDiagnostics(t *testing.T) {
	const state = `"application":"already_applied","durability":"unconfirmed","trace":"pending"`
	longDiagnostic := strings.Repeat("long diagnostic λ ", 40) + "end of diagnostic"
	tests := []struct {
		name         string
		outcome      string
		warnings     string
		wantOutcome  string
		wantWarnings []string
	}{
		{
			name: "overlap", outcome: `{` + state + `,"diagnostics":["accepted; trace pending"]}`,
			warnings: `["accepted; trace pending"]`, wantOutcome: `{` + state + `,"diagnostics":[]}`,
			wantWarnings: []string{"accepted; trace pending"},
		},
		{
			name: "exact matches only", outcome: `{` + state + `,"diagnostics":["pending","pending detail","pen"," pending","pending ","unmatched","unmatched"]}`,
			warnings: `["pending"]`, wantOutcome: `{` + state + `,"diagnostics":["pending detail","pen"," pending","pending ","unmatched","unmatched"]}`,
			wantWarnings: []string{"pending"},
		},
		{
			name: "escaped Unicode", outcome: `{` + state + `,"diagnostics":["λ 雪 😀 \"quoted\" \\path\nnext","unmatched 雪 \"detail\""]}`,
			warnings:     `["\u03bb \u96ea \ud83d\ude00 \"quoted\" \\path\nnext"]`,
			wantOutcome:  `{` + state + `,"diagnostics":["unmatched 雪 \"detail\""]}`,
			wantWarnings: []string{"λ 雪 😀 \"quoted\" \\path\nnext"},
		},
		{
			name: "repeated warnings", outcome: `{` + state + `,"diagnostics":["same","same","other"]}`,
			warnings: `["same","same","top-level only"]`, wantOutcome: `{` + state + `,"diagnostics":["other"]}`,
			wantWarnings: []string{"same", "same", "top-level only"},
		},
		{
			name: "nonoverlap", outcome: `{` + state + `,"diagnostics":["outcome only"]}`,
			warnings: `["top-level only"]`, wantOutcome: `{` + state + `,"diagnostics":["outcome only"]}`,
			wantWarnings: []string{"top-level only"},
		},
		{
			name: "long diagnostics", outcome: `{` + state + `,"diagnostics":["same","` + longDiagnostic + `"]}`,
			warnings:     `["same","` + longDiagnostic + ` top-level"]`,
			wantOutcome:  `{` + state + `,"diagnostics":["` + longDiagnostic + `"]}`,
			wantWarnings: []string{"same", longDiagnostic + " top-level"},
		},
		{
			name: "nonstring diagnostics retained", outcome: `{` + state + `,"diagnostics":[null,42,{"detail":"retained"},["nested"],"same"]}`,
			warnings: `["same"]`, wantOutcome: `{` + state + `,"diagnostics":[null,42,{"detail":"retained"},["nested"]]}`,
			wantWarnings: []string{"same"},
		},
		{
			name: "null diagnostics", outcome: `{` + state + `,"diagnostics":null}`,
			warnings: `["top-level only"]`, wantOutcome: `{` + state + `,"diagnostics":null}`,
			wantWarnings: []string{"top-level only"},
		},
		{
			name: "missing diagnostics", outcome: `{` + state + `}`,
			warnings: `["top-level only"]`, wantOutcome: `{` + state + `}`,
			wantWarnings: []string{"top-level only"},
		},
		{
			name: "null outcome", outcome: `null`, warnings: `["top-level only"]`,
			wantWarnings: []string{"top-level only"},
		},
		{
			name: "missing outcome", warnings: `["top-level only"]`,
			wantWarnings: []string{"top-level only"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := `{"mission_id":"m-2026-11-05-222","round":2,"recommendation":"continue","private_unrendered":"do-not-copy-the-original-payload","warnings":` + tt.warnings
			if tt.outcome != "" {
				result += `,"outcome":` + tt.outcome
			}
			data := makeToolPayload("mission", "reflect", result+"}")
			var out bytes.Buffer
			require.NoError(t, HandleFormatOutput(bytes.NewReader(data), &out))
			display := parseFormatResult(t, out.String()).HookSpecificOutput
			assert.Equal(t, "Reflected m-2026-11-05-222 round 2 (continue)", display.UpdatedMCPToolOutput)
			assert.NotContains(t, display.AdditionalContext, "do-not-copy-the-original-payload")
			wantWarnings := "Warnings:\n  - " + strings.Join(tt.wantWarnings, "\n  - ")
			if tt.wantOutcome == "" {
				assert.Equal(t, wantWarnings, display.AdditionalContext)
			} else {
				outcomeContext, warningsContext, found := strings.Cut(display.AdditionalContext, "\n\nWarnings:")
				require.True(t, found)
				assert.JSONEq(t, tt.wantOutcome, outcomeContext)
				assert.Equal(t, wantWarnings, "Warnings:"+warningsContext)
			}
			assert.ErrorIs(t, HandleFormatOutput(bytes.NewReader(data), mutationWarningWriter{}), io.ErrClosedPipe)
		})
	}
}

func TestFormatOutputReflectWarningFreeOutcomeCompatibility(t *testing.T) {
	const outcome = `{"application":"applied","diagnostics":["retained λ diagnostic"],"durability":"confirmed","trace":"complete"}`
	const base = `{"mission_id":"m-2026-11-05-222","round":2,"recommendation":"continue","outcome":` + outcome
	const want = `{"hookSpecificOutput":{"hookEventName":"PostToolUse","updatedMCPToolOutput":"Reflected m-2026-11-05-222 round 2 (continue)","additionalContext":"{\"application\":\"applied\",\"diagnostics\":[\"retained λ diagnostic\"],\"durability\":\"confirmed\",\"trace\":\"complete\"}"}}` + "\n"
	for _, warnings := range []string{"", `,"warnings":null`, `,"warnings":[]`} {
		var out bytes.Buffer
		require.NoError(t, HandleFormatOutput(bytes.NewReader(makeToolPayload("mission", "reflect", base+warnings+"}")), &out))
		assert.Equal(t, want, out.String(), "warning-free output bytes must not change")
	}
}

func TestFormatOutputMutationWarningsFallback(t *testing.T) {
	for _, method := range []string{"advance", "future_mutation"} {
		t.Run(method, func(t *testing.T) {
			// No mission ID/round: even the malformed-success fallback must
			// retain warnings that occur beyond its compact summary's limit.
			data, err := json.Marshal(map[string]any{
				"unrendered": strings.Repeat("private data ", 40),
				"warnings":   []string{"accepted; optional trace pending"},
			})
			require.NoError(t, err)
			var out bytes.Buffer
			require.NoError(t, HandleFormatOutput(bytes.NewReader(makeToolPayload("mission", method, string(data))), &out))
			display := parseFormatResult(t, out.String()).HookSpecificOutput
			assert.Equal(t, "Warnings:\n  - accepted; optional trace pending", display.AdditionalContext)
			assert.NotContains(t, display.AdditionalContext, "private data")
		})
	}
}

func TestFormatOutputMutationWarningsMalformed(t *testing.T) {
	for _, warnings := range []string{`"private-value"`, `{}`, `42`, `true`, `["valid",42]`, `[null]`, `[{}]`, `[["nested"]]`} {
		t.Run(warnings, func(t *testing.T) {
			var out bytes.Buffer
			data := makeToolPayload("mission", "advance", `{"mission_id":"m-2026-11-05-197","current_round":2,"warnings":`+warnings+`}`)
			err := HandleFormatOutput(bytes.NewReader(data), &out)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "mission.advance")
			assert.Contains(t, err.Error(), "warnings")
			assert.NotContains(t, err.Error(), "private-value")
			assert.Empty(t, out.String(), "malformed warnings must not render a clean success")
		})
	}
}

type mutationWarningWriter struct{}

var _ io.Writer = mutationWarningWriter{}

func (mutationWarningWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestFormatOutputMutationWarningsWriterFailure(t *testing.T) {
	for _, warnings := range []string{"", `,"warnings":["accepted; trace pending"]`} {
		data := makeToolPayload("mission", "result", `{"mission_id":"m-2026-11-05-197","round":1,"verdict":"pass"`+warnings+`}`)
		assert.ErrorIs(t, HandleFormatOutput(bytes.NewReader(data), mutationWarningWriter{}), io.ErrClosedPipe)
	}
}

func TestFormatOutputMutationWarningsWithNotes(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"tool_name": "mcp__ethos__mission", "tool_input": map[string]string{"method": "advance"},
		"tool_response": []map[string]string{
			{"text": `{"mission_id":"m-2026-11-05-197","current_round":2,"warnings":["accepted; trace pending"]}`},
			{"text": "note: repository lookup unavailable"},
		},
	})
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, HandleFormatOutput(bytes.NewReader(data), &out))
	r := parseFormatResult(t, out.String()).HookSpecificOutput
	assert.Equal(t, "Advanced m-2026-11-05-197 to round 2\nnote: repository lookup unavailable", r.UpdatedMCPToolOutput)
	assert.Equal(t, "Warnings:\n  - accepted; trace pending\n\nnote: repository lookup unavailable", r.AdditionalContext)
	assert.ErrorIs(t, HandleFormatOutput(bytes.NewReader(data), mutationWarningWriter{}), io.ErrClosedPipe)
}

func TestFormatOutputMutationWarningsErrorPassthrough(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"tool_name": "mcp__ethos__mission", "tool_input": map[string]string{"method": "advance"},
		"tool_response": []map[string]any{{"text": `{"error":"refused","warnings":42}`, "is_error": true}},
	})
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, HandleFormatOutput(bytes.NewReader(data), &out))
	assert.Empty(t, out.String(), "MCP errors remain the host's responsibility, even with malformed diagnostics")

	// A JSON error without the host's MCP error marker retains the existing
	// error summary path rather than being interpreted as mutation success.
	out.Reset()
	data = makeToolPayload("mission", "advance", `{"error":"refused","warnings":42}`)
	require.NoError(t, HandleFormatOutput(bytes.NewReader(data), &out))
	r := parseFormatResult(t, out.String()).HookSpecificOutput
	assert.Equal(t, "error: refused", r.UpdatedMCPToolOutput)
	assert.Empty(t, r.AdditionalContext)
}
