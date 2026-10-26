package hook

import (
	"encoding/json"
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

// TestPathField_OnlyTheMidPathHomeRuleIsOff: a plain repo-relative path field
// skips only the rule that reads a mid-path /home/<name> as another user's
// home. This machine's own slug and a foreign slug are still redacted by the
// writer and by the seal guard on a raw live line, and app/home/page.tsx and
// app/Users/page.tsx are stored as written by both.
func TestPathField_OnlyTheMidPathHomeRuleIsOff(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	repo := t.TempDir()
	for _, p := range []string{
		"cache/-home-bob-project/x",
		".tmp/claude-1000/-home-zed-proj/s1/tasks/b1.output",
		"@-home-zed/pkg",
		"+-home-zed/pkg",
		"*/-home-zed/x",
		"./cache/-home-zed-x/y",
		"-/home/0",
		"x-/home/bob/y",
	} {
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", repo, time.Now())
		got, _ := e.ToolInput["file_path"].(string)
		assert.NotContains(t, got, "-home-zed", "writer: %q", p)
		assert.NotContains(t, got, "-home-bob", "writer: %q", p)
		out, rule := guardAuditLineWhy(rawReadLine(p))
		assert.NotEmpty(t, rule, "guard: %q", p)
		assert.NotContains(t, string(out), "-home-zed", "guard: %q", p)
		assert.NotContains(t, string(out), "-home-bob", "guard: %q", p)
		// The own slug is refused by the mission rule too (LeakRuleFor); a
		// foreign slug is not tested against it here.
		if strings.Contains(p, "-home-zed") {
			assert.NotEmpty(t, mission.LeakRuleFor(p, true), "mission: %q", p)
		}
	}
	for _, p := range []string{"app/home/page.tsx", "app/Users/page.tsx"} {
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", repo, time.Now())
		assert.Equal(t, p, e.ToolInput["file_path"], "writer")
		line, err := json.Marshal(e)
		require.NoError(t, err)
		_, rule := guardAuditLineWhy(line)
		assert.Empty(t, rule, "guard")
		assert.Empty(t, mission.LeakRuleFor(p, true), "mission")
	}
}

// TestAudit_WindowsProjectSlugIsRedacted: a project directory named by the
// dash encoding of a home (C--Users-al-Coding-repo, -var-home-bob-proj) holds a
// slug; a Read of a file under it is stored as [redacted-path] by the writer,
// and the seal guard flags a raw line.
func TestAudit_WindowsProjectSlugIsRedacted(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for _, p := range []string{
		".claude/projects/C--Users-al-Coding-repo/abc.jsonl",
		".claude/projects/-var-home-bob-proj/abc.jsonl",
		".claude/projects/-System-Volumes-Data-Users-bob-Coding-x/abc.jsonl",
		".claude/projects/-mnt-home-bob-proj/abc.jsonl",
		"D--data-home-bob-x/y",
	} {
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", t.TempDir(), time.Now())
		assert.Equal(t, "[redacted-path]", e.ToolInput["file_path"], p)
		out, rule := guardAuditLineWhy(rawReadLine(p))
		assert.NotEmpty(t, rule, p)
		assert.NotContains(t, string(out), "bob", p)
		assert.NotContains(t, string(out), "Users-al", p)
	}
}

// TestAudit_SlugsInPathFieldsAreRedactedByBothSinks: in the audit log a false
// positive costs only a file name, so a foreign slug in a path field is read
// broadly: after any non-alphanumeric byte, and anywhere in a component that
// starts with a dash. The writer and the seal guard agree. A mid-path
// /home/<name> is still not a home (app/home/page.tsx stays as written).
func TestAudit_SlugsInPathFieldsAreRedactedByBothSinks(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	for _, p := range []string{
		"x.-home-bob-x/f.jsonl", "backup--home-bob-x", "-var-home-bob/f.jsonl",
		"-System-Volumes-Data-Users-bob", "--server-share-Users-bob/f.jsonl", "C--Users-al-Coding-repo",
		// No closing dash, after a non-alphanumeric byte: the first alternative
		// of leakSlugAudit alone.
		"x.-home-bob/f.jsonl", "_-home-bob", "x/@-users-al", "backup--home-bob/f.jsonl",
	} {
		e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}, "s", t.TempDir(), time.Now())
		got, _ := e.ToolInput["file_path"].(string)
		assert.NotContains(t, got, "bob", "writer: %q", p)
		assert.NotContains(t, got, "Users-al", "writer: %q", p)
		out, rule := guardAuditLineWhy(rawReadLine(p))
		assert.NotEmpty(t, rule, "guard: %q", p)
		assert.NotContains(t, string(out), "bob", "guard: %q", p)
		assert.NotContains(t, string(out), "Users-al", "guard: %q", p)
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
