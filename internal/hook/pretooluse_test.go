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
	assert.Equal(t, "allow", result.Decision)
	assert.Empty(t, result.Reason)
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
			assert.Equal(t, "allow", r.Decision,
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
			assert.Equal(t, "allow", result.Decision,
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
	assert.Equal(t, "allow", r.Decision)
	assert.True(t, r.Continue, "Tier B response must set continue=true")
	require.NotNil(t, r.AdditionalEnv,
		"Tier B response must include additional_env block")
	assert.Equal(t, missionID, r.AdditionalEnv["MISSION_ID"],
		"Tier B must echo MISSION_ID from the input env")
	assert.Equal(t, "sess-outer-42", r.AdditionalEnv["PARENT_SESSION_ID"],
		"Tier B must echo session_id as PARENT_SESSION_ID")
	assert.NotEmpty(t, r.AdditionalEnv["DELEGATION_ID"],
		"Tier B must allocate a fresh DELEGATION_ID")
	assert.True(t, strings.HasPrefix(r.AdditionalEnv["DELEGATION_ID"], "d-"),
		"DELEGATION_ID must use the d-YYYY-MM-DD-NNN shape")
	assert.Equal(t, r.AdditionalEnv["DELEGATION_ID"], r.AdditionalEnv["PARENT_DELEGATION_ID"],
		"PARENT_DELEGATION_ID must mirror this spawn's DELEGATION_ID so the child sees a parent in its chain (Bugbot HIGH on PR #327)")

	artifactsDir := r.AdditionalEnv["MISSION_ARTIFACTS_DIR"]
	require.NotEmpty(t, artifactsDir,
		"Tier B response must include MISSION_ARTIFACTS_DIR")
	want := mission.DelegationDir(repo, missionID, r.AdditionalEnv["DELEGATION_ID"])
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
			if r.Decision != "allow" {
				results <- result{err: errors.New("decision was not allow: " + r.Reason)}
				return
			}
			results <- result{dir: r.AdditionalEnv["MISSION_ARTIFACTS_DIR"]}
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
	dir := filepath.Join(repo, ".ethos", "missions", missionID)
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
		done <- result{decision: r.Decision, waited: waited}
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
	missionDir := filepath.Join(repo, ".ethos", "missions", missionID)
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
	assert.Equal(t, "block", r.Decision,
		"a lock-acquire failure must surface as a named block, not an allow")
	assert.Contains(t, r.Reason, "acquiring mission lock")

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
	assert.Equal(t, "block", r.Decision,
		"a config-error must surface as a named block, not an allow")
	assert.Contains(t, r.Reason, "max_delegation_depth",
		"refusal reason must name the config error")

	// The skeleton was written before the depth gate fired; the depth
	// gate's config-error branch must close it with verdict=aborted.
	// Walk the per-mission delegations directory to find the single
	// record.yaml the dispatch produced and assert its verdict.
	delegationsDir := filepath.Join(
		repo, ".ethos", "missions", missionID, "delegations",
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
	assert.Equal(t, "block", r.Decision,
		"malformed MISSION_ID must block, not fall through to Tier A")
	assert.Contains(t, r.Reason, "MISSION_ID")
	assert.Contains(t, r.Reason, "m-2026-05-22-999")
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
	assert.Equal(t, "allow", r.Decision)
	assert.True(t, r.Continue, "Tier A response must set continue=true")
	require.NotNil(t, r.AdditionalEnv,
		"Tier A response must include additional_env block")
	assert.Equal(t, "sess-outer-7", r.AdditionalEnv["PARENT_SESSION_ID"],
		"Tier A must echo session_id as PARENT_SESSION_ID")
	assert.NotEmpty(t, r.AdditionalEnv["DELEGATION_ID"],
		"Tier A must still allocate a DELEGATION_ID for audit binding")
	assert.Equal(t, r.AdditionalEnv["DELEGATION_ID"], r.AdditionalEnv["PARENT_DELEGATION_ID"],
		"PARENT_DELEGATION_ID must mirror DELEGATION_ID so a Tier A child spawn sees its parent in the chain")
	_, hasMissionID := r.AdditionalEnv["MISSION_ID"]
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
	assert.Equal(t, "allow", r.Decision)
	assert.Empty(t, r.AdditionalEnv,
		"non-Agent passthrough must not emit additional_env")
	assert.False(t, r.Continue,
		"non-Agent passthrough must not set continue=true")
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
