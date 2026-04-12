package mission

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lintContract returns a contract that passes all six heuristics
// cleanly: paired .go and _test.go, CHANGELOG.md present, no
// README mention in criteria, no input files, real evaluator handle.
func lintContract() Contract {
	return Contract{
		Leader: "claude",
		Worker: "bwk",
		Evaluator: Evaluator{
			Handle:   "mdm",
			PinnedAt: "2026-04-12T00:00:00Z",
		},
		WriteSet: []string{
			"internal/mission/lint.go",
			"internal/mission/lint_test.go",
			"CHANGELOG.md",
		},
		SuccessCriteria: []string{"make check passes"},
		Budget:          Budget{Rounds: 2},
	}
}

func TestLint_CleanContract(t *testing.T) {
	c := lintContract()
	ws := Lint(&c)
	assert.Empty(t, ws, "clean contract should produce no warnings")
}

func TestLint_NilContract(t *testing.T) {
	ws := Lint(nil)
	assert.Empty(t, ws)
}

func TestLint(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Contract)
		wantMsg string   // substring in at least one warning
		wantSev Severity // severity of the matching warning
	}{
		// Heuristic 1: adjacent test file missing
		{
			name: "H1: .go without _test.go",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/mission/lint.go", "CHANGELOG.md"}
			},
			wantMsg: "lint_test.go in write_set",
			wantSev: SeverityWarn,
		},
		{
			name: "H1: directory covers test file — no H1 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/mission/lint.go", "internal/mission/", "CHANGELOG.md"}
			},
			wantMsg: "",
		},
		// Heuristic 2: CHANGELOG gap
		{
			name: "H2: production code without CHANGELOG",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/mission/lint.go", "internal/mission/lint_test.go"}
			},
			wantMsg: "CHANGELOG.md",
			wantSev: SeverityInfo,
		},
		{
			name: "H2: CHANGELOG present — no H2 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/mission/lint.go", "internal/mission/lint_test.go", "CHANGELOG.md"}
			},
			wantMsg: "",
		},
		{
			name: "H2: no production code — no H2 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"CHANGELOG.md", "README.md"}
			},
			wantMsg: "",
		},
		// Heuristic 3: README in criteria but not in write_set
		{
			name: "H3: criteria mention README, not in write_set",
			mutate: func(c *Contract) {
				c.SuccessCriteria = []string{"Update README with new command"}
			},
			wantMsg: "README.md is not in write_set",
			wantSev: SeverityWarn,
		},
		{
			name: "H3: criteria mention documentation, not in write_set",
			mutate: func(c *Contract) {
				c.SuccessCriteria = []string{"Update documentation for new feature"}
			},
			wantMsg: "README.md is not in write_set",
			wantSev: SeverityWarn,
		},
		{
			name: "H3: criteria mention README, present in write_set — no H3 warning",
			mutate: func(c *Contract) {
				c.SuccessCriteria = []string{"Update README with new command"}
				c.WriteSet = append(c.WriteSet, "README.md")
			},
			wantMsg: "",
		},
		// Heuristic 4: inverted test gap
		{
			name: "H4: _test.go without corresponding .go",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/mission/lint_test.go", "CHANGELOG.md"}
			},
			wantMsg: "lint.go in write_set",
			wantSev: SeverityInfo,
		},
		{
			name: "H4: both present — no H4 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/mission/lint.go", "internal/mission/lint_test.go", "CHANGELOG.md"}
			},
			wantMsg: "",
		},
		{
			name: "H4: directory covers production file — no H4 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/mission/lint_test.go", "internal/mission/", "CHANGELOG.md"}
			},
			wantMsg: "",
		},
		// Heuristic 5: inputs.files not in write_set
		{
			name: "H5: input file not in write_set",
			mutate: func(c *Contract) {
				c.Inputs.Files = []string{"cmd/ethos/mission.go"}
			},
			wantMsg: "cmd/ethos/mission.go is in inputs.files but not in write_set",
			wantSev: SeverityInfo,
		},
		{
			name: "H5: input file covered by write_set directory — no H5 warning",
			mutate: func(c *Contract) {
				c.Inputs.Files = []string{"internal/mission/validate.go"}
				c.WriteSet = append(c.WriteSet, "internal/mission/")
			},
			wantMsg: "",
		},
		{
			name: "H5: input file in write_set — no H5 warning",
			mutate: func(c *Contract) {
				c.Inputs.Files = []string{"internal/mission/lint.go"}
			},
			wantMsg: "",
		},
		// Heuristic 6: placeholder evaluator handle
		{
			name: "H6: evaluator handle is 'evaluator'",
			mutate: func(c *Contract) {
				c.Evaluator.Handle = "evaluator"
			},
			wantMsg: "looks like a placeholder",
			wantSev: SeverityWarn,
		},
		{
			name: "H6: evaluator handle is 'tbd'",
			mutate: func(c *Contract) {
				c.Evaluator.Handle = "tbd"
			},
			wantMsg: "looks like a placeholder",
			wantSev: SeverityWarn,
		},
		{
			name: "H6: evaluator handle is 'TBD'",
			mutate: func(c *Contract) {
				c.Evaluator.Handle = "TBD"
			},
			wantMsg: "looks like a placeholder",
			wantSev: SeverityWarn,
		},
		{
			name: "H6: evaluator handle is empty",
			mutate: func(c *Contract) {
				c.Evaluator.Handle = ""
			},
			wantMsg: "looks like a placeholder",
			wantSev: SeverityWarn,
		},
		{
			name: "H6: real evaluator handle — no H6 warning",
			mutate: func(c *Contract) {
				c.Evaluator.Handle = "mdm"
			},
			wantMsg: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := lintContract()
			tc.mutate(&c)
			ws := Lint(&c)
			if tc.wantMsg == "" {
				assert.Empty(t, ws, "expected no warnings; got %v", ws)
				return
			}
			require.NotEmpty(t, ws, "expected at least one warning containing %q", tc.wantMsg)
			found := false
			for _, w := range ws {
				if strings.Contains(w.Message, tc.wantMsg) {
					found = true
					if tc.wantSev != "" {
						assert.Equal(t, tc.wantSev, w.Severity)
					}
					break
				}
			}
			assert.True(t, found, "no warning contains %q; got %v", tc.wantMsg, ws)
		})
	}
}

func TestLint_MultipleWarnings(t *testing.T) {
	c := Contract{
		Evaluator: Evaluator{Handle: "tbd"},
		WriteSet:  []string{"internal/mission/lint.go"},
		SuccessCriteria: []string{
			"Update README with lint command",
		},
	}
	ws := Lint(&c)
	// H1 (missing _test.go), H2 (no CHANGELOG), H3 (README in
	// criteria), H6 (placeholder evaluator) — at least 4 warnings.
	assert.GreaterOrEqual(t, len(ws), 4, "expected >= 4 warnings; got %v", ws)

	msgs := make([]string, len(ws))
	for i, w := range ws {
		msgs[i] = w.Message
	}
	joined := strings.Join(msgs, " | ")
	assert.Contains(t, joined, "lint_test.go")
	assert.Contains(t, joined, "CHANGELOG")
	assert.Contains(t, joined, "README")
	assert.Contains(t, joined, "placeholder")
}
