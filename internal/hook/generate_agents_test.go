package hook

import (
	"os"
	"path/filepath"
	"strings"
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
				parts := strings.SplitN(content, "---", 3)
				require.Len(t, parts, 3, "expected frontmatter delimiters")
				frontmatter := parts[1]
				assert.NotContains(t, frontmatter, "model:")
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
				parts := strings.SplitN(content, "---", 3)
				require.Len(t, parts, 3, "expected frontmatter delimiters")
				frontmatter := parts[1]
				assert.Contains(t, frontmatter, `model: "sonnet"`)
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
		{
			name: "skills frontmatter includes baseline-ops",
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")
				data, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				content := string(data)
				parts := strings.SplitN(content, "---", 3)
				require.Len(t, parts, 3, "expected frontmatter delimiters")
				frontmatter := parts[1]
				assert.Contains(t, frontmatter, "skills:\n  - baseline-ops\n")

				// Log the generated file so binary verification is visible
				// in -v test output (spec success criterion 5).
				t.Logf("generated bwk.md:\n%s", content)
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


// TestGenerateAgentFiles_AntiResponsibilities covers the "## What You
// Don't Do" section derived from reports_to edges (ethos-9ai.1).
func TestGenerateAgentFiles_AntiResponsibilities(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, root string)
		assert func(t *testing.T, content string)
	}{
		{
			// Mirrors the real engineering team: go-specialist reports_to coo,
			// and coo has the byte-for-byte responsibilities from the real
			// .punt-labs/ethos/roles/coo.yaml. The assertion doubles as the
			// worked-example verification.
			name: "single reports_to, non-empty target",
			setup: func(t *testing.T, root string) {
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
					"name":         "engineering",
					"repositories": []string{"punt-labs/ethos"},
					"members": []map[string]string{
						{"identity": "claude", "role": "coo"},
						{"identity": "bwk", "role": "go-specialist"},
					},
					"collaborations": []map[string]string{
						{"from": "go-specialist", "to": "coo", "type": "reports_to"},
					},
				})
				writeYAML(t, filepath.Join(ethosDir, "roles", "coo.yaml"), map[string]interface{}{
					"name": "coo",
					"responsibilities": []string{
						"execution quality and velocity across all engineering",
						"sub-agent delegation and review",
						"release management",
						"operational decisions",
					},
				})
			},
			assert: func(t *testing.T, content string) {
				want := "## What You Don't Do\n\n" +
					"You report to coo. These are not yours:\n\n" +
					"- execution quality and velocity across all engineering (coo)\n" +
					"- sub-agent delegation and review (coo)\n" +
					"- release management (coo)\n" +
					"- operational decisions (coo)\n"
				assert.Contains(t, content, want)
				// Section must sit after Responsibilities and before Talents.
				respIdx := strings.Index(content, "## Responsibilities")
				antiIdx := strings.Index(content, "## What You Don't Do")
				talentsIdx := strings.Index(content, "Talents:")
				require.True(t, respIdx >= 0 && antiIdx >= 0 && talentsIdx >= 0,
					"all three anchors must be present")
				assert.Less(t, respIdx, antiIdx, "anti-responsibilities must follow Responsibilities")
				assert.Less(t, antiIdx, talentsIdx, "anti-responsibilities must precede Talents")
				// Binary verification visible in -v output.
				t.Logf("generated bwk.md:\n%s", content)
			},
		},
		{
			// No collaborations in the fixture => no reports_to => no
			// section. Default setupTestRepo has no collaborations already,
			// so the default state is sufficient.
			name:  "no reports_to edges",
			setup: func(t *testing.T, root string) {},
			assert: func(t *testing.T, content string) {
				assert.NotContains(t, content, "## What You Don't Do")
				assert.NotContains(t, content, "These are not yours:")
			},
		},
		{
			// go-specialist reports_to BOTH coo (non-empty) and ceo-empty
			// (zero responsibilities). Preamble must name only coo; bullets
			// come only from coo.
			name: "multiple reports_to, mixed emptiness",
			setup: func(t *testing.T, root string) {
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				// Add an empty-responsibilities target role.
				writeYAML(t, filepath.Join(ethosDir, "roles", "ceo-empty.yaml"), map[string]interface{}{
					"name":             "ceo-empty",
					"responsibilities": []string{},
				})
				writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
					"name":         "engineering",
					"repositories": []string{"punt-labs/ethos"},
					"members": []map[string]string{
						{"identity": "claude", "role": "coo"},
						{"identity": "bwk", "role": "go-specialist"},
					},
					"collaborations": []map[string]string{
						{"from": "go-specialist", "to": "ceo-empty", "type": "reports_to"},
						{"from": "go-specialist", "to": "coo", "type": "reports_to"},
					},
				})
				writeYAML(t, filepath.Join(ethosDir, "roles", "coo.yaml"), map[string]interface{}{
					"name": "coo",
					"responsibilities": []string{
						"execution quality and velocity across all engineering",
						"sub-agent delegation and review",
					},
				})
			},
			assert: func(t *testing.T, content string) {
				assert.Contains(t, content, "## What You Don't Do\n\n")
				// Preamble must name only coo — ceo-empty contributed no
				// bullets, so it must not appear in "You report to ...".
				assert.Contains(t, content, "You report to coo. These are not yours:")
				assert.NotContains(t, content, "ceo-empty. These are")
				assert.NotContains(t, content, "and ceo-empty")
				assert.NotContains(t, content, "ceo-empty,")
				// Bullets from coo present; none attributed to ceo-empty.
				assert.Contains(t, content, "- execution quality and velocity across all engineering (coo)\n")
				assert.Contains(t, content, "- sub-agent delegation and review (coo)\n")
				assert.NotContains(t, content, "(ceo-empty)")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _, _, _ := setupTestRepo(t)
			tt.setup(t, root)

			// Rebuild stores after setup modifications.
			ethosDir := filepath.Join(root, ".punt-labs", "ethos")
			ids := identity.NewLayeredStore(
				identity.NewStore(ethosDir),
				identity.NewStore(ethosDir),
			)
			teams := team.NewLayeredStore(ethosDir, ethosDir)
			roles := role.NewLayeredStore(ethosDir, ethosDir)

			err := GenerateAgentFiles(root, ids, teams, roles)
			require.NoError(t, err)

			agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")
			data, readErr := os.ReadFile(agentPath)
			require.NoError(t, readErr)

			tt.assert(t, string(data))
		})
	}
}

