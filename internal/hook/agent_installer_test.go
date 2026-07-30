package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/team"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallAgentDefinitions(t *testing.T) {
	tests := []struct {
		name         string
		sourceFiles  map[string]string // filename → content in agents/
		destFiles    map[string]string // filename → content in .claude/agents/
		generated    map[string]bool   // handles the generator owns
		wantDeployed []string
		wantContent  map[string]string // filename → expected content after install
		wantAbsent   []string          // filenames that must NOT exist in dest
	}{
		{
			name: "both files deployed when dest is empty",
			sourceFiles: map[string]string{
				"bwk.md": "# bwk agent",
				"mdm.md": "# mdm agent",
			},
			destFiles:    nil,
			wantDeployed: []string{"bwk.md", "mdm.md"},
			wantContent: map[string]string{
				"bwk.md": "# bwk agent",
				"mdm.md": "# mdm agent",
			},
		},
		{
			name: "only differing file deployed when one is identical",
			sourceFiles: map[string]string{
				"bwk.md": "# bwk agent",
				"mdm.md": "# mdm agent",
			},
			destFiles: map[string]string{
				"bwk.md": "# bwk agent", // identical
			},
			wantDeployed: []string{"mdm.md"},
			wantContent: map[string]string{
				"bwk.md": "# bwk agent",
				"mdm.md": "# mdm agent",
			},
		},
		{
			name:         "no error when agents dir does not exist",
			sourceFiles:  nil, // don't create agents dir
			destFiles:    nil,
			wantDeployed: nil,
		},
		{
			name: "dest directory created automatically",
			sourceFiles: map[string]string{
				"bwk.md": "# bwk agent",
			},
			destFiles:    nil, // .claude/agents/ does not exist
			wantDeployed: []string{"bwk.md"},
			wantContent: map[string]string{
				"bwk.md": "# bwk agent",
			},
		},
		{
			name: "differing content triggers overwrite",
			sourceFiles: map[string]string{
				"bwk.md": "# bwk agent v2",
			},
			destFiles: map[string]string{
				"bwk.md": "# bwk agent v1",
			},
			wantDeployed: []string{"bwk.md"},
			wantContent: map[string]string{
				"bwk.md": "# bwk agent v2",
			},
		},
		{
			name: "non-md files ignored",
			sourceFiles: map[string]string{
				"bwk.md":    "# bwk agent",
				"notes.txt": "some notes",
			},
			destFiles:    nil,
			wantDeployed: []string{"bwk.md"},
			wantContent: map[string]string{
				"bwk.md": "# bwk agent",
			},
		},
		{
			name: "generated handle stub is not copied; non-generated is",
			sourceFiles: map[string]string{
				"bwk.md": "# bwk stub",
				"mdm.md": "# mdm stub",
			},
			destFiles:    nil,
			generated:    map[string]bool{"bwk": true},
			wantDeployed: []string{"mdm.md"},
			wantContent: map[string]string{
				"mdm.md": "# mdm stub",
			},
			wantAbsent: []string{"bwk.md"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up a fake repo root with .git marker.
			repoRoot := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755))

			// Chdir to the repo root so FindRepoRoot works.
			orig, err := os.Getwd()
			require.NoError(t, err)
			require.NoError(t, os.Chdir(repoRoot))
			t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

			ethosRoot := filepath.Join(repoRoot, ".punt-labs", "ethos")

			if tt.sourceFiles != nil {
				agentsDir := filepath.Join(ethosRoot, "agents")
				require.NoError(t, os.MkdirAll(agentsDir, 0o755))
				for name, content := range tt.sourceFiles {
					require.NoError(t, os.WriteFile(filepath.Join(agentsDir, name), []byte(content), 0o644))
				}
			}

			if tt.destFiles != nil {
				destDir := filepath.Join(repoRoot, ".claude", "agents")
				require.NoError(t, os.MkdirAll(destDir, 0o755))
				for name, content := range tt.destFiles {
					require.NoError(t, os.WriteFile(filepath.Join(destDir, name), []byte(content), 0o644))
				}
			}

			deployed, err := InstallAgentDefinitions(ethosRoot, tt.generated)
			require.NoError(t, err)

			assert.ElementsMatch(t, tt.wantDeployed, deployed)

			for name, want := range tt.wantContent {
				got, readErr := os.ReadFile(filepath.Join(repoRoot, ".claude", "agents", name))
				require.NoError(t, readErr, "reading deployed file %s", name)
				assert.Equal(t, want, string(got), "content of %s", name)
			}

			for _, name := range tt.wantAbsent {
				_, statErr := os.Stat(filepath.Join(repoRoot, ".claude", "agents", name))
				assert.True(t, os.IsNotExist(statErr), "stub for generator-owned handle %s must not be copied", name)
			}
		})
	}
}

