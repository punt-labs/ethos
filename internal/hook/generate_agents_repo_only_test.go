package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Under DES-057 repo-only, an identity whose attributes were not
// vendored FAILS its agent rather than being skipped in silence. A skip
// would report success while producing fewer agents than the team has —
// the caller has no signal that bwk lost its personality.
func TestGenerateAgentFiles_RepoOnlyIncompleteSetFailsTheAgent(t *testing.T) {
	root, _, teams, roles := setupTestRepo(t)
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")

	// bwk's writing style is referenced but absent from the repo layer —
	// the shape a partial `ethos vendor` leaves behind.
	require.NoError(t, os.Remove(filepath.Join(ethosDir, "writing-styles", "kernighan-prose.md")))

	repoOnly := identity.NewLayeredStoreWithBundle(
		identity.NewStore(ethosDir), nil, identity.NewStore(t.TempDir()), true)

	var err error
	stderr := captureStderr(t, func() {
		err = GenerateAgentFiles(root, repoOnly, teams, roles)
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bwk", "the error must name the failed member")
	assert.Contains(t, err.Error(), "generated 0 of 1")
	assert.Contains(t, stderr, "writing-styles/kernighan-prose")
	assert.NoFileExists(t, filepath.Join(root, ".claude", "agents", "bwk.md"))
}

// The same missing file in layered mode resolves from the global layer,
// so agent generation succeeds unchanged. This is the regression guard:
// repo-only must be the only thing that changes behavior here.
func TestGenerateAgentFiles_LayeredStillFallsBackToGlobal(t *testing.T) {
	root, _, teams, roles := setupTestRepo(t)
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")
	globalRoot := t.TempDir()

	require.NoError(t, os.Remove(filepath.Join(ethosDir, "writing-styles", "kernighan-prose.md")))
	writeFile(t, filepath.Join(globalRoot, "writing-styles", "kernighan-prose.md"),
		"# Kernighan Prose\n\nTechnical writing.\n")

	layered := identity.NewLayeredStoreWithBundle(
		identity.NewStore(ethosDir), nil, identity.NewStore(globalRoot), false)

	require.NoError(t, GenerateAgentFiles(root, layered, teams, roles))
	assert.FileExists(t, filepath.Join(root, ".claude", "agents", "bwk.md"))
}
