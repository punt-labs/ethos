package hook

import (
	"bytes"
	"io"
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

// captureStderr redirects os.Stderr to an in-memory buffer for the
// duration of fn and returns everything fn wrote to stderr.
//
// WARNING: this mutates the package-global os.Stderr. Tests that use
// this helper must NOT call t.Parallel(), and no other test in the
// package may run concurrently with one that uses it. Adding parallel
// tests to this file requires reworking this helper to use a per-test
// file descriptor (not a global swap).
//
// Not suitable for subprocesses — see feedback_subprocess_tests.md.
//
// A drain goroutine reads from the pipe concurrently with fn so stderr
// output larger than the pipe buffer (~64 KiB on Linux) cannot
// deadlock. Cleanup happens in a single deferred path so the helper
// is panic-safe: os.Stderr is restored first (any write after this
// point goes to the real stderr, not the pipe), then the writer is
// closed to unblock the drain goroutine, the drain is joined, the
// reader is closed, and the drained buffer is copied into the named
// return value. Both file descriptors are always freed, even if fn
// panics.
func captureStderr(t *testing.T, fn func()) (out string) {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	defer func() {
		os.Stderr = old
		_ = w.Close()
		<-done
		_ = r.Close()
		out = buf.String()
	}()

	fn()
	return
}

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

	// Roles. The go-specialist responsibilities list matches the
	// canonical target shape in the fix-round-2 spec so the worked
	// example assertion in TestGenerateAgentFiles_AntiResponsibilities
	// can anchor against the full file body byte-for-byte.
	writeYAML(t, filepath.Join(ethosDir, "roles", "go-specialist.yaml"), map[string]interface{}{
		"name": "go-specialist",
		"responsibilities": []string{
			"Go package implementation with tests",
			"code review for Go projects",
			"adherence to punt-kit/standards/go.md",
		},
		"tools": []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob"},
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
				// bwk has Write in its tools list, so the PostToolUse hook
				// block must be present, anchored by the preceding skills
				// block and the closing frontmatter delimiter.
				assert.Contains(t, content,
					"skills:\n"+
						"  - baseline-ops\n"+
						"hooks:\n"+
						"  PostToolUse:\n"+
						"    - matcher: \"Write|Edit\"\n"+
						"      hooks:\n"+
						"        - type: command\n"+
						"          command: \"(cd \\\"$CLAUDE_PROJECT_DIR\\\" && make check) 2>&1 | tail -20\"\n"+
						"---\n")

				// Body checks.
				assert.Contains(t, content, "You are Brian K (bwk),")
				assert.Contains(t, content, "You report to Claude Agento (COO/VP Engineering).")

				// Section-shape anchors — every `## Heading` gets a blank
				// line after it, and `Talents:` is separated from the
				// preceding bullet list by a blank line.
				assert.Contains(t, content, "## Writing Style\n\n")
				assert.Contains(t, content, "## Responsibilities\n\n- Go package implementation with tests\n")
				assert.Contains(t, content, "- adherence to punt-kit/standards/go.md\n\nTalents: engineering\n")
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
					"name":  "go-specialist",
					"model": "sonnet",
					"responsibilities": []string{
						"Go package implementation with tests",
						"code review for Go projects",
						"adherence to punt-kit/standards/go.md",
					},
					"tools": []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob"},
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
				// bwk has Write in its tools list, so the hooks block
				// follows skills in the same frontmatter section.
				assert.Contains(t, frontmatter,
					"skills:\n"+
						"  - baseline-ops\n"+
						"hooks:\n"+
						"  PostToolUse:\n"+
						"    - matcher: \"Write|Edit\"\n"+
						"      hooks:\n"+
						"        - type: command\n"+
						"          command: \"(cd \\\"$CLAUDE_PROJECT_DIR\\\" && make check) 2>&1 | tail -20\"\n")

				// Log the generated file so binary verification is visible
				// in -v test output (spec success criterion 5).
				t.Logf("generated bwk.md:\n%s", content)
			},
		},
		{
			// Write-enabled role emits the PostToolUse hook block. bwk's
			// default fixture already has Write in its tools list, so the
			// generated frontmatter must include the exact block anchored
			// between `skills:` and the closing `---`. The leading skills
			// anchor and the trailing `---\n` together lock placement.
			name: "write-enabled role emits hooks",
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")
				data, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				content := string(data)
				want := "skills:\n" +
					"  - baseline-ops\n" +
					"hooks:\n" +
					"  PostToolUse:\n" +
					"    - matcher: \"Write|Edit\"\n" +
					"      hooks:\n" +
					"        - type: command\n" +
					"          command: \"(cd \\\"$CLAUDE_PROJECT_DIR\\\" && make check) 2>&1 | tail -20\"\n" +
					"---\n"
				assert.Contains(t, content, want)
			},
		},
		{
			// Review-only role — tools list excludes Write and Edit —
			// emits NO hooks block. The frontmatter must close with
			// `skills:` → `  - baseline-ops` → `---` directly.
			name: "review-only role omits hooks",
			setup: func(t *testing.T, root string, ids identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore) {
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				// Review-only role with Read, Grep, Glob, Bash — mirrors
				// the real security-engineer tool set for djb.
				writeYAML(t, filepath.Join(ethosDir, "roles", "security-engineer.yaml"), map[string]interface{}{
					"name":             "security-engineer",
					"responsibilities": []string{"threat modeling"},
					"tools":            []string{"Read", "Grep", "Glob", "Bash"},
				})
				writeYAML(t, filepath.Join(ethosDir, "identities", "djb.yaml"), map[string]interface{}{
					"name":          "Dan B",
					"handle":        "djb",
					"kind":          "agent",
					"personality":   "security-minded",
					"writing_style": "kernighan-prose",
					"talents":       []string{"security"},
				})
				writeFile(t, filepath.Join(ethosDir, "personalities", "security-minded.md"),
					"# Security Minded\n\nSecurity reviewer sub-agent.\n")
				// Add djb to the team so it actually gets generated.
				writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
					"name":         "engineering",
					"repositories": []string{"punt-labs/ethos"},
					"members": []map[string]string{
						{"identity": "claude", "role": "coo"},
						{"identity": "bwk", "role": "go-specialist"},
						{"identity": "djb", "role": "security-engineer"},
					},
				})
			},
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "djb.md")
				data, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				content := string(data)
				// No hooks block at all — not a key, not the matcher
				// string.
				assert.NotContains(t, content, "hooks:")
				assert.NotContains(t, content, "PostToolUse")

				// Frontmatter must still close cleanly: skills block
				// directly followed by `---\n`.
				assert.Contains(t, content,
					"skills:\n"+
						"  - baseline-ops\n"+
						"---\n")
			},
		},
		{
			// Edit-only role — tools list has Edit but not Write — still
			// gets the hook block. The matcher `Write|Edit` is unchanged;
			// what gates emission is the helper's OR test over the tools
			// list, not which of the two tools happens to be present.
			name: "Edit-only role emits hooks",
			setup: func(t *testing.T, root string, ids identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore) {
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				writeYAML(t, filepath.Join(ethosDir, "roles", "edit-only.yaml"), map[string]interface{}{
					"name":             "edit-only",
					"responsibilities": []string{"targeted edits"},
					"tools":            []string{"Read", "Edit", "Bash"},
				})
				writeYAML(t, filepath.Join(ethosDir, "identities", "eon.yaml"), map[string]interface{}{
					"name":          "Edit Only",
					"handle":        "eon",
					"kind":          "agent",
					"personality":   "kernighan",
					"writing_style": "kernighan-prose",
					"talents":       []string{"editing"},
				})
				writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
					"name":         "engineering",
					"repositories": []string{"punt-labs/ethos"},
					"members": []map[string]string{
						{"identity": "claude", "role": "coo"},
						{"identity": "bwk", "role": "go-specialist"},
						{"identity": "eon", "role": "edit-only"},
					},
				})
			},
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "eon.md")
				data, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				content := string(data)
				want := "skills:\n" +
					"  - baseline-ops\n" +
					"hooks:\n" +
					"  PostToolUse:\n" +
					"    - matcher: \"Write|Edit\"\n" +
					"      hooks:\n" +
					"        - type: command\n" +
					"          command: \"(cd \\\"$CLAUDE_PROJECT_DIR\\\" && make check) 2>&1 | tail -20\"\n" +
					"---\n"
				assert.Contains(t, content, want)
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
			// Mirrors the real engineering team: go-specialist reports_to
			// coo, and coo has the byte-for-byte responsibilities from the
			// real .punt-labs/ethos/roles/coo.yaml. The assertion doubles
			// as the worked-example binary verification against the
			// fix-round-2 canonical target shape.
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
				// Leading \n\n locks the blank line above the heading;
				// trailing \nTalents: locks the blank line above Talents.
				// Together they pin every whitespace boundary in the
				// canonical target shape — a regression that fused
				// Responsibilities with What-You-Don't-Do, or bullets
				// with Talents:, would no longer pass.
				want := "\n\n## What You Don't Do\n\n" +
					"You report to coo. These are not yours:\n\n" +
					"- execution quality and velocity across all engineering (coo)\n" +
					"- sub-agent delegation and review (coo)\n" +
					"- release management (coo)\n" +
					"- operational decisions (coo)\n" +
					"\nTalents: engineering\n"
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
			// go-specialist reports_to BOTH ceo-empty (zero
			// responsibilities) and coo (non-empty). ceo-empty has no
			// responsibilities, so deriveAntiResponsibilities appends
			// nothing for it; the bucketing pass in buildAgentFile
			// therefore never sees ceo-empty and cannot name it in the
			// preamble. The edge is not "filtered out" by preamble logic —
			// it simply never enters the derived data in the first place.
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
		{
			// Two non-empty targets. The preamble must list targets in
			// walk order ("coo and architect") and the bullet block must
			// group by target: all coo bullets first, then all architect
			// bullets. This locks the outer-loop-over-targets grouping
			// against a regression that reverts to iterating antiResps
			// in walk order, which would interleave bullets if a future
			// change reordered collaborations.
			name: "two non-empty targets, bullets grouped by target",
			setup: func(t *testing.T, root string) {
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				writeYAML(t, filepath.Join(ethosDir, "roles", "architect.yaml"), map[string]interface{}{
					"name": "architect",
					"responsibilities": []string{
						"system design reviews",
						"interface stability",
					},
				})
				writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
					"name":         "engineering",
					"repositories": []string{"punt-labs/ethos"},
					"members": []map[string]string{
						{"identity": "claude", "role": "coo"},
						{"identity": "bwk", "role": "go-specialist"},
					},
					"collaborations": []map[string]string{
						{"from": "go-specialist", "to": "coo", "type": "reports_to"},
						{"from": "go-specialist", "to": "architect", "type": "reports_to"},
					},
				})
				writeYAML(t, filepath.Join(ethosDir, "roles", "coo.yaml"), map[string]interface{}{
					"name": "coo",
					"responsibilities": []string{
						"release management",
						"operational decisions",
					},
				})
			},
			assert: func(t *testing.T, content string) {
				want := "\n\n## What You Don't Do\n\n" +
					"You report to coo and architect. These are not yours:\n\n" +
					"- release management (coo)\n" +
					"- operational decisions (coo)\n" +
					"- system design reviews (architect)\n" +
					"- interface stability (architect)\n" +
					"\nTalents: engineering\n"
				assert.Contains(t, content, want)
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
// overall derivation. The other target's bullets still appear, and
// the stderr warning is captured and asserted so a future refactor
// that silently drops the warning will fail this test.
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

	stderr := captureStderr(t, func() {
		err := GenerateAgentFiles(root, ids, teams, roles)
		require.NoError(t, err)
	})

	// The warning is the only signal the user gets that the team graph
	// references a missing role. Protect it from silent removal.
	assert.Contains(t, stderr, "anti-responsibilities")
	assert.Contains(t, stderr, "ghost")
	assert.Contains(t, stderr, "not found")

	data, readErr := os.ReadFile(filepath.Join(root, ".claude", "agents", "bwk.md"))
	require.NoError(t, readErr)
	content := string(data)

	assert.Contains(t, content, "## What You Don't Do")
	assert.Contains(t, content, "You report to coo. These are not yours:")
	assert.Contains(t, content, "- release management (coo)\n")
	assert.NotContains(t, content, "ghost")
}

