package hook

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedactAbsolutePaths covers the six cases the privacy defect
// (ethos-u7um) requires: HOME rewrite, repoRoot rewrite, mixed Bash
// command rewrite, nested-map recursion, hash portability across
// machines, and pass-through for a path under neither prefix.
func TestRedactAbsolutePaths(t *testing.T) {
	const (
		home = "/Users/user"
		repo = "/Users/user/Coding/punt-labs/ethos"
	)

	t.Run("home path becomes tilde", func(t *testing.T) {
		in := map[string]any{
			"file_path": "/Users/user/.claude/plugins/cache/x.json",
		}
		got := redactAbsolutePaths(in, home, "")
		assert.Equal(t, "~/.claude/plugins/cache/x.json", got["file_path"])
	})

	t.Run("repo path becomes <repo>", func(t *testing.T) {
		in := map[string]any{
			"file_path": "/Users/user/Coding/punt-labs/ethos/internal/hook/audit_entry.go",
		}
		got := redactAbsolutePaths(in, "", repo)
		assert.Equal(t, "<repo>/internal/hook/audit_entry.go", got["file_path"])
	})

	t.Run("repo wins over home when nested", func(t *testing.T) {
		in := map[string]any{
			"file_path": "/Users/user/Coding/punt-labs/ethos/internal/hook/audit_entry.go",
		}
		got := redactAbsolutePaths(in, home, repo)
		assert.Equal(t, "<repo>/internal/hook/audit_entry.go", got["file_path"])
	})

	t.Run("bash command rewrites both prefixes", func(t *testing.T) {
		in := map[string]any{
			"command": "cp /Users/user/.claude/config.json " +
				"/Users/user/Coding/punt-labs/ethos/.tmp/config.json",
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
				"prompt":    "see /Users/user/notes.md",
				"file_path": "/Users/user/Coding/punt-labs/ethos/main.go",
				"meta": map[string]any{
					"cwd": "/Users/user/Coding/punt-labs/ethos",
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
				"/Users/user/a.txt",
				"/Users/user/Coding/punt-labs/ethos/b.txt",
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
		in := map[string]any{"file_path": "/Users/user/x"}
		got := redactAbsolutePaths(in, "", "")
		assert.Equal(t, "/Users/user/x", got["file_path"])
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
			"file_path": "/Users/user/x.txt",
			"nested":    map[string]any{"p": "/Users/user/y.txt"},
		}
		_ = redactAbsolutePaths(in, home, repo)
		assert.Equal(t, "/Users/user/x.txt", in["file_path"])
		inner := in["nested"].(map[string]any)
		assert.Equal(t, "/Users/user/y.txt", inner["p"])
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
		home1 = "/Users/user"
		repo1 = "/Users/user/Coding/punt-labs/ethos"
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

// TestRedactToolInput_ForeignHomePaths: a path pasted from another
// machine names a person as surely as this one does.
func TestRedactToolInput_ForeignHomePaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	in := map[string]any{"tool_input": map[string]any{
		"command": "cat /home/alice/notes.txt",
		"paths":   []any{"/Users/bob/x", "ok"},
	}}
	got, _ := redactToolInput(in, "Bash", t.TempDir())
	assert.Equal(t, map[string]any{
		"command": "cat ~[redacted-user]/notes.txt",
		"paths":   []any{"~[redacted-user]/x", "ok"},
	}, got)
}

// TestRedactToolInput_PathKeysAndLeaks: path-valued keys get the path pass,
// including after a run of slashes or a scheme, and any scalar that still
// names a home afterwards is replaced, never written. The audit writer
// cannot block a tool call, so it replaces rather than refuses.
func TestRedactToolInput_PathKeysAndLeaks(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	tests := []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{"scheme", map[string]any{"file_path": "file:///home/zed/x"}, map[string]any{"file_path": "file://~/x"}},
		{"run of slashes, foreign", map[string]any{"file_path": "///home/bob/.ssh/id_ed25519"}, map[string]any{"file_path": "//~[redacted-user]/.ssh/id_ed25519"}},
		{"own home before a comma", map[string]any{"file_path": "/home/zed,bak"}, map[string]any{"file_path": "~,bak"}},
		{"glued to a flag", map[string]any{"file_path": "-I/home/bob/inc"}, map[string]any{"file_path": "[redacted-path]"}},
		{"glued to a dash", map[string]any{"file_path": "x-/home/bob/y"}, map[string]any{"file_path": "[redacted-path]"}},
		{"dash and a numeric name", map[string]any{"file_path": "-/home/0"}, map[string]any{"file_path": "[redacted-path]"}},
		{"slug inside a name, in prose", map[string]any{"command": "ls %2Fhome%2Fbob%2Fx"}, map[string]any{"command": "[redacted-path]"}},
		{"list", map[string]any{"paths": []any{"/home/zed/a", "-I/home/bob/b"}}, map[string]any{"paths": []any{"~/a", "[redacted-path]"}}},
		{"nested", map[string]any{"edits": []any{map[string]any{"old": "%2Fhome%2Fbob%2Fy"}}}, map[string]any{"edits": []any{map[string]any{"old": "[redacted-path]"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, removed := redactToolInput(map[string]any{"tool_input": tt.in}, "Read", t.TempDir())
			assert.Equal(t, tt.want, got)
			if tt.name == "glued to a flag" || tt.name == "glued to a dash" || tt.name == "dash and a numeric name" || tt.name == "slug inside a name, in prose" || tt.name == "list" || tt.name == "nested" {
				assert.True(t, removed, "a replaced scalar is content removed")
			}
		})
	}
}

// TestBuildAuditEntry_CarriesThePathFormat: a line written by this format
// says so, so a reader knows ~ in it is this machine's home.
func TestBuildAuditEntry_CarriesThePathFormat(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	e := buildAuditEntry(map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": "x"}}, "s", t.TempDir(), time.Now())
	assert.Equal(t, auditPathFormat, e.PathFormat)
	data, err := json.Marshal(e)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"path_format":`)
}

// TestRedactToolInput_MapKeys: a key names a home as a value does. The path
// pass runs on it, and one that still leaks is renamed, never written.
func TestRedactToolInput_MapKeys(t *testing.T) {
	t.Setenv("HOME", "/home/zed")
	in := map[string]any{"env": map[string]any{
		"/home/zed/a": "1", "%2Fhome%2Fbob%2Finc": "2", "%2Fhome%2Fbob%2Flib": "3", "ok": "4",
	}}
	got, removed := redactToolInput(map[string]any{"tool_input": in}, "Bash", t.TempDir())
	assert.True(t, removed)
	env, ok := got["env"].(map[string]any)
	require.True(t, ok)
	assert.Len(t, env, 4)
	assert.Equal(t, "1", env["~/a"])
	assert.Equal(t, "4", env["ok"])
	assert.Contains(t, env, redactedKey)
	assert.Contains(t, env, redactedKey+"#2")
}
