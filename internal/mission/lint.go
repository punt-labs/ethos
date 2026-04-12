package mission

import (
	"path/filepath"
	"strings"
)

// Severity classifies a lint warning.
type Severity string

const (
	SeverityWarn Severity = "warn"
	SeverityInfo Severity = "info"
)

// Warning is a single advisory finding from Lint.
type Warning struct {
	Field    string   `json:"field" yaml:"field"`
	Message  string   `json:"message" yaml:"message"`
	Severity Severity `json:"severity" yaml:"severity"`
}

// Lint runs advisory heuristics on a parsed Contract and returns
// warnings. It does not require a store round-trip. All checks are
// non-blocking — callers should print warnings but not fail on them.
//
// Six heuristics:
//  1. write_set contains a .go file but not the adjacent _test.go
//  2. write_set contains production code but no CHANGELOG.md entry
//  3. success_criteria mention README/docs but README.md is not in write_set
//  4. write_set contains _test.go but no corresponding .go (inverted gap)
//  5. inputs.files contains paths not in write_set
//  6. evaluator identity has no role bound (handle is "evaluator" placeholder)
func Lint(c *Contract) []Warning {
	if c == nil {
		return nil
	}
	var ws []Warning
	ws = lintAdjacentTest(c, ws)
	ws = lintChangelog(c, ws)
	ws = lintReadmeInCriteria(c, ws)
	ws = lintInvertedTestGap(c, ws)
	ws = lintInputsNotInWriteSet(c, ws)
	ws = lintEvaluatorRole(c, ws)
	return ws
}

// lintAdjacentTest warns when a .go file in write_set has no adjacent
// _test.go. Skips files that are themselves test files.
func lintAdjacentTest(c *Contract, ws []Warning) []Warning {
	set := writeSetIndex(c.WriteSet)
	for _, p := range c.WriteSet {
		if !strings.HasSuffix(p, ".go") {
			continue
		}
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		want := strings.TrimSuffix(p, ".go") + "_test.go"
		if !set[CanonicalPath(want)] {
			// Also check directory coverage: if the write_set
			// contains the directory of the test file, the worker
			// can create it.
			dir := filepath.Dir(want) + "/"
			if !set[CanonicalPath(dir)] {
				ws = append(ws, Warning{
					Field:    "write_set",
					Message:  p + " has no adjacent " + filepath.Base(want) + " in write_set",
					Severity: SeverityWarn,
				})
			}
		}
	}
	return ws
}

// lintChangelog warns when the write_set contains production code but
// no CHANGELOG.md.
func lintChangelog(c *Contract, ws []Warning) []Warning {
	hasCode := false
	hasCL := false
	for _, p := range c.WriteSet {
		cp := CanonicalPath(p)
		if cp == "CHANGELOG.md" || cp == "changelog.md" {
			hasCL = true
		}
		if isProductionCode(p) {
			hasCode = true
		}
	}
	if hasCode && !hasCL {
		ws = append(ws, Warning{
			Field:    "write_set",
			Message:  "write_set has production code but no CHANGELOG.md",
			Severity: SeverityInfo,
		})
	}
	return ws
}

// lintReadmeInCriteria warns when success_criteria mention README or
// docs but README.md is not in write_set.
func lintReadmeInCriteria(c *Contract, ws []Warning) []Warning {
	mentionsReadme := false
	for _, sc := range c.SuccessCriteria {
		low := strings.ToLower(sc)
		if strings.Contains(low, "readme") || strings.Contains(low, "documentation") {
			mentionsReadme = true
			break
		}
	}
	if !mentionsReadme {
		return ws
	}
	set := writeSetIndex(c.WriteSet)
	if !set["README.md"] && !set["readme.md"] {
		// Check whether any directory in write_set could contain README.md.
		hasDir := false
		for _, p := range c.WriteSet {
			if strings.HasSuffix(p, "/") {
				hasDir = true
				break
			}
		}
		if !hasDir {
			ws = append(ws, Warning{
				Field:    "success_criteria",
				Message:  "criteria mention README but README.md is not in write_set",
				Severity: SeverityWarn,
			})
		}
	}
	return ws
}

// lintInvertedTestGap warns when write_set contains a _test.go but
// not the corresponding production .go file.
func lintInvertedTestGap(c *Contract, ws []Warning) []Warning {
	set := writeSetIndex(c.WriteSet)
	for _, p := range c.WriteSet {
		if !strings.HasSuffix(p, "_test.go") {
			continue
		}
		prod := strings.TrimSuffix(p, "_test.go") + ".go"
		if !set[CanonicalPath(prod)] {
			dir := filepath.Dir(prod) + "/"
			if !set[CanonicalPath(dir)] {
				ws = append(ws, Warning{
					Field:    "write_set",
					Message:  p + " has no corresponding " + filepath.Base(prod) + " in write_set",
					Severity: SeverityInfo,
				})
			}
		}
	}
	return ws
}

// lintInputsNotInWriteSet warns when an inputs.files path is absent
// from write_set. This signals the leader may have intended to add
// the file to write_set but forgot.
func lintInputsNotInWriteSet(c *Contract, ws []Warning) []Warning {
	set := writeSetIndex(c.WriteSet)
	for _, f := range c.Inputs.Files {
		cf := CanonicalPath(f)
		if cf == "" {
			continue
		}
		if set[cf] {
			continue
		}
		// Check directory coverage. CanonicalPath strips the
		// trailing slash, so we re-add "/" to enforce a segment
		// boundary — without it "internal/foo" would match
		// "internal/foobar/file.go".
		covered := false
		for _, w := range c.WriteSet {
			if strings.HasSuffix(w, "/") && strings.HasPrefix(cf, CanonicalPath(w)+"/") {
				covered = true
				break
			}
		}
		if !covered {
			ws = append(ws, Warning{
				Field:    "inputs.files",
				Message:  f + " is in inputs.files but not in write_set",
				Severity: SeverityInfo,
			})
		}
	}
	return ws
}

// lintEvaluatorRole warns when the evaluator handle looks like a
// placeholder. A real evaluator should be a named identity with a
// role binding; common placeholders suggest the leader forgot to
// assign one.
func lintEvaluatorRole(c *Contract, ws []Warning) []Warning {
	h := strings.TrimSpace(c.Evaluator.Handle)
	if h == "" || h == "evaluator" || h == "tbd" || h == "TBD" {
		ws = append(ws, Warning{
			Field:    "evaluator.handle",
			Message:  "evaluator handle " + h + " looks like a placeholder — assign a named identity",
			Severity: SeverityWarn,
		})
	}
	return ws
}

// writeSetIndex builds a set of canonical paths from write_set for
// O(1) membership checks.
func writeSetIndex(writeSet []string) map[string]bool {
	m := make(map[string]bool, len(writeSet))
	for _, p := range writeSet {
		cp := CanonicalPath(p)
		if cp != "" {
			m[cp] = true
		}
	}
	return m
}

// isProductionCode reports whether the path looks like production
// source code (not tests, not docs, not config).
func isProductionCode(p string) bool {
	if strings.HasSuffix(p, "_test.go") {
		return false
	}
	ext := filepath.Ext(p)
	switch ext {
	case ".go", ".py", ".rs", ".js", ".ts", ".c", ".h":
		return true
	}
	return false
}
