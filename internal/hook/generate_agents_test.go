package hook

import (
	"bytes"
	"errors"
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
						"          command: \"(cd \\\"$CLAUDE_PROJECT_DIR\\\" && make check) 2>&1 | head -n 60\"\n"+
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
						"          command: \"(cd \\\"$CLAUDE_PROJECT_DIR\\\" && make check) 2>&1 | head -n 60\"\n")

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
					"          command: \"(cd \\\"$CLAUDE_PROJECT_DIR\\\" && make check) 2>&1 | head -n 60\"\n" +
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
				// Anchor the absence checks to the frontmatter only,
				// so a future personality or writing-style edit that
				// happens to mention the literal "hooks:" or
				// "PostToolUse" in the body cannot cause a false
				// positive. The invariant under test is strictly
				// about the YAML header.
				parts := strings.SplitN(content, "---", 3)
				require.Len(t, parts, 3, "expected frontmatter delimiters")
				frontmatter := parts[1]
				assert.NotContains(t, frontmatter, "hooks:")
				assert.NotContains(t, frontmatter, "PostToolUse")

				// Frontmatter must still close cleanly: skills block
				// directly followed by `---\n`. This anchor straddles
				// the closing delimiter, so it runs against `content`.
				assert.Contains(t, content,
					"skills:\n"+
						"  - baseline-ops\n"+
						"---\n")
			},
		},
		{
			// Role with output_format set — generator must emit a
			// `## Output Format` section at the END of the body, after
			// `Talents:`. The role provides only the body; the heading
			// is generator-owned. The exact byte-for-byte block is
			// asserted, plus the section's trailing position is locked
			// with HasSuffix so a future regression that emits another
			// section after Output Format fails this case.
			name: "role with output_format emits section",
			setup: func(t *testing.T, root string, ids identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore) {
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				writeYAML(t, filepath.Join(ethosDir, "roles", "with-format.yaml"), map[string]interface{}{
					"name":             "with-format",
					"responsibilities": []string{"do work"},
					"tools":            []string{"Read", "Write", "Bash"},
					"output_format":    "Worker report template:\n- field1\n- field2\n",
				})
				writeYAML(t, filepath.Join(ethosDir, "identities", "wfm.yaml"), map[string]interface{}{
					"name":          "With Format",
					"handle":        "wfm",
					"kind":          "agent",
					"personality":   "kernighan",
					"writing_style": "kernighan-prose",
					"talents":       []string{"engineering"},
				})
				writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
					"name":         "engineering",
					"repositories": []string{"punt-labs/ethos"},
					"members": []map[string]string{
						{"identity": "claude", "role": "coo"},
						{"identity": "bwk", "role": "go-specialist"},
						{"identity": "wfm", "role": "with-format"},
					},
				})
			},
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "wfm.md")
				data, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				content := string(data)

				// Byte-for-byte: the heading is generator-owned, the
				// body is the role's literal content, and a single
				// terminal newline ends the section.
				want := "\n## Output Format\n\n" +
					"Worker report template:\n" +
					"- field1\n" +
					"- field2\n"
				assert.Contains(t, content, want)

				// Exactly-once: the HasSuffix anchor below catches a
				// regression that puts the section somewhere other
				// than the end, but would still pass if the block got
				// emitted twice (once mid-body, once at the end).
				// Count locks the count.
				assert.Equal(t, 1, strings.Count(content, "## Output Format"),
					"Output Format section must appear exactly once")

				// Last-position anchor: Output Format must be the
				// final section in the file. TrimRight strips any
				// trailing newlines so HasSuffix can match the bare
				// final bullet.
				assert.True(t,
					strings.HasSuffix(strings.TrimRight(content, "\n"), "- field2"),
					"## Output Format must be the last section in the file; got tail:\n%s",
					content[max(0, len(content)-80):])

				// Coexistence with the 9ai.2 hooks block: wfm's role
				// has Write in its tools list, so the PostToolUse
				// block must still be in the frontmatter alongside
				// the new Output Format section. A regression that
				// made the two mutually exclusive would break the
				// production case — every write-enabled worker needs
				// both.
				assert.Contains(t, content,
					"skills:\n"+
						"  - baseline-ops\n"+
						"hooks:\n"+
						"  PostToolUse:\n",
					"write-enabled role must still emit hooks block when output_format is set")
				assert.Contains(t, content, "---\n",
					"frontmatter must still close with --- delimiter")

				// Idempotency: regenerating against the same repo
				// root must produce a byte-identical file. This
				// catches normalization drift — e.g., a future
				// change that sorts or re-cases any section on every
				// run would fail here even though a single run
				// looks correct. Rebuild the stores from scratch so
				// the second call reloads every YAML from disk, the
				// same way the session-start hook does in
				// production.
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				ids2 := identity.NewLayeredStore(
					identity.NewStore(ethosDir),
					identity.NewStore(ethosDir),
				)
				teams2 := team.NewLayeredStore(ethosDir, ethosDir)
				roles2 := role.NewLayeredStore(ethosDir, ethosDir)
				require.NoError(t, GenerateAgentFiles(root, ids2, teams2, roles2))
				secondData, readErr2 := os.ReadFile(agentPath)
				require.NoError(t, readErr2)
				assert.Equal(t, content, string(secondData),
					"GenerateAgentFiles must be idempotent for roles with output_format")
			},
		},
		{
			// Role with no output_format — generator must NOT emit a
			// `## Output Format` heading, no blank lines, no trailing
			// section. bwk's default fixture has no output_format set,
			// so this case reuses the default setup (nil) and asserts
			// the absence on bwk.md. The heading-anchored form is the
			// only invariant under test here; a personality or
			// writing-style edit that mentions the words "Output
			// Format" elsewhere in prose is not a regression.
			name: "role without output_format omits section",
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")
				data, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				content := string(data)
				assert.NotContains(t, content, "## Output Format")
			},
		},
		{
			// Role with safety_constraints — generator emits a
			// "## Safety Constraints" section between anti-responsibilities
			// (or responsibilities) and Talents.
			name: "role with safety_constraints emits section",
			setup: func(t *testing.T, root string, ids identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore) {
				ethosDir := filepath.Join(root, ".punt-labs", "ethos")
				writeYAML(t, filepath.Join(ethosDir, "roles", "go-specialist.yaml"), map[string]interface{}{
					"name": "go-specialist",
					"responsibilities": []string{
						"Go package implementation with tests",
						"code review for Go projects",
						"adherence to punt-kit/standards/go.md",
					},
					"tools": []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob"},
					"safety_constraints": []map[string]string{
						{"tool": "Bash", "message": "Never run destructive rm commands"},
						{"tool": "Write|Edit", "message": "Never modify dotenv files"},
					},
				})
			},
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")
				data, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				content := string(data)

				// Section must be present with the correct heading.
				assert.Contains(t, content, "## Safety Constraints\n\n")
				assert.Contains(t, content,
					"These restrictions apply to your tool usage. Violations will be caught by review.\n\n")

				// Both constraints rendered as bullets.
				assert.Contains(t, content, "- **Bash**: Never run destructive rm commands\n")
				assert.Contains(t, content, "- **Write|Edit**: Never modify dotenv files\n")

				// Ordering: Responsibilities < Safety Constraints < Talents.
				respIdx := strings.Index(content, "## Responsibilities")
				safetyIdx := strings.Index(content, "## Safety Constraints")
				talentsIdx := strings.Index(content, "\nTalents:")
				require.True(t, respIdx >= 0 && safetyIdx >= 0 && talentsIdx >= 0,
					"all three anchors must be present")
				assert.Less(t, respIdx, safetyIdx,
					"Safety Constraints must follow Responsibilities")
				assert.Less(t, safetyIdx, talentsIdx,
					"Safety Constraints must precede Talents")
			},
		},
		{
			// Role without safety_constraints — no section emitted.
			// bwk's default fixture has no safety_constraints.
			name: "role without safety_constraints omits section",
			check: func(t *testing.T, root string, err error) {
				require.NoError(t, err)

				agentPath := filepath.Join(root, ".claude", "agents", "bwk.md")
				data, readErr := os.ReadFile(agentPath)
				require.NoError(t, readErr)

				assert.NotContains(t, string(data), "## Safety Constraints")
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
					"          command: \"(cd \\\"$CLAUDE_PROJECT_DIR\\\" && make check) 2>&1 | head -n 60\"\n" +
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
			//
			// Post-ethos-2z2: ValidateStructural requires every
			// collaboration from/to role to be filled by a team member.
			// jfreeman (kind: human in setupTestRepo) fills ceo-empty
			// here so the team passes Load. jfreeman's human kind means
			// the main generator loop skips building a jfreeman.md
			// file; no cross-test contamination.
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
						{"identity": "jfreeman", "role": "ceo-empty"},
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
			// Post-ethos-2z2: jfreeman (kind: human from setupTestRepo)
			// fills the architect role so ValidateStructural passes on
			// Load. The generator skips jfreeman because kind != agent,
			// so no jfreeman.md is produced and the assertion anchors
			// below still match bwk.md byte-for-byte.
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
						{"identity": "jfreeman", "role": "architect"},
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
		{
			// All four body sections stacked in the production order:
			// Responsibilities, reports_to anti-responsibilities,
			// Talents, Output Format. This is the shape every
			// write-enabled agent will have once the Punt Labs team
			// roles opt into output_format. The byte-anchor locks the
			// blank-line discipline between every boundary so a
			// regression that fused any pair would fail here.
			name: "all four body sections: responsibilities + anti + talents + output_format",
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
						"release management",
					},
				})
				// Reuse go-specialist from setupTestRepo but add
				// output_format. The responsibilities list stays
				// byte-identical so the tail anchor below can
				// include the bullet list verbatim.
				writeYAML(t, filepath.Join(ethosDir, "roles", "go-specialist.yaml"), map[string]interface{}{
					"name": "go-specialist",
					"responsibilities": []string{
						"Go package implementation with tests",
						"code review for Go projects",
						"adherence to punt-kit/standards/go.md",
					},
					"tools":         []string{"Read", "Write", "Edit", "Bash", "Grep", "Glob"},
					"output_format": "Report template:\n- item1\n- item2\n",
				})
			},
			assert: func(t *testing.T, content string) {
				// Byte-for-byte tail anchor: every section boundary
				// gets exactly one blank line above and below, and
				// the file ends at the final bullet.
				want := "\n## Responsibilities\n\n" +
					"- Go package implementation with tests\n" +
					"- code review for Go projects\n" +
					"- adherence to punt-kit/standards/go.md\n" +
					"\n## What You Don't Do\n\n" +
					"You report to coo. These are not yours:\n\n" +
					"- release management (coo)\n" +
					"\nTalents: engineering\n" +
					"\n## Output Format\n\n" +
					"Report template:\n" +
					"- item1\n" +
					"- item2\n"
				assert.Contains(t, content, want)

				// Output Format is still the last section in the file.
				assert.True(t,
					strings.HasSuffix(strings.TrimRight(content, "\n"), "- item2"),
					"combined-sections case: Output Format must remain last; tail was:\n%s",
					content[max(0, len(content)-120):])

				// Section ordering anchors: each ## heading index
				// must strictly increase down the file.
				respIdx := strings.Index(content, "## Responsibilities")
				antiIdx := strings.Index(content, "## What You Don't Do")
				talentsIdx := strings.Index(content, "\nTalents:")
				outputIdx := strings.Index(content, "## Output Format")
				require.True(t,
					respIdx >= 0 && antiIdx >= 0 && talentsIdx >= 0 && outputIdx >= 0,
					"every section must be present: resp=%d anti=%d talents=%d output=%d",
					respIdx, antiIdx, talentsIdx, outputIdx)
				assert.Less(t, respIdx, antiIdx)
				assert.Less(t, antiIdx, talentsIdx)
				assert.Less(t, talentsIdx, outputIdx)
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
//
// Post-ethos-2z2 Store.Load calls ValidateStructural, which rejects
// any collaboration whose from/to role is not filled by a team
// member. To preserve the "role file is missing" semantic under
// test, the fixture adds jfreeman (a kind: human identity from
// setupTestRepo) as a member with role "ghost" — that makes "ghost"
// a filled role on the roster so Load accepts the team, but the
// on-disk .punt-labs/ethos/roles/ghost.yaml file still does not
// exist, so roles.Load("ghost") inside deriveAntiResponsibilities
// still fails with the same "not found" error the test asserts.
// jfreeman being kind human means the main generator loop skips
// building a jfreeman.md file; no cross-test contamination.
func TestDeriveAntiResponsibilities_MissingTarget(t *testing.T) {
	root, _, _, _ := setupTestRepo(t)
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")

	// Wire two reports_to edges, one pointing at a role whose YAML
	// file does not exist on disk.
	writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
		"name":         "engineering",
		"repositories": []string{"punt-labs/ethos"},
		"members": []map[string]string{
			{"identity": "claude", "role": "coo"},
			{"identity": "bwk", "role": "go-specialist"},
			// jfreeman fills "ghost" so ValidateStructural passes.
			// The ghost role YAML is deliberately not created, so
			// roles.Load("ghost") downstream still fails — the
			// invariant under test.
			{"identity": "jfreeman", "role": "ghost"},
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
// non-reports_to edge from the agent's role is warned about and
// skipped, not silently dropped.
//
// Post-ethos-2z2: team.Store.Load calls ValidateStructural, which
// rejects any collaboration type not in validCollabTypes. So the
// only way a non-reports_to edge reaches the generator is if it is
// a valid-but-deferred type — collaborates_with or delegates_to.
// Those are semantic-level "not handled by MVP" decisions, not
// structural errors, and deriveAntiResponsibilities warns on them
// and continues. The fixture uses collaborates_with specifically to
// exercise the valid-but-deferred path; a typo'd type like
// "report_to" would be rejected at Load time and could never reach
// this test.
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

// TestGenerateAgentFiles_MalformedConfig covers bug ethos-9ai.6: a
// malformed .punt-labs/ethos.yaml must propagate the parse error wrapped
// with "generate agents". Before the fix, yaml.Unmarshal failures were
// swallowed and the generator returned nil — the user had no signal
// that their config was broken.
func TestGenerateAgentFiles_MalformedConfig(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	// Unclosed bracket — yaml.Unmarshal fails. The file exists and is
	// readable, so the not-found and permission branches in
	// LoadRepoConfig are bypassed; this is the parse-error path.
	require.NoError(t, os.WriteFile(
		filepath.Join(cfgDir, "ethos.yaml"),
		[]byte("team: [unclosed bracket\n"), 0o644))

	ethosDir := filepath.Join(cfgDir, "ethos")
	ids := identity.NewLayeredStore(
		identity.NewStore(ethosDir),
		identity.NewStore(ethosDir),
	)
	teams := team.NewLayeredStore(ethosDir, ethosDir)
	roles := role.NewLayeredStore(ethosDir, ethosDir)

	err := GenerateAgentFiles(root, ids, teams, roles)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generate agents",
		"error must be wrapped with the caller's operation context")
	// The chain must include LoadRepoConfig's own parse-error wrapper,
	// so a user reading the message sees both layers: which caller
	// failed, and which underlying operation failed inside the loader.
	assert.Contains(t, err.Error(), "parsing repo config",
		"error chain must surface the underlying yaml parse failure")

	// %w chain must unwrap, not just render. This protects the caller
	// and LoadRepoConfig wraps against a future refactor that drops
	// either one to a plain %v — the rendered string would still
	// contain the substrings above but errors.Unwrap would return nil
	// at the wrong layer. Count the layers explicitly:
	//   outer = "generate agents: ..."            (GenerateAgentFiles)
	//   mid   = "parsing repo config: ..."        (LoadRepoConfig)
	//   leaf  = "yaml: line 1: did not find ..."  (yaml.v3 scanner)
	// yaml.v3 returns a plain *errors.errorString for scanner errors
	// (TypeError is only for type-conversion failures), so we probe
	// the chain by depth, not by target type.
	mid := errors.Unwrap(err)
	require.NotNil(t, mid,
		"outer wrap must use %%w; errors.Unwrap returned nil at depth 1")
	assert.Contains(t, mid.Error(), "parsing repo config",
		"depth-1 error must be LoadRepoConfig's parse-error wrap")
	leaf := errors.Unwrap(mid)
	require.NotNil(t, leaf,
		"LoadRepoConfig parse wrap must use %%w; errors.Unwrap returned nil at depth 2")
	assert.Contains(t, leaf.Error(), "yaml:",
		"depth-2 error must be the yaml.v3 scanner error")
	// The chain terminates here — yaml.v3's scanner error is a leaf
	// string error with no further wrapping. Unwrapping a third time
	// must return nil; any non-nil result would mean an unexpected
	// wrap layer snuck into the chain.
	assert.Nil(t, errors.Unwrap(leaf),
		"yaml scanner error must be a leaf; unexpected wrap at depth 3")
}

// TestGenerateAgentFiles_UnreadableConfig covers bug ethos-9ai.6 on the
// I/O-error path: a .punt-labs/ethos.yaml with 0o000 permissions must
// propagate the read error wrapped with "generate agents". Running as
// root defeats chmod, so this test skips in that case. Mirrors
// TestLoadRepoConfig_PermissionError in internal/resolve.
func TestGenerateAgentFiles_UnreadableConfig(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-denied test is meaningless as root")
	}
	root := t.TempDir()
	cfgDir := filepath.Join(root, ".punt-labs")
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))
	cfgPath := filepath.Join(cfgDir, "ethos.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("agent: claude\nteam: engineering\n"), 0o644))
	require.NoError(t, os.Chmod(cfgPath, 0o000))
	// Restore permissions so t.TempDir cleanup can remove the file.
	t.Cleanup(func() { _ = os.Chmod(cfgPath, 0o644) })

	ethosDir := filepath.Join(cfgDir, "ethos")
	ids := identity.NewLayeredStore(
		identity.NewStore(ethosDir),
		identity.NewStore(ethosDir),
	)
	teams := team.NewLayeredStore(ethosDir, ethosDir)
	roles := role.NewLayeredStore(ethosDir, ethosDir)

	err := GenerateAgentFiles(root, ids, teams, roles)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generate agents",
		"error must be wrapped with the caller's operation context")
	// LoadRepoConfig wraps os.ReadFile errors with "reading <path>: %w".
	// Surface that layer too so the user sees which file failed.
	assert.Contains(t, err.Error(), "reading",
		"error chain must surface the underlying read failure")
	// The wrapped path must identify the exact file that failed, so a
	// user debugging a permission mishap does not have to guess whether
	// the new or the legacy config path is at fault. LoadRepoConfig's
	// "reading %s: %w" format means the full path is in the message.
	assert.Contains(t, err.Error(), filepath.Join(".punt-labs", "ethos.yaml"),
		"error must name the file that failed to read")
}

