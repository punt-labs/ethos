package hook

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/audit"
	"github.com/punt-labs/ethos/v4/internal/mission"
)

// rawReadLine is a live audit line, as an older binary left it, for a Read of p.
func rawReadLine(p string) []byte {
	b, _ := json.Marshal(map[string]any{
		"ts": audit.FormatLineTS(100), "session": "s", "tool": "Read",
		"tool_input": map[string]any{"file_path": p},
	})
	return b
}

// TestPathField_InsideTheCheckoutOwnSlug: this machine's dash slug in a value
// inside the checkout is rewritten by the writer and by the seal guard on a raw
// live line, and the writer and the guard agree on every value. A mid-path
// /home/<name> (app/home/page.tsx) is a file.
func TestPathField_InsideTheCheckoutOwnSlug(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	repo := t.TempDir()
	for _, p := range []string{
		".tmp/claude-1000/-home-zed-proj/s1/tasks/b1.output",
		"@-home-zed/pkg",
		"+-home-zed/pkg",
		"*/-home-zed/x",
		"./cache/-home-zed-x/y",
	} {
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", repo, time.Now())
		got, _ := e.ToolInput["file_path"].(string)
		assert.NotContains(t, got, "-home-zed", "writer: %q", p)
		out, rule := guardAuditLineWhy(rawReadLine(p))
		assert.NotEmpty(t, rule, "guard: %q", p)
		assert.NotContains(t, string(out), "-home-zed", "guard: %q", p)
		assert.NotEmpty(t, mission.LeakRuleFor(p, true), "mission: %q", p)
	}
	for _, p := range []string{"app/home/page.tsx", "app/Users/page.tsx", "cache/-home-bob-project/x"} {
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", repo, time.Now())
		line, err := json.Marshal(e)
		require.NoError(t, err)
		_, rule := guardAuditLineWhy(line)
		assert.Empty(t, rule, "the guard agrees with the writer: %q", p)
		if strings.HasPrefix(p, "app/") {
			assert.Equal(t, p, e.ToolInput["file_path"], "writer")
		}
	}
}

// TestPathField_GluedHomeIsRedactedByBothSinks: a value that holds an absolute
// home glued to a flag or a name is not a file of the checkout, whatever its
// shape: the writer redacts it and the seal guard flags a raw line.
func TestPathField_GluedHomeIsRedactedByBothSinks(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for _, p := range []string{"-I/home/bob/inc", "x-/home/bob/y", "-/home/0", "x@/home/bob/y", "@/home/bob/args", "*/home/bob/y", "+x/home/bob/f"} {
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", t.TempDir(), time.Now())
		got, _ := e.ToolInput["file_path"].(string)
		assert.NotContains(t, got, "bob", "writer: %q", p)
		out, rule := guardAuditLineWhy(rawReadLine(p))
		assert.NotEmpty(t, rule, "guard: %q", p)
		assert.NotContains(t, string(out), "bob", "guard: %q", p)
	}
	for _, p := range []string{"src/styles/nav--home-page.scss", "@types/home/page.tsx"} {
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", t.TempDir(), time.Now())
		assert.Equal(t, p, e.ToolInput["file_path"], "writer: %q", p)
		_, rule := guardAuditLineWhy(rawReadLine(p))
		assert.Empty(t, rule, "guard: %q", p)
	}
}

// TestAudit_WindowsProjectSlugIsRedacted: a project directory named by the
// dash encoding of a home (C--Users-al-Coding-repo, -var-home-bob-proj) holds a
// slug; a Read of a file under it, outside the checkout, is stored as
// [redacted-path] by the writer, and the seal guard flags a raw line.
func TestAudit_WindowsProjectSlugIsRedacted(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for _, p := range []string{
		"/home/zed/.claude/projects/C--Users-al-Coding-repo/abc.jsonl",
		"/home/zed/.claude/projects/-var-home-bob-proj/abc.jsonl",
		"/home/zed/.claude/projects/-System-Volumes-Data-Users-bob-Coding-x/abc.jsonl",
		"/home/zed/.claude/projects/-mnt-home-bob-proj/abc.jsonl",
		"/data/D--data-home-bob-x/y",
	} {
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", t.TempDir(), time.Now())
		assert.Equal(t, "[redacted-path]", e.ToolInput["file_path"], p)
		raw := strings.Replace(p, "/home/zed", "~", 1)
		out, rule := guardAuditLineWhy(rawReadLine(raw))
		assert.NotEmpty(t, rule, p)
		assert.NotContains(t, string(out), "bob", p)
		assert.NotContains(t, string(out), "Users-al", p)
	}
}

