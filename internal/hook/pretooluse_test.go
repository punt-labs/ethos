package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/punt-labs/ethos/v4/internal/mission"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandlePreToolUse_NoAllowlist(t *testing.T) {
	// When ETHOS_VERIFIER_ALLOWLIST is unset, all tools pass through.
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")

	payload := `{"tool_name":"Read","tool_input":{"file_path":"/anywhere/at/all.go"}}`
	var out bytes.Buffer
	err := HandlePreToolUse(strings.NewReader(payload), &out)
	require.NoError(t, err)

	var result PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "allow", result.HookSpecificOutput.PermissionDecision)
	assert.Empty(t, result.HookSpecificOutput.PermissionDecisionReason)
}

// TestHandlePreToolUse_DenyInRepoMCPWrite pins DES-069: verifier
// spawns deny the two in-repo MCP write families outright, before
// any allowlist path check, and the deny survives both plugin-prefix
// variants (released and -dev). A worker spawn (no
// ETHOS_VERIFIER_ALLOWLIST) must never see the deny.
func TestHandlePreToolUse_DenyInRepoMCPWrite(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		toolInput  map[string]any
		verifier   bool
		wantDecide string
	}{
		{
			name:       "verifier: identity create denied, released prefix",
			toolName:   "mcp__plugin_ethos_self__identity",
			toolInput:  map[string]any{"method": "create"},
			verifier:   true,
			wantDecide: "deny",
		},
		{
			name:       "verifier: identity create denied, dev prefix",
			toolName:   "mcp__plugin_ethos-dev_self__identity",
			toolInput:  map[string]any{"method": "create"},
			verifier:   true,
			wantDecide: "deny",
		},
		{
			name:       "verifier: identity whoami allowed (not create)",
			toolName:   "mcp__plugin_ethos_self__identity",
			toolInput:  map[string]any{"method": "whoami"},
			verifier:   true,
			wantDecide: "allow",
		},
		{
			name:       "verifier: zspec check denied, released prefix",
			toolName:   "mcp__plugin_z-spec_zspec__check",
			toolInput:  map[string]any{},
			verifier:   true,
			wantDecide: "deny",
		},
		{
			name:       "verifier: zspec model_check denied, dev prefix",
			toolName:   "mcp__plugin_z-spec-dev_zspec__model_check",
			toolInput:  map[string]any{},
			verifier:   true,
			wantDecide: "deny",
		},
		{
			name:       "verifier: zspec test denied",
			toolName:   "mcp__plugin_z-spec_zspec__test",
			toolInput:  map[string]any{},
			verifier:   true,
			wantDecide: "deny",
		},
		{
			name:       "verifier: zspec animate denied",
			toolName:   "mcp__plugin_z-spec_zspec__animate",
			toolInput:  map[string]any{},
			verifier:   true,
			wantDecide: "deny",
		},
		{
			name:       "verifier: zspec browse allowed (read-only)",
			toolName:   "mcp__plugin_z-spec_zspec__browse",
			toolInput:  map[string]any{},
			verifier:   true,
			wantDecide: "allow",
		},
		{
			name:       "verifier: unclassified direct-server tool (mcp__github__create_or_update_file) denied fail-closed",
			toolName:   "mcp__github__create_or_update_file",
			toolInput:  map[string]any{},
			verifier:   true,
			wantDecide: "deny",
		},
		{
			name:       "verifier: quarry__find (read-only) allowed",
			toolName:   "mcp__plugin_quarry_quarry__find",
			toolInput:  map[string]any{},
			verifier:   true,
			wantDecide: "allow",
		},
		{
			name:       "worker: identity create passes through unaffected",
			toolName:   "mcp__plugin_ethos_self__identity",
			toolInput:  map[string]any{"method": "create"},
			verifier:   false,
			wantDecide: "allow",
		},
		{
			name:       "worker: zspec check passes through unaffected",
			toolName:   "mcp__plugin_z-spec_zspec__check",
			toolInput:  map[string]any{},
			verifier:   false,
			wantDecide: "allow",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.verifier {
				t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "internal/hook")
			} else {
				t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
			}

			payload := map[string]any{
				"tool_name":  tt.toolName,
				"tool_input": tt.toolInput,
			}
			data, err := json.Marshal(payload)
			require.NoError(t, err)

			var out bytes.Buffer
			require.NoError(t, HandlePreToolUse(strings.NewReader(string(data)), &out))
			var r PreToolUseResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &r))
			assert.Equal(t, tt.wantDecide, r.HookSpecificOutput.PermissionDecision)
			if tt.wantDecide == "deny" {
				assert.Contains(t, r.HookSpecificOutput.PermissionDecisionReason, tt.toolName)
				assert.NotContains(t, r.HookSpecificOutput.PermissionDecisionReason, "/")
			}
		})
	}
}

// TestHandlePreToolUse_NoAllowlistWithExtractInto pins the worker
// passthrough invariant: ETHOS_VERIFIER_ALLOWLIST gates the hook
// firing at all, so a worker spawn that somehow has
// ETHOS_VERIFIER_EXTRACT_INTO set in its environment (a mis-set
// inherited variable, a test leak) must still pass every tool call
// through. Workers are unconstrained by design — only verifier
// spawns set the allowlist.
func TestHandlePreToolUse_NoAllowlistWithExtractInto(t *testing.T) {
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("ETHOS_VERIFIER_EXTRACT_INTO", "internal/foo/:docs/")

	tests := []struct {
		name string
		tool string
		path string
	}{
		{"Write outside any directory", "Write", "/etc/passwd"},
		{"Edit outside any directory", "Edit", "/tmp/anywhere.go"},
		{"Write inside an extract_into dir", "Write", "internal/foo/new.go"},
		{"Read anywhere", "Read", "/anywhere"},
		{"Bash anywhere", "Bash", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toolInput := map[string]any{}
			if tt.path != "" {
				toolInput["file_path"] = tt.path
			}
			payload := map[string]any{
				"tool_name":  tt.tool,
				"tool_input": toolInput,
			}
			data, err := json.Marshal(payload)
			require.NoError(t, err)

			var out bytes.Buffer
			require.NoError(t, HandlePreToolUse(strings.NewReader(string(data)), &out))
			var r PreToolUseResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &r))
			assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision,
				"worker spawn (no ALLOWLIST) must pass through regardless of EXTRACT_INTO")
		})
	}
}

func TestHandlePreToolUse_AllowAndBlock(t *testing.T) {
	allowlist := "internal/hook/pretooluse.go:internal/hook/pretooluse_test.go:cmd/ethos/hook.go:/home/user/.punt-labs/ethos/missions/m-001.yaml"

	tests := []struct {
		name     string
		tool     string
		input    map[string]any
		decision string
	}{
		{
			name:     "Read file in allowlist (exact)",
			tool:     "Read",
			input:    map[string]any{"file_path": "internal/hook/pretooluse.go"},
			decision: "allow",
		},
		{
			name:     "Read absolute contract file",
			tool:     "Read",
			input:    map[string]any{"file_path": "/home/user/.punt-labs/ethos/missions/m-001.yaml"},
			decision: "allow",
		},
		{
			name:     "Write file in allowlist",
			tool:     "Write",
			input:    map[string]any{"file_path": "cmd/ethos/hook.go"},
			decision: "allow",
		},
		{
			name:     "Edit file in allowlist",
			tool:     "Edit",
			input:    map[string]any{"file_path": "internal/hook/pretooluse_test.go"},
			decision: "allow",
		},
		{
			name:     "Read file outside allowlist is allowed (read-only)",
			tool:     "Read",
			input:    map[string]any{"file_path": "internal/session/store.go"},
			decision: "allow",
		},
		{
			name:     "Write file outside allowlist",
			tool:     "Write",
			input:    map[string]any{"file_path": "/etc/passwd"},
			decision: "deny",
		},
		{
			name:     "Edit file outside allowlist",
			tool:     "Edit",
			input:    map[string]any{"file_path": "go.mod"},
			decision: "deny",
		},
		{
			name:     "Bash tool is always allowed",
			tool:     "Bash",
			input:    map[string]any{"command": "cat /etc/passwd"},
			decision: "allow",
		},
		{
			name:     "unknown tool is allowed",
			tool:     "SomeNewTool",
			input:    map[string]any{"anything": "goes"},
			decision: "allow",
		},
		{
			name:     "Read always allowed regardless of path",
			tool:     "Read",
			input:    map[string]any{"file_path": "/etc/shadow"},
			decision: "allow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ETHOS_VERIFIER_ALLOWLIST", allowlist)

			payload := map[string]any{
				"tool_name":  tt.tool,
				"tool_input": tt.input,
			}
			data, err := json.Marshal(payload)
			require.NoError(t, err)

			var out bytes.Buffer
			err = HandlePreToolUse(strings.NewReader(string(data)), &out)
			require.NoError(t, err)

			var result PreToolUseResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &result))
			assert.Equal(t, tt.decision, result.HookSpecificOutput.PermissionDecision, "tool=%s path=%v", tt.tool, tt.input)
			if tt.decision == "deny" {
				assert.NotEmpty(t, result.HookSpecificOutput.PermissionDecisionReason)
			}
		})
	}
}

func TestHandlePreToolUse_DirectoryEntryAllowsChildren(t *testing.T) {
	// A directory entry in the allowlist permits Write/Edit to any file under it.
	// Read is unrestricted and does not check the allowlist.
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "internal/hook/:cmd/ethos/")

	tests := []struct {
		name     string
		path     string
		decision string
	}{
		{"write file under allowed dir", "internal/hook/pretooluse.go", "allow"},
		{"write nested file under allowed dir", "internal/hook/deep/nested.go", "allow"},
		{"write dir entry itself", "internal/hook", "allow"},
		{"write sibling dir blocked", "internal/mission/store.go", "deny"},
		{"write partial prefix not matched", "internal/hookextra/file.go", "deny"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]any{
				"tool_name":  "Write",
				"tool_input": map[string]any{"file_path": tt.path},
			}
			data, err := json.Marshal(payload)
			require.NoError(t, err)

			var out bytes.Buffer
			err = HandlePreToolUse(strings.NewReader(string(data)), &out)
			require.NoError(t, err)

			var result PreToolUseResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &result))
			assert.Equal(t, tt.decision, result.HookSpecificOutput.PermissionDecision, "path=%s", tt.path)
		})
	}
}

func TestHandlePreToolUse_GlobAndGrep(t *testing.T) {
	// Glob and Grep are unrestricted — verifiers need full read access.
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "internal/hook/")

	tests := []struct {
		name     string
		tool     string
		path     string
		decision string
	}{
		{"Glob inside allowlist", "Glob", "internal/hook", "allow"},
		{"Glob outside allowlist", "Glob", "internal/mission", "allow"},
		{"Grep inside allowlist", "Grep", "internal/hook", "allow"},
		{"Grep outside allowlist", "Grep", "/some/other/path", "allow"},
		{"Grep with no path (cwd)", "Grep", "", "allow"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := map[string]any{"pattern": ".*"}
			if tt.path != "" {
				input["path"] = tt.path
			}
			payload := map[string]any{
				"tool_name":  tt.tool,
				"tool_input": input,
			}
			data, err := json.Marshal(payload)
			require.NoError(t, err)

			var out bytes.Buffer
			err = HandlePreToolUse(strings.NewReader(string(data)), &out)
			require.NoError(t, err)

			var result PreToolUseResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &result))
			assert.Equal(t, tt.decision, result.HookSpecificOutput.PermissionDecision, "tool=%s path=%s", tt.tool, tt.path)
		})
	}
}

