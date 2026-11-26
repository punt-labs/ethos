package hook

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandoffBuildCacheDoesNotPublishFailedGate(t *testing.T) {
	root, run := handoffMetadataFixture(t, "printf stable > \"$GOCACHE/known\"\n")
	writeRuntimeFixture(t, root, nil, nil)
	sidecar := filepath.Join(root, ".punt-labs/ethos/checks/snapshots/project.yaml")
	policy, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	writeFile(t, sidecar, string(policy)+"    cache: [{name: gocache, env: GOCACHE}]\n")

	first := run()
	skipIfHandoffSandboxUnavailable(t, errors.New(first.Blocking))
	require.Empty(t, first.Blocking)
	caches, err := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".punt-labs/local/ethos/handoff-cache/*/*/gocache"))
	require.NoError(t, err)
	require.Len(t, caches, 1)
	before, err := os.ReadFile(filepath.Join(caches[0], "known"))
	require.NoError(t, err)
	require.Equal(t, "stable", string(before))

	writeFile(t, filepath.Join(root, "gate.sh"), `set -eu
test "$(cat "$GOCACHE/known")" = stable
printf tainted > "$GOCACHE/known"
printf extra > "$GOCACHE/new"
exit 14
`)
	failed := run()
	require.Contains(t, failed.Blocking, "exit status 14")
	require.Empty(t, failed.Notice)
	after, err := os.ReadFile(filepath.Join(caches[0], "known"))
	require.NoError(t, err)
	require.Equal(t, before, after)
	entries, err := os.ReadDir(caches[0])
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