// TestGenerateAgentFiles_NoConfigFile covers invariant 1: when neither
// .punt-labs/ethos.yaml nor the legacy config path exists, LoadRepoConfig
// returns (nil, nil) and GenerateAgentFiles returns nil. This is the
// "ethos is installed but the repo isn't configured" case and must stay
// silent. The test is explicit rather than implicit so a future change
// that breaks the cfg == nil branch cannot hide behind other tests.
func TestGenerateAgentFiles_NoConfigFile(t *testing.T) {
	root := t.TempDir()
	// Intentionally do NOT create .punt-labs/ethos.yaml or
	// .punt-labs/ethos/config.yaml. LoadRepoConfig must return
	// (nil, nil) for this case.

	ethosDir := filepath.Join(root, ".punt-labs", "ethos")
	ids := identity.NewLayeredStore(
		identity.NewStore(ethosDir),
		identity.NewStore(ethosDir),
	)
	teams := team.NewLayeredStore(ethosDir, ethosDir)
	roles := role.NewLayeredStore(ethosDir, ethosDir)

	err := GenerateAgentFiles(root, ids, teams, roles)
	require.NoError(t, err,
		"missing repo config is not an error — it is the unconfigured case")

	// No agents directory should have been created either, because
	// the function returns before the generation loop.
	_, statErr := os.Stat(filepath.Join(root, ".claude", "agents"))
	assert.True(t, os.IsNotExist(statErr),
		".claude/agents must not be created when there is no repo config")
}