func TestExtractTargetPath(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input map[string]any
		want  string
	}{
		{"Read unrestricted", "Read", map[string]any{"file_path": "/a/b.go"}, ""},
		{"Write", "Write", map[string]any{"file_path": "x.go"}, "x.go"},
		{"Edit", "Edit", map[string]any{"file_path": "y.go"}, "y.go"},
		{"Glob unrestricted", "Glob", map[string]any{"path": "/some/dir"}, ""},
		{"Grep unrestricted", "Grep", map[string]any{"path": "src/"}, ""},
		{"Bash", "Bash", map[string]any{"command": "ls"}, ""},
		{"nil input", "Write", nil, ""},
		{"missing key", "Write", map[string]any{"other": "val"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTargetPath(tt.tool, tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSplitAllowlist(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", []string{}},
		{"single", "a.go", []string{"a.go"}},
		{"multiple", "a.go:b.go:c/", []string{"a.go", "b.go", "c/"}},
		{"trailing colon", "a.go:", []string{"a.go"}},
		{"leading colon", ":a.go", []string{"a.go"}},
		{"double colon", "a.go::b.go", []string{"a.go", "b.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitAllowlist(tt.raw)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPathAllowed(t *testing.T) {
	entries := []string{"internal/hook/pretooluse.go", "cmd/ethos/", "/abs/contract.yaml"}

	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{"exact file match", "internal/hook/pretooluse.go", true},
		{"under directory", "cmd/ethos/hook.go", true},
		{"exact directory", "cmd/ethos", true},
		{"absolute match", "/abs/contract.yaml", true},
		{"outside all entries", "internal/mission/store.go", false},
		{"partial prefix no sep", "cmd/ethosX/hook.go", false},
		{"clean trailing slash", "internal/hook/pretooluse.go/", true},
		{"dot-slash normalized", "./internal/hook/pretooluse.go", true},
		{"dot-slash dir child", "./cmd/ethos/main.go", true},
		{"traversal escapes allowlist", "internal/hook/../../secret.go", false},
		{"traversal into sibling", "cmd/ethos/../../internal/mission/store.go", false},
		{"traversal that stays inside", "cmd/ethos/sub/../hook.go", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pathAllowed(tt.target, entries))
		})
	}
}

// TestPathAllowed_GlobEntry asserts ethos-qy7k on the enforcement
// side: the allowlist is built from the write_set, so an entry that
// declares a glob must admit the paths the glob names. Compared
// literally, `docs/**` authorized nothing at all — the verifier was
// refused every write the contract had allowed.
func TestPathAllowed_GlobEntry(t *testing.T) {
	entries := []string{"docs/**", "internal/mission/*.go"}

	tests := []struct {
		name   string
		target string
		want   bool
	}{
		{"doublestar one level down", "docs/audited-delegation.md", true},
		{"doublestar many levels down", "docs/design/adr/des-054.md", true},
		{"single star inside one segment", "internal/mission/store.go", true},
		{"single star does not span a separator", "internal/mission/sub/store.go", false},
		{"outside every entry", "cmd/ethos/hook.go", false},
		{"sibling of the glob root", "docsite/index.md", false},
		{"traversal out of a glob entry", "docs/../secret.md", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pathAllowed(tt.target, entries))
		})
	}
}

// TestPathAllowed_GlobDoesNotAdmitTraversal pins the escape Copilot
// found on PR #415: a `**` segment matched `..` like any other
// segment, so a leading-doublestar entry — legal, since it names a
// real file — authorized a verifier to write OUTSIDE the repo. The
// literal matcher this replaced could not express that, so the guard
// arrives with the glob.
func TestPathAllowed_GlobDoesNotAdmitTraversal(t *testing.T) {
	entries := []string{"**/notes.go", "**", "docs/**", "*.go"}

	tests := []struct {
		name   string
		target string
	}{
		{"leading traversal under a doublestar entry", "../notes.go"},
		{"double traversal", "../../etc/passwd"},
		{"traversal that resolves above the repo", "internal/hook/../../../escape.go"},
		{"traversal reaching a name a glob entry would otherwise match", "../../notes.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, pathAllowed(tt.target, entries),
				"%q must not be admitted by any glob entry", tt.target)
		})
	}

	// The same entries still admit what they legitimately name.
	assert.True(t, pathAllowed("internal/hook/notes.go", entries))
	assert.True(t, pathAllowed("docs/design/adr.md", entries))
}