// TestDeriveAntiResponsibilities_UnsupportedEdgeType verifies that a
// non-reports_to edge from the agent's role (a typo like "report_to"
// or a deferred type like "collaborates_with") is warned about and
// skipped, not silently dropped. The team package's Load does not
// call Validate, so hand-edited YAML with an invalid type can reach
// the generator — the user must see the warning.
func TestDeriveAntiResponsibilities_UnsupportedEdgeType(t *testing.T) {
	root, _, _, _ := setupTestRepo(t)
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")

	writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
		"name":         "engineering",
		"repositories": []string{"punt-labs/ethos"},
		"members": []map[string]string{
			{"identity": "claude", "role": "coo"},
			{"identity": "bwk", "role": "go-specialist"},
		},
		"collaborations": []map[string]string{
			{"from": "go-specialist", "to": "coo", "type": "collaborates_with"},
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

	stderr := captureStderr(t, func() {
		err := GenerateAgentFiles(root, ids, teams, roles)
		require.NoError(t, err)
	})

	// The warning must name the From role, the To role, and the
	// offending type so a user grepping stderr can locate the exact
	// edge in the YAML without scanning every outgoing edge from From.
	assert.Contains(t, stderr, "unsupported edge")
	assert.Contains(t, stderr, "collaborates_with")
	assert.Contains(t, stderr, `"go-specialist"`)
	assert.Contains(t, stderr, `"coo"`)

	data, readErr := os.ReadFile(filepath.Join(root, ".claude", "agents", "bwk.md"))
	require.NoError(t, readErr)
	content := string(data)

	// No reports_to edges contributed → no section.
	assert.NotContains(t, content, "## What You Don't Do")
	assert.NotContains(t, content, "These are not yours:")
}

// TestDeriveAntiResponsibilities_Normalization exercises the whitespace
// normalization applied to each responsibility string: leading/trailing
// whitespace is trimmed, internal newlines collapse to spaces, and
// strings empty after normalization are dropped with a stderr warning.
// Content is never otherwise rewritten.
func TestDeriveAntiResponsibilities_Normalization(t *testing.T) {
	root, _, _, _ := setupTestRepo(t)
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
	// Each responsibility exercises one normalization path:
	// 1. leading/trailing whitespace → trimmed
	// 2. embedded newline → single space
	// 3. empty string → skipped with warning
	// 4. whitespace-only → skipped with warning
	// 5. embedded \r\n → single space
	writeYAML(t, filepath.Join(ethosDir, "roles", "coo.yaml"), map[string]interface{}{
		"name": "coo",
		"responsibilities": []string{
			"  leading and trailing  ",
			"line1\nline2",
			"",
			"   ",
			"crlf1\r\ncrlf2",
		},
	})

	ids := identity.NewLayeredStore(
		identity.NewStore(ethosDir),
		identity.NewStore(ethosDir),
	)
	teams := team.NewLayeredStore(ethosDir, ethosDir)
	roles := role.NewLayeredStore(ethosDir, ethosDir)

	stderr := captureStderr(t, func() {
		err := GenerateAgentFiles(root, ids, teams, roles)
		require.NoError(t, err)
	})

	// The two empty-after-trim entries each produce one warning.
	assert.Equal(t, 2, strings.Count(stderr, "empty responsibility"),
		"expected exactly two empty-responsibility warnings, got stderr: %s", stderr)

	data, readErr := os.ReadFile(filepath.Join(root, ".claude", "agents", "bwk.md"))
	require.NoError(t, readErr)
	content := string(data)

	// Non-empty bullets emit with whitespace cleaned up.
	assert.Contains(t, content, "- leading and trailing (coo)\n")
	assert.Contains(t, content, "- line1 line2 (coo)\n")
	assert.Contains(t, content, "- crlf1 crlf2 (coo)\n")
	// Empty and whitespace-only entries produce no bullets.
	assert.NotContains(t, content, "-  (coo)")
	assert.NotContains(t, content, "- (coo)")
}

// TestNormalizeResponsibility exercises the string-level normalization
// helper directly so the edge cases are covered without setting up a
// full generator fixture.
func TestNormalizeResponsibility(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "release management", "release management"},
		{"leading and trailing whitespace", "  release management  ", "release management"},
		{"empty", "", ""},
		{"whitespace only", "   \t  ", ""},
		{"bare newline", "\n", ""},
		{"embedded LF", "line1\nline2", "line1 line2"},
		{"embedded CRLF", "line1\r\nline2", "line1 line2"},
		{"embedded CR", "line1\rline2", "line1 line2"},
		{"line separator U+2028", "line1\u2028line2", "line1 line2"},
		{"paragraph separator U+2029", "line1\u2029line2", "line1 line2"},
		{"newline then trim", "\n line with lf \n", "line with lf"},
		{"multiple embedded newlines", "a\nb\nc", "a b c"},
		{"newline with indented continuation", "hello\n  world", "hello world"},
		{"double newline", "a\n\nb", "a b"},
		{"trailing whitespace before newline", "hello  \n  world", "hello world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeResponsibility(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
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

// TestHasWriteTool covers the exact-string membership predicate that
// gates the PostToolUse hook block in generated frontmatter. Only the
// literal strings "Write" and "Edit" count — no case folding, no
// substring matching, no inference from related tool names.
func TestHasWriteTool(t *testing.T) {
	tests := []struct {
		name  string
		tools []string
		want  bool
	}{
		{"nil", nil, false},
		{"empty", []string{}, false},
		{"write alone", []string{"Write"}, true},
		{"edit alone", []string{"Edit"}, true},
		{"both write and edit", []string{"Write", "Edit"}, true},
		{"write among others", []string{"Read", "Write", "Bash"}, true},
		{"edit among others", []string{"Read", "Edit", "Grep"}, true},
		{"read only", []string{"Read", "Grep", "Glob", "Bash"}, false},
		{"no overlap", []string{"Task", "WebFetch"}, false},
		{"case mismatch write", []string{"write"}, false},
		{"case mismatch edit", []string{"EDIT"}, false},
		{"substring not enough", []string{"MultiEdit"}, false},
		{"prefix not enough", []string{"WriteFile"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasWriteTool(tt.tools)
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