// setupStubRepo builds a temp git repo holding one stub agent file and
// chdirs into it, so FindRepoRoot and FindRepoEthosRoot resolve there
// rather than into the real checkout. Returns the repo root.
func setupStubRepo(t *testing.T, stub string) string {
	t.Helper()
	repoRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755))

	orig, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoRoot))
	t.Cleanup(func() { os.Chdir(orig) }) //nolint:errcheck

	agentsDir := filepath.Join(repoRoot, ".punt-labs", "ethos", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(agentsDir, stub+".md"), []byte("# "+stub+" stub"), 0o644))
	return repoRoot
}

// TestInstallStubAgentDefs_FailsClosedOnLookupError asserts that when the
// ownership lookup fails, NO stub is installed.
//
// A nil owned-set means "copy every stub", so falling through on the
// error path would copy stubs over generator-owned files — the exact
// clobber the DES-026 subordination prevents. Fail closed: the generator
// writes the authoritative files, and no stub beats a wrong stub.
func TestInstallStubAgentDefs_FailsClosedOnLookupError(t *testing.T) {
	repoRoot := setupStubRepo(t, "bwk")
	ethosDir := filepath.Join(repoRoot, ".punt-labs", "ethos")

	// Config names a team the store does not hold, so
	// GeneratedAgentHandles fails at teams.Load and cannot report which
	// handles the generator owns.
	writeYAML(t, filepath.Join(repoRoot, ".punt-labs", "ethos.yaml"), map[string]string{
		"agent": "claude",
		"team":  "no-such-team",
	})
	ids := identity.NewLayeredStore(identity.NewStore(ethosDir), identity.NewStore(ethosDir))
	teams := team.NewLayeredStore(ethosDir, ethosDir)

	stderr := captureStderr(t, func() {
		installStubAgentDefs(repoRoot, ids, teams)
	})

	_, statErr := os.Stat(filepath.Join(repoRoot, ".claude", "agents", "bwk.md"))
	assert.True(t, os.IsNotExist(statErr),
		"an ownership-lookup failure must install no stub; got %v", statErr)
	assert.Contains(t, stderr, "resolving generated agents")
	assert.Contains(t, stderr, "installing no stubs")
}

// TestInstallStubAgentDefs_CopiesWhenNothingIsOwned asserts the other
// branch stays intact: a SUCCESSFUL lookup that owns nothing still copies
// every stub. Here no team is configured, so the generator owns nothing
// and the stubs are the only source for those agents.
func TestInstallStubAgentDefs_CopiesWhenNothingIsOwned(t *testing.T) {
	repoRoot := setupStubRepo(t, "bwk")
	ethosDir := filepath.Join(repoRoot, ".punt-labs", "ethos")

	writeYAML(t, filepath.Join(repoRoot, ".punt-labs", "ethos.yaml"), map[string]string{
		"agent": "claude",
	})
	ids := identity.NewLayeredStore(identity.NewStore(ethosDir), identity.NewStore(ethosDir))
	teams := team.NewLayeredStore(ethosDir, ethosDir)

	captureStderr(t, func() {
		installStubAgentDefs(repoRoot, ids, teams)
	})

	got, err := os.ReadFile(filepath.Join(repoRoot, ".claude", "agents", "bwk.md"))
	require.NoError(t, err, "no team configured means the generator owns nothing; stubs still install")
	assert.Equal(t, "# bwk stub", string(got))
}

// TestInstallStubAgentDefs_SkipsOwnedHandle asserts the success path still
// subordinates to the generator: a stub for an owned handle is not copied.
func TestInstallStubAgentDefs_SkipsOwnedHandle(t *testing.T) {
	repoRoot := setupStubRepo(t, "bwk")
	ethosDir := filepath.Join(repoRoot, ".punt-labs", "ethos")

	writeYAML(t, filepath.Join(repoRoot, ".punt-labs", "ethos.yaml"), map[string]string{
		"agent": "claude",
		"team":  "engineering",
	})
	writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
		"name":         "engineering",
		"repositories": []string{"punt-labs/ethos"},
		"members": []map[string]string{
			{"identity": "claude", "role": "coo"},
			{"identity": "bwk", "role": "go-specialist"},
		},
	})
	writeYAML(t, filepath.Join(ethosDir, "identities", "bwk.yaml"), map[string]interface{}{
		"name": "Brian K", "handle": "bwk", "kind": "agent",
	})
	ids := identity.NewLayeredStore(identity.NewStore(ethosDir), identity.NewStore(ethosDir))
	teams := team.NewLayeredStore(ethosDir, ethosDir)

	captureStderr(t, func() {
		installStubAgentDefs(repoRoot, ids, teams)
	})

	_, statErr := os.Stat(filepath.Join(repoRoot, ".claude", "agents", "bwk.md"))
	assert.True(t, os.IsNotExist(statErr),
		"a stub for a generator-owned handle must not be copied; got %v", statErr)
}
