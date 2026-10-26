package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

// checkoutUnderHome is a git repo under a temp HOME, with the global root.
func checkoutUnderHome(t *testing.T) (home, repo, global string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	repo = filepath.Join(home, "repo")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	initGitRepo(t, repo)
	return home, repo, filepath.Join(home, ".punt-labs", "ethos")
}

func implicitContract() *mission.Contract {
	return makeContractWithPreconditions([]mission.Precondition{{
		Form: mission.PreconditionFormImplicit, Message: "read before write",
	}}, mission.Inputs{}, nil)
}

// TestPathField_InsideTheCheckoutSkipsOnlyTheMidPathHomeRule: a path field
// naming a file of the checkout, repo-relative or absolute under it, skips only
// the mid-path /home/<name> rule (leakSlug and the rest still apply inside the
// checkout): nav--home-page.scss and @types/home/page.tsx are stored as written
// (or as <repo>/...), the seal guard agrees with the writer, and a Read of one
// satisfies a read-before-write precondition.
func TestPathField_InsideTheCheckoutSkipsOnlyTheMidPathHomeRule(t *testing.T) {
	_, repo, global := checkoutUnderHome(t)
	for _, rel := range []string{"src/styles/nav--home-page.scss", "@types/home/page.tsx"} {
		for _, form := range []string{"relative", "absolute"} {
			p := rel
			want := rel
			if form == "absolute" {
				p, want = filepath.Join(repo, rel), "<repo>/"+rel
			}
			sess := "sess-" + form + "-" + strings.NewReplacer("/", "-", "@", "at").Replace(rel)
			auditRead(t, repo, global, sess, p)

			e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, sess, repo, time.Now())
			assert.Equal(t, want, e.ToolInput["file_path"], "%s %s: stored as written", form, rel)
			line, err := json.Marshal(e)
			require.NoError(t, err)
			_, rule := guardAuditLineWhy(line)
			assert.Empty(t, rule, "%s %s: the seal guard agrees with the writer", form, rel)

			reason, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": filepath.Join(repo, rel)}, sess, repo, repo)
			require.NoError(t, err)
			assert.False(t, deny, "%s %s: the Read satisfies it: %s", form, rel, reason)
		}
	}
}

// TestPathField_ForeignProjectSlugUnderHomeIsRedacted: a path under a home but
// outside the checkout, with a project slug of another home, is redacted by the
// writer and the seal guard (leakSlug applies to every path field).
func TestPathField_ForeignProjectSlugUnderHomeIsRedacted(t *testing.T) {
	home, repo, _ := checkoutUnderHome(t)
	for _, tail := range []string{
		".claude/projects/C--Users-al-Coding-repo/abc.jsonl",
		".claude/projects/-var-home-bob-x/f.jsonl",
	} {
		abs := filepath.Join(home, tail)
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": abs}}, "s", repo, time.Now())
		assert.Equal(t, "[redacted-path]", e.ToolInput["file_path"], "writer: %s", tail)
		out, rule := guardAuditLineWhy(rawReadLine("~/" + tail))
		assert.NotEmpty(t, rule, "guard: %s", tail)
		assert.NotContains(t, string(out), "bob")
		assert.NotContains(t, string(out), "Users-al")
	}
}

// TestPathField_OwnSlugInsideTheCheckoutBecomesAPlaceholder: this machine's
// dash slug in a checkout path is written as -~, which a precondition expands.
func TestPathField_OwnSlugInsideTheCheckoutBecomesAPlaceholder(t *testing.T) {
	home, repo, global := checkoutUnderHome(t)
	slug := strings.ReplaceAll(home, string(filepath.Separator), "-")
	p := filepath.Join(repo, "cache", slug+"-proj", "x.txt")
	auditRead(t, repo, global, "sess-slug", p)
	e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", repo, time.Now())
	got, _ := e.ToolInput["file_path"].(string)
	assert.Contains(t, got, "-~")
	assert.NotContains(t, got, slug)
	reason, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": p}, "sess-slug", repo, repo)
	require.NoError(t, err)
	assert.False(t, deny, "the Read satisfies it: %s", reason)
}

// TestSealGuard_KeepsNumbersAndTheHash: a line the writer produced passes the
// seal guard without a rewrite, a large number keeps every digit, and the
// tool_input_hash is not changed.
func TestSealGuard_KeepsNumbersAndTheHash(t *testing.T) {
	line := []byte(`{"ts":"2026-01-01T00:00:00Z","session":"s","tool":"Bash","tool_input":{"offset":12345678901234567890,"f":1.50},"tool_input_hash":"abc"}`)
	out, rule := guardAuditLineWhy(line)
	assert.Empty(t, rule)
	assert.Contains(t, string(out), "12345678901234567890")
	assert.Contains(t, string(out), "1.50")
	assert.Contains(t, string(out), `"tool_input_hash":"abc"`)
}

