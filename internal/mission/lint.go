package mission

import (
	"path/filepath"
	"regexp"
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
// Ten heuristics:
//  1. write_set contains a .go file but not the adjacent _test.go
//  2. write_set contains production code but no CHANGELOG.md entry
//  3. success_criteria mention README/docs but README.md is not in write_set
//  4. write_set contains _test.go but no corresponding .go (inverted gap)
//  5. inputs.files contains paths not in write_set
//  6. evaluator identity has no role bound (handle is "evaluator" placeholder)
//  7. context references another repo but no cross-repo collaboration noted
//  8. design mission has no user-visible impact criterion
//  9. docs write-set with a generalist evaluator
//  10. pipeline selector: suggests quick/standard/full based on contract size
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
	ws = lintCrossRepoContext(c, ws)
	ws = lintDesignImpact(c, ws)
	ws = lintDocsEvaluator(c, ws)
	ws = lintPipelineSelector(c, ws)
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
			Message:  "evaluator handle " + formatHandle(h) + " looks like a placeholder — assign a named identity",
			Severity: SeverityWarn,
		})
	}
	return ws
}

// formatHandle returns a quoted representation of h for use in
// diagnostic messages. An empty handle becomes "(empty)" so the
// message reads naturally without a double space.
func formatHandle(h string) string {
	if h == "" {
		return "(empty)"
	}
	return `"` + h + `"`
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

// repoNamePattern matches owner/repo (e.g. "punt-labs/ethos") or a
// bare repo-like name (word-word or word, 2+ chars, not a file path).
var repoNamePattern = regexp.MustCompile(`\b[a-zA-Z0-9_-]+/[a-zA-Z0-9_-]+\b`)

// collaborationEvidence reports whether s contains @handle references
// or collaboration phrases.
func collaborationEvidence(s string) bool {
	if strings.Contains(s, "@") {
		return true
	}
	low := strings.ToLower(s)
	for _, phrase := range []string{"discussed with", "agreed with", "coordinated with"} {
		if strings.Contains(low, phrase) {
			return true
		}
	}
	return false
}

// lintCrossRepoContext warns when context references another repo but
// contains no evidence of cross-repo collaboration (no @handle, no
// "discussed with"/"agreed with" phrases).
func lintCrossRepoContext(c *Contract, ws []Warning) []Warning {
	ctx := c.Context
	if ctx == "" {
		return ws
	}
	if !hasExternalRepoRef(ctx, c.WriteSet) {
		return ws
	}
	if collaborationEvidence(ctx) {
		return ws
	}
	return append(ws, Warning{
		Field:    "context",
		Message:  "context references another repo but no cross-repo collaboration noted",
		Severity: SeverityWarn,
	})
}

// hasExternalRepoRef reports whether s contains a word/word pattern
// that is not a prefix of any path in writeSet. Patterns like
// "internal/mission" match repoNamePattern but are file paths, not
// repo references.
func hasExternalRepoRef(s string, writeSet []string) bool {
	matches := repoNamePattern.FindAllString(s, -1)
	for _, m := range matches {
		if !isWriteSetPrefix(m, writeSet) {
			return true
		}
	}
	return false
}

// isWriteSetPrefix reports whether candidate is a path prefix of any
// entry in writeSet. Both sides are canonicalized for comparison.
func isWriteSetPrefix(candidate string, writeSet []string) bool {
	cc := CanonicalPath(candidate)
	if cc == "" {
		return false
	}
	prefix := cc + "/"
	for _, w := range writeSet {
		cw := CanonicalPath(w)
		if cw == cc || strings.HasPrefix(cw, prefix) {
			return true
		}
	}
	return false
}

// isDocsOnlyWriteSet reports whether every path in write_set is a
// documentation file or inside a docs/ directory.
func isDocsOnlyWriteSet(writeSet []string) bool {
	return allDocPaths(writeSet)
}

// lintDesignImpact warns when the write_set is docs-only but no
// success criterion mentions user-visible impact.
func lintDesignImpact(c *Contract, ws []Warning) []Warning {
	if !isDocsOnlyWriteSet(c.WriteSet) {
		return ws
	}
	for _, sc := range c.SuccessCriteria {
		low := strings.ToLower(sc)
		if strings.Contains(low, "before") && strings.Contains(low, "after") {
			return ws
		}
		if strings.Contains(low, "user-visible") || strings.Contains(low, "user-facing") {
			return ws
		}
	}
	return append(ws, Warning{
		Field:    "success_criteria",
		Message:  "design mission has no user-visible impact criterion",
		Severity: SeverityWarn,
	})
}

// lintDocsEvaluator warns (info) when the write_set is docs-only and
// the evaluator handle looks like a generalist placeholder.
func lintDocsEvaluator(c *Contract, ws []Warning) []Warning {
	if !isDocsOnlyWriteSet(c.WriteSet) {
		return ws
	}
	h := strings.TrimSpace(c.Evaluator.Handle)
	switch h {
	case "", "evaluator", "tbd", "TBD", "claude", "default":
		return append(ws, Warning{
			Field:    "evaluator.handle",
			Message:  "evaluator may not have domain expertise for this design",
			Severity: SeverityInfo,
		})
	}
	return ws
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

// lintPipelineSelector suggests a pipeline template based on the
// nature and size of the contract. Nature-based detection (from
// context and write_set patterns) takes priority over size-based
// fallback. Skips when the contract already specifies a pipeline.
func lintPipelineSelector(c *Contract, ws []Warning) []Warning {
	if hasPipeline(c) {
		return ws
	}

	ctx := strings.ToLower(c.Context)

	// Nature-based selection — priority order.
	if name, reason := detectNature(ctx, c.WriteSet); name != "" {
		return append(ws, Warning{
			Field:    "pipeline",
			Message:  "consider pipeline: " + name + " (" + reason + ")",
			Severity: SeverityInfo,
		})
	}

	// Size-based fallback.
	nFiles := len(c.WriteSet)
	nCriteria := len(c.SuccessCriteria)

	switch {
	case nFiles > 10 || hasMultipleRepoRefs(c.Context, c.WriteSet):
		return append(ws, Warning{
			Field:    "pipeline",
			Message:  "consider pipeline: full (" + pipelineReason(nFiles, nCriteria, c.Context, c.WriteSet) + ")",
			Severity: SeverityInfo,
		})
	case nFiles >= 4 || nCriteria >= 3:
		return append(ws, Warning{
			Field:    "pipeline",
			Message:  "consider pipeline: standard (" + pipelineReason(nFiles, nCriteria, c.Context, c.WriteSet) + ")",
			Severity: SeverityInfo,
		})
	case nFiles >= 1 && nFiles <= 3 && nCriteria >= 1 && nCriteria <= 2:
		return append(ws, Warning{
			Field:    "pipeline",
			Message:  "consider pipeline: quick (" + pipelineReason(nFiles, nCriteria, c.Context, c.WriteSet) + ")",
			Severity: SeverityInfo,
		})
	}
	return ws
}

// detectNature checks context and write_set patterns for nature-based
// pipeline selection. Returns the pipeline name and reason, or empty
// strings if no nature match is found.
func detectNature(ctx string, writeSet []string) (string, string) {
	// product: context mentions product validation AND write_set is non-empty
	if len(writeSet) > 0 {
		productKeywords := []string{"prfaq", "pr/faq", "working backwards", "new feature", "product validation"}
		if kw, ok := contextContainsAny(ctx, productKeywords); ok {
			return "product", "context mentions " + kw
		}
	}

	// formal
	formalKeywords := []string{"z-spec", "zspec", "formal spec", "model check", "invariant", "state machine", "protocol"}
	if kw, ok := contextContainsAny(ctx, formalKeywords); ok {
		return "formal", "context mentions " + kw
	}

	// coe
	coeKeywords := []string{"cause of error", "recurring bug", "data corruption", "incident", "fixed before", "postmortem"}
	if kw, ok := contextContainsAny(ctx, coeKeywords); ok {
		return "coe", "context mentions " + kw
	}

	// docs: ALL write_set entries match doc patterns
	if allDocPaths(writeSet) {
		return "docs", "write_set is all documentation files"
	}

	// coverage
	coverageKeywords := []string{"test gap"}
	if kw, ok := contextContainsAny(ctx, coverageKeywords); ok {
		return "coverage", "context mentions " + kw
	}

	return "", ""
}

// contextContainsAny reports whether ctx contains any of the keywords.
// The caller must lowercase ctx before calling; keywords are expected to
// be lowercase. Returns the first matched keyword and true, or empty
// string and false.
func contextContainsAny(ctx string, keywords []string) (string, bool) {
	for _, kw := range keywords {
		if strings.Contains(ctx, kw) {
			return kw, true
		}
	}
	return "", false
}

// isDocPathCanonical reports whether a canonicalized path matches a
// documentation file pattern: *.md, *.tex, docs/*, *.pdf.
func isDocPathCanonical(cp string) bool {
	if strings.HasPrefix(cp, "docs/") {
		return true
	}
	ext := filepath.Ext(cp)
	switch ext {
	case ".md", ".tex", ".pdf":
		return true
	}
	return false
}

// allDocPaths reports whether every entry in writeSet is a
// documentation file. Returns false for an empty write_set.
// Directory entries are doc paths only if under docs/.
func allDocPaths(writeSet []string) bool {
	if len(writeSet) == 0 {
		return false
	}
	for _, p := range writeSet {
		cp := CanonicalPath(p)
		// Treat "docs" and "docs/..." as doc directories regardless
		// of whether the original entry had a trailing slash.
		if cp == "docs" || strings.HasPrefix(cp, "docs/") {
			continue
		}
		if strings.HasSuffix(p, "/") {
			// Non-doc directory.
			return false
		}
		if !isDocPathCanonical(cp) {
			return false
		}
	}
	return true
}

// hasPipeline reports whether the contract already specifies a
// pipeline. Returns false until the Pipeline field is added to
// Contract.
func hasPipeline(_ *Contract) bool {
	return false
}

// hasMultipleRepoRefs reports whether s contains two or more distinct
// external repo references (owner/repo patterns not matching write_set
// path prefixes).
func hasMultipleRepoRefs(s string, writeSet []string) bool {
	if s == "" {
		return false
	}
	matches := repoNamePattern.FindAllString(s, -1)
	seen := make(map[string]bool)
	for _, m := range matches {
		if !isWriteSetPrefix(m, writeSet) {
			seen[m] = true
		}
	}
	return len(seen) >= 2
}

// pipelineReason returns a short explanation for the suggested
// pipeline tier.
func pipelineReason(nFiles, nCriteria int, ctx string, writeSet []string) string {
	parts := make([]string, 0, 2)
	if nFiles > 10 {
		parts = append(parts, "11+ files in write_set")
	} else if nFiles >= 4 {
		parts = append(parts, "4+ files in write_set")
	} else {
		parts = append(parts, "1-3 files in write_set")
	}
	if nCriteria >= 3 {
		parts = append(parts, "3+ success criteria")
	} else {
		parts = append(parts, "1-2 success criteria")
	}
	if hasMultipleRepoRefs(ctx, writeSet) {
		parts = append(parts, "multiple repos in context")
	}
	return strings.Join(parts, ", ")
}
