package hook

import (
	"bytes"
	"encoding/json"
	"io"
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
			name:     "Read file outside allowlist is allowed (read-only)",
			tool:     "Read",
			input:    map[string]any{"file_path": "internal/session/store.go"},
			decision: "allow",
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
			assert.Equal(t, tt.decision, result.Decision, "tool=%s path=%v", tt.tool, tt.input)
			if tt.decision == "block" {
				assert.NotEmpty(t, result.Reason)
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
		{"write sibling dir blocked", "internal/mission/store.go", "block"},
		{"write partial prefix not matched", "internal/hookextra/file.go", "block"},
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
			assert.Equal(t, tt.decision, result.Decision, "path=%s", tt.path)
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
	assert.Equal(t, "allow", r.Decision)

	// Blocked: Write to a file not in the allowlist.
	out.Reset()
	payload = `{"tool_name":"Write","tool_input":{"file_path":"internal/session/store.go"}}`
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "block", r.Decision)
	assert.Contains(t, r.Reason, "outside the verifier file allowlist")

	// Read is always allowed, even outside the allowlist.
	out.Reset()
	payload = `{"tool_name":"Read","tool_input":{"file_path":"internal/session/store.go"}}`
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "allow", r.Decision)
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
		assert.Equal(t, "allow", r.Decision)
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
		assert.Equal(t, "allow", r.Decision)
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
		assert.Equal(t, "allow", r.Decision)
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
		assert.Equal(t, "block", r.Decision,
			"existing file under extract_into must require write_set match")
		assert.Contains(t, r.Reason, "outside the verifier file allowlist")
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
		assert.Equal(t, "block", r.Decision)
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
		assert.Equal(t, "block", r.Decision)
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

	// Redirect stderr to a pipe so the test can read the audit line.
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = oldStderr
	})

	payload := map[string]any{
		"tool_name":  "Write",
		"tool_input": map[string]any{"file_path": target},
	}
	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var out bytes.Buffer
	hookErr := HandlePreToolUse(strings.NewReader(string(data)), &out)
	require.NoError(t, w.Close())
	os.Stderr = oldStderr
	require.NoError(t, hookErr)

	stderrBytes, err := io.ReadAll(r)
	require.NoError(t, err)
	stderrText := string(stderrBytes)

	var result PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "block", result.Decision,
		"ambiguous stat must fall through to block")
	assert.Contains(t, stderrText, "pre-tool-use: stat",
		"stderr audit line must fire on the non-IsNotExist branch")
	assert.Contains(t, stderrText, target,
		"stderr audit line must name the target path")
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
	assert.Equal(t, "allow", r.Decision)

	// Set: Write enforced against the allowlist.
	os.Setenv(key, "only/this.go")
	defer os.Unsetenv(key)

	out.Reset()
	require.NoError(t, HandlePreToolUse(strings.NewReader(payload), &out))
	require.NoError(t, json.Unmarshal(out.Bytes(), &r))
	assert.Equal(t, "block", r.Decision)
}
