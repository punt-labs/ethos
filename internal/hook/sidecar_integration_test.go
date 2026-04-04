package hook

// Note: this test uses writeYAML from generate_agents_test.go (same package).
import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func integrationRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Join(wd, "..", "..")
}

func copyFlatDir(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dst, 0o755))
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644))
	}
}

func TestSidecarIntegration(t *testing.T) {
	repoRoot := integrationRepoRoot(t)
	sidecarDir := filepath.Join(repoRoot, "internal", "seed", "sidecar")

	root := t.TempDir()
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")

	// Copy sidecar roles and talents.
	copyFlatDir(t, filepath.Join(sidecarDir, "roles"), filepath.Join(ethosDir, "roles"))
	copyFlatDir(t, filepath.Join(sidecarDir, "talents"), filepath.Join(ethosDir, "talents"))

	// Create minimal personality and writing style.
	require.NoError(t, os.MkdirAll(filepath.Join(ethosDir, "personalities"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(ethosDir, "personalities", "test-personality.md"),
		[]byte("# Test Personality\n\nDirect and evidence-driven. Simplicity over complexity.\n\n## Principles\n\n1. Test first\n2. Keep it simple\n"),
		0o644,
	))
	require.NoError(t, os.MkdirAll(filepath.Join(ethosDir, "writing-styles"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(ethosDir, "writing-styles", "test-style.md"),
		[]byte("# Test Style\n\nShort sentences. Lead with the answer.\n"),
		0o644,
	))

	// Repo config.
	writeYAML(t, filepath.Join(root, ".punt-labs", "ethos.yaml"), map[string]string{
		"agent": "main-agent",
		"team":  "test-team",
	})

	// Team with members using sidecar roles.
	writeYAML(t, filepath.Join(ethosDir, "teams", "test-team.yaml"), map[string]interface{}{
		"name":         "test-team",
		"repositories": []string{"test/repo"},
		"members": []map[string]string{
			{"identity": "main-agent", "role": "implementer"},
			{"identity": "test-impl", "role": "implementer"},
			{"identity": "test-rev", "role": "reviewer"},
		},
	})

	// Identities.
	writeYAML(t, filepath.Join(ethosDir, "identities", "main-agent.yaml"), map[string]interface{}{
		"name": "Main Agent", "handle": "main-agent", "kind": "agent",
		"personality": "test-personality",
	})
	writeYAML(t, filepath.Join(ethosDir, "identities", "test-impl.yaml"), map[string]interface{}{
		"name": "Test Impl", "handle": "test-impl", "kind": "agent",
		"personality": "test-personality", "writing_style": "test-style",
		"talents": []string{"go", "testing"},
	})
	writeYAML(t, filepath.Join(ethosDir, "identities", "test-rev.yaml"), map[string]interface{}{
		"name": "Test Rev", "handle": "test-rev", "kind": "agent",
		"personality": "test-personality", "writing_style": "test-style",
		"talents": []string{"code-review", "security"},
	})

	// Build stores.
	repoStore := identity.NewStore(ethosDir)
	globalStore := identity.NewStore(t.TempDir())
	ids := identity.NewLayeredStore(repoStore, globalStore)

	roles := role.NewLayeredStore(ethosDir, t.TempDir())
	teams := team.NewLayeredStore(ethosDir, t.TempDir())

	// Generate.
	err := GenerateAgentFiles(root, ids, teams, roles)
	require.NoError(t, err)

	// main-agent should be SKIPPED.
	_, err = os.Stat(filepath.Join(root, ".claude", "agents", "main-agent.md"))
	assert.True(t, os.IsNotExist(err), "main-agent should not be generated")

	// test-impl: implementer role.
	implData, err := os.ReadFile(filepath.Join(root, ".claude", "agents", "test-impl.md"))
	require.NoError(t, err)
	implParts := strings.SplitN(string(implData), "---", 3)
	require.Len(t, implParts, 3)
	implFM := implParts[1]

	assert.Contains(t, implFM, "name: test-impl")
	assert.Contains(t, implFM, "- Read")
	assert.Contains(t, implFM, "- Write")
	assert.Contains(t, implFM, "- Bash")
	assert.Contains(t, implFM, `model: "sonnet"`)

	implBody := implParts[2]
	assert.Contains(t, implBody, "Test Impl (test-impl)")
	assert.Contains(t, implBody, "Talents: go, testing")

	// test-rev: reviewer role.
	revData, err := os.ReadFile(filepath.Join(root, ".claude", "agents", "test-rev.md"))
	require.NoError(t, err)
	revParts := strings.SplitN(string(revData), "---", 3)
	require.Len(t, revParts, 3)
	revFM := revParts[1]

	assert.Contains(t, revFM, "name: test-rev")
	assert.Contains(t, revFM, "- Read", "reviewer should have Read")
	assert.Contains(t, revFM, "- Grep")
	assert.NotContains(t, revFM, "- Write")
	assert.NotContains(t, revFM, "- Edit")
	assert.Contains(t, revFM, `model: "opus"`)

	revBody := revParts[2]
	assert.Contains(t, revBody, "Test Rev (test-rev)")
	assert.Contains(t, revBody, "Talents: code-review, security")
}