// TestDeriveAntiResponsibilities_MissingTarget verifies that a load
// failure on a target role is logged-and-skipped without failing the
// overall derivation. The other target's bullets still appear.
func TestDeriveAntiResponsibilities_MissingTarget(t *testing.T) {
	root, _, _, _ := setupTestRepo(t)
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")

	// Wire two reports_to edges, one pointing at a nonexistent role.
	writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
		"name":         "engineering",
		"repositories": []string{"punt-labs/ethos"},
		"members": []map[string]string{
			{"identity": "claude", "role": "coo"},
			{"identity": "bwk", "role": "go-specialist"},
		},
		"collaborations": []map[string]string{
			{"from": "go-specialist", "to": "ghost", "type": "reports_to"},
			{"from": "go-specialist", "to": "coo", "type": "reports_to"},
		},
	})
	writeYAML(t, filepath.Join(ethosDir, "roles", "coo.yaml"), map[string]interface{}{
		"name":             "coo",
		"responsibilities": []string{"release management"},
	})

	ids := identity.NewLayeredStore(
		identity.NewStore(ethosDir),
		identity.NewStore(ethosDir),
	)
	teams := team.NewLayeredStore(ethosDir, ethosDir)
	roles := role.NewLayeredStore(ethosDir, ethosDir)

	err := GenerateAgentFiles(root, ids, teams, roles)
	require.NoError(t, err)

	data, readErr := os.ReadFile(filepath.Join(root, ".claude", "agents", "bwk.md"))
	require.NoError(t, readErr)
	content := string(data)

	assert.Contains(t, content, "## What You Don't Do")
	assert.Contains(t, content, "You report to coo. These are not yours:")
	assert.Contains(t, content, "- release management (coo)\n")
	assert.NotContains(t, content, "ghost")
}

func TestJoinWithOxford(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  string
	}{
		{"empty", nil, ""},
		{"one", []string{"coo"}, "coo"},
		{"two", []string{"coo", "ceo"}, "coo and ceo"},
		{"three", []string{"coo", "ceo", "cto"}, "coo, ceo, and cto"},
		{"four", []string{"a", "b", "c", "d"}, "a, b, c, and d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinWithOxford(tt.names)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestUniqueTargetsInOrder(t *testing.T) {
	tests := []struct {
		name string
		in   []antiResponsibility
		want []string
	}{
		{"empty", nil, nil},
		{
			"single target, multiple bullets",
			[]antiResponsibility{
				{Responsibility: "a", TargetRole: "coo"},
				{Responsibility: "b", TargetRole: "coo"},
			},
			[]string{"coo"},
		},
		{
			"two targets interleaved",
			[]antiResponsibility{
				{Responsibility: "a", TargetRole: "coo"},
				{Responsibility: "x", TargetRole: "ceo"},
				{Responsibility: "b", TargetRole: "coo"},
			},
			[]string{"coo", "ceo"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := uniqueTargetsInOrder(tt.in)
			assert.Equal(t, tt.want, got)
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