// TestHandlePreToolUse_EnvVarFromSubagentStart verifies end-to-end
// that the env var format produced by buildVerifierAllowlistEnv is
// correctly consumed by HandlePreToolUse.
func TestHandlePreToolUse_EnvVarFromSubagentStart(t *testing.T) {
	// Simulate the env var that SubagentStart would set.
	allowlist := "internal/hook/pretooluse.go:internal/hook/pretooluse_test.go:/home/user/missions/m-001.yaml"
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", allowlist)

	// Allowed: Write to a file in the allowlist.
	payload := `{"tool_name":"Write","tool_input":{"file_path":"internal/hook/pretooluse.go"}}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)

	// Blocked: Write to a file not in the allowlist.
	out.Reset()
	payload = `{"tool_name":"Write","tool_input":{"file_path":"internal/session/store.go"}}`
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision)
	assert.Contains(t, r.HookSpecificOutput.PermissionDecisionReason, "outside the verifier file allowlist")

	// Read is always allowed, even outside the allowlist.
	out.Reset()
	payload = `{"tool_name":"Read","tool_input":{"file_path":"internal/session/store.go"}}`
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
}

// TestHandlePreToolUse_EmptyInput gracefully handles empty or
// missing stdin — should default to allow (passthrough).
func TestHandlePreToolUse_EmptyInput(t *testing.T) {
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "some/path")

	var out bytes.Buffer
	err := HandlePreToolUse(strings.NewReader(""), &out)
	require.NoError(t, err)

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	// Empty input means no tool info — allow passthrough.
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
}

// TestHandlePreToolUse_ExtractInto covers the DES-052 stat-then-allow
// branch. Four cases pin the contract:
//
//  1. ETHOS_VERIFIER_ALLOWLIST unset -> allow (passthrough).
//  2. Existing file under write_set -> allow via the allowlist match;
//     extract_into is not consulted.
//  3. Non-existing file under an extract_into directory -> allow via
//     the stat-then-allow branch.
//  4. Existing file under extract_into but NOT under write_set ->
//     block. This is the modify-via-extract_into attack the field is
//     designed to prevent.
func TestHandlePreToolUse_ExtractInto(t *testing.T) {
	dir := t.TempDir()
	existing := dir + "/existing.go"
	require.NoError(t, os.WriteFile(existing, []byte("package x"), 0o600))
	missing := dir + "/missing.go"

	t.Run("env unset -> allow", func(t *testing.T) {
		t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
		t.Setenv("ETHOS_VERIFIER_EXTRACT_INTO", "")
		payload := map[string]any{
			"tool_name":  "Write",
			"tool_input": map[string]any{"file_path": missing},
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, HandlePreToolUse(strings.NewReader(string(data)), &out))
		var r PreToolUseResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &r))
		assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
	})

	t.Run("existing file under write_set -> allow", func(t *testing.T) {
		// write_set allowlist contains the existing file; extract_into is
		// not even consulted because the allowlist match short-circuits.
		t.Setenv("ETHOS_VERIFIER_ALLOWLIST", existing)
		t.Setenv("ETHOS_VERIFIER_EXTRACT_INTO", dir+"/other-dir")
		payload := map[string]any{
			"tool_name":  "Write",
			"tool_input": map[string]any{"file_path": existing},
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, HandlePreToolUse(strings.NewReader(string(data)), &out))
		var r PreToolUseResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &r))
		assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
	})

	t.Run("non-existing file under extract_into -> allow", func(t *testing.T) {
		// write_set allowlist names a different file so the allowlist
		// check fails and the stat-then-allow branch fires.
		t.Setenv("ETHOS_VERIFIER_ALLOWLIST", dir+"/declared.go")
		t.Setenv("ETHOS_VERIFIER_EXTRACT_INTO", dir)
		payload := map[string]any{
			"tool_name":  "Write",
			"tool_input": map[string]any{"file_path": missing},
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, HandlePreToolUse(strings.NewReader(string(data)), &out))
		var r PreToolUseResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &r))
		assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
	})

	t.Run("existing file under extract_into but not write_set -> block", func(t *testing.T) {
		// The modify-via-extract_into attack: extract_into authorizes
		// creation under dir, but the file already exists. PreToolUse
		// must block — modification requires a write_set match.
		t.Setenv("ETHOS_VERIFIER_ALLOWLIST", dir+"/declared.go")
		t.Setenv("ETHOS_VERIFIER_EXTRACT_INTO", dir)
		payload := map[string]any{
			"tool_name":  "Write",
			"tool_input": map[string]any{"file_path": existing},
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, HandlePreToolUse(strings.NewReader(string(data)), &out))
		var r PreToolUseResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &r))
		assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision,
			"existing file under extract_into must require write_set match")
		assert.Contains(t, r.HookSpecificOutput.PermissionDecisionReason, "outside the verifier file allowlist")
	})

	t.Run("Edit existing under extract_into -> block", func(t *testing.T) {
		// Edit is treated symmetrically with Write for the
		// stat-then-allow branch.
		t.Setenv("ETHOS_VERIFIER_ALLOWLIST", dir+"/declared.go")
		t.Setenv("ETHOS_VERIFIER_EXTRACT_INTO", dir)
		payload := map[string]any{
			"tool_name":  "Edit",
			"tool_input": map[string]any{"file_path": existing},
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, HandlePreToolUse(strings.NewReader(string(data)), &out))
		var r PreToolUseResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &r))
		assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision)
	})

	t.Run("missing file outside extract_into -> block", func(t *testing.T) {
		// New file outside every extract_into entry must still block.
		other := t.TempDir() + "/elsewhere.go"
		t.Setenv("ETHOS_VERIFIER_ALLOWLIST", dir+"/declared.go")
		t.Setenv("ETHOS_VERIFIER_EXTRACT_INTO", dir)
		payload := map[string]any{
			"tool_name":  "Write",
			"tool_input": map[string]any{"file_path": other},
		}
		data, err := json.Marshal(payload)
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, HandlePreToolUse(strings.NewReader(string(data)), &out))
		var r PreToolUseResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &r))
		assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision)
	})
}

// TestTargetExists pins the os.Stat wrapper. Clean existence and
// clean non-existence both report nil error; only the ambiguous
// branch (any non-IsNotExist stat failure) surfaces the error so the
// caller can audit-log it.
func TestTargetExists(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/present.go"
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	exists, err := targetExists(file)
	require.NoError(t, err)
	assert.True(t, exists, "existing file must report as existing")

	exists, err = targetExists(dir + "/missing.go")
	require.NoError(t, err)
	assert.False(t, exists,
		"missing file under existing dir must report as not existing")

	exists, err = targetExists(dir)
	require.NoError(t, err)
	assert.True(t, exists, "existing directory must report as existing")
}

// TestTargetExists_AmbiguousStat exercises the non-IsNotExist branch.
// A path under a directory with mode 0 (no execute permission)
// produces an EACCES on stat — neither nil-existence nor nil-error,
// so the caller must surface both signals to its audit log.
func TestTargetExists_AmbiguousStat(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses unix permission checks")
	}
	parent := t.TempDir()
	locked := parent + "/locked"
	require.NoError(t, os.Mkdir(locked, 0o700))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o700)
	})

	exists, err := targetExists(locked + "/anything.go")
	require.Error(t, err, "permission-denied stat must surface as error")
	assert.True(t, exists,
		"ambiguous stat must report as existing so the caller blocks")
}

// TestHandlePreToolUse_StatAmbiguous_LogsAndBlocks asserts the audit
// trail and the block decision when the stat returns a non-IsNotExist
// error. The verifier session must see "ethos: pre-tool-use: stat ..."
// on stderr so the operator can diagnose permission-denied paths,
// and the decision must still be block — the conservative default.
func TestHandlePreToolUse_StatAmbiguous_LogsAndBlocks(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses unix permission checks")
	}
	parent := t.TempDir()
	locked := parent + "/locked"
	require.NoError(t, os.Mkdir(locked, 0o700))
	require.NoError(t, os.Chmod(locked, 0o000))
	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o700)
	})
	target := locked + "/anything.go"

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", parent+"/declared.go")
	t.Setenv("ETHOS_VERIFIER_EXTRACT_INTO", locked)

	payload := map[string]any{
		"tool_name":  "Write",
		"tool_input": map[string]any{"file_path": target},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var out bytes.Buffer
	var hookErr error
	stderrText := captureStderr(t, func() {
		hookErr = HandlePreToolUse(strings.NewReader(string(data)), &out)
	})
	require.NoError(t, hookErr)

	var result PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "deny", result.HookSpecificOutput.PermissionDecision,
		"ambiguous stat must fall through to block")
	assert.Contains(t, stderrText, "pre-tool-use: stat",
		"stderr audit line must fire on the non-IsNotExist branch")
	assert.Contains(t, stderrText, target,
		"stderr audit line must name the target path")
}

// TestHandlePreToolUse_TierAAdvice covers the DES-054 Tier A advice
// path. The hook emits a one-line suggestion to stderr when an
// ad-hoc Agent spawn has no governance context, and suppresses the
// line when the operator has opted out or when the spawn is nested
// under a session that already saw the advice.
//
// Five cases pin the contract:
//
//  1. Non-Agent tool → no advice (the advice is Agent-specific).
//  2. Agent tool, bare env → advice on stderr, allow.
//  3. Agent tool, ETHOS_QUIET_ADVICE=1 → no advice, allow.
//  4. Agent tool, PARENT_SESSION_ID set → no advice, allow.
//  5. Agent tool, both signals set → no advice (either alone
//     suffices, not both required).
func TestHandlePreToolUse_TierAAdvice(t *testing.T) {
	tests := []struct {
		name         string
		tool         string
		quietAdvice  string
		parentSessID string
		wantAdvice   bool
	}{
		{"non-Agent tool emits no advice", "Read", "", "", false},
		{"bare Agent spawn emits advice", "Agent", "", "", true},
		{"ETHOS_QUIET_ADVICE=1 silences", "Agent", "1", "", false},
		{"PARENT_SESSION_ID silences", "Agent", "", "outer-sess-123", false},
		{"both signals silence", "Agent", "1", "outer-sess-123", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
			t.Setenv("ETHOS_QUIET_ADVICE", tt.quietAdvice)
			t.Setenv("PARENT_SESSION_ID", tt.parentSessID)
			t.Setenv("MISSION_ID", "")
			// Isolate the per-day counter root so the Agent path's
			// DELEGATION_ID allocation does not touch ~/.punt-labs/.
			t.Setenv("HOME", t.TempDir())

			oldStderr := os.Stderr
			r, w, err := os.Pipe()
			require.NoError(t, err)
			os.Stderr = w
			t.Cleanup(func() { os.Stderr = oldStderr })

			payload := map[string]any{
				"tool_name":  tt.tool,
				"tool_input": map[string]any{},
			}
			data, err := json.Marshal(payload)
			require.NoError(t, err)

			var out bytes.Buffer
			hookErr := HandlePreToolUse(strings.NewReader(string(data)), &out)
			require.NoError(t, w.Close())
			os.Stderr = oldStderr
			require.NoError(t, hookErr)

			stderrBytes, readErr := io.ReadAll(r)
			require.NoError(t, readErr)
			require.NoError(t, r.Close())
			stderrText := string(stderrBytes)

			var result PreToolUseResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &result))
			assert.Equal(t, "allow", result.HookSpecificOutput.PermissionDecision,
				"PreToolUse must allow regardless of advice state")

			if tt.wantAdvice {
				assert.Contains(t, stderrText, "ad-hoc Agent spawn")
				assert.Contains(t, stderrText, "ethos mission dispatch")
				assert.Contains(t, stderrText, "ETHOS_QUIET_ADVICE=1")
			} else {
				assert.NotContains(t, stderrText, "ad-hoc Agent spawn",
					"advice must be silenced")
			}
		})
	}
}

// TestTierAAdviceLiteral pins the exact stderr line shape against
// DESIGN.md §"PreToolUse-on-Agent". A drift here means the design
// doc and the runtime disagree — fix one or the other.
func TestTierAAdviceLiteral(t *testing.T) {
	want := "ethos: ad-hoc Agent spawn (no mission contract). " +
		"Consider 'ethos mission dispatch' for governed delegation. " +
		"(set ETHOS_QUIET_ADVICE=1 to silence)"
	assert.Equal(t, want, tierAAdvice)
}

// TestMaybeEmitTierAAdvice exercises the helper directly. Each
// suppression signal is independent — clearing the other must still
// suppress.
func TestMaybeEmitTierAAdvice(t *testing.T) {
	tests := []struct {
		name         string
		quietAdvice  string
		parentSessID string
		wantWrite    bool
	}{
		{"bare env writes advice", "", "", true},
		{"quiet=1 suppresses", "1", "", false},
		{"quiet=other does not suppress", "yes", "", true},
		{"parent session suppresses", "", "sess-1", false},
		{"both set suppresses", "1", "sess-1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ETHOS_QUIET_ADVICE", tt.quietAdvice)
			t.Setenv("PARENT_SESSION_ID", tt.parentSessID)
			var buf bytes.Buffer
			maybeEmitTierAAdvice(&buf)
			if tt.wantWrite {
				assert.Contains(t, buf.String(), "ad-hoc Agent spawn")
			} else {
				assert.Empty(t, buf.String())
			}
		})
	}
}

// stageContract creates a minimal valid mission contract in the
// given home directory and returns the missionID. Used by Tier B
// dispatch tests that need a real on-disk contract for Store.Load
// to resolve. Isolates HOME so the counter file and contract path
// land under t.TempDir().
func stageContract(t *testing.T, home, missionID string) {
	t.Helper()
	root := filepath.Join(home, ".punt-labs", "ethos")
	store := mission.NewStore(root)
	c := &mission.Contract{
		MissionID: missionID,
		Status:    mission.StatusOpen,
		CreatedAt: "2026-05-22T21:30:00Z",
		UpdatedAt: "2026-05-22T21:30:00Z",
		Leader:    "claude",
		Worker:    "bwk",
		Evaluator: mission.Evaluator{
			Handle:   "djb",
			PinnedAt: "2026-05-22T21:30:00Z",
		},
		Inputs: mission.Inputs{
			Ticket: "ethos-7i29",
			Files:  []string{"internal/hook/pretooluse.go"},
		},
		WriteSet:        []string{"internal/hook/", "internal/mission/"},
		Tools:           []string{"Read", "Write", "Edit"},
		SuccessCriteria: []string{"make check passes"},
		Budget: mission.Budget{
			Rounds:              3,
			ReflectionAfterEach: true,
		},
		CurrentRound: 1,
	}
	require.NoError(t, store.Create(c))
}

// stageRepoRoot creates a fake repo directory and runs git init so
// FindRepoRoot stops there rather than walking up to the real ethos
// checkout. Returns the repo path. The test chdirs into the repo and
// restores cwd on cleanup.
func stageRepoRoot(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	cmd := exec.Command("git", "init", repo)
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"PATH=" + os.Getenv("PATH"),
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init failed: %s", out)

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repo))
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return repo
}

// TestHandlePreToolUse_TierBDispatch covers the MISSION_ID-set
// branch. The hook resolves the contract, allocates a delegation_id,
// writes the record skeleton, and emits additional_env with
// DELEGATION_ID, MISSION_ID (echoed), PARENT_SESSION_ID (from input
// session_id), and MISSION_ARTIFACTS_DIR (the per-delegation dir).
//
// The on-disk record.yaml is asserted at the expected path:
// <repo>/.ethos/missions/<mission-id>/delegations/<NN>/record.yaml.
func TestHandlePreToolUse_TierBDispatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	missionID := "m-2026-05-22-001"
	stageContract(t, home, missionID)

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-outer-42"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
	// Continue field removed in PreToolUse schema fix.
	require.NotNil(t, r.HookSpecificOutput.AdditionalEnv,
		"Tier B response must include additional_env block")
	assert.Equal(t, missionID, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"Tier B must echo MISSION_ID from the input env")
	assert.Equal(t, "sess-outer-42", r.HookSpecificOutput.AdditionalEnv["PARENT_SESSION_ID"],
		"Tier B must echo session_id as PARENT_SESSION_ID")
	assert.NotEmpty(t, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"],
		"Tier B must allocate a fresh DELEGATION_ID")
	assert.True(t, strings.HasPrefix(r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"], "d-"),
		"DELEGATION_ID must use the d-YYYY-MM-DD-NNN shape")
	assert.Equal(t, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"], r.HookSpecificOutput.AdditionalEnv["PARENT_DELEGATION_ID"],
		"PARENT_DELEGATION_ID must mirror this spawn's DELEGATION_ID so the child sees a parent in its chain (Bugbot HIGH on PR #327)")

	artifactsDir := r.HookSpecificOutput.AdditionalEnv["MISSION_ARTIFACTS_DIR"]
	require.NotEmpty(t, artifactsDir,
		"Tier B response must include MISSION_ARTIFACTS_DIR")
	want := mission.DelegationDir(repo, missionID, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"])
	assert.Equal(t, want, artifactsDir,
		"MISSION_ARTIFACTS_DIR must point at the per-delegation dir")

	recordPath := filepath.Join(artifactsDir, "record.yaml")
	info, err := os.Stat(recordPath)
	require.NoError(t, err, "record.yaml must exist at the per-delegation path")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"record.yaml mode must be 0o600")

	d, err := mission.LoadDelegation(recordPath)
	require.NoError(t, err)
	assert.Equal(t, mission.TierB, d.Tier)
	assert.Equal(t, missionID, d.Mission)
	assert.Equal(t, mission.DelegationVerdictOpen, d.Verdict,
		"fresh skeleton verdict must be open")
	assert.Equal(t, "sess-outer-42", d.ParentSession)
	assert.NotEmpty(t, d.CreatedAt, "opened_at must be stamped")
}

// TestTierBDispatch_FromWorktreeResolvesMainStore pins CR#1: a Tier B
// dispatch from inside a linked worktree resolves the mission store in the
// MAIN work tree — where `ethos mission create` wrote the contract — not the
// worktree's own empty tree. Before the fix the dispatch read a different
// store than the CLI and refused the spawn with "resolving MISSION_ID".
func TestTierBDispatch_FromWorktreeResolvesMainStore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ETHOS_REPO_ROOT", "") // resolution must come from the cwd walk
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")

	gitAt := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = []string{
			"HOME=" + home,
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"PATH=" + os.Getenv("PATH"),
		}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	base := t.TempDir()
	main := filepath.Join(base, "main")
	require.NoError(t, os.MkdirAll(main, 0o755))
	gitAt(main, "init")
	gitAt(main, "config", "user.email", "t@example.com")
	gitAt(main, "config", "user.name", "t")
	gitAt(main, "commit", "--allow-empty", "-m", "init")

	// Stage the contract ONLY in the main repo store; the global tree stays
	// empty, so a worktree that fails to resolve the main store cannot find
	// the mission by global fallback.
	missionID := "m-2026-07-24-050"
	store := mission.NewStoreWithRoots(main, globalRoot)
	require.NoError(t, store.Create(&mission.Contract{
		MissionID:       missionID,
		Status:          mission.StatusOpen,
		CreatedAt:       "2026-07-24T00:00:00Z",
		UpdatedAt:       "2026-07-24T00:00:00Z",
		Leader:          "claude",
		Worker:          "bwk",
		Evaluator:       mission.Evaluator{Handle: "djb", PinnedAt: "2026-07-24T00:00:00Z"},
		WriteSet:        []string{"internal/hook/"},
		Tools:           []string{"Read", "Write", "Edit"},
		SuccessCriteria: []string{"make check passes"},
		Budget:          mission.Budget{Rounds: 1, ReflectionAfterEach: true},
		CurrentRound:    1,
	}))

	wt := filepath.Join(base, "wt")
	gitAt(main, "worktree", "add", wt)

	// Enter the worktree — its own .punt-labs/ethos does not exist.
	orig, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(wt))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-wt"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision,
		"dispatch from a worktree must resolve the main store and allow, not refuse MISSION_ID")

	// The delegation skeleton must land in the MAIN store — the tree the CLI
	// reads — not in the worktree's own tree.
	artifactsDir := r.HookSpecificOutput.AdditionalEnv["MISSION_ARTIFACTS_DIR"]
	require.NotEmpty(t, artifactsDir)
	_, err = os.Stat(filepath.Join(artifactsDir, "record.yaml"))
	require.NoError(t, err, "delegation skeleton must exist under the resolved store")

	evalSym := func(p string) string {
		r, err := filepath.EvalSymlinks(p)
		require.NoError(t, err)
		return r
	}
	assert.True(t, strings.HasPrefix(evalSym(artifactsDir), evalSym(main)),
		"skeleton must be written under the MAIN store, got %s", artifactsDir)
	assert.False(t, strings.HasPrefix(evalSym(artifactsDir), evalSym(wt)),
		"skeleton must NOT be written under the worktree tree")
}

// TestTierBDispatch_BadOverrideInWorktreeDoesNotWriteWorktree pins the
// code-review round-2 finding: a set-but-invalid ETHOS_REPO_ROOT makes
// StoreRepoRoot return "" (F1 fail-closed). tierBStoreRoot must NOT
// substitute the raw worktree cwd — doing so would resolve the store into
// the worktree's own tree, reintroducing ethos-yofr behind a bad override.
// The mission is staged in the global tree so Load succeeds and the write
// path is reached; the fix makes the lock fail loud on the empty repoRoot
// rather than writing a skeleton into the worktree.
func TestTierBDispatch_BadOverrideInWorktreeDoesNotWriteWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")

	gitAt := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = []string{
			"HOME=" + home,
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"PATH=" + os.Getenv("PATH"),
		}
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}

	base := t.TempDir()
	main := filepath.Join(base, "main")
	require.NoError(t, os.MkdirAll(main, 0o755))
	gitAt(main, "init")
	gitAt(main, "config", "user.email", "t@example.com")
	gitAt(main, "config", "user.name", "t")
	gitAt(main, "commit", "--allow-empty", "-m", "init")
	wt := filepath.Join(base, "wt")
	gitAt(main, "worktree", "add", wt)

	// Stage the mission in the GLOBAL tree so Load succeeds and the dispatch
	// reaches the write path (the lock + skeleton), where the old Getwd
	// fallback would have written into the worktree.
	missionID := "m-2026-07-24-060"
	require.NoError(t, mission.NewStore(globalRoot).Create(&mission.Contract{
		MissionID:       missionID,
		Status:          mission.StatusOpen,
		CreatedAt:       "2026-07-24T00:00:00Z",
		UpdatedAt:       "2026-07-24T00:00:00Z",
		Leader:          "claude",
		Worker:          "bwk",
		Evaluator:       mission.Evaluator{Handle: "djb", PinnedAt: "2026-07-24T00:00:00Z"},
		WriteSet:        []string{"internal/hook/"},
		SuccessCriteria: []string{"make check passes"},
		Budget:          mission.Budget{Rounds: 1, ReflectionAfterEach: true},
		CurrentRound:    1,
	}))

	orig, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(wt))

	// A bad override: an existing directory with no .punt-labs/ethos store.
	// F1 makes StoreRepoRoot return ""; tierBStoreRoot must not fall back to
	// the worktree cwd.
	t.Setenv("ETHOS_REPO_ROOT", t.TempDir())
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-bad"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision,
		"a bad override in a worktree must fail loud, not resolve worktree-local")

	// The decisive assertion: no mission tree may be created under the
	// worktree. The old Getwd fallback would have written the skeleton here.
	_, statErr := os.Stat(filepath.Join(wt, ".punt-labs", "ethos", "missions", missionID))
	assert.True(t, os.IsNotExist(statErr),
		"dispatch must not create a mission tree in the worktree under a bad override")
}

// TestHandlePreToolUse_TierBDispatch_ConcurrentSharedLock asserts the
// shared mission lock contract: two Tier B spawns under the same
// mission must both succeed without blocking each other and write
// distinct per-delegation directories. If this test deadlocks or
// reports both spawns writing the same delegation dir, the mission
// lock has been silently promoted to exclusive or the delegation ID
// allocator has lost its uniqueness.
func TestHandlePreToolUse_TierBDispatch_ConcurrentSharedLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	missionID := "m-2026-05-22-002"
	stageContract(t, home, missionID)

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-x"}`
	type result struct {
		dir string
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			var out bytes.Buffer
			if err := HandlePreToolUse(strings.NewReader(payload), &out); err != nil {
				results <- result{err: err}
				return
			}
			var r PreToolUseResult
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				results <- result{err: err}
				return
			}
			if r.HookSpecificOutput.PermissionDecision != "allow" {
				results <- result{err: errors.New("decision was not allow: " + r.HookSpecificOutput.PermissionDecisionReason)}
				return
			}
			results <- result{dir: r.HookSpecificOutput.AdditionalEnv["MISSION_ARTIFACTS_DIR"]}
		}()
	}

	var dirs []string
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			require.NoError(t, r.err)
			require.NotEmpty(t, r.dir)
			dirs = append(dirs, r.dir)
		case <-time.After(5 * time.Second):
			t.Fatal("dispatch goroutines did not complete within 5s — likely lock deadlock")
		}
	}
	assert.NotEqual(t, dirs[0], dirs[1],
		"two concurrent Tier B spawns must land in distinct delegation dirs")
}