// TestPreconditions_RedactedPlaceholderIsNotARead: a Read whose path the writer
// replaced by the placeholder is not recorded as a Read of a file named
// [redacted-path].
func TestPreconditions_RedactedPlaceholderIsNotARead(t *testing.T) {
	_, repo, global := checkoutUnderHome(t)
	auditRead(t, repo, global, "sess-ph", filepath.Join(repo, "C--Users-bob-x", "f"))
	for _, target := range []string{"[redacted-path]", filepath.Join(repo, "[redacted-path]"), "<repo>/[redacted-path]"} {
		_, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": target}, "sess-ph", repo, repo)
		require.NoError(t, err)
		assert.True(t, deny, "a Write of %q is denied", target)
	}
}

// TestPreconditions_LiteralPlaceholderNameIsNotTheExpandedOne: a file whose
// name holds a placeholder token (cache/-~-project/config.json) is stored
// unchanged and expands, when read back, to this machine's slug. A Read of it
// must not allow a Write of the file that name expands to, and a Write of a
// name holding a placeholder is denied, since a Read of it cannot be verified.
func TestPreconditions_LiteralPlaceholderNameIsNotTheExpandedOne(t *testing.T) {
	home, repo, global := checkoutUnderHome(t)
	slug := strings.ReplaceAll(home, string(filepath.Separator), "-")
	literal := "cache/-~-project/config.json"
	expanded := "cache/" + slug + "-project/config.json"
	write := func(sess, p string) (string, bool) {
		reason, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": p}, sess, repo, repo)
		require.NoError(t, err)
		return reason, deny
	}

	auditRead(t, repo, global, "sess-lit", literal)
	_, deny := write("sess-lit", expanded)
	assert.True(t, deny, "a Read of the literal placeholder name does not allow the expanded file")
	reason, deny := write("sess-lit", literal)
	assert.True(t, deny, "a Write of a name holding a placeholder is denied")
	assert.Contains(t, reason, "redaction placeholder")

	auditRead(t, repo, global, "sess-exp", expanded)
	reason, deny = write("sess-exp", expanded)
	assert.False(t, deny, "a Read of the file allows a Write of it: %s", reason)
}

// TestPreconditions_PlaceholderInTheMiddleOfAName: a token inside a name, not at
// a path boundary, round-trips through the path pass and Expand exactly, so a
// Read of the file allows a Write of it, for the implicit form and for an
// explicit require_read.
func TestPreconditions_PlaceholderInTheMiddleOfAName(t *testing.T) {
	_, repo, global := checkoutUnderHome(t)
	n := 0
	for _, name := range []string{"src/a-~b", "src/foo-~", "src/n-~1.c", "src/[redacted-x]/y"} {
		n++
		sess := "sess-mid-" + strconv.Itoa(n)
		auditRead(t, repo, global, sess, name)
		reason, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": name}, sess, repo, repo)
		require.NoError(t, err)
		assert.False(t, deny, "implicit %q: %s", name, reason)
		reason, deny, err = EvaluatePreconditions(redactedRequireRead(t, repo, name), "Write", map[string]any{"file_path": "x.go"}, sess, repo, repo)
		require.NoError(t, err)
		assert.False(t, deny, "explicit %q: %s", name, reason)
	}
}

// TestPreconditions_AmbiguousNameIsExplained: a name whose placeholder is at a
// path boundary (src/-~-proj/x, ~/x/-~-y) reads back as another file. A Write of
// it is denied, and when an explicit require_read names it, the deny reason
// counts the Reads that were ignored for that.
func TestPreconditions_AmbiguousNameIsExplained(t *testing.T) {
	_, repo, global := checkoutUnderHome(t)
	for i, name := range []string{"src/-~-proj/x", "~/x/-~-y"} {
		sess := "sess-amb-" + strconv.Itoa(i)
		auditRead(t, repo, global, sess, name)
		reason, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": name}, sess, repo, repo)
		require.NoError(t, err)
		assert.True(t, deny, "implicit %q", name)
		assert.Contains(t, reason, "redaction placeholder")

		reason, deny, err = EvaluatePreconditions(redactedRequireRead(t, repo, name), "Write", map[string]any{"file_path": "x.go"}, sess, repo, repo)
		require.NoError(t, err)
		assert.True(t, deny, "explicit %q", name)
		assert.Contains(t, reason, "(1 Read(s) were ignored: the file name contains a redaction placeholder)")
	}
}

// TestAmbiguousPath_FoldsCaseWhereThePathPassDoes: on a case-insensitive file
// system the path pass folds the case of a repo prefix, so a Read of
// /users/alice/work/a.go is stored as <repo>/a.go and read back as
// /Users/Alice/work/a.go: the same file, not an ambiguous name. Where the path
// pass is strict the lower-case spelling is another user's home.
func TestAmbiguousPath_FoldsCaseWhereThePathPassDoes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := mission.PathCaseFold
	t.Cleanup(func() { mission.PathCaseFold = old })

	mission.PathCaseFold = true
	amb, err := ambiguousPath("/users/alice/work/a.go", "/Users/Alice/work")
	require.NoError(t, err)
	assert.False(t, amb, "case folded: the same file")

	mission.PathCaseFold = false
	amb, err = ambiguousPath("/users/alice/work/a.go", "/Users/Alice/work")
	require.NoError(t, err)
	assert.True(t, amb, "strict: another user's home reads back as a different path")
}
