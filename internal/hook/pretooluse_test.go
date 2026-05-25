package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/mission"
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
		name    string
		tool    string
		path    string
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
	assert.Contains(t, r.HookSpecificOutput.PermissionDecisionReason, "MISSION_ID")
}