// TestHandlePreToolUse_TierBDispatch_ExclusiveBlocks verifies the
// exclusive-side of the lock contract: a sibling holder of LOCK_EX
// on the per-mission .lock file must block the Tier B dispatch until
// it releases. The test holds LOCK_EX in a goroutine for 80ms, fires
// the dispatch in another goroutine, and asserts the dispatch's wait
// time reflects the hold.
func TestHandlePreToolUse_TierBDispatch_ExclusiveBlocks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	missionID := "m-2026-05-22-003"
	stageContract(t, home, missionID)

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	// Stage the per-mission dir + .lock so we can hold LOCK_EX on the
	// same path the dispatch will try to share-lock.
	dir := filepath.Join(repo, ".punt-labs", "ethos", "missions", missionID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	lockPath := filepath.Join(dir, ".lock")
	excl, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(excl.Fd()), syscall.LOCK_EX))

	type result struct {
		decision string
		waited   time.Duration
		err      error
	}
	done := make(chan result, 1)
	go func() {
		payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-y"}`
		start := time.Now()
		var out bytes.Buffer
		if err := HandlePreToolUse(strings.NewReader(payload), &out); err != nil {
			done <- result{err: err}
			return
		}
		waited := time.Since(start)
		var r PreToolUseResult
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			done <- result{err: err}
			return
		}
		done <- result{decision: r.HookSpecificOutput.PermissionDecision, waited: waited}
	}()

	hold := 80 * time.Millisecond
	time.Sleep(hold)
	require.NoError(t, syscall.Flock(int(excl.Fd()), syscall.LOCK_UN))
	require.NoError(t, excl.Close())

	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.Equal(t, "allow", r.decision,
			"dispatch must allow after exclusive holder releases")
		assert.GreaterOrEqual(t, r.waited, 60*time.Millisecond,
			"dispatch wait must reflect the exclusive hold (got %v)", r.waited)
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not complete within 5s after exclusive release")
	}
}

// TestCloseDelegationAborted_NotExistDistinctMessage pins the D4
// silent-failure contract: when the skeleton CloseDelegationSkeleton
// returns an fs.ErrNotExist error, the stderr line names it as an
// order-of-operations bug rather than the generic close-failure
// message. The distinction matters because fs.ErrNotExist on close
// means the depth-refusal path fired before WriteDelegationSkeleton —
// a programmer bug, not a runtime fault — and the operator needs that
// signal to find the offending call ordering in source.
func TestCloseDelegationAborted_NotExistDistinctMessage(t *testing.T) {
	// No skeleton on disk at this path — every CloseDelegationSkeleton
	// call will return fs.ErrNotExist.
	repo := t.TempDir()
	missionID := "m-2026-05-22-005"
	delegationID := "d-2026-05-22-077"

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = oldStderr })

	closeDelegationAborted(repo, missionID, delegationID)
	require.NoError(t, w.Close())
	os.Stderr = oldStderr

	stderrBytes, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	stderrText := string(stderrBytes)

	assert.Contains(t, stderrText, "order-of-operations bug",
		"fs.ErrNotExist on close must surface as the distinct order-of-operations diagnostic")
	assert.Contains(t, stderrText, delegationID)
	assert.Contains(t, stderrText, missionID)
	assert.NotContains(t, stderrText, "closing aborted skeleton:",
		"the generic close-failure line must be suppressed on the fs.ErrNotExist branch")
}

// TestDispatchTierB_LockAcquireFailureRollsBackCounter asserts the
// ID-rollback contract on the lock-acquisition failure path. NewID
// allocates a delegation_id and bumps the counter; if a subsequent
// step in dispatchTierB fails — here, AcquireMissionLock — the
// deferred release(false) must decrement the counter back. Otherwise
// every transient lock failure permanently burns one delegation ID,
// drifting the per-day counter away from the actual on-disk record
// count.
//
// Failure injection: pre-create a directory at the per-mission .lock
// path. os.OpenFile(O_RDWR) refuses to open a directory, so
// AcquireMissionLock returns an error and the dispatch falls into
// the deferred rollback path.
func TestDispatchTierB_LockAcquireFailureRollsBackCounter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	missionID := "m-2026-05-22-004"
	stageContract(t, home, missionID)

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	// Stage a directory at the lock path so AcquireMissionLock's
	// os.OpenFile call fails with EISDIR. The mission directory itself
	// must exist; the .lock entry must be a directory rather than a
	// regular file.
	missionDir := filepath.Join(repo, ".punt-labs", "ethos", "missions", missionID)
	require.NoError(t, os.MkdirAll(filepath.Join(missionDir, ".lock"), 0o700))

	// Snapshot the counter before dispatch. The counter file lives at
	// <home>/.punt-labs/ethos/counters/delegations-YYYY-MM-DD. We have
	// to allocate one ID first to materialize the counter file (a
	// missing file reads as 0), then check that the rollback returns
	// to that pre-dispatch value.
	day := time.Now().UTC().Format("2006-01-02")
	counterPath := filepath.Join(
		home, ".punt-labs", "ethos", "counters", "delegations-"+day,
	)
	primer, primerRelease, err := mission.NewID(mission.NamespaceDelegations, time.Now())
	require.NoError(t, err)
	require.NotEmpty(t, primer)
	primerRelease(true)
	preValue, err := os.ReadFile(counterPath)
	require.NoError(t, err)

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-roll"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision,
		"a lock-acquire failure must surface as a named block, not an allow")
	assert.Contains(t, r.HookSpecificOutput.PermissionDecisionReason, "acquiring mission lock")

	postValue, err := os.ReadFile(counterPath)
	require.NoError(t, err)
	assert.Equal(t, string(preValue), string(postValue),
		"counter must roll back to pre-dispatch value when the lock acquire fails")
}

// TestEnforceDelegationDepth_ConfigErrorClosesSkeleton pins HIGH-1.
// When ResolveMaxDelegationDepth fails — here, the repo's
// .punt-labs/ethos.yaml carries a negative max_delegation_depth —
// the depth gate must refuse the spawn AND close the just-written
// skeleton with verdict=aborted. Returning the refusal without
// closing leaks the skeleton at verdict=open: every downstream audit
// reader sees a spawn that "ran" but never reported in. That is the
// silent-failure regression class DES-054 phase 2 was designed to
// prevent; the other two refusal branches (depth-walk error, depth-
// exceeds-limit) already close correctly, so this test pins the
// third branch to the same contract.
func TestEnforceDelegationDepth_ConfigErrorClosesSkeleton(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	// Stage a repo config that fails ResolveMaxDelegationDepth: a
	// negative value surfaces as an error rather than silently
	// flipping to the default.
	cfgDir := filepath.Join(repo, ".punt-labs")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(cfgDir, "ethos.yaml"),
		[]byte("max_delegation_depth: -5\n"),
		0o600,
	))

	missionID := "m-2026-05-22-005"
	stageContract(t, home, missionID)

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-cfgerr"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision,
		"a config-error must surface as a named block, not an allow")
	assert.Contains(t, r.HookSpecificOutput.PermissionDecisionReason, "max_delegation_depth",
		"refusal reason must name the config error")

	// The skeleton was written before the depth gate fired; the depth
	// gate's config-error branch must close it with verdict=aborted.
	// Walk the per-mission delegations directory to find the single
	// record.yaml the dispatch produced and assert its verdict.
	delegationsDir := filepath.Join(
		repo, ".punt-labs", "ethos", "missions", missionID, "delegations",
	)
	entries, err := os.ReadDir(delegationsDir)
	require.NoError(t, err, "delegations dir must exist — the skeleton write came before the refusal")
	require.Len(t, entries, 1, "exactly one delegation skeleton must be on disk")

	recordPath := filepath.Join(delegationsDir, entries[0].Name(), "record.yaml")
	d, err := mission.LoadDelegation(recordPath)
	require.NoError(t, err)
	assert.Equal(t, mission.DelegationVerdictAborted, d.Verdict,
		"config-error refusal must close the skeleton at verdict=aborted, not leave it open")
	assert.NotEmpty(t, d.ClosedAt,
		"config-error refusal must stamp closed_at — an open skeleton with no closed_at is the silent-failure shape")
}

// TestHandlePreToolUse_TierBMalformedMissionID asserts the
// security-review contract: a MISSION_ID that does not resolve to a
// contract on disk surfaces as a block decision with a named reason.
// No silent fall-through to Tier A — Phase 2b's threat model
// requires the Agent spawn be refused so the operator sees the
// mismatch.
func TestHandlePreToolUse_TierBMalformedMissionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "m-2026-05-22-999")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-A"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision,
		"malformed MISSION_ID must block, not fall through to Tier A")
	assert.Contains(t, r.HookSpecificOutput.PermissionDecisionReason, "MISSION_ID")
	assert.Contains(t, r.HookSpecificOutput.PermissionDecisionReason, "m-2026-05-22-999")
}

// TestHandlePreToolUse_TierADispatch covers the MISSION_ID-unset
// branch: the round-3 advice line lands on stderr, AND the response
// carries DELEGATION_ID + PARENT_SESSION_ID in additional_env. The
// MISSION_ID key MUST NOT appear in the Tier A response — there
// isn't one.
func TestHandlePreToolUse_TierADispatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	oldStderr := os.Stderr
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = pw
	t.Cleanup(func() { os.Stderr = oldStderr })

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-outer-7"}`
	var out bytes.Buffer
	hookErr := HandlePreToolUse(strings.NewReader(payload), &out)
	require.NoError(t, pw.Close())
	os.Stderr = oldStderr
	require.NoError(t, hookErr)

	stderrBytes, err := io.ReadAll(pr)
	require.NoError(t, err)
	require.NoError(t, pr.Close())
	stderrText := string(stderrBytes)

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
	// Continue field removed in PreToolUse schema fix.
	require.NotNil(t, r.HookSpecificOutput.AdditionalEnv,
		"Tier A response must include additional_env block")
	assert.Equal(t, "sess-outer-7", r.HookSpecificOutput.AdditionalEnv["PARENT_SESSION_ID"],
		"Tier A must echo session_id as PARENT_SESSION_ID")
	assert.NotEmpty(t, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"],
		"Tier A must still allocate a DELEGATION_ID for audit binding")
	assert.Equal(t, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"], r.HookSpecificOutput.AdditionalEnv["PARENT_DELEGATION_ID"],
		"PARENT_DELEGATION_ID must mirror DELEGATION_ID so a Tier A child spawn sees its parent in the chain")
	_, hasMissionID := r.HookSpecificOutput.AdditionalEnv["MISSION_ID"]
	assert.False(t, hasMissionID,
		"Tier A response must NOT carry MISSION_ID — there isn't one")

	// Round-3 behavior preserved: advice on stderr.
	assert.Contains(t, stderrText, "ad-hoc Agent spawn",
		"Tier A round-3 advice must still land on stderr")
}

