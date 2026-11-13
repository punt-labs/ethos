package hook

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/v4/internal/resolve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionAgentHelpersSnapshot_RefuseCapturedFailure(t *testing.T) {
	for _, field := range []string{"store", "ethos", "config", "checkout"} {
		t.Run(field, func(t *testing.T) {
			root := t.TempDir()
			loc := resolve.Located{Store: root, Checkout: root, Ethos: filepath.Join(root, ".punt-labs", "ethos")}
			failure := errors.New("captured " + field + " failure")
			switch field {
			case "store":
				loc.StoreErr = failure
			case "ethos":
				loc.EthosErr = failure
			case "config":
				loc.ConfigErr = failure
			case "checkout":
				loc.CheckoutErr = failure
			}
			stubDir := filepath.Join(loc.Ethos, "agents")
			require.NoError(t, os.MkdirAll(stubDir, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(stubDir, "stub.md"), []byte("# Stub\n"), 0o600))
			// Nil readers make any attempt to plan past the captured failure a
			// test failure; the helper must refuse before consuming its inputs.
			plan, err := planSessionAgents(loc, nil, nil, nil)
			require.ErrorIs(t, err, failure)
			assert.Empty(t, plan.Files)
			_, installErr := installAgentDefsFor(loc, nil)
			require.ErrorIs(t, installErr, failure)
			assert.NoDirExists(t, filepath.Join(root, ".claude"))
		})
	}
}

func TestSessionStartSnapshot_BlockedInput(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, replacement := range []string{"agent: beta\nteam: beta-team\nrecord: full\n", "agent: [invalid\n"} {
			t.Run(fmt.Sprintf("explicit=%t/%s", explicit, replacement), func(t *testing.T) {
				loc, s, ss, ts, rs := snapshotHookStores(t)
				deps := SessionStartDeps{Store: s, Sessions: ss, Teams: ts, Roles: rs}
				if explicit {
					deps.Located = &loc
				}
				out, err := withSnapshotPipe(t, filepath.Join(loc.Store, ".punt-labs", "ethos.yaml"), replacement, func(r io.Reader) error {
					return HandleSessionStart(r, deps)
				})
				require.NoError(t, err)
				assert.Contains(t, out, "Alpha Agent")
				assert.Contains(t, out, "## Team: alpha-team")
				assert.NotContains(t, out, "beta-team")
				assert.FileExists(t, filepath.Join(loc.Checkout, ".claude", "agents", "gamma.md"))
				assert.NoFileExists(t, filepath.Join(loc.Checkout, ".claude", "agents", "delta.md"))
				roster, err := ss.Load("snapshot-session")
				require.NoError(t, err)
				require.GreaterOrEqual(t, len(roster.Participants), 2)
				assert.Equal(t, "alpha", roster.Participants[1].Persona)
			})
		}
	}
}

func TestSessionStartSnapshot_StubCheckoutDoesNotRediscover(t *testing.T) {
	loc, s, ss, ts, rs := snapshotHookStores(t)
	stubDir := filepath.Join(loc.Ethos, "agents")
	require.NoError(t, os.MkdirAll(stubDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "snapshot-stub.md"), []byte("# Snapshot Stub\n"), 0o644))
	otherCheckout := t.TempDir()
	// The explicit dependency snapshot remains A, even if a second discovery
	// would now choose B. No process-global environment is changed concurrently.
	t.Setenv("ETHOS_REPO_ROOT", otherCheckout)
	deps := SessionStartDeps{Located: &loc, Store: s, Sessions: ss, Teams: ts, Roles: rs}
	out, err := withSnapshotPipe(t, filepath.Join(loc.Store, ".punt-labs", "ethos.yaml"), "agent: beta\nteam: beta-team\n", func(r io.Reader) error {
		return HandleSessionStart(r, deps)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "Alpha Agent")
	assert.FileExists(t, filepath.Join(loc.Checkout, ".claude", "agents", "snapshot-stub.md"))
	assert.NoFileExists(t, filepath.Join(otherCheckout, ".claude", "agents", "snapshot-stub.md"))
	assert.FileExists(t, filepath.Join(loc.Checkout, ".claude", "agents", "gamma.md"))
	assert.NoFileExists(t, filepath.Join(otherCheckout, ".claude", "agents", "gamma.md"))
}

func TestSessionStartSnapshot_StubFailureIsReturned(t *testing.T) {
	loc, s, ss, ts, rs := snapshotHookStores(t)
	stubDir := filepath.Join(loc.Ethos, "agents")
	require.NoError(t, os.MkdirAll(stubDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "snapshot-stub.md"), []byte("# Snapshot Stub\n"), 0o644))
	destDir := filepath.Join(loc.Checkout, ".claude", "agents")
	require.NoError(t, os.MkdirAll(destDir, 0o755))
	outside := filepath.Join(t.TempDir(), "outside.md")
	require.NoError(t, os.WriteFile(outside, []byte("unchanged\n"), 0o644))
	if err := os.Symlink(outside, filepath.Join(destDir, "snapshot-stub.md")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	deps := SessionStartDeps{Located: &loc, Store: s, Sessions: ss, Teams: ts, Roles: rs}
	_, err := withSnapshotPipe(t, filepath.Join(loc.Store, ".punt-labs", "ethos.yaml"), "agent: beta\nteam: beta-team\n", func(r io.Reader) error {
		return HandleSessionStart(r, deps)
	})
	require.ErrorContains(t, err, "symlink")
	data, err := os.ReadFile(outside)
	require.NoError(t, err)
	assert.Equal(t, "unchanged\n", string(data))
}
