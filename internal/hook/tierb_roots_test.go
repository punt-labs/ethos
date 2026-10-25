package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