// TestHandlePreToolUse_NonAgentPassthroughUnchanged asserts that
// non-Agent tools (the allowlist-enforcement path) do NOT carry
// additional_env. The Phase 2b dispatch is Agent-only — Read, Write,
// Edit etc. continue to emit the legacy {decision, reason} shape.
func TestHandlePreToolUse_NonAgentPassthroughUnchanged(t *testing.T) {
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "m-anything") // ignored on non-Agent path

	payload := `{"tool_name":"Read","tool_input":{"file_path":"/anywhere.go"}}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
	assert.Empty(t, r.HookSpecificOutput.AdditionalEnv,
		"non-Agent passthrough must not emit additional_env")
	// Continue field removed in PreToolUse schema fix — no longer
	// part of the hook protocol. The assertion that non-Agent calls
	// don't set continue was valid under the old schema; under the
	// new schema the field doesn't exist at all.
}

// stageContractCustomWriteSet stages a contract with a caller-
// supplied write_set so two contracts in the same test do not
// conflict on the WriteSet-overlap admission rule.
func stageContractCustomWriteSet(t *testing.T, home, missionID string, writeSet []string) {
	t.Helper()
	root := filepath.Join(home, ".punt-labs", "ethos")
	store := mission.NewStore(root)
	c := &mission.Contract{
		MissionID: missionID,
		Status:    mission.StatusOpen,
		CreatedAt: "2026-05-22T21:30:00Z",
		UpdatedAt: "2026-05-22T21:30:00Z",
		Leader:    "claude",
		Worker:    "bwk",
		Evaluator: mission.Evaluator{
			Handle:   "djb",
			PinnedAt: "2026-05-22T21:30:00Z",
		},
		Inputs: mission.Inputs{
			Ticket: "ethos-7i29",
			Files:  []string{"internal/hook/pretooluse.go"},
		},
		WriteSet:        writeSet,
		Tools:           []string{"Read", "Write", "Edit"},
		SuccessCriteria: []string{"make check passes"},
		Budget: mission.Budget{
			Rounds:              3,
			ReflectionAfterEach: true,
		},
		CurrentRound: 1,
	}
	require.NoError(t, store.Create(c))
}

// stageContractWithDelegations stages a contract whose Delegations[]
// list pins a single template. Used by the inheritance-dispatch tests
// to model a parent contract that authorizes a child spawn via
// SpawnPattern + InheritsContract=true.
func stageContractWithDelegations(
	t *testing.T,
	home, missionID string,
	templates []mission.DelegationTemplate,
) {
	t.Helper()
	stageContract(t, home, missionID)

	root := filepath.Join(home, ".punt-labs", "ethos")
	store := mission.NewStore(root)
	c, err := store.Load(missionID)
	require.NoError(t, err)
	c.Delegations = templates
	require.NoError(t, store.Update(c))
}

// stageParentDelegationSkeleton writes a Tier B parent delegation
// record on disk under repo/.ethos/missions/<m>/delegations/<d>/.
// The skeleton lets the inheritance resolver Load the parent record
// and read its Mission field — the resolver needs the missionID to
// fetch the ancestor's contract.
func stageParentDelegationSkeleton(
	t *testing.T,
	repo, missionID, delegationID string,
	parentDelegation string,
) {
	t.Helper()
	_, err := mission.WriteDelegationSkeleton(repo, missionID, delegationID, mission.DelegationSkeleton{
		Tier:             mission.TierB,
		ParentDelegation: parentDelegation,
		AgentType:        "bwk",
	})
	require.NoError(t, err)
}

// TestDispatchAgent_InheritanceHit pins the happy path: parent
// contract has a Delegations[] entry with SpawnPattern matching
// CLAUDE_AGENT_TYPE and InheritsContract=true. The child spawn
// inherits the parent missionID — the response carries MISSION_ID
// in additional_env and the record.yaml lands under the parent
// missions tree.
func TestDispatchAgent_InheritanceHit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	parentMission := "m-2026-05-22-200"
	parentDelegation := "d-2026-05-22-200"
	stageContractWithDelegations(t, home, parentMission, []mission.DelegationTemplate{
		{Role: "verifier", SpawnPattern: "djb", InheritsContract: true},
	})
	stageParentDelegationSkeleton(t, repo, parentMission, parentDelegation, "")

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", parentDelegation)
	t.Setenv("CLAUDE_AGENT_TYPE", "djb")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-child"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
	assert.Equal(t, parentMission, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"inheritance match must promote the child to Tier B with the parent missionID")
	assert.NotEmpty(t, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"])
	assert.True(t, strings.HasPrefix(r.HookSpecificOutput.AdditionalEnv["MISSION_ARTIFACTS_DIR"],
		filepath.Join(repo, ".punt-labs", "ethos", "missions", parentMission)),
		"artifacts dir must nest under the inherited mission")
}

// TestDispatchAgent_InheritanceNoMatch pins the fall-through: the
// parent contract has a Delegations[] entry but the child's
// CLAUDE_AGENT_TYPE does not match any SpawnPattern. The dispatch
// falls through to Tier A — no MISSION_ID echoed, advice on stderr.
func TestDispatchAgent_InheritanceNoMatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	parentMission := "m-2026-05-22-201"
	parentDelegation := "d-2026-05-22-201"
	stageContractWithDelegations(t, home, parentMission, []mission.DelegationTemplate{
		{Role: "verifier", SpawnPattern: "djb", InheritsContract: true},
	})
	stageParentDelegationSkeleton(t, repo, parentMission, parentDelegation, "")

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", parentDelegation)
	t.Setenv("CLAUDE_AGENT_TYPE", "mdm") // does not match "djb"
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-child"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
	_, hasMissionID := r.HookSpecificOutput.AdditionalEnv["MISSION_ID"]
	assert.False(t, hasMissionID,
		"no spawn_pattern match must fall through to Tier A — MISSION_ID must NOT be echoed")
	assert.NotEmpty(t, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"],
		"Tier A still allocates a DELEGATION_ID for audit binding")
}

// TestDispatchAgent_InheritanceNotInheritsContract pins the
// InheritsContract=false branch. A matching SpawnPattern with
// InheritsContract unset (default false) must NOT promote the
// child — the dispatch falls through to Tier A.
func TestDispatchAgent_InheritanceNotInheritsContract(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	parentMission := "m-2026-05-22-202"
	parentDelegation := "d-2026-05-22-202"
	stageContractWithDelegations(t, home, parentMission, []mission.DelegationTemplate{
		{Role: "verifier", SpawnPattern: "djb", InheritsContract: false},
	})
	stageParentDelegationSkeleton(t, repo, parentMission, parentDelegation, "")

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", parentDelegation)
	t.Setenv("CLAUDE_AGENT_TYPE", "djb")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-child"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
	_, hasMissionID := r.HookSpecificOutput.AdditionalEnv["MISSION_ID"]
	assert.False(t, hasMissionID,
		"InheritsContract=false must NOT promote — Tier A fall-through")
}

// TestDispatchAgent_InheritanceMalformedRegex pins the non-blocking
// runtime behavior on a bad pattern. Admission-time validation
// (Contract.Validate, DES-054 phase 3) rejects a malformed regex
// before persistence, so reaching this code path requires a
// hand-edited contract on disk. The runtime fallback is defense
// in depth: a malformed regex surfaces as a stderr warning + Tier A
// fall-through — never a block. djb's rule: no silent admit, but
// also no refusal.
//
// To exercise the defense, stage a contract with a well-formed
// pattern, then overwrite the on-disk YAML with the malformed form.
func TestDispatchAgent_InheritanceMalformedRegex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	parentMission := "m-2026-05-22-203"
	parentDelegation := "d-2026-05-22-203"
	stageContractWithDelegations(t, home, parentMission, []mission.DelegationTemplate{
		{Role: "verifier", SpawnPattern: "djb", InheritsContract: true},
	})
	// Bypass Contract.Validate by editing the on-disk YAML directly.
	// This models a contract that was hand-edited after persistence —
	// the only path by which a malformed pattern can reach the runtime
	// matcher now that admission-time validation rejects it.
	contractPath := filepath.Join(home, ".punt-labs", "ethos", "missions", parentMission+".yaml")
	contractBytes, err := os.ReadFile(contractPath)
	require.NoError(t, err)
	patched := strings.Replace(string(contractBytes),
		"spawn_pattern: djb", "spawn_pattern: djb(", 1)
	require.NotEqual(t, string(contractBytes), patched, "patch must change the YAML")
	require.NoError(t, os.WriteFile(contractPath, []byte(patched), 0o600))
	stageParentDelegationSkeleton(t, repo, parentMission, parentDelegation, "")

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", parentDelegation)
	t.Setenv("CLAUDE_AGENT_TYPE", "djb")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	oldStderr := os.Stderr
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = pw
	t.Cleanup(func() { os.Stderr = oldStderr })

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-child"}`
	var out bytes.Buffer
	hookErr := HandlePreToolUse(strings.NewReader(payload), &out)
	require.NoError(t, pw.Close())
	os.Stderr = oldStderr
	require.NoError(t, hookErr)

	stderrBytes, err := io.ReadAll(pr)
	require.NoError(t, err)
	require.NoError(t, pr.Close())
	stderrText := string(stderrBytes)

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision,
		"a malformed regex must NOT block — non-blocking inheritance is the design")
	_, hasMissionID := r.HookSpecificOutput.AdditionalEnv["MISSION_ID"]
	assert.False(t, hasMissionID,
		"malformed regex falls through to Tier A — MISSION_ID must not echo")
	// Admission-time validation (DES-054 phase 3) rejects the patched
	// contract at Store.Load, so the runtime stderr now reports the
	// load failure rather than the legacy "bad spawn_pattern" warning.
	// Either form is acceptable — the requirement is that the operator
	// sees the malformed pattern in stderr before the spawn proceeds.
	assert.Contains(t, stderrText, "spawn_pattern",
		"the malformed pattern must land in stderr so the operator sees it")
}

// TestDispatchAgent_InheritanceChainTooDeep pins the depth bound.
// A chain longer than ResolveMaxDelegationDepth must surface as a
// stderr warning and fall through to Tier A — never spin, never
// block.
func TestDispatchAgent_InheritanceChainTooDeep(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	// Pin max_delegation_depth=2 so a chain of three parents trips
	// the bound. The walker starts at depth 0 on the immediate
	// parent and increments per ancestor — depth>limit fires after
	// the third hop.
	cfgDir := filepath.Join(repo, ".punt-labs")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(cfgDir, "ethos.yaml"),
		[]byte("max_delegation_depth: 2\n"),
		0o600,
	))

	// Build a chain: root <- A <- B <- C, all under one mission, no
	// matching template. The walker should give up after the bound.
	parentMission := "m-2026-05-22-204"
	stageContractWithDelegations(t, home, parentMission, []mission.DelegationTemplate{
		{Role: "verifier", SpawnPattern: "never-matches", InheritsContract: true},
	})
	stageParentDelegationSkeleton(t, repo, parentMission, "d-A", "")
	stageParentDelegationSkeleton(t, repo, parentMission, "d-B", "d-A")
	stageParentDelegationSkeleton(t, repo, parentMission, "d-C", "d-B")
	stageParentDelegationSkeleton(t, repo, parentMission, "d-D", "d-C")

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "d-D")
	t.Setenv("CLAUDE_AGENT_TYPE", "djb")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	oldStderr := os.Stderr
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = pw
	t.Cleanup(func() { os.Stderr = oldStderr })

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-child"}`
	var out bytes.Buffer
	hookErr := HandlePreToolUse(strings.NewReader(payload), &out)
	require.NoError(t, pw.Close())
	os.Stderr = oldStderr
	require.NoError(t, hookErr)

	stderrBytes, err := io.ReadAll(pr)
	require.NoError(t, err)
	require.NoError(t, pr.Close())
	stderrText := string(stderrBytes)

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision,
		"chain-too-deep falls through to Tier A — never block")
	_, hasMissionID := r.HookSpecificOutput.AdditionalEnv["MISSION_ID"]
	assert.False(t, hasMissionID)
	assert.Contains(t, stderrText, "chain exceeds max_delegation_depth",
		"depth bound must land in stderr so the operator sees the runaway-chain warning")
}

// TestHandlePreToolUse_DepthRefusalThenAbandonNeedsNoDisclaim is review
// finding K11(b) (full-branch review, m-2026-09-08-004 round 3):
// DESIGN.md calls
// `TestStore_Abandon_SucceedsAfterDepthRefusalWithNoDisclaim` (package
// mission) "the end-to-end proof using the real depth-refusal shape,"
// but that test's own comment says "simulate" — it hand-calls
// CloseDelegationSkeleton directly rather than driving the depth gate
// through HandlePreToolUse. package mission cannot import package hook
// (hook already imports mission), so a TRUE end-to-end proof has to
// live here instead. This drives a real Tier B (MISSION_ID) spawn
// through enforceDelegationDepth with the ceiling exceeded, confirms
// the hook denies it and closes the skeleton verdict=aborted itself
// (not a test-injected shortcut), then confirms Store.Abandon succeeds
// on that mission with no --disclaim, mirroring the package-mission
// test's own assertion but from the real refusal path.
func TestHandlePreToolUse_DepthRefusalThenAbandonNeedsNoDisclaim(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	missionID := "m-2026-09-08-740"
	stageContract(t, home, missionID) // Worker: "bwk", global tree

	// max_delegation_depth=1: one staged ancestor (depth 1) plus this
	// spawn (proposed depth 2) exceeds it.
	cfgDir := filepath.Join(repo, ".punt-labs")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(cfgDir, "ethos.yaml"),
		[]byte("max_delegation_depth: 1\n"),
		0o600,
	))
	// Staged under a DIFFERENT mission than the one under test: the
	// depth walker resolves an ancestor delegation ID by scanning every
	// mission tree (delegationLoader), so its own mission membership
	// does not matter to the walk -- but if it were staged under
	// missionID itself, it would sit in that mission's own
	// delegations/ directory as a still-OPEN record and block Abandon
	// for a real, unrelated reason, defeating this test's own premise
	// that the depth-refused delegation is the mission's ONLY one.
	stageParentDelegationSkeleton(t, repo, "m-2026-09-08-741", "d-ancestor", "")

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("PARENT_DELEGATION_ID", "d-ancestor")
	t.Setenv("CLAUDE_AGENT_TYPE", "bwk")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-depth-refused"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision)
	assert.Contains(t, r.HookSpecificOutput.PermissionDecisionReason, "max_delegation_depth")

	// Discover the delegation ID the hook itself allocated -- the only
	// entry under this mission's delegations/ dir other than the staged
	// ancestor.
	delegationsDir := filepath.Dir(mission.DelegationDir(repo, missionID, "x"))
	entries, err := os.ReadDir(delegationsDir)
	require.NoError(t, err)
	var refusedID string
	for _, e := range entries {
		if e.Name() != "d-ancestor" {
			refusedID = e.Name()
		}
	}
	require.NotEmpty(t, refusedID, "the hook must have written a delegation skeleton before refusing it")

	d, err := mission.LoadDelegation(filepath.Join(delegationsDir, refusedID, "record.yaml"))
	require.NoError(t, err)
	assert.Equal(t, mission.DelegationVerdictAborted, d.Verdict,
		"the depth-refused skeleton must be closed aborted by the hook itself, not a test shortcut")

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	s := mission.NewStoreWithRoots(repo, globalRoot)
	abandoned, err := s.Abandon(missionID, "the only delegation was refused before it ever ran")
	require.NoError(t, err, "a mission whose only delegation was depth-refused must be abandonable with no disclaim")
	assert.Equal(t, mission.StatusAbandoned, abandoned.Status)
}

