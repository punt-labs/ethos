package hook

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRedactAbsolutePaths covers the six cases the privacy defect
// (ethos-u7um) requires: HOME rewrite, repoRoot rewrite, mixed Bash
// command rewrite, nested-map recursion, hash portability across
// machines, and pass-through for a path under neither prefix.
func TestRedactAbsolutePaths(t *testing.T) {
	const (
		home = "/Users/jfreeman"
		repo = "/Users/jfreeman/Coding/punt-labs/ethos"
	)

	t.Run("home path becomes tilde", func(t *testing.T) {
		in := map[string]any{
			"file_path": "/Users/jfreeman/.claude/plugins/cache/x.json",
		}
		got := redactAbsolutePaths(in, home, "")
		assert.Equal(t, "~/.claude/plugins/cache/x.json", got["file_path"])
	})

	t.Run("repo path becomes <repo>", func(t *testing.T) {
		in := map[string]any{
			"file_path": "/Users/jfreeman/Coding/punt-labs/ethos/internal/hook/audit_entry.go",
		}
		got := redactAbsolutePaths(in, "", repo)
		assert.Equal(t, "<repo>/internal/hook/audit_entry.go", got["file_path"])
	})

	t.Run("repo wins over home when nested", func(t *testing.T) {
		in := map[string]any{
			"file_path": "/Users/jfreeman/Coding/punt-labs/ethos/internal/hook/audit_entry.go",
		}
		got := redactAbsolutePaths(in, home, repo)
		assert.Equal(t, "<repo>/internal/hook/audit_entry.go", got["file_path"])
	})

	t.Run("bash command rewrites both prefixes", func(t *testing.T) {
		in := map[string]any{
			"command": "cp /Users/jfreeman/.claude/config.json " +
				"/Users/jfreeman/Coding/punt-labs/ethos/.tmp/config.json",
		}
		got := redactAbsolutePaths(in, home, repo)
		assert.Equal(t,
			"cp ~/.claude/config.json <repo>/.tmp/config.json",
			got["command"])
	})

	t.Run("nested map recurses", func(t *testing.T) {
		in := map[string]any{
			"tool_name": "Agent",
			"tool_input": map[string]any{
				"prompt":    "see /Users/jfreeman/notes.md",
				"file_path": "/Users/jfreeman/Coding/punt-labs/ethos/main.go",
				"meta": map[string]any{
					"cwd": "/Users/jfreeman/Coding/punt-labs/ethos",
				},
			},
		}
		got := redactAbsolutePaths(in, home, repo)
		inner, ok := got["tool_input"].(map[string]any)
		if !ok {
			t.Fatal("nested map lost during redaction")
		}
		assert.Equal(t, "see ~/notes.md", inner["prompt"])
		assert.Equal(t, "<repo>/main.go", inner["file_path"])
		meta, ok := inner["meta"].(map[string]any)
		if !ok {
			t.Fatal("doubly-nested map lost during redaction")
		}
		assert.Equal(t, "<repo>", meta["cwd"])
	})

	t.Run("slice recurses", func(t *testing.T) {
		in := map[string]any{
			"args": []any{
				"/Users/jfreeman/a.txt",
				"/Users/jfreeman/Coding/punt-labs/ethos/b.txt",
				"keep me",
			},
		}
		got := redactAbsolutePaths(in, home, repo)
		args, ok := got["args"].([]any)
		if !ok {
			t.Fatal("slice lost during redaction")
		}
		assert.Equal(t, "~/a.txt", args[0])
		assert.Equal(t, "<repo>/b.txt", args[1])
		assert.Equal(t, "keep me", args[2])
	})

	t.Run("path under neither prefix passes through", func(t *testing.T) {
		in := map[string]any{
			"file_path": "/etc/hosts",
			"other":     "/opt/foo/bar",
		}
		got := redactAbsolutePaths(in, home, repo)
		assert.Equal(t, "/etc/hosts", got["file_path"])
		assert.Equal(t, "/opt/foo/bar", got["other"])
	})

	t.Run("nil input returns nil", func(t *testing.T) {
		assert.Nil(t, redactAbsolutePaths(nil, home, repo))
	})

	t.Run("empty prefixes disable substitution", func(t *testing.T) {
		in := map[string]any{"file_path": "/Users/jfreeman/x"}
		got := redactAbsolutePaths(in, "", "")
		assert.Equal(t, "/Users/jfreeman/x", got["file_path"])
	})

	t.Run("non-string scalars pass through", func(t *testing.T) {
		in := map[string]any{
			"timeout": float64(60),
			"force":   true,
			"count":   42,
		}
		got := redactAbsolutePaths(in, home, repo)
		assert.Equal(t, float64(60), got["timeout"])
		assert.Equal(t, true, got["force"])
		assert.Equal(t, 42, got["count"])
	})

	t.Run("does not mutate input", func(t *testing.T) {
		in := map[string]any{
			"file_path": "/Users/jfreeman/x.txt",
			"nested":    map[string]any{"p": "/Users/jfreeman/y.txt"},
		}
		_ = redactAbsolutePaths(in, home, repo)
		assert.Equal(t, "/Users/jfreeman/x.txt", in["file_path"])
		inner := in["nested"].(map[string]any)
		assert.Equal(t, "/Users/jfreeman/y.txt", inner["p"])
	})
}

// TestRedactAbsolutePaths_HashPortability is the cross-machine
// invariant: two callers running the same logical tool call from
// different HOME/repoRoot pairs produce identical tool_input_hash
// values after redaction. If this breaks, the hash becomes
// machine-specific and the collision detector (DES-052) stops working
// across operators.
func TestRedactAbsolutePaths_HashPortability(t *testing.T) {
	logical := func(home, repo string) map[string]any {
		return map[string]any{
			"tool_input": map[string]any{
				"file_path": repo + "/internal/hook/audit_entry.go",
				"command":   "cat " + home + "/.gitconfig",
				"meta": map[string]any{
					"cwd": repo,
				},
			},
		}
	}

	const (
		home1 = "/Users/jfreeman"
		repo1 = "/Users/jfreeman/Coding/punt-labs/ethos"
		home2 = "/home/alice"
		repo2 = "/home/alice/work/ethos"
	)

	a := logical(home1, repo1)
	a["tool_input"] = redactAbsolutePaths(
		a["tool_input"].(map[string]any), home1, repo1)

	b := logical(home2, repo2)
	b["tool_input"] = redactAbsolutePaths(
		b["tool_input"].(map[string]any), home2, repo2)

	ha := hashToolInput(a)
	hb := hashToolInput(b)
	assert.NotEmpty(t, ha, "hash must be non-empty after redaction")
	assert.Equal(t, ha, hb,
		"redacted hash must be identical across machines for the "+
			"same logical call — otherwise the collision detector "+
			"becomes machine-specific")
}
