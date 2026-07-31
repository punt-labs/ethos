package main

import (
	"testing"

	"github.com/punt-labs/ethos/internal/mission"
	"github.com/stretchr/testify/assert"
)

// TestWarnWriteSetExpanded pins the pipeline half of the ethos-t2lb
// visibility rule. A --var holding several paths expands one stage
// entry into several, which is the fix — but it also widens the claim,
// and the instantiate summary table prints mission IDs, not paths. The
// growth is the exact signal: expansion can never shrink a list.
func TestWarnWriteSetExpanded(t *testing.T) {
	tests := []struct {
		name      string
		stage     []string // the stage template's write_set
		contract  []string // what expansion produced
		wantWarn  bool
		wantParts []string
	}{
		{
			name:      "one entry became two",
			stage:     []string{"{target}"},
			contract:  []string{"docs/a.md", "docs/b.md"},
			wantWarn:  true,
			wantParts: []string{`stage "implement"`, "expanded to 2 entries", `"docs/a.md"`, `"docs/b.md"`},
		},
		{
			name:     "a single path is quiet",
			stage:    []string{"{target}"},
			contract: []string{"docs/a.md"},
			wantWarn: false,
		},
		{
			name:     "an unchanged count is quiet",
			stage:    []string{"docs/", "internal/"},
			contract: []string{"docs/", "internal/"},
			wantWarn: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stages := []mission.Stage{{Name: "implement", WriteSet: tt.stage}}
			contracts := []*mission.Contract{{WriteSet: tt.contract}}

			out := captureStderrFn(t, func() { warnWriteSetExpanded(stages, contracts) })

			if !tt.wantWarn {
				assert.Empty(t, out, "an unexpanded write_set must not warn")
				return
			}
			for _, part := range tt.wantParts {
				assert.Contains(t, out, part)
			}
		})
	}
}

// TestWarnWriteSetExpanded_ShortContractSlice asserts the helper
// tolerates a contract list shorter than the stage list, and a nil
// contract, rather than panicking on a caller that failed midway.
func TestWarnWriteSetExpanded_ShortContractSlice(t *testing.T) {
	stages := []mission.Stage{{Name: "one"}, {Name: "two"}}
	contracts := []*mission.Contract{nil}

	out := captureStderrFn(t, func() { warnWriteSetExpanded(stages, contracts) })
	assert.Empty(t, out)
}
