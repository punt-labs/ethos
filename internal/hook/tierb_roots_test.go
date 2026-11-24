package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatchAgentDeniesExplicitMissionWhenHomeCannotBeResolved(t *testing.T) {
	t.Setenv("HOME", "relative-home")
	t.Setenv("USERPROFILE", "relative-home")
	t.Setenv("MISSION_ID", "m-2026-05-22-999")
	t.Setenv("PARENT_DELEGATION_ID", "")

	var out bytes.Buffer
	require.NoError(t, dispatchAgent(&out, "session-1", nil, resolve.Located{}))

	var result PreToolUseResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "deny", result.HookSpecificOutput.Decision())
	assert.Contains(t, result.HookSpecificOutput.PermissionDecisionReason, "home directory must be absolute")
}

// tierBMissionStore builds the store from the roots dispatchAgent located,
// and looks nothing up: with a working directory elsewhere (and an
// ETHOS_REPO_ROOT that would answer differently) the store still points at
// the roots it was given.
func TestTierBMissionStoreUsesTheRootsItIsGiven(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	store, checkout := t.TempDir(), t.TempDir()
	other := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(other, ".punt-labs", "ethos"), 0o755))
	t.Setenv("ETHOS_REPO_ROOT", other) // a second lookup would answer with this
	t.Chdir(t.TempDir())

	ms, err := tierBMissionStore(tierBRoots{store: store, checkout: checkout})
	require.NoError(t, err)
	assert.Equal(t, store, ms.RepoRoot())
	assert.Equal(t, checkout, ms.CheckoutRoot())
}
