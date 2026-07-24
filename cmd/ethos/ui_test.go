package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunUI_RefusedOverrideDoesNotResurrect pins the Bugbot MED on PR #370:
// runUI must not fall the store root back to repoRoot (EnvRepoRoot), which
// re-accepts an ETHOS_REPO_ROOT that StoreRepoRoot refused (F1 fail-closed).
// A bad override — an existing directory with no .punt-labs/ethos store —
// makes StoreRepoRoot return "" while EnvRepoRoot accepts the path; runUI must
// fail loud rather than serve the dashboard off the refused path.
func TestRunUI_RefusedOverrideDoesNotResurrect(t *testing.T) {
	dir := t.TempDir() // exists, but has no .punt-labs/ethos store
	t.Setenv("ETHOS_REPO_ROOT", dir)

	err := runUI()
	require.Error(t, err, "a refused override must not start the UI")
	assert.Contains(t, err.Error(), "store root",
		"the error must name the unresolved store root, not proceed against the refused path")
	assert.NotContains(t, err.Error(), "listen",
		"runUI must fail before binding a port")
}
