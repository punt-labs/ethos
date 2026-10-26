package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
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
