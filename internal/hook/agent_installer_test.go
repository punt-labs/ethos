package hook

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInstallAgentDefinitions(t *testing.T) {
	tests := []struct {
		name         string
		sourceFiles  map[string]string // filename → content in agents/
		destFiles    map[string]string // filename → content in .claude/agents/
		wantDeployed []string
		wantContent  map[string]string // filename → expected content after install
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

			deployed, err := InstallAgentDefinitions(ethosRoot)
			require.NoError(t, err)

			assert.ElementsMatch(t, tt.wantDeployed, deployed)

			for name, want := range tt.wantContent {
				got, readErr := os.ReadFile(filepath.Join(repoRoot, ".claude", "agents", name))
				require.NoError(t, readErr, "reading deployed file %s", name)
				assert.Equal(t, want, string(got), "content of %s", name)
			}
		})
	}
}
