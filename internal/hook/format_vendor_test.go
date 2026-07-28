package hook

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The plan's whole job is to let an operator decide before vendor writes
// into git-tracked space, so the summary must lead with the number that
// decides it: how much bigger the closure is than what they asked for.
func TestFormatVendorSummarizesTheBlastRadius(t *testing.T) {
	result := `{
	  "dest": "/repo/.punt-labs/ethos",
	  "seeds": ["bwk"],
	  "identities": ["bwk", "claudia", "mal"],
	  "personalities": ["kernighan", "prose"],
	  "roles": ["go-specialist", "writer"],
	  "teams": ["engineering"],
	  "ext_files": 2,
	  "applied": false
	}`
	r := parseFormatResult(t, runFormat(t, makeToolPayload("vendor", "", result)))

	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Would vendor 3 identities")
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "2 pulled in via team membership")
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "/repo/.punt-labs/ethos")

	ctx := r.HookSpecificOutput.AdditionalContext
	assert.Contains(t, ctx, "bwk, claudia, mal")
	assert.Contains(t, ctx, "kernighan, prose")
	assert.Contains(t, ctx, "engineering")
	assert.Contains(t, ctx, "Nothing written")
}

func TestFormatVendorApplied(t *testing.T) {
	result := `{
	  "dest": "/repo/.punt-labs/ethos",
	  "seeds": ["bwk"],
	  "identities": ["bwk"],
	  "applied": true,
	  "files_written": 4
	}`
	r := parseFormatResult(t, runFormat(t, makeToolPayload("vendor", "", result)))

	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Vendored 1 identities")
	assert.NotContains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "pulled in",
		"no blast radius to report when the closure is exactly the seeds")
	assert.NotContains(t, r.HookSpecificOutput.AdditionalContext, "Nothing written")
}

func TestFormatVendorSurfacesWarningsAndPrunes(t *testing.T) {
	result := `{
	  "seeds": ["bwk"],
	  "identities": ["bwk"],
	  "warnings": [{"handle": "bwk", "namespace": "quarry", "key": "voice_key"}],
	  "pruned": ["/repo/.punt-labs/ethos/identities/stale.yaml"],
	  "applied": false
	}`
	ctx := parseFormatResult(t, runFormat(t, makeToolPayload("vendor", "", result))).
		HookSpecificOutput.AdditionalContext

	assert.Contains(t, ctx, "voice_key")
	assert.Contains(t, ctx, "stale.yaml")
	assert.Contains(t, ctx, "No longer in the closure (1)")
}

// A payload the formatter cannot read must degrade to the raw text, not
// render a confident but empty table.
func TestFormatVendorFallsBackOnAnUnreadablePayload(t *testing.T) {
	r := parseFormatResult(t, runFormat(t, makeToolPayload("vendor", "", `{"unexpected": true}`)))
	assert.Contains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "unexpected")
	assert.NotContains(t, r.HookSpecificOutput.UpdatedMCPToolOutput, "Would vendor")
}

// Every MCP tool needs a formatter before it ships (DES-020). This
// asserts the dispatch is wired, not just that the function exists.
func TestFormatOutputDispatchesVendor(t *testing.T) {
	out := runFormat(t, makeToolPayload("vendor", "",
		`{"seeds":["bwk"],"identities":["bwk"],"applied":false}`))
	assert.True(t, strings.Contains(out, "Would vendor"),
		"the vendor tool must reach formatVendor, not the default truncation")
}
