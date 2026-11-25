package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/punt-labs/ethos/v4/internal/mission"
)

// Feed the real producer's complete env map unchanged to the real consumer.
// No splitting, rejoining, renaming keys or handwritten allowlist repairs are
// permitted in this harness: delimiter/key drift must affect hook decisions.
func TestVerifierEnvironmentComposition(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := stageRepoRoot(t)
	const missionID = "m-2026-09-08-804"
	stageContractCustomWriteSet(t, home, missionID, []string{"src/first.go", "src/second.go"})
	store := mission.NewStore(filepath.Join(home, ".punt-labs", "ethos"))
	c, err := store.Load(missionID)
	require.NoError(t, err)
	c.WriteSet = append(c.WriteSet, "write-dir/**")
	c.ExtractInto = []string{"extract-one/", "extract-two/", "extract-link/"}
	require.NoError(t, store.Update(c))
	c, err = store.Load(missionID)
	require.NoError(t, err)
	contractPath, err := store.ContractPath(missionID)
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(contractPath), "contract entry must exercise an absolute store path")
	require.FileExists(t, contractPath)
	for _, path := range []string{"src/first.go", "src/second.go", "extract-one/existing.go", "extract-two/existing.go"} {
		abs := filepath.Join(repo, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o700))
		require.NoError(t, os.WriteFile(abs, []byte("package fixture\n"), 0o600))
	}
	alias := filepath.Join(t.TempDir(), "checkout-alias")
	require.NoError(t, os.Symlink(repo, alias))
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "existing.go"), nil, 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(repo, "write-dir"), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(repo, "write-dir", "escape")))
	require.NoError(t, os.Symlink(outside, filepath.Join(repo, "extract-link")))
	require.NoError(t, os.Symlink(filepath.Join(outside, "missing.go"), filepath.Join(repo, "extract-one", "dangling.go")))
	require.NoError(t, os.Symlink(filepath.Join(repo, "src"), filepath.Join(repo, "undeclared")))

	// Clear ambient verifier state first. A producer key mutation must not
	// inherit a working value from the operator or another test.
	t.Setenv("ETHOS_VERIFIER_ALLOWLIST", "")
	t.Setenv("ETHOS_VERIFIER_EXTRACT_INTO", "")
	t.Setenv("MISSION_ID", "")
	t.Setenv("ETHOS_REPO_ROOT", repo)
	env := buildVerifierAllowlistEnv([]verifierMission{{Contract: c}}, store)
	require.NotEmpty(t, env)
	for key, value := range env {
		t.Setenv(key, value)
	}

	tests := []struct {
		name     string
		tool     string
		path     string
		decision string
	}{
		{"write first declared file", "Write", filepath.Join(repo, "src", "first.go"), ""},
		{"edit second declared file", "Edit", filepath.Join(repo, "src", "second.go"), ""},
		{"write absolute contract", "Write", contractPath, ""},
		{"edit absolute contract", "Edit", contractPath, ""},
		{"create in first extract directory", "Write", filepath.Join(repo, "extract-one", "new.go"), ""},
		{"create in second extract directory", "Write", filepath.Join(repo, "extract-two", "new.go"), ""},
		{"deny existing first extract file", "Write", filepath.Join(repo, "extract-one", "existing.go"), "deny"},
		{"deny existing second extract file", "Edit", filepath.Join(repo, "extract-two", "existing.go"), "deny"},
		{"deny sibling of declared file", "Write", filepath.Join(repo, "src", "third.go"), "deny"},
		{"deny outside extract directories", "Edit", filepath.Join(repo, "elsewhere", "new.go"), "deny"},
		{"read undeclared existing file", "Read", filepath.Join(repo, "extract-one", "existing.go"), ""},
		{"write through checkout alias", "Write", filepath.Join(alias, "src", "first.go"), ""},
		{"create through checkout alias with missing parents", "Write", filepath.Join(alias, "extract-one", "nested", "new.go"), ""},
		{"deny existing extract file through alias", "Write", filepath.Join(alias, "extract-one", "existing.go"), "deny"},
		{"deny write-set symlink escape", "Edit", filepath.Join(repo, "write-dir", "escape", "existing.go"), "deny"},
		{"deny extract directory symlink escape", "Write", filepath.Join(repo, "extract-link", "new.go"), "deny"},
		{"deny dangling extract symlink", "Write", filepath.Join(repo, "extract-one", "dangling.go"), "deny"},
		{"deny undeclared symlink inside checkout", "Write", filepath.Join(repo, "undeclared", "first.go"), "deny"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(struct {
				ToolName  string            `json:"tool_name"`
				ToolInput map[string]string `json:"tool_input"`
			}{tt.tool, map[string]string{"file_path": tt.path}})
			require.NoError(t, err)
			var out bytes.Buffer
			require.NoError(t, HandlePreToolUse(strings.NewReader(string(payload)), &out))
			var result PreToolUseResult
			require.NoError(t, json.Unmarshal(out.Bytes(), &result))
			assert.Equal(t, tt.decision, result.HookSpecificOutput.Decision())
			if tt.decision == "deny" {
				assert.Contains(t, result.HookSpecificOutput.PermissionDecisionReason, "outside the verifier file allowlist")
			}
		})
	}
}