// TestDispatchAgent_InheritanceEmptyParent confirms the existing
// Tier A path is unchanged: with PARENT_DELEGATION_ID unset, the
// hook never enters the inheritance walk and the response shape
// matches the bare Tier A contract (DELEGATION_ID + PARENT_SESSION_ID,
// no MISSION_ID).
func TestDispatchAgent_InheritanceEmptyParent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "djb")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-bare"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
	_, hasMissionID := r.HookSpecificOutput.AdditionalEnv["MISSION_ID"]
	assert.False(t, hasMissionID,
		"empty PARENT_DELEGATION_ID must skip inheritance walk entirely")
	assert.NotEmpty(t, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"])
	assert.Equal(t, "sess-bare", r.HookSpecificOutput.AdditionalEnv["PARENT_SESSION_ID"])
}

// TestDispatchAgent_InheritanceMissionIDTakesPrecedence pins that
// the inheritance walk is SKIPPED when MISSION_ID is explicitly
// set. Explicit dispatch always wins — even if a parent contract
// would have inherited a different mission.
func TestDispatchAgent_InheritanceMissionIDTakesPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	parentMission := "m-2026-05-22-205"
	explicitMission := "m-2026-05-22-206"
	parentDelegation := "d-2026-05-22-205"
	stageContractWithDelegations(t, home, parentMission, []mission.DelegationTemplate{
		{Role: "verifier", SpawnPattern: "djb", InheritsContract: true},
	})
	stageContractCustomWriteSet(t, home, explicitMission, []string{"cmd/ethos/"})
	// Stage the parent skeleton under BOTH missions' trees: the
	// inheritance walk would resolve it under parentMission and
	// match the SpawnPattern, but with MISSION_ID set the dispatch
	// must skip the walk entirely and run under explicitMission.
	// The depth gate (which always runs under explicitMission)
	// needs to find the parent record in explicitMission's tree to
	// compute the chain depth, so the skeleton must exist there too.
	stageParentDelegationSkeleton(t, repo, parentMission, parentDelegation, "")
	stageParentDelegationSkeleton(t, repo, explicitMission, parentDelegation, "")

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", explicitMission)
	t.Setenv("PARENT_DELEGATION_ID", parentDelegation)
	t.Setenv("CLAUDE_AGENT_TYPE", "djb")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-explicit"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision, "reason: %s", r.HookSpecificOutput.PermissionDecisionReason)
	assert.Equal(t, explicitMission, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"explicit MISSION_ID must beat inheritance — the child runs under the explicit contract")
}

// TestDispatchAgent_InheritanceUnknownParent pins the fall-through
// when PARENT_DELEGATION_ID refers to a delegation that doesn't
// exist on disk. No record → no walk → Tier A; no block.
func TestDispatchAgent_InheritanceUnknownParent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "d-does-not-exist")
	t.Setenv("CLAUDE_AGENT_TYPE", "djb")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-ghost"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision,
		"a parent delegation that is not on disk must fall through to Tier A — never block")
	_, hasMissionID := r.HookSpecificOutput.AdditionalEnv["MISSION_ID"]
	assert.False(t, hasMissionID)
}

// Confirm that os.Getenv is the actual mechanism (not a mock).
func TestHandlePreToolUse_ReadsRealEnvVar(t *testing.T) {
	key := "ETHOS_VERIFIER_ALLOWLIST"

	// Unset: passthrough — Write allowed anywhere.
	t.Setenv(key, "")
	var out bytes.Buffer
	payload := `{"tool_name":"Write","tool_input":{"file_path":"anything.go"}}`
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)

	// Set: Write enforced against the allowlist.
	os.Setenv(key, "only/this.go")
	defer os.Unsetenv(key)

	out.Reset()
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision)
}

// TestDispatchAgent_InheritanceDepthWalkCrossesMissionTrees pins the
// fix for Bugbot MED on PR #328 ("Depth gate single-mission loader").
//
// Tier B inheritance can promote a child under an ancestor's missionID
// (M_anc) while the child's immediate PARENT_DELEGATION_ID points to a
// delegation that lives under a DIFFERENT mission tree (M_other) —
// the inheritance walker climbed from M_other up to M_anc to find a
// matching SpawnPattern. Before the fix, the depth walker's loader was
// keyed on M_anc only, so loading the parent delegation under M_other
// failed and the depth gate aborted an otherwise-valid spawn.
//
// Scenario:
//   - M_anc has Delegations[] = [{SpawnPattern: "djb", InheritsContract: true}]
//   - D_anc lives under M_anc with parent_delegation=""
//   - M_other contract is unrelated; D_p lives under M_other with
//     parent_delegation=D_anc (i.e., D_p's parent is in another tree)
//   - Child spawn: MISSION_ID="", PARENT_DELEGATION_ID=D_p,
//     CLAUDE_AGENT_TYPE="djb"
//
// Expected: inheritance walks D_p → D_anc, finds the matching template
// under M_anc, promotes the child to Tier B under M_anc. The depth
// gate then walks D_p (under M_other) → D_anc (under M_anc) → done at
// depth 2; proposed = 3, within the default limit; spawn allowed.
func TestDispatchAgent_InheritanceDepthWalkCrossesMissionTrees(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	ancMission := "m-2026-05-23-300"
	ancDelegation := "d-2026-05-23-300"
	stageContractWithDelegations(t, home, ancMission, []mission.DelegationTemplate{
		{Role: "verifier", SpawnPattern: "djb", InheritsContract: true},
	})
	stageParentDelegationSkeleton(t, repo, ancMission, ancDelegation, "")

	// Intermediate parent in a DIFFERENT mission tree. M_other's
	// contract is not stageContractWithDelegations'd — it carries no
	// matching template, so the inheritance walker climbs past it to
	// reach M_anc.
	otherMission := "m-2026-05-23-301"
	stageContractCustomWriteSet(t, home, otherMission, []string{"docs/"})
	parentDelegation := "d-2026-05-23-301"
	stageParentDelegationSkeleton(t, repo, otherMission, parentDelegation, ancDelegation)

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", parentDelegation)
	t.Setenv("CLAUDE_AGENT_TYPE", "djb")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"sess-cross"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision,
		"depth walk must follow parent_delegation across mission trees — single-mission loader would refuse here")
	assert.Equal(t, ancMission, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"inheritance must promote the child to the ancestor missionID")
	assert.NotEmpty(t, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"])
	assert.True(t, strings.HasPrefix(r.HookSpecificOutput.AdditionalEnv["MISSION_ARTIFACTS_DIR"],
		filepath.Join(repo, ".punt-labs", "ethos", "missions", ancMission)),
		"artifacts dir must nest under the inherited mission")
}

// TestDispatchAgent_ActiveMissionSidecar pins the end-to-end binding
// the active-mission sidecar exists for: a leader-in-Claude-Code call
// to Agent() cannot set MISSION_ID in its own env, but a prior
// `ethos mission claim` has staged the sidecar at
// <globalRoot>/sessions/<id>/active-mission. The PreToolUse hook
// reads the sidecar, dispatches Tier B with that missionID, and
// writes the per-delegation skeleton on disk so audit-show can
// reconstruct the binding.
//
// MISSION_ID is empty; PARENT_DELEGATION_ID is empty. The only thing
// pointing the dispatch at Tier B is the sidecar.
func TestDispatchAgent_ActiveMissionSidecar(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	missionID := "m-2026-05-23-620"
	stageContract(t, home, missionID)

	// Stage the sidecar at the path the dispatch will read.
	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-sidecar"
	require.NoError(t, mission.WriteActiveMission(globalRoot, sessionID, missionID))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "bwk")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision)
	assert.Equal(t, missionID, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"sidecar must promote the spawn to Tier B with the claimed missionID")
	assert.Equal(t, sessionID, r.HookSpecificOutput.AdditionalEnv["PARENT_SESSION_ID"])
	assert.NotEmpty(t, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"])

	artifactsDir := r.HookSpecificOutput.AdditionalEnv["MISSION_ARTIFACTS_DIR"]
	require.NotEmpty(t, artifactsDir,
		"Tier B response must include MISSION_ARTIFACTS_DIR")
	want := mission.DelegationDir(repo, missionID, r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"])
	assert.Equal(t, want, artifactsDir)

	recordPath := filepath.Join(artifactsDir, "record.yaml")
	info, err := os.Stat(recordPath)
	require.NoError(t, err,
		"sidecar dispatch must write the per-delegation skeleton — this is the audit-show binding")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	d, err := mission.LoadDelegation(recordPath)
	require.NoError(t, err)
	assert.Equal(t, mission.TierB, d.Tier,
		"sidecar dispatch must record Tier B, not Tier A")
	assert.Equal(t, missionID, d.Mission)
	assert.Equal(t, sessionID, d.ParentSession)
}

// TestDispatchAgent_ActiveMissionSidecarStaleWarns pins ethos-7vo3:
// a sidecar left pointing at a mission that has since closed must not
// take the spawn. Closing clears the sidecar in the closing session
// only, so a mission closed from anywhere else leaves the binding
// behind — and a delegation filed under a mission whose results are
// already in is a false audit trail. The spawn runs as Tier A and the
// stale binding is named on stderr.
func TestDispatchAgent_ActiveMissionSidecarStaleWarns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	missionID := "m-2026-05-23-623"
	stageClosedContract(t, home, missionID)

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-stale-sidecar"
	require.NoError(t, mission.WriteActiveMission(globalRoot, sessionID, missionID))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "bwk")
	t.Setenv("ETHOS_QUIET_ADVICE", "1")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
	var out bytes.Buffer
	warning := captureStderr(t, func() {
		require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	})

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision,
		"a stale binding must not block the spawn")
	assert.Empty(t, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"a closed mission must not take a new delegation")
	assert.Contains(t, warning, missionID, "the warning must name the stale mission")
	assert.Contains(t, warning, "closed")
}

// stageClosedContract stages a mission and walks it to closed through
// the real lifecycle — the close gate requires a result artifact, so
// there is no shortcut that leaves a well-formed contract on disk.
func stageClosedContract(t *testing.T, home, missionID string) {
	t.Helper()
	stageContract(t, home, missionID)
	store := mission.NewStore(filepath.Join(home, ".punt-labs", "ethos"))
	require.NoError(t, store.AppendResult(missionID, &mission.Result{
		Mission:    missionID,
		Round:      1,
		Author:     "bwk",
		Verdict:    mission.VerdictPass,
		Confidence: 0.9,
		Evidence:   []mission.EvidenceCheck{{Name: "make check", Status: mission.EvidenceStatusPass}},
	}))
	_, err := store.Close(missionID, mission.StatusClosed)
	require.NoError(t, err)
}

// captureStderr (generate_agents_test.go, same package) is reused here
// instead of a second local copy; see its doc comment for the
// pipe/cleanup contract every capture helper in this repo now follows.

// TestDispatchAgent_ActiveMissionSidecarPrefersEnv asserts the
// dispatch ordering: a MISSION_ID env override beats the sidecar.
// Operator-set env wins so a worker that explicitly overrides keeps
// its precedence — the sidecar is a fallback for the leader, not a
// usurper of intentional env state.
func TestDispatchAgent_ActiveMissionSidecarPrefersEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	envMission := "m-2026-05-23-621"
	sidecarMission := "m-2026-05-23-622"
	stageContractCustomWriteSet(t, home, envMission, []string{"cmd/ethos/"})
	stageContractCustomWriteSet(t, home, sidecarMission, []string{"docs/"})

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-precedence"
	require.NoError(t, mission.WriteActiveMission(globalRoot, sessionID, sidecarMission))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", envMission)
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "bwk")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, envMission, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"MISSION_ID env must win over the sidecar — the sidecar is a fallback, not an override")
}

// TestDispatchAgent_ExplicitMissionIDConsumesMatchingPendingEntry pins
// review finding P1 (qodo #2, full-branch review of PR #509,
// m-2026-09-08-004 round 3): an explicit MISSION_ID is a SUPPORTED
// override -- matchDispatchPending's own ambiguity warning tells the
// operator to set it explicitly to pick a DIFFERENT pending mission
// than FIFO would. Before this fix, the explicit-MISSION_ID branch
// never consumed a pending-dispatch entry at all, so following our own
// advice left the entry live to capture the NEXT matching-worker spawn
// too -- a second bwk spawn, with nothing to do with the mission the
// operator explicitly named, would still be silently attributed to it.
func TestDispatchAgent_ExplicitMissionIDConsumesMatchingPendingEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	missionID := "m-2026-09-08-742"
	stageContract(t, home, missionID) // Worker: "bwk", status: open

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-explicit-consumes-pending"
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionID, "bwk"))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", missionID)
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "bwk")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, missionID, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"])

	pending, _, err := mission.ReadDispatchPending(globalRoot, sessionID)
	require.NoError(t, err)
	assert.Empty(t, pending,
		"an explicit MISSION_ID matching a pending entry must consume it -- otherwise the entry survives to capture the next matching-worker spawn too")
}

