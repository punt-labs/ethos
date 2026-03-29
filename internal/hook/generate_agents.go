package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/resolve"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"
)

// GenerateAgentFiles creates .claude/agents/<handle>.md files from ethos
// identity, personality, writing-style, and role data. Skips the main
// agent (from repo config) and human identities. Writes are idempotent:
// files are only written when content differs.
func GenerateAgentFiles(repoRoot string, identities identity.IdentityStore, teams *team.LayeredStore, roles *role.LayeredStore) error {
	if teams == nil || roles == nil {
		return fmt.Errorf("teams or roles store is nil")
	}

	cfg, err := resolve.LoadRepoConfig(repoRoot)
	if err != nil {
		return fmt.Errorf("loading repo config: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("no repo config found")
	}

	mainAgent := cfg.Agent
	teamName := cfg.Team
	if teamName == "" {
		return fmt.Errorf("no team configured in repo config")
	}

	t, err := teams.Load(teamName)
	if err != nil {
		return fmt.Errorf("loading team %q: %w", teamName, err)
	}

	destDir := filepath.Join(repoRoot, ".claude", "agents")

	var expected, generated int

	for _, m := range t.Members {
		if m.Identity == mainAgent {
			continue
		}

		id, err := identities.Load(m.Identity)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: generate-agents: skipping %q: %v\n", m.Identity, err)
			continue
		}
		if id.Kind != "agent" {
			continue
		}

		expected++

		r, err := roles.Load(m.Role)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: generate-agents: skipping %q: role %q: %v\n", m.Identity, m.Role, err)
			continue
		}
		if len(r.Tools) == 0 {
			continue
		}

		if id.PersonalityContent == "" {
			fmt.Fprintf(os.Stderr, "ethos: generate-agents: skipping %q: no personality content\n", m.Identity)
			continue
		}

		content := buildAgentFile(id, r)

		destPath := filepath.Join(destDir, id.Handle+".md")

		existing, readErr := os.ReadFile(destPath)
		if readErr == nil && string(existing) == content {
			generated++
			continue
		}

		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return fmt.Errorf("creating agents dir: %w", err)
		}
		if err := os.WriteFile(destPath, []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing agent file %s: %w", destPath, err)
		}
		fmt.Fprintf(os.Stderr, "ethos: generate-agents: wrote %s\n", destPath)
		generated++
	}

	if expected > 0 && generated == 0 {
		return fmt.Errorf("generated 0 of %d expected agent files", expected)
	}

	return nil
}

// buildAgentFile assembles a .claude/agents/<handle>.md from identity,
// personality, writing-style, and role data.
func buildAgentFile(id *identity.Identity, r *role.Role) string {
	var b strings.Builder

	// Extract description: first non-heading content line from personality.
	desc := extractDescription(id.PersonalityContent)

	// Frontmatter.
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", id.Handle)
	fmt.Fprintf(&b, "description: %s\n", desc)
	b.WriteString("tools:\n")
	for _, t := range r.Tools {
		fmt.Fprintf(&b, "  - %s\n", t)
	}
	b.WriteString("---\n")

	// Body.
	personalityBody := stripLeadingHeading(id.PersonalityContent)

	// Opening line.
	firstLine := firstContentSentence(id.PersonalityContent)
	firstLine = strings.TrimSuffix(firstLine, ".")
	fmt.Fprintf(&b, "\nYou are %s (%s), %s.\n", id.Name, id.Handle, firstLine)
	fmt.Fprintf(&b, "You report to Claude Agento (COO/VP Engineering).\n")

	// Personality content (after first paragraph, since opening line uses it).
	remaining := skipFirstParagraph(personalityBody)
	remaining = strings.TrimRight(remaining, "\n")
	if remaining != "" {
		b.WriteString("\n")
		b.WriteString(remaining)
		b.WriteString("\n")
	}

	// Writing style.
	if id.WritingStyleContent != "" {
		wsBody := stripLeadingHeading(id.WritingStyleContent)
		wsBody = strings.TrimRight(wsBody, "\n")
		if wsBody != "" {
			b.WriteString("\n## Writing Style\n")
			b.WriteString(wsBody)
			b.WriteString("\n")
		}
	}

	// Responsibilities.
	if len(r.Responsibilities) > 0 {
		b.WriteString("\n## Responsibilities\n")
		for _, resp := range r.Responsibilities {
			fmt.Fprintf(&b, "- %s\n", resp)
		}
	}

	// Talents.
	if len(id.Talents) > 0 {
		fmt.Fprintf(&b, "Talents: %s\n", strings.Join(id.Talents, ", "))
	}

	return b.String()
}

// extractDescription returns the first non-heading, non-blank line
// from markdown content, suitable for the frontmatter description field.
func extractDescription(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return trimmed
	}
	return ""
}
