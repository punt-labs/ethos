package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

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
	assert.Equal(t, "allow", result.Decision)
	assert.Empty(t, result.Reason)
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
			name:     "Read file outside allowlist",
			tool:     "Read",
			input:    map[string]any{"file_path": "internal/session/store.go"},
			decision: "block",
		},
		{
			name:     "Write file outside allowlist",
			tool:     "Write",
			input:    map[string]any{"file_path": "/etc/passwd"},
			decision: "block",
		},
		{
			name:     "Edit file outside allowlist",
			tool:     "Edit",
			input:    map[string]any{"file_path": "go.mod"},
			decision: "block",
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
			name:     "Read with no file_path field",
			tool:     "Read",
			input:    map[string]any{},
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
			assert.Equal(t, tt.decision, result.Decision, "tool=%s path=%v", tt.tool, tt.input)
			if tt.decision == "block" {
				assert.NotEmpty(t, result.Reason)
			}
		})
	}
}

func TestHandlePreToolUse_DirectoryEntryAllowsChildren(t *testing.T) {
	// A directory entry in the allowlist permits any file under it.
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "internal/hook/:cmd/ethos/")

	tests := []struct {
		name     string
		path     string
		decision string
	}{
		{"file under allowed dir", "internal/hook/pretooluse.go", "allow"},
		{"nested file under allowed dir", "internal/hook/deep/nested.go", "allow"},
		{"dir entry itself", "internal/hook", "allow"},
		{"sibling dir blocked", "internal/mission/store.go", "block"},
		{"partial prefix not matched", "internal/hookextra/file.go", "block"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := map[string]any{
				"tool_name":  "Read",
				"tool_input": map[string]any{"file_path": tt.path},
			}
			data, err := json.Marshal(payload)
			require.NoError(t, err)

			var out bytes.Buffer
			err = HandlePreToolUse(strings.NewReader(string(data)), &out)
			require.NoError(t, err)

			var result PreToolUseResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &result))
			assert.Equal(t, tt.decision, result.Decision, "path=%s", tt.path)
		})
	}
}

func TestHandlePreToolUse_GlobAndGrep(t *testing.T) {
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "internal/hook/")

	tests := []struct {
		name     string
		tool     string
		path     string
		decision string
	}{
		{"Glob inside allowlist", "Glob", "internal/hook", "allow"},
		{"Glob outside allowlist", "Glob", "internal/mission", "block"},
		{"Grep inside allowlist", "Grep", "internal/hook", "allow"},
		{"Grep outside allowlist", "Grep", "/some/other/path", "block"},
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
			assert.Equal(t, tt.decision, result.Decision, "tool=%s path=%s", tt.tool, tt.path)
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
		{"Read", "Read", map[string]any{"file_path": "/a/b.go"}, "/a/b.go"},
		{"Write", "Write", map[string]any{"file_path": "x.go"}, "x.go"},
		{"Edit", "Edit", map[string]any{"file_path": "y.go"}, "y.go"},
		{"Glob", "Glob", map[string]any{"path": "/some/dir"}, "/some/dir"},
		{"Grep", "Grep", map[string]any{"path": "src/"}, "src/"},
		{"Bash", "Bash", map[string]any{"command": "ls"}, ""},
		{"nil input", "Read", nil, ""},
		{"missing key", "Read", map[string]any{"other": "val"}, ""},
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

// TestHandlePreToolUse_EnvVarFromSubagentStart verifies end-to-end
// that the env var format produced by buildVerifierAllowlistEnv is
// correctly consumed by HandlePreToolUse.
func TestHandlePreToolUse_EnvVarFromSubagentStart(t *testing.T) {
	// Simulate the env var that SubagentStart would set.
	allowlist := "internal/hook/pretooluse.go:internal/hook/pretooluse_test.go:/home/user/missions/m-001.yaml"
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", allowlist)

	// Allowed: file in the allowlist.
	payload := `{"tool_name":"Read","tool_input":{"file_path":"internal/hook/pretooluse.go"}}`
	var out bytes.Buffer
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.Decision)

	// Blocked: file not in the allowlist.
	out.Reset()
	payload = `{"tool_name":"Read","tool_input":{"file_path":"internal/session/store.go"}}`
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "block", r.Decision)
	assert.Contains(t, r.Reason, "outside the verifier file allowlist")
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
	assert.Equal(t, "allow", r.Decision)
}

// Confirm that os.Getenv is the actual mechanism (not a mock).
func TestHandlePreToolUse_ReadsRealEnvVar(t *testing.T) {
	key := "ETHOS_VERIFIER_ALLOWLIST"

	// Unset: passthrough.
	t.Setenv(key, "")
	var out bytes.Buffer
	payload := `{"tool_name":"Read","tool_input":{"file_path":"anything.go"}}`
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	var r PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.Decision)

	// Set: enforced.
	os.Setenv(key, "only/this.go")
	defer os.Unsetenv(key)

	out.Reset()
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "block", r.Decision)
}
