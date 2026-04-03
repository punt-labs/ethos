package hook

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// setupTestRepo creates a temp directory tree with repo config, team,
// identities, personalities, writing-styles, and roles. Returns the
// repo root path and layered stores.
func setupTestRepo(t *testing.T) (string, identity.IdentityStore, *team.LayeredStore, *role.LayeredStore) {
	t.Helper()

	root := t.TempDir()
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")

	// Repo config.
	writeYAML(t, filepath.Join(root, ".punt-labs", "ethos.yaml"), map[string]string{
		"agent": "claude",
		"team":  "engineering",
	})

	// Team.
	writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
		"name":         "engineering",
		"repositories": []string{"punt-labs/ethos"},
		"members": []map[string]string{
			{"identity": "claude", "role": "coo"},
			{"identity": "bwk", "role": "go-specialist"},
			{"identity": "jfreeman", "role": "ceo"},
		},
	})

	// Identities.
	writeYAML(t, filepath.Join(ethosDir, "identities", "claude.yaml"), map[string]interface{}{
		"name":          "Claude Agento",
		"handle":        "claude",
		"kind":          "agent",
		"personality":   "friendly-direct",
		"writing_style": "direct-with-quips",
		"talents":       []string{"management"},
	})
	writeYAML(t, filepath.Join(ethosDir, "identities", "bwk.yaml"), map[string]interface{}{
		"name":          "Brian K",
		"handle":        "bwk",
		"kind":          "agent",
		"personality":   "kernighan",
		"writing_style": "kernighan-prose",
		"talents":       []string{"engineering"},
	})
	writeYAML(t, filepath.Join(ethosDir, "identities", "jfreeman.yaml"), map[string]interface{}{
		"name":   "Jim Freeman",
		"handle": "jfreeman",
		"kind":   "human",
		"email":  "jim@punt-labs.com",
	})

	// Personalities.
	writeFile(t, filepath.Join(ethosDir, "personalities", "kernighan.md"),
		"# Kernighan\n\nGo specialist sub-agent.\n\n## Core Principles\n\nSimplicity, clarity, generality.\n")
	writeFile(t, filepath.Join(ethosDir, "personalities", "friendly-direct.md"),
		"# Friendly Direct\n\nA friendly and direct communicator.\n")

	// Writing styles.
	writeFile(t, filepath.Join(ethosDir, "writing-styles", "kernighan-prose.md"),
		"# Kernighan Prose\n\nTechnical writing in the style of Kernighan & Pike.\n\n## Prose\n\n- One sentence per idea\n")
	writeFile(t, filepath.Join(ethosDir, "writing-styles", "direct-with-quips.md"),
		"# Direct With Quips\n\nClear and direct with occasional humor.\n")

	// Roles.
	writeYAML(t, filepath.Join(ethosDir, "roles", "go-specialist.yaml"), map[string]interface{}{
		"name":             "go-specialist",
		"responsibilities": []string{"Go package implementation with tests", "code review"},
		"tools":            []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob"},
	})
	writeYAML(t, filepath.Join(ethosDir, "roles", "coo.yaml"), map[string]interface{}{
		"name":             "coo",
		"responsibilities": []string{"execution quality"},
	})
	writeYAML(t, filepath.Join(ethosDir, "roles", "ceo.yaml"), map[string]interface{}{
		"name":             "ceo",
		"responsibilities": []string{"strategic direction"},
	})

	identities := identity.NewLayeredStore(
		identity.NewStore(ethosDir),
		identity.NewStore(ethosDir),
	)
	teams := team.NewLayeredStore(ethosDir, ethosDir)
	roles := role.NewLayeredStore(ethosDir, ethosDir)

	return root, identities, teams, roles
}

