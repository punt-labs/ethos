package mission

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lintContract returns a contract that passes H1-H9 cleanly: paired
// .go and _test.go, CHANGELOG.md present, no README mention in
// criteria, no input files, real evaluator handle, no cross-repo
// context, non-docs write_set, non-generalist evaluator.
//
// H10 (pipeline selector) fires because Pipeline is empty.
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
	// H10 fires (info) because Pipeline is empty.
	require.Len(t, ws, 1, "expected exactly one warning (H10)")
	assert.Equal(t, SeverityInfo, ws[0].Severity)
	assert.Contains(t, ws[0].Message, "consider pipeline:")
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
				c.WriteSet = []string{"CHANGELOG.md", "README.md", "config.yaml"}
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
			name: "H5: prefix overlap without segment boundary — not covered",
			mutate: func(c *Contract) {
				c.Inputs.Files = []string{"internal/missionextra/file.go"}
				c.WriteSet = append(c.WriteSet, "internal/mission/")
			},
			wantMsg: "internal/missionextra/file.go is in inputs.files but not in write_set",
			wantSev: SeverityInfo,
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
		// Heuristic 7: cross-repo context without collaboration
		{
			name: "H7: context references repo without collaboration",
			mutate: func(c *Contract) {
				c.Context = "Extends punt-labs/ethos with new lint rules"
			},
			wantMsg: "no cross-repo collaboration noted",
			wantSev: SeverityWarn,
		},
		{
			name: "H7: context references repo with @handle — no H7 warning",
			mutate: func(c *Contract) {
				c.Context = "Extends punt-labs/ethos with new lint rules, @bwk agreed"
			},
			wantMsg: "",
		},
		{
			name: "H7: context references repo with discussed-with — no H7 warning",
			mutate: func(c *Contract) {
				c.Context = "Extends punt-labs/ethos, discussed with bwk"
			},
			wantMsg: "",
		},
		{
			name: "H7: empty context — no H7 warning",
			mutate: func(c *Contract) {
				c.Context = ""
			},
			wantMsg: "",
		},
		{
			name: "H7: context with no repo reference — no H7 warning",
			mutate: func(c *Contract) {
				c.Context = "This mission adds three design heuristics"
			},
			wantMsg: "",
		},
		{
			name: "H7: context mentions file path matching write_set — no H7 warning",
			mutate: func(c *Contract) {
				c.Context = "Changes to internal/mission linting logic"
			},
			wantMsg: "",
		},
		{
			name: "H7: context has both file path and real repo ref — H7 fires",
			mutate: func(c *Contract) {
				c.Context = "Changes to internal/mission plus punt-labs/biff integration"
			},
			wantMsg: "no cross-repo collaboration noted",
			wantSev: SeverityWarn,
		},
		// Heuristic 8: design mission without user-visible impact
		{
			name: "H8: docs-only write_set without impact criterion",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/architecture.md", "DESIGN.md"}
				c.SuccessCriteria = []string{"Document the architecture"}
			},
			wantMsg: "no user-visible impact criterion",
			wantSev: SeverityWarn,
		},
		{
			name: "H8: docs-only with before/after in criteria — no H8 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/architecture.md", "DESIGN.md"}
				c.SuccessCriteria = []string{"Show before and after comparison"}
			},
			wantMsg: "",
		},
		{
			name: "H8: docs-only with user-visible in criteria — no H8 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/architecture.md", "DESIGN.md"}
				c.SuccessCriteria = []string{"Include user-visible change summary"}
			},
			wantMsg: "",
		},
		{
			name: "H8: docs-only with user-facing in criteria — no H8 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/architecture.md"}
				c.SuccessCriteria = []string{"user-facing behavior documented"}
			},
			wantMsg: "",
		},
		{
			name: "H8: docs-only with directory entry — no H8 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/", "DESIGN.md"}
				c.SuccessCriteria = []string{"user-visible change documented"}
			},
			wantMsg: "",
		},
		{
			name: "H8: non-docs write_set — no H8 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/mission/lint.go", "internal/mission/lint_test.go", "CHANGELOG.md"}
				c.SuccessCriteria = []string{"make check passes"}
			},
			wantMsg: "",
		},
		{
			name: "H8: empty write_set — no H8 warning",
			mutate: func(c *Contract) {
				c.WriteSet = nil
				c.SuccessCriteria = []string{"Document the architecture"}
			},
			wantMsg: "",
		},
		// Heuristic 9: docs evaluator is generalist
		{
			name: "H9: docs write_set with generalist evaluator 'claude'",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/architecture.md", "DESIGN.md"}
				c.Evaluator.Handle = "claude"
			},
			wantMsg: "evaluator may not have domain expertise",
			wantSev: SeverityInfo,
		},
		{
			name: "H9: docs write_set with evaluator 'default'",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/architecture.md"}
				c.Evaluator.Handle = "default"
			},
			wantMsg: "evaluator may not have domain expertise",
			wantSev: SeverityInfo,
		},
		{
			name: "H9: docs write_set with empty evaluator",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/architecture.md"}
				c.Evaluator.Handle = ""
			},
			wantMsg: "evaluator may not have domain expertise",
			wantSev: SeverityInfo,
		},
		{
			name: "H9: docs write_set with named evaluator — no H9 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/architecture.md", "DESIGN.md"}
				c.Evaluator.Handle = "djb"
				c.SuccessCriteria = []string{"user-visible change documented"}
			},
			wantMsg: "",
		},
		{
			name: "H9: non-docs write_set with generalist evaluator — no H9 warning",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/mission/lint.go", "internal/mission/lint_test.go", "CHANGELOG.md"}
				c.Evaluator.Handle = "claude"
			},
			wantMsg: "",
		},
		// Heuristic 10: pipeline selector — nature-based detection
		{
			name: "H10: product — context mentions prfaq with non-empty write_set",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/foo/bar.go", "internal/foo/bar_test.go", "CHANGELOG.md"}
				c.SuccessCriteria = []string{"make check passes"}
				c.Context = "This is a new feature requiring prfaq validation"
			},
			wantMsg: "consider pipeline: product",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: formal — context mentions z-spec",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/foo/bar.go", "internal/foo/bar_test.go", "CHANGELOG.md"}
				c.SuccessCriteria = []string{"make check passes"}
				c.Context = "Complex state machine requiring z-spec verification"
			},
			wantMsg: "consider pipeline: formal",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: coe — context mentions recurring bug",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/foo/bar.go", "internal/foo/bar_test.go", "CHANGELOG.md"}
				c.SuccessCriteria = []string{"make check passes"}
				c.Context = "This bug was fixed before, a recurring bug observed again"
			},
			wantMsg: "consider pipeline: coe",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: docs — write_set is all documentation files",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/design.md", "README.md", "CHANGELOG.md"}
				c.SuccessCriteria = []string{"docs reviewed", "user-visible change documented"}
			},
			wantMsg: "consider pipeline: docs",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: docs — directory under docs/ counts as doc path",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/", "README.md"}
				c.SuccessCriteria = []string{"docs reviewed", "user-visible change documented"}
			},
			wantMsg: "consider pipeline: docs",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: docs — nested docs directory counts as doc path",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs/design/", "README.md"}
				c.SuccessCriteria = []string{"docs reviewed", "user-visible change documented"}
			},
			wantMsg: "consider pipeline: docs",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: docs — bare 'docs' without trailing slash is a doc dir",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs", "README.md"}
				c.SuccessCriteria = []string{"docs reviewed", "user-visible change documented"}
			},
			wantMsg: "consider pipeline: docs",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: docs-guide prefix look-alike is not a doc path",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"docs-guide/foo.go", "CHANGELOG.md"}
				c.SuccessCriteria = []string{"make check passes"}
			},
			wantMsg: "consider pipeline: quick",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: non-doc directory is not a doc path — no docs pipeline",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/foo/", "README.md"}
				c.SuccessCriteria = []string{"make check passes"}
			},
			wantMsg: "consider pipeline: quick",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: lone non-doc directory — size fallback, not docs",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/foo/"}
				c.SuccessCriteria = []string{"make check passes"}
			},
			wantMsg: "consider pipeline: quick",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: coverage — context mentions test gap",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/foo/bar.go", "internal/foo/bar_test.go", "CHANGELOG.md"}
				c.SuccessCriteria = []string{"make check passes"}
				c.Context = "Fill test gap in mission package"
			},
			wantMsg: "consider pipeline: coverage",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: nature wins over size — coe with 11 files",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go", "j.go", "k.go"}
				c.SuccessCriteria = []string{"tests pass"}
				c.Context = "This is a cause of error investigation for data corruption"
			},
			wantMsg: "consider pipeline: coe",
			wantSev: SeverityInfo,
		},
		// Heuristic 10: pipeline selector — size-based fallback
		{
			name: "H10: quick — 1-3 files, 1-2 criteria",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"internal/mission/lint.go", "internal/mission/lint_test.go"}
				c.SuccessCriteria = []string{"make check passes"}
			},
			wantMsg: "consider pipeline: quick",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: docs — single README.md triggers docs nature",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"README.md"}
				c.SuccessCriteria = []string{"updated"}
			},
			wantMsg: "consider pipeline: docs",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: quick — 3 files, 2 criteria",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"a.go", "a_test.go", "CHANGELOG.md"}
				c.SuccessCriteria = []string{"tests pass", "lint clean"}
			},
			wantMsg: "consider pipeline: quick",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: standard — 4 files",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"a.go", "a_test.go", "b.go", "b_test.go"}
				c.SuccessCriteria = []string{"tests pass"}
			},
			wantMsg: "consider pipeline: standard",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: standard — 3+ criteria",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"a.go", "a_test.go"}
				c.SuccessCriteria = []string{"tests pass", "lint clean", "docs updated"}
			},
			wantMsg: "consider pipeline: standard",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: standard — 10 files (boundary)",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go", "j.go"}
				c.SuccessCriteria = []string{"tests pass"}
			},
			wantMsg: "consider pipeline: standard",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: full — 11 files",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go", "j.go", "k.go"}
				c.SuccessCriteria = []string{"tests pass"}
			},
			wantMsg: "consider pipeline: full",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: full — multiple repos in context",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"a.go", "a_test.go"}
				c.SuccessCriteria = []string{"tests pass"}
				c.Context = "Coordinate punt-labs/ethos and punt-labs/biff for identity sync"
			},
			wantMsg: "consider pipeline: full",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: single repo in context — falls to size",
			mutate: func(c *Contract) {
				c.WriteSet = []string{"a.go", "a_test.go"}
				c.SuccessCriteria = []string{"tests pass"}
				c.Context = "Extends punt-labs/biff with refactoring"
			},
			wantMsg: "consider pipeline: quick",
			wantSev: SeverityInfo,
		},
		{
			name: "H10: empty write_set and criteria — no H10 warning",
			mutate: func(c *Contract) {
				c.WriteSet = nil
				c.SuccessCriteria = nil
			},
			wantMsg: "",
		},
		{
			name: "H10: empty write_set with criteria — no H10 warning",
			mutate: func(c *Contract) {
				c.WriteSet = nil
				c.SuccessCriteria = []string{"design complete"}
			},
			wantMsg: "",
		},
		{
			name: "H10: pipeline already set — no H10 warning",
			mutate: func(c *Contract) {
				c.Pipeline = "standard-2026-04-13-abc123"
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
				// Filter H10 pipeline suggestions — advisory,
				// fires on contracts without a Pipeline value.
				var filtered []Warning
				for _, w := range ws {
					if w.Field != "pipeline" {
						filtered = append(filtered, w)
					}
				}
				assert.Empty(t, filtered, "expected no warnings (ignoring pipeline); got %v", filtered)
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
	// criteria), H6 (placeholder evaluator), H10 (pipeline) — at
	// least 5 warnings.
	assert.GreaterOrEqual(t, len(ws), 5, "expected >= 5 warnings; got %v", ws)

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

// TestLintMonolithPressure pins H11 (DES-052). The heuristic fires
// when every write_set entry is file-shaped, extract_into is empty,
// and at least one success criterion mentions an extraction verb. Any
// of the three signals missing silences the warning.
func TestLintMonolithPressure(t *testing.T) {
	tests := []struct {
		name        string
		writeSet    []string
		extractInto []string
		criteria    []string
		wantWarn    bool
	}{
		{
			name:     "extract verb without authorization fires",
			writeSet: []string{"internal/foo/bar.go"},
			criteria: []string{"extract helpers from bar.go"},
			wantWarn: true,
		},
		{
			name:     "decompose verb without authorization fires",
			writeSet: []string{"internal/foo/bar.go"},
			criteria: []string{"decompose the god object"},
			wantWarn: true,
		},
		{
			name:     "refactor verb without authorization fires",
			writeSet: []string{"internal/foo/bar.go"},
			criteria: []string{"refactor the package layout"},
			wantWarn: true,
		},
		{
			name:     "split verb without authorization fires",
			writeSet: []string{"internal/foo/bar.go"},
			criteria: []string{"split bar.go into smaller files"},
			wantWarn: true,
		},
		{
			name:        "extract_into populated silences warning",
			writeSet:    []string{"internal/foo/bar.go"},
			extractInto: []string{"internal/foo/"},
			criteria:    []string{"extract helpers from bar.go"},
			wantWarn:    false,
		},
		{
			name:     "directory in write_set silences warning",
			writeSet: []string{"internal/foo/bar.go", "internal/foo/"},
			criteria: []string{"extract helpers from bar.go"},
			wantWarn: false,
		},
		{
			name:     "no decomposition mention silences warning",
			writeSet: []string{"internal/foo/bar.go"},
			criteria: []string{"add JSON output flag"},
			wantWarn: false,
		},
		{
			name:     "case-insensitive verb match",
			writeSet: []string{"internal/foo/bar.go"},
			criteria: []string{"REFACTOR the package"},
			wantWarn: true,
		},
		{
			name:     "empty write_set does not fire",
			writeSet: nil,
			criteria: []string{"extract helpers"},
			wantWarn: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Contract{
				WriteSet:        tt.writeSet,
				ExtractInto:     tt.extractInto,
				SuccessCriteria: tt.criteria,
			}
			ws := lintMonolithPressure(c, nil)
			if tt.wantWarn {
				if assert.Len(t, ws, 1) {
					assert.Equal(t, "extract_into", ws[0].Field)
					assert.Equal(t, SeverityWarn, ws[0].Severity)
					assert.Contains(t, ws[0].Message,
						"consider declaring extract_into")
				}
			} else {
				assert.Empty(t, ws)
			}
		})
	}
}

// TestLintExtractIntoFileEntries pins H12 (DES-052). One warning per
// extract_into entry whose basename carries a code-file extension.
// Validate would reject the contract outright at create time; lint
// surfaces the same problem earlier and per-entry.
func TestLintExtractIntoFileEntries(t *testing.T) {
	tests := []struct {
		name        string
		extractInto []string
		wantCount   int
		wantPaths   []string
	}{
		{
			name:        "empty extract_into no warning",
			extractInto: nil,
			wantCount:   0,
		},
		{
			name:        "directory entry no warning",
			extractInto: []string{"internal/foo/", "docs/"},
			wantCount:   0,
		},
		{
			name:        "go file extension warns",
			extractInto: []string{"internal/foo/bar.go"},
			wantCount:   1,
			wantPaths:   []string{"internal/foo/bar.go"},
		},
		{
			name:        "markdown file extension warns",
			extractInto: []string{"README.md"},
			wantCount:   1,
			wantPaths:   []string{"README.md"},
		},
		{
			name: "one warning per offending entry",
			extractInto: []string{
				"internal/foo/bar.go",
				"docs/",
				"internal/foo/baz.go",
			},
			wantCount: 2,
			wantPaths: []string{
				"internal/foo/bar.go",
				"internal/foo/baz.go",
			},
		},
		{
			name:        "uppercase extension still detected",
			extractInto: []string{"README.MD"},
			wantCount:   1,
		},
		{
			name:        "directory with dot in name no warning",
			extractInto: []string{"data/v1.2/"},
			wantCount:   0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Contract{ExtractInto: tt.extractInto}
			ws := lintExtractIntoFileEntries(c, nil)
			assert.Len(t, ws, tt.wantCount)
			for _, w := range ws {
				assert.Equal(t, "extract_into", w.Field)
				assert.Equal(t, SeverityWarn, w.Severity)
				assert.Contains(t, w.Message,
					"extract_into entries should be directories")
			}
			for _, p := range tt.wantPaths {
				found := false
				for _, w := range ws {
					if strings.Contains(w.Message, p) {
						found = true
						break
					}
				}
				assert.True(t, found,
					"expected warning for %q", p)
			}
		})
	}
}