// TestDispatchAgent_ActiveMissionSidecarMalformedFallsThrough asserts
// the non-blocking contract: a sidecar pointing at a missionID the
// store cannot Load surfaces the Tier B refusal (named MISSION_ID),
// not a silent fall-through. djb's rule: malformed env never silently
// admits. The sidecar takes the same path as MISSION_ID env once a
// non-empty value is read.
func TestDispatchAgent_ActiveMissionSidecarMalformedRefuses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-bad-sidecar"
	require.NoError(t, mission.WriteActiveMission(globalRoot, sessionID, "m-2026-05-23-999"))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "bwk")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision,
		"a sidecar pointing at an unresolvable mission must block (same contract as MISSION_ID env)")
	// H1 (full-branch review, m-2026-09-08-004 round 3): a claim-bound
	// block must name the actual clearable source (the sidecar) and its
	// remedy (`ethos mission release`), not a generic "MISSION_ID" phrase
	// that would send the operator looking at the wrong thing.
	assert.Contains(t, r.HookSpecificOutput.PermissionDecisionReason, "m-2026-05-23-999")
	assert.Contains(t, r.HookSpecificOutput.PermissionDecisionReason, "ethos mission release")
}

// TestDispatchAgent_ActiveMissionSidecarLegacyDispatchOriginNotCaptured
// pins review finding K7(b) (full-branch review, m-2026-09-08-004 round
// 3): C2's fix (round 3, hardened further by the BindOriginUnknown
// change) was verified against a MALFORMED origin file — truncated or
// naming a different mission — but never against the WELL-FORMED shape
// that actually exists in the field: a matching active-mission +
// active-mission-origin pair naming BindOriginDispatch, exactly what
// the CURRENTLY RELEASED (pre-round-3) `bindDispatchedMission` writes
// on every dispatch, for every ethos install upgrading into this round.
// On upgrade, any session with such a pair still on disk from before
// the upgrade must not have its next same-type spawn captured by it —
// readActiveMissionForDispatch's claim branch must refuse a
// BindOriginDispatch-tagged binding exactly like it refuses
// BindOriginUnknown, since only BindOriginClaim is ever accepted.
func TestDispatchAgent_ActiveMissionSidecarLegacyDispatchOriginNotCaptured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-legacy-dispatch-origin"
	missionID := "m-2026-09-08-730"
	stageContract(t, home, missionID) // Worker: "bwk", status: open

	// The exact well-formed shape the pre-round-3 bindDispatchedMission
	// wrote: active-mission and active-mission-origin agree on the same
	// mission, origin tagged "dispatch". WriteActiveMissionOrigin writes
	// both files consistently, unlike the malformed-origin tests above
	// which construct a deliberately broken pair.
	require.NoError(t, mission.WriteActiveMissionOrigin(globalRoot, sessionID, missionID, mission.BindOriginDispatch))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "bwk")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.NotEqual(t, missionID, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"a well-formed legacy dispatch-origin pair must never capture a spawn -- only a claim can")
}

// TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_MismatchedWorkerNotCaptured
// pins DES-076's regression case, reproduced live 2026-09-07 (ethos-7tqd):
// `ethos mission dispatch --worker bwk` binds the session's
// active-mission sidecar immediately. Before this fix, ANY next Agent()
// spawn in the session — regardless of its own agent type — was
// attributed to that mission as a Tier B delegation, even work with
// nothing to do with it. This spawns a DIFFERENT agent type than the
// contract's declared Worker and asserts it is neither promoted to Tier
// B nor recorded as a delegation, and that the dispatch binding is left
// in place for the real worker's later spawn.
//
// Confirmed failing against the pre-fix code: MISSION_ID was populated
// in additional_env and record.yaml existed under delegations/ for the
// mismatched agent type.
func TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_MismatchedWorkerNotCaptured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	missionID := "m-2026-09-08-700"
	stageContract(t, home, missionID) // Worker: "bwk"

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-dispatch-mismatch"
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionID, "bwk"))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "mdm") // NOT the dispatched worker (bwk)
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.HookSpecificOutput.PermissionDecision,
		"an unrelated spawn must not be blocked by someone else's dispatch binding")
	assert.Empty(t, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"a spawn whose agent type does not match the dispatched Worker must not be captured")

	delegationsDir := filepath.Join(mission.RepoStatePath(repo, "missions"), missionID, "delegations")
	entries, err := os.ReadDir(delegationsDir)
	if err == nil {
		assert.Empty(t, entries, "no delegation record may exist for the mismatched spawn")
	} else {
		assert.True(t, os.IsNotExist(err), "delegations dir should not exist at all: %v", err)
	}

	// Review finding C1 (m-2026-09-08-004 round 2): the pending entry
	// itself, not just a mission-ID string, must survive so the real
	// worker's later spawn can still consume it. Checking the entry
	// directly (rather than a bare mission-ID read) closes the same
	// class of gap review finding F6 (round 1) flagged: it is not
	// possible for this entry to silently degrade into an unrelated
	// binding shape the way the old shared active-mission/origin pair
	// could, because a pending-dispatch entry has no second file to
	// drift out of sync with.
	entriesList, warnings, err := mission.ReadDispatchPending(globalRoot, sessionID)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, entriesList, 1, "the dispatch binding must survive an unrelated spawn so the real worker can still consume it")
	assert.Equal(t, missionID, entriesList[0].MissionID)
	assert.Equal(t, "bwk", entriesList[0].Worker)
}

// TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_MatchingWorkerConsumesBinding
// pins the single-use half of DES-076: the ONE spawn whose agent type
// matches the contract's declared Worker is bound Tier B, and the
// dispatch binding is cleared immediately afterward so it cannot also
// capture whatever spawns next.
func TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_MatchingWorkerConsumesBinding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	missionID := "m-2026-09-08-701"
	stageContract(t, home, missionID) // Worker: "bwk"

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-dispatch-match"
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionID, "bwk"))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "bwk") // the dispatched worker
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, missionID, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"the matching worker's spawn must be bound Tier B")

	entriesList, _, err := mission.ReadDispatchPending(globalRoot, sessionID)
	require.NoError(t, err)
	assert.Empty(t, entriesList,
		"a dispatch binding is single-use — it must be cleared once its matching spawn consumes it")

	// A second spawn in the same session, even of the same agent type,
	// must NOT find a mission to bind to: the binding is gone.
	var out2 bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out2))
	var r2 PreToolUseResult
	require.NoError(t, json.Unmarshal(out2.Bytes(), &r2))
	assert.Empty(t, r2.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"a consumed dispatch binding must not resurrect itself for a later spawn")
}

// TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_UsesToolInputSubagentType
// is review finding C4 (m-2026-09-08-004 round 2): `subagent_type`
// appeared in ZERO test files repo-wide before this test, even though
// it is spawnAgentType's PRIMARY input and the one a real leader
// Agent() call actually sets — every prior test drove agent type
// through CLAUDE_AGENT_TYPE with an empty tool_input, leaving the
// tool_input branch (and therefore its precedence over the env var)
// completely unexercised. Sets subagent_type and CLAUDE_AGENT_TYPE to
// DIFFERENT values and asserts both that the pending-dispatch match
// uses subagent_type, and that the written record.yaml's AgentType
// field also reflects it, not the env var.
func TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_UsesToolInputSubagentType(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)

	missionID := "m-2026-09-08-706"
	stageContract(t, home, missionID) // Worker: "bwk"

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-dispatch-tool-input"
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionID, "bwk"))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "mdm") // deliberately DIFFERENT from tool_input
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payloadMap := map[string]any{
		"tool_name":  "Agent",
		"tool_input": map[string]any{"subagent_type": "bwk"},
		"session_id": sessionID,
	}
	data, err := json.Marshal(payloadMap)
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(bytes.NewReader(data), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, missionID, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"the match must use tool_input's subagent_type ('bwk'), not CLAUDE_AGENT_TYPE ('mdm')")

	delegationID := r.HookSpecificOutput.AdditionalEnv["DELEGATION_ID"]
	require.NotEmpty(t, delegationID)
	recordPath := filepath.Join(mission.DelegationDir(repo, missionID, delegationID), "record.yaml")
	d, err := mission.LoadDelegation(recordPath)
	require.NoError(t, err)
	assert.Equal(t, "bwk", d.AgentType,
		"the written delegation record must carry tool_input's subagent_type, not the env var")
}

// TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_TwoPendingSameWorkerCoexist
// is the direct regression test for review finding C1 (m-2026-09-08-004
// round 2), reproducing the leader's own repro sequence: two missions
// dispatched to the SAME Worker handle back to back, before either
// spawn happens. Before this fix, the second `dispatch --worker bwk`
// silently discarded the first mission's binding (single overwritable
// slot); the first mission's eventual worker spawn would then be
// misattributed to the second mission, and the second mission's own
// worker spawn would go completely unattributed (Tier A). This repo
// pins ONE handle per specialty domain, so this exact sequence — not a
// corner case — is the normal two-mission dispatch workflow.
func TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_TwoPendingSameWorkerCoexist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	missionA := "m-2026-09-08-704"
	missionB := "m-2026-09-08-705"
	stageContract(t, home, missionA)                                  // Worker: "bwk"
	stageContractCustomWriteSet(t, home, missionB, []string{"docs/"}) // Worker: "bwk", disjoint write_set

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-dispatch-two-pending"
	// Dispatch A, then B, before either spawn — the exact sequence C1
	// reported. Sleep a tick between writes so mtime ordering (the FIFO
	// discriminator) is unambiguous on filesystems with coarse mtime
	// resolution.
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionA, "bwk"))
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionB, "bwk"))

	entriesList, _, err := mission.ReadDispatchPending(globalRoot, sessionID)
	require.NoError(t, err)
	require.Len(t, entriesList, 2, "both pending dispatches must coexist -- neither overwrites the other")

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "bwk")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`

	// First bwk spawn: FIFO resolves to the OLDER pending dispatch, A —
	// exactly the leader's own dispatch-then-spawn ordering.
	var out1 bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out1))
	var r1 PreToolUseResult
	require.NoError(t, json.Unmarshal(out1.Bytes(), &r1))
	assert.Equal(t, missionA, r1.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"the first matching spawn must resolve to the OLDER pending dispatch")

	// Second bwk spawn: A's entry is consumed, so this one resolves to
	// B — no misattribution, no unattributed Tier A fallback.
	var out2 bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out2))
	var r2 PreToolUseResult
	require.NoError(t, json.Unmarshal(out2.Bytes(), &r2))
	assert.Equal(t, missionB, r2.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"the second matching spawn must resolve to the remaining pending dispatch, not fall through to Tier A")

	remaining, _, err := mission.ReadDispatchPending(globalRoot, sessionID)
	require.NoError(t, err)
	assert.Empty(t, remaining, "both pending dispatches must be consumed after their matching spawns")
}

// setContractStatus rewrites the on-disk contract's status field
// directly, for tests that need a mission to exist as non-open without
// walking the full Close/Abandon lifecycle.
func setContractStatus(t *testing.T, home, missionID, status string) {
	t.Helper()
	root := filepath.Join(home, ".punt-labs", "ethos")
	found := ""
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".yaml") && strings.Contains(p, missionID) {
			found = p
		}
		return nil
	}))
	require.NotEmpty(t, found, "contract file not found under %s", root)
	data, err := os.ReadFile(found)
	require.NoError(t, err)
	out := strings.Replace(string(data), "status: open", "status: "+status, 1)
	require.NotEqual(t, string(data), out, "status: open not present in %s", found)
	if status != mission.StatusOpen {
		// decodeAndValidate refuses a terminal status with no closed_at
		// -- a hand-edited contract missing it fails to Load entirely
		// (a validation error, not a clean non-open read), which is a
		// different failure shape than the one these tests target.
		out += "\nclosed_at: \"2026-09-08T00:00:00Z\"\n"
	}
	require.NoError(t, os.WriteFile(found, []byte(out), 0o600))
}

// TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_StaleEntryDoesNotHeadOfLineBlock
// closes a gap a local review probe found in the FIFO redesign above: a
// pending entry naming a NON-OPEN mission (e.g. one abandoned after it
// was dispatched but before its worker ever spawned) was never
// consumed, and FIFO always re-selects the OLDEST matching entry first
// — so that one stale entry permanently blocked every NEWER pending
// dispatch for the same Worker from ever being reached, for the rest
// of the session. matchDispatchPending now skips AND clears any entry
// it can PROVE is non-open (a successful Load reporting a non-open
// status), while still handing a genuinely UNRESOLVABLE entry (a Load
// failure) to dispatchTierB's own gate unchanged, per C3's doctrine.
func TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_StaleEntryDoesNotHeadOfLineBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	missionA := "m-2026-09-08-707"
	missionB := "m-2026-09-08-708"
	stageContract(t, home, missionA)
	stageContractCustomWriteSet(t, home, missionB, []string{"docs/"})
	setContractStatus(t, home, missionA, "abandoned")

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-stale-head-of-line"
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionA, "bwk"))
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionB, "bwk"))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "bwk")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, missionB, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"the stale entry for the abandoned mission must be skipped, not permanently block mission B")

	remaining, _, err := mission.ReadDispatchPending(globalRoot, sessionID)
	require.NoError(t, err)
	assert.Empty(t, remaining, "the stale entry must be cleared, and the matched entry consumed")
}

// TestMatchDispatchPending_AmbiguitySignal pins the FIFO ambiguity
// warning requested against m-2026-09-08-004 round 3: when two or more
// LIVE pending dispatches match the spawning worker, matchDispatchPending
// still resolves to the oldest (FIFO is unchanged), but must name the
// ambiguity on stderr — the count, the worker, every competing mission
// ID, which one was chosen, and that MISSION_ID overrides the match.
//
// Confirmed failing against the pre-fix code: matchDispatchPending
// returned the first match with no signal of the second candidate at
// all.
func TestMatchDispatchPending_AmbiguitySignal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	missionA := "m-2026-09-08-712"
	missionB := "m-2026-09-08-713"
	stageContract(t, home, missionA)
	stageContractCustomWriteSet(t, home, missionB, []string{"docs/"})

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-fifo-ambiguity"
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionA, "bwk"))
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionB, "bwk"))

	var matched string
	stderrText := captureStderr(t, func() {
		matched = matchDispatchPending(globalRoot, sessionID, "bwk")
	})

	assert.Equal(t, missionA, matched, "FIFO still resolves to the oldest entry")
	assert.Contains(t, stderrText, "2 pending dispatches match worker \"bwk\"")
	assert.Contains(t, stderrText, missionA)
	assert.Contains(t, stderrText, missionB)
	assert.Contains(t, stderrText, "MISSION_ID")
}

// TestMatchDispatchPending_NoAmbiguitySignalForDifferentWorkers is the
// negative case the leader called out explicitly: a session with
// pending dispatches for two DIFFERENT workers is not ambiguous for
// either one's spawn and must stay quiet.
func TestMatchDispatchPending_NoAmbiguitySignalForDifferentWorkers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	missionA := "m-2026-09-08-714"
	missionB := "m-2026-09-08-715"
	stageContract(t, home, missionA)                                  // Worker: "bwk"
	stageContractCustomWriteSet(t, home, missionB, []string{"docs/"}) // Worker: "bwk" too, but re-tagged rmh below

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-no-ambiguity-cross-worker"
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionA, "bwk"))
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionB, "rmh"))

	var matched string
	stderrText := captureStderr(t, func() {
		matched = matchDispatchPending(globalRoot, sessionID, "bwk")
	})

	assert.Equal(t, missionA, matched)
	assert.NotContains(t, stderrText, "pending dispatches match",
		"one match for this spawn's worker is not ambiguous, even with another pending dispatch for a different worker")
}

// TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_UnloadableMissionNamesRemedy
// pins review finding H1 (full-branch review, m-2026-09-08-004 round 3):
// a spawn that matched a pending dispatch whose mission contract cannot
// load must be blocked with a message naming the worker, the session,
// and `ethos mission release` as the remedy — not the generic
// MISSION_ID-env wording, which sends the operator looking at an
// environment variable that was never involved.
//
// Confirmed failing against the pre-fix code: dispatchTierB's Load
// failure always produced `resolving MISSION_ID %q: %v`, regardless of
// whether missionID came from the MISSION_ID env var or a pending
// dispatch sidecar.
// This exercises dispatchTierB directly rather than through the full
// HandlePreToolUse pipeline: K1 (full-branch review, m-2026-09-08-004
// round 3) changed matchDispatchPending so a pending entry that is the
// SOLE candidate and cannot Load is skipped, not matched — it falls
// through to Tier A/B rather than reaching dispatchTierB at all (see
// TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_UnresolvableEntrySkipsNotBlocks).
// dispatchTierB's own boundVia-aware messaging (H1) is still reachable
// for a dispatch-bound missionID via the narrower TOCTOU race the
// function's own doc comment describes (matched-open at classify time,
// gone by dispatchTierB's own Load) — calling it directly pins that
// messaging without needing to fabricate that race.
func TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_UnloadableMissionNamesRemedy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	missionID := "m-2026-09-08-711"
	sessionID := "sess-unloadable-dispatch"
	// Deliberately never staged: store.Load(missionID) must fail.

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	var out bytes.Buffer
	toolInput := map[string]any{"subagent_type": "bwk"}
	require.NoError(t, dispatchTierB(&out, sessionID, missionID, toolInput, mission.BoundViaSidecarDispatch, nil))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "deny", r.HookSpecificOutput.PermissionDecision)
	reason := r.HookSpecificOutput.PermissionDecisionReason
	assert.Contains(t, reason, sessionID)
	assert.Contains(t, reason, "bwk")
	assert.Contains(t, reason, missionID)
	assert.Contains(t, reason, "ethos mission release")
	assert.NotContains(t, reason, "MISSION_ID",
		"a pending-dispatch block must not read as a MISSION_ID environment-variable problem")
}

// TestMatchDispatchPending_ConcurrentCallsNeverDoubleMatch closes a
// second gap a local review probe found: two concurrent calls to
// matchDispatchPending (standing in for two Agent() tool calls the
// leader batched in one turn — an explicitly encouraged pattern in
// this org's own conventions) could both read the pending store before
// either consumed its match, resolving to the SAME oldest entry twice.
// dispatchAgent now holds AcquireDispatchPendingLock across the whole
// match-through-admit sequence; this test exercises matchDispatchPending
// directly under that same lock discipline to prove two serialized
// callers never collide.
func TestMatchDispatchPending_ConcurrentCallsNeverDoubleMatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	missionA := "m-2026-09-08-709"
	missionB := "m-2026-09-08-710"
	stageContract(t, home, missionA)
	stageContractCustomWriteSet(t, home, missionB, []string{"docs/"})

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-concurrent-match"
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionA, "bwk"))
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionB, "bwk"))

	match := func() string {
		release, err := mission.AcquireDispatchPendingLock(globalRoot, sessionID)
		require.NoError(t, err)
		defer release()
		m := matchDispatchPending(globalRoot, sessionID, "bwk")
		if m != "" {
			require.NoError(t, mission.ConsumeDispatchPending(globalRoot, sessionID, m))
		}
		return m
	}

	m1 := match()
	m2 := match()
	assert.NotEqual(t, m1, m2, "two serialized matches must never resolve to the same mission")
	assert.ElementsMatch(t, []string{missionA, missionB}, []string{m1, m2})
}

// TestDispatchAgent_ActiveMissionSidecarClaimOrigin_StaysAfterConsume is
// the non-regression guard for DES-076: an `ethos mission claim`
// binding (BindOriginClaim) is the operator explicitly saying "I am
// working on this," and stays sticky across every spawn in the session
// until an explicit claim/release — the worker-match consumption rule
// applies ONLY to BindOriginDispatch.
func TestDispatchAgent_ActiveMissionSidecarClaimOrigin_StaysAfterConsume(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	missionID := "m-2026-09-08-702"
	stageContract(t, home, missionID) // Worker: "bwk"

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-claim-sticky"
	require.NoError(t, mission.WriteActiveMission(globalRoot, sessionID, missionID))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "some-other-agent") // deliberately NOT Worker
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, missionID, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"a claim binds regardless of agent type — it is not gated on Worker")

	after, err := mission.ReadActiveMission(globalRoot, sessionID)
	require.NoError(t, err)
	assert.Equal(t, missionID, after,
		"a claim binding must stay sticky after use — only dispatch bindings are single-use")

	// K11(a) (full-branch review, m-2026-09-08-004 round 3): the dispatch
	// side of this same mirror (round 1's F6) asserts Origin explicitly,
	// not only the mission ID that survives both a claim and a stale
	// dispatch shape identically. The claim side was never made
	// symmetric — assert it never silently converted to a dispatch
	// origin, which ReadActiveMission's bare mission-ID string could
	// never distinguish from this claim staying a claim.
	binding, err := mission.ReadActiveMissionBinding(globalRoot, sessionID)
	require.NoError(t, err)
	assert.Equal(t, mission.BindOriginClaim, binding.Origin,
		"a claim binding must still read back as a claim after use, never drift to another origin")
}

// TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_MatchedButUnresolvableContractBlocks
// is review findings C3/C8 (m-2026-09-08-004 round 2), and a deliberate
// DOCTRINE REVISION from DES-076 round 1: round 1's matching path
// needed a contract Load to even discover a pending mission's Worker,
// so a Load failure during matching was genuinely ambiguous (mismatch,
// or unresolvable match?) and had to fall through non-blocking. Round
// 3 records the Worker directly in the pending-dispatch file at
// dispatch time (see active.go's dispatch-pending doc comment), so
// matching a spawn against a pending entry needs no Load at all — a
// match is certain BEFORE any Load is attempted. A contract that then
// fails to load for an ALREADY-MATCHED pending dispatch is therefore
// structurally identical to an explicit MISSION_ID naming an unloadable
// contract (case 1): a genuine, actionable problem for THIS spawn, not
// ambient noise sitting behind every spawn in the session. dispatchTierB
// already blocks on that case; this test pins that the same
// (Load-failure -> block) path is now reached for a matched pending
// dispatch too, so the dispatched worker's absence from the audit trail
// is a loud refusal, never a silent, unaudited Tier A fallback (the
// exact defect review finding C3 reported against round 1's code,
// which discarded the Load error and printed a misleading `worker ""`
// mismatch message for what was actually the correctly dispatched
// worker).
// TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_UnresolvableEntrySkipsNotBlocks
// pins review finding K1 (full-branch review, m-2026-09-08-004 round 3),
// which reversed this test's prior assertion. round 3 originally folded
// an unresolvable pending entry (Load fails) into the match and let
// dispatchTierB deny the spawn -- but since a Load failure is NOT
// deletable evidence (C3's doctrine) and FIFO always re-selects the
// SAME oldest entry, an unloadable entry at the head of the queue
// denied EVERY subsequent same-worker spawn forever: reachable without
// exotic faults, since mission contracts are git-tracked and
// `mission dispatch` followed by `git checkout` to a branch without the
// contract file reproduces it directly. The entry is now SKIPPED (not
// matched, not cleared) when it is the only candidate, so the spawn
// falls through to Tier A/B instead of being denied permanently.
func TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_UnresolvableEntrySkipsNotBlocks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)
	// Deliberately no stageContract call: the pending entry names a
	// mission the store cannot Load, but the Worker is already known
	// from the pending file itself (dispatch time recorded it), so the
	// match attempt happens before any Load.

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-dispatch-unresolvable"
	missionID := "m-2026-09-08-703"
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, missionID, "bwk"))

	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("PARENT_DELEGATION_ID", "")
	t.Setenv("CLAUDE_AGENT_TYPE", "bwk")
	t.Setenv("ETHOS_QUIET_ADVICE", "")
	t.Setenv("PARENT_SESSION_ID", "")

	payload := `{"tool_name":"Agent","tool_input":{},"session_id":"` + sessionID + `"}`
	var out bytes.Buffer
	stderrText := captureStderr(t, func() {
		require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	})

	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.NotEqual(t, "deny", r.HookSpecificOutput.PermissionDecision,
		"an unresolvable pending dispatch must not deny the spawn forever -- it falls through to Tier A/B")
	assert.NotEqual(t, missionID, r.HookSpecificOutput.AdditionalEnv["MISSION_ID"],
		"the unresolvable mission must never be bound as the spawn's MISSION_ID")

	assert.Contains(t, stderrText, missionID)
	assert.Contains(t, stderrText, "ethos mission release")

	// The pending entry is left in place -- unproven, not deleted (C3's
	// doctrine): the failure may be transient and the contract may come
	// back on a later branch switch or retry.
	entriesList, _, err := mission.ReadDispatchPending(globalRoot, sessionID)
	require.NoError(t, err)
	require.Len(t, entriesList, 1)
	assert.Equal(t, missionID, entriesList[0].MissionID)
}

// TestMatchDispatchPending_UnresolvableEntryDoesNotBlockNewerEntry is
// K1's other half: an unresolvable entry at the head of the queue must
// not block a newer, resolvable entry for the same worker from being
// matched. Before this fix, the unresolvable entry was always picked
// (oldest wins FIFO) and returned as the sole candidate, so a
// perfectly valid newer entry was unreachable behind it for the rest of
// the session's life.
func TestMatchDispatchPending_UnresolvableEntryDoesNotBlockNewerEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageRepoRoot(t)

	unresolvable := "m-2026-09-08-716"
	resolvable := "m-2026-09-08-717"
	stageContract(t, home, resolvable) // Worker: "bwk"
	// unresolvable is deliberately never staged.

	globalRoot := filepath.Join(home, ".punt-labs", "ethos")
	sessionID := "sess-unresolvable-then-resolvable"
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, unresolvable, "bwk"))
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, mission.WriteDispatchPending(globalRoot, sessionID, resolvable, "bwk"))

	var matched string
	stderrText := captureStderr(t, func() {
		matched = matchDispatchPending(globalRoot, sessionID, "bwk")
	})

	assert.Equal(t, resolvable, matched,
		"the unresolvable head entry must not block a newer, resolvable entry for the same worker")
	assert.Contains(t, stderrText, unresolvable)
	assert.Contains(t, stderrText, "ethos mission release")
	assert.NotContains(t, stderrText, "2 pending dispatches match",
		"the unresolvable entry is skipped, not a live candidate -- there is no genuine ambiguity here")

	// The skipped entry is never cleared -- only the matched one is
	// consumed by the caller (ConsumeDispatchPending is not called by
	// matchDispatchPending itself for a live match either).
	entriesList, _, err := mission.ReadDispatchPending(globalRoot, sessionID)
	require.NoError(t, err)
	require.Len(t, entriesList, 2)
}