func writeYAML(t *testing.T, path string, data interface{}) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	b, err := yaml.Marshal(data)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o644))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestGenerateAgentFiles(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, root string, ids identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore)
		check  func(t *testing.T, root string, err error)
	}{
		{
			name: "basic generation",
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")
				data, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				content := string(data)
				// Frontmatter checks.
				assert.Contains(t, content, "name: bwk")
				assert.Contains(t, content, `description: "Go specialist sub-agent."`)
				assert.Contains(t, content, "  - Read")
				assert.Contains(t, content, "  - Bash")

				// Body checks.
				assert.Contains(t, content, "You are Brian K (bwk),")
				assert.Contains(t, content, "You report to Claude Agento (COO/VP Engineering).")
				assert.Contains(t, content, "## Writing Style")
				assert.Contains(t, content, "## Responsibilities")
				assert.Contains(t, content, "- Go package implementation with tests")
				assert.Contains(t, content, "Talents: engineering")
			},
		},
		{
			name: "skip main agent",
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)
				// claude is the main agent — should NOT be generated.
				claudePath := filepath.Join(root, ".claude", "agents", "claude.md")
				_, readErr := os.ReadFile(claudePath)
				assert.True(t, os.IsNotExist(readErr), "main agent file should not be generated")
			},
		},
		{
			name: "skip humans",
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)
				// jfreeman is human — should NOT be generated.
				humanPath := filepath.Join(root, ".claude", "agents", "jfreeman.md")
				_, readErr := os.ReadFile(humanPath)
				assert.True(t, os.IsNotExist(readErr), "human agent file should not be generated")
			},
		},
		{
			name: "idempotent",
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")
				first, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				// Run again.
				rerunRoot, ids, teams, roles := setupTestRepo(t)
				// Copy the generated file to the new root so we can test
				// content comparison.
				destDir := filepath.Join(rerunRoot, ".claude", "agents")
				require.NoError(t, os.MkdirAll(destDir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(destDir, "bwk.md"), first, 0o644))

				err2 := GenerateAgentFiles(rerunRoot, ids, teams, roles)
				require.NoError(t, err2)

				second, readErr := os.ReadFile(filepath.Join(rerunRoot, ".claude", "agents", "bwk.md"))
				require.NoError(t, readErr)

				assert.Equal(t, string(first), string(second))
			},
		},
		{
			name: "staleness - no rewrite when content matches",
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")

				// Set mtime to the past.
				past := time.Now().Add(-1 * time.Hour)
				require.NoError(t, os.Chtimes(agentPath, past, past))

				info1, err := os.Stat(agentPath)
				require.NoError(t, err)

				// Rebuild stores and run again on the SAME root.
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				ids := identity.NewLayeredStore(
					identity.NewStore(ethosDir),
					identity.NewStore(ethosDir),
				)
				teams := team.NewLayeredStore(ethosDir, ethosDir)
				roles := role.NewLayeredStore(ethosDir, ethosDir)

				err2 := GenerateAgentFiles(root, ids, teams, roles)
				require.NoError(t, err2)

				info2, err := os.Stat(agentPath)
				require.NoError(t, err)

				assert.Equal(t, info1.ModTime(), info2.ModTime(), "file should not be rewritten when content matches")
			},
		},
		{
			name: "model field empty omits model from frontmatter",
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")
				data, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				content := string(data)
				assert.NotContains(t, content, "model:")
			},
		},
		{
			name: "model field set appears in frontmatter",
			setup: func(t *testing.T, root string, ids identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore) {
				// Rewrite the go-specialist role with a model field.
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				writeYAML(t, filepath.Join(ethosDir, "roles", "go-specialist.yaml"), map[string]interface{}{
					"name":             "go-specialist",
					"model":            "sonnet",
					"responsibilities": []string{"Go package implementation with tests", "code review"},
					"tools":            []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob"},
				})
			},
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")
				data, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				content := string(data)
				assert.Contains(t, content, "model: sonnet")
			},
		},
		{
			name: "missing personality skips agent",
			setup: func(t *testing.T, root string, ids identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore) {
				// Remove the personality file.
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				require.NoError(t, os.Remove(filepath.Join(ethosDir, "personalities", "kernighan.md")))
			},
			check: func(t *testing.T, root string, err error) {
				// bwk is the only agent with tools; missing personality
				// means it's skipped before incrementing expected, so no
				// error is returned.
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")
				_, readErr := os.ReadFile(agentPath)
				assert.True(t, os.IsNotExist(readErr), "agent with missing personality should be skipped")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, ids, teams, roles := setupTestRepo(t)
			if tt.setup != nil {
				tt.setup(t, root, ids, teams, roles)
				// Rebuild stores after setup modifications.
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				ids = identity.NewLayeredStore(
					identity.NewStore(ethosDir),
					identity.NewStore(ethosDir),
				)
				teams = team.NewLayeredStore(ethosDir, ethosDir)
				roles = role.NewLayeredStore(ethosDir, ethosDir)
			}
			err := GenerateAgentFiles(root, ids, teams, roles)
			tt.check(t, root, err)
		})
	}
}


func TestExtractDescription(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "heading then description",
			content: "# Kernighan\n\nGo specialist sub-agent.\n",
			want:    "Go specialist sub-agent.",
		},
		{
			name:    "no heading",
			content: "A direct communicator.\n",
			want:    "A direct communicator.",
		},
		{
			name:    "empty",
			content: "",
			want:    "",
		},
		{
			name:    "heading only",
			content: "# Title\n",
			want:    "",
		},
		{
			name:    "headings and bullets only",
			content: "# Rules\n\n- Rule one\n- Rule two\n",
			want:    "",
		},
		{
			name:    "bullet then prose",
			content: "# Title\n\n- Rule one\n\nActual description here.\n",
			want:    "Actual description here.",
		},
		{
			name:    "description with hash character",
			content: "# Rules\n\nRule #1: be clear.\n",
			want:    "Rule #1: be clear.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDescription(tt.content)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestYamlQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain string",
			in:   "Go specialist sub-agent.",
			want: `"Go specialist sub-agent."`,
		},
		{
			name: "string with hash",
			in:   "Rule #1: be clear.",
			want: `"Rule #1: be clear."`,
		},
		{
			name: "string with double quote",
			in:   `Says "hello" often.`,
			want: `"Says \"hello\" often."`,
		},
		{
			name: "string with backslash",
			in:   `Path is C:\Users.`,
			want: `"Path is C:\\Users."`,
		},
		{
			name: "empty string",
			in:   "",
			want: `""`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := yamlQuote(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}