// TestGenerateAgentFiles_PartialWriteFailure covers bug ethos-9ai.7: when
// some agent files fail to write and others succeed, the function must
// return an error naming both counts. Before the fix, the narrow check
// `expected > 0 && generated == 0` only caught the total-failure case,
// so a team of 10 with 5 write failures returned nil and the user saw a
// clean exit code plus 5 stderr warnings — no way for a caller to gate
// on partial success.
//
// Force technique: pre-create one destination path as a directory. The
// generator's ReadFile returns EISDIR (not nil), so the idempotent-skip
// branch is bypassed; MkdirAll(destDir, 0o755) succeeds because the
// parent already exists; and os.WriteFile(destPath, ...) fails with
// EISDIR because you cannot open a directory with O_WRONLY. The other
// agent's write path is untouched and succeeds.
func TestGenerateAgentFiles_PartialWriteFailure(t *testing.T) {
	root, ids, teams, roles := setupTestRepo(t)

	// Add a second agent identity that shares bwk's personality, writing
	// style, and role so expected == 2 with no new fixture scaffolding.
	// The team membership rewrite below puts both agents in the roster.
	//
	// Loop-ordering assumption: the team roster is [claude, bwk, bwk2].
	// claude is the main agent and the first loop iteration skips it;
	// bwk is processed second and hits the pre-created-directory EISDIR
	// write failure; bwk2 is processed third and writes successfully.
	// The "generated 1 of 2" and "failed: bwk" assertions below depend
	// on this ordering, which is deterministic because YAML sequences
	// preserve order and team.Load preserves the sequence order into
	// t.Members, and GenerateAgentFiles iterates t.Members sequentially.
	ethosDir := filepath.Join(root, ".punt-labs", "ethos")
	writeYAML(t, filepath.Join(ethosDir, "identities", "bwk2.yaml"), map[string]interface{}{
		"name":          "Brian K Two",
		"handle":        "bwk2",
		"kind":          "agent",
		"personality":   "kernighan",
		"writing_style": "kernighan-prose",
		"talents":       []string{"engineering"},
	})
	writeYAML(t, filepath.Join(ethosDir, "teams", "engineering.yaml"), map[string]interface{}{
		"name":         "engineering",
		"repositories": []string{"punt-labs/ethos"},
		"members": []map[string]string{
			{"identity": "claude", "role": "coo"},
			{"identity": "bwk", "role": "go-specialist"},
			{"identity": "bwk2", "role": "go-specialist"},
		},
	})

	// Pre-create bwk.md as a directory. WriteFile will fail with
	// EISDIR when the generator tries to open it for writing; bwk2's
	// write path is untouched and will succeed.
	destDir := filepath.Join(root, ".claude", "agents")
	require.NoError(t, os.MkdirAll(filepath.Join(destDir, "bwk.md"), 0o755))

	// Rebuild stores so the new bwk2 identity and the updated team are
	// visible. Matches the setup-modification pattern used elsewhere in
	// this file.
	ids = identity.NewLayeredStore(
		identity.NewStore(ethosDir),
		identity.NewStore(ethosDir),
	)
	teams = team.NewLayeredStore(ethosDir, ethosDir)
	roles = role.NewLayeredStore(ethosDir, ethosDir)

	stderr := captureStderr(t, func() {
		err := GenerateAgentFiles(root, ids, teams, roles)
		require.Error(t, err,
			"partial write failure must return an error, not swallow it")
		assert.Contains(t, err.Error(), "generated 1 of 2",
			"error must name both counts honestly")
		// The summary error must also name the failing member(s) so a
		// caller reading only the returned error can identify which
		// agents failed without cross-referencing the stderr warnings.
		// bwk is the member whose write path hit EISDIR in this case;
		// bwk2 wrote successfully, so only bwk should be listed. The
		// anchor "(failed: bwk)" includes the closing paren so a
		// regression that listed "bwk2" alone (matching "failed: bwk"
		// as a prefix) would still fail this test — the paren is the
		// membership boundary.
		assert.Contains(t, err.Error(), "(failed: bwk)",
			"error must name exactly the failing member, closed by a paren")
	})

	// The stderr warning for the failed bwk write must still fire — it
	// is the per-failure signal. Without this, a user cannot tell which
	// agent failed even though the summary error reports the count.
	assert.Contains(t, stderr, "writing agent file",
		"per-failure stderr warning must fire for the EISDIR write path")
	assert.Contains(t, stderr, "bwk",
		"stderr warning must name the failing member")

	// The successful agent's file must exist with correct content. A
	// regression that aborted the loop on the first failure would leave
	// bwk2.md absent, so this is the complementary anchor to the error
	// assertion above.
	bwk2Data, readErr := os.ReadFile(filepath.Join(destDir, "bwk2.md"))
	require.NoError(t, readErr, "successful agent's file must still be written")
	assert.Contains(t, string(bwk2Data), "name: bwk2")
	assert.Contains(t, string(bwk2Data), "You are Brian K Two (bwk2)")

	// The pre-created directory is still a directory — the failing
	// write did not silently convert it. This locks the EISDIR path
	// against a future change that might `os.RemoveAll` before
	// retrying, which would hide the failure instead of propagating it.
	info, statErr := os.Stat(filepath.Join(destDir, "bwk.md"))
	require.NoError(t, statErr)
	assert.True(t, info.IsDir(),
		"pre-created directory at bwk.md must stay a directory")
}