// TestAudit_PathFieldsFollowTheFullRule: an audit path field meets redact's
// full LeakRule, as every other string does. The one exemption is for a value
// that names a file of the checkout (repo-relative, <repo>/..., or ~/ and a
// repo-relative path) with no home glued into it: the mid-path /home/<name> and
// /Users/<name> rule is skipped there, because app/home/page.tsx is a file. The
// writer and the seal guard agree on every row.
func TestAudit_PathFieldsFollowTheFullRule(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	repo := t.TempDir()
	tests := []struct {
		p        string
		redacted bool
	}{
		{"x.-home-bob-x/f", true},
		{"backup--home-bob-x/f", true},
		{"1--home-bob-x/f", true},
		{"ab-C--Users-bob-x/f", true},
		{"C--Users-bob-x", true},
		{"-var-home-bob-x/f", true},
		{".claude/projects/C--Users-al-Coding-repo/abc.jsonl", true},
		{"~/.claude/projects/C--Users-al-Coding-repo/abc.jsonl", true},
		{"/srv/-var-home-bob-x/f.jsonl", true},
		{"-I/home/bob/inc", true},
		{"x-/home/bob/y", true},
		{"/srv/home/bob/f", true},
		{"<repo>/~/x/home/bob", true},
		{"nav--home-page.scss", false},
		{"src/styles/btn--users-list.css", false},
		{"a/b--home-page.scss", false},
		{"_-home-hero.scss", false},
		{"@types/home/page.tsx", false},
		{"app/home/page.tsx", false},
		{"x/home/bob/f", false},
		{"~/x-wt/app/home/page.tsx", false},
	}
	for _, tt := range tests {
		in := tt.p
		if strings.HasPrefix(in, "~/") {
			in = "/home/zed" + in[1:]
		}
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": in}}, "s", repo, time.Now())
		got, _ := e.ToolInput["file_path"].(string)
		out, rule := guardAuditLineWhy(rawReadLine(tt.p))
		if tt.redacted {
			assert.NotEqual(t, tt.p, got, "writer: %q", tt.p)
			assert.NotContains(t, got, "bob", "writer: %q", tt.p)
			assert.NotContains(t, got, "Users-al", "writer: %q", tt.p)
			assert.NotEmpty(t, rule, "guard: %q", tt.p)
			assert.NotContains(t, string(out), "bob", "guard: %q", tt.p)
			assert.NotContains(t, string(out), "Users-al", "guard: %q", tt.p)
			continue
		}
		assert.Equal(t, tt.p, got, "writer: %q", tt.p)
		assert.Empty(t, rule, "guard: %q", tt.p)
	}
}

// TestAudit_FilesReadThenWriteInsideAndInASiblingWorktree: names that look a
// little like a slug or a home (nav--home-page.scss, a/b--home-page.scss,
// _-home-hero.scss, app/home/page.tsx) are files: a Read of one then a Write of
// it is allowed in the checkout and in a sibling worktree under ~.
func TestAudit_FilesReadThenWriteInsideAndInASiblingWorktree(t *testing.T) {
	home, repo, global := checkoutUnderHome(t)
	n := 0
	for _, rel := range []string{
		"src/styles/nav--home-page.scss", "src/styles/btn--users-list.css", "a/b--home-page.scss",
		"_-home-hero.scss", "@types/home/page.tsx", "app/home/page.tsx",
	} {
		for place, abs := range map[string]string{
			"inside":  filepath.Join(repo, rel),
			"sibling": filepath.Join(home, "x-wt", rel),
		} {
			n++
			sess := "sess-" + strconv.Itoa(n)
			auditRead(t, repo, global, sess, abs)
			e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": abs}}, sess, repo, time.Now())
			got, _ := e.ToolInput["file_path"].(string)
			assert.True(t, strings.HasSuffix(got, rel), "%s %s: stored as written: %s", place, rel, got)
			line, err := json.Marshal(e)
			require.NoError(t, err)
			_, rule := guardAuditLineWhy(line)
			assert.Empty(t, rule, "%s %s: the guard agrees", place, rel)
			reason, deny, err := EvaluatePreconditions(implicitContract(), "Write", map[string]any{"file_path": abs}, sess, repo, repo)
			require.NoError(t, err)
			assert.False(t, deny, "%s %s: the Read satisfies it: %s", place, rel, reason)
		}
	}
}

// TestAudit_PlainPathFieldKeepsAMidPathHome: the exemption the comment above
// claims: app/home/page.tsx is stored as written by the writer and the guard.
func TestAudit_PlainPathFieldKeepsAMidPathHome(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	const p = "app/home/page.tsx"
	e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", t.TempDir(), time.Now())
	assert.Equal(t, p, e.ToolInput["file_path"])
	line, err := json.Marshal(e)
	require.NoError(t, err)
	_, rule := guardAuditLineWhy(line)
	assert.Empty(t, rule)
}
