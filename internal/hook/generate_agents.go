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
		return nil // not configured — nothing to generate
	}

	cfg, err := resolve.LoadRepoConfig(repoRoot)
	if err != nil {
		return nil // no repo config — nothing to generate
	}
	if cfg == nil {
		return nil
	}

	mainAgent := cfg.Agent
	teamName := cfg.Team
	if teamName == "" {
		return nil // no team configured — nothing to generate
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

		expected++

		antiResps := deriveAntiResponsibilities(m.Role, t.Collaborations, roles)
		content := buildAgentFile(id, r, antiResps)

		destPath := filepath.Join(destDir, id.Handle+".md")

		existing, readErr := os.ReadFile(destPath)
		if readErr == nil && string(existing) == content {
			generated++
			continue
		}

		if err := os.MkdirAll(destDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: generate-agents: skipping %q: creating agents dir: %v\n", m.Identity, err)
			continue
		}
		if err := os.WriteFile(destPath, []byte(content), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "ethos: generate-agents: skipping %q: writing agent file: %v\n", m.Identity, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "ethos: generate-agents: wrote %s\n", destPath)
		generated++
	}

	if expected > 0 && generated == 0 {
		return fmt.Errorf("generated 0 of %d expected agent files", expected)
	}

	return nil
}

// antiResponsibility is a responsibility belonging to a role the agent
// reports to — i.e., something explicitly not the agent's job.
type antiResponsibility struct {
	Responsibility string
	TargetRole     string
}

// respNormalizer collapses every line-break character a YAML
// double-quoted scalar can legally contain down to a single space:
// LF, CRLF, bare CR, Unicode line separator U+2028, and Unicode
// paragraph separator U+2029. Caller collapses the resulting runs
// of whitespace and trims.
var respNormalizer = strings.NewReplacer(
	"\r\n", " ",
	"\n", " ",
	"\r", " ",
	"\u2028", " ",
	"\u2029", " ",
)

// normalizeResponsibility applies whitespace-only cleanup to a
// responsibility string: every line-break form becomes a space, runs
// of whitespace collapse to a single space, and outer whitespace is
// trimmed. The result is always a single line. Content is never
// otherwise rewritten — a responsibility string containing markdown
// metacharacters (e.g. a leading "- ") is the role author's choice
// and passes through verbatim.
func normalizeResponsibility(s string) string {
	s = respNormalizer.Replace(s)
	// strings.Fields splits on any Unicode whitespace run and drops
	// leading/trailing whitespace in one pass. Rejoining with a single
	// space collapses every internal run — tabs, double spaces, and
	// the indentation that follows a replaced newline.
	return strings.Join(strings.Fields(s), " ")
}

// deriveAntiResponsibilities walks the team's collaboration edges
// starting from roleName and returns the reports_to targets'
// responsibilities as a flat list in walk order. Each responsibility is
// normalized: embedded newlines collapse to spaces, surrounding
// whitespace is trimmed, and strings empty after normalization are
// dropped with a stderr warning. Targets whose role fails to load are
// also skipped with a warning. Non-reports_to edges from roleName are
// warned about — this catches typos ("report_to", "reports-to") and
// future edge types (collaborates_with, delegates_to) that the team
// package's Load does not validate. Returns nil if roleName has no
// outgoing reports_to edges or every target contributes zero bullets.
func deriveAntiResponsibilities(roleName string, collabs []team.Collaboration, roles *role.LayeredStore) []antiResponsibility {
	var out []antiResponsibility
	for _, c := range collabs {
		if c.From != roleName {
			continue
		}
		if c.Type != "reports_to" {
			fmt.Fprintf(os.Stderr,
				"ethos: generate-agents: anti-responsibilities: unsupported edge from %q to %q with type %q (expected \"reports_to\") — skipping\n",
				c.From, c.To, c.Type)
			continue
		}
		target, err := roles.Load(c.To)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ethos: generate-agents: anti-responsibilities: target role %q: %v\n", c.To, err)
			continue
		}
		for _, resp := range target.Responsibilities {
			norm := normalizeResponsibility(resp)
			if norm == "" {
				fmt.Fprintf(os.Stderr,
					"ethos: generate-agents: anti-responsibilities: role %q: empty responsibility skipped\n",
					c.To)
				continue
			}
			out = append(out, antiResponsibility{
				Responsibility: norm,
				TargetRole:     c.To,
			})
		}
	}
	return out
}

// joinWithOxford joins names in English. Two items: "a and b". Three or
// more: Oxford-comma "a, b, and c". One item returns as-is. Zero items
// returns the empty string.
func joinWithOxford(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

// buildAgentFile assembles a .claude/agents/<handle>.md from identity,
// personality, writing-style, and role data. antiResps is the flat list
// of responsibilities belonging to roles this agent reports to; when
// non-empty, it is rendered as a "## What You Don't Do" section between
// Responsibilities and Talents.
func buildAgentFile(id *identity.Identity, r *role.Role, antiResps []antiResponsibility) string {
	var b strings.Builder

	// Extract description: first non-heading content line from personality.
	desc := extractDescription(id.PersonalityContent)

	// Frontmatter.
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", id.Handle)
	fmt.Fprintf(&b, "description: %s\n", yamlQuote(desc))
	b.WriteString("tools:\n")
	for _, t := range r.Tools {
		fmt.Fprintf(&b, "  - %s\n", t)
	}
	if r.Model != "" {
		fmt.Fprintf(&b, "model: %s\n", yamlQuote(r.Model))
	}
	b.WriteString("skills:\n")
	b.WriteString("  - baseline-ops\n")
	b.WriteString("---\n")

	// Body.
	personalityBody := stripLeadingHeading(id.PersonalityContent)

	// Opening line.
	firstLine := firstContentSentence(id.PersonalityContent)
	firstLine = strings.TrimSuffix(firstLine, ".")
	if firstLine == "" {
		fmt.Fprintf(&b, "\nYou are %s (%s).\n", id.Name, id.Handle)
	} else {
		fmt.Fprintf(&b, "\nYou are %s (%s), %s.\n", id.Name, id.Handle, firstLine)
	}
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
			b.WriteString("\n## Writing Style\n\n")
			b.WriteString(wsBody)
			b.WriteString("\n")
		}
	}

	// Responsibilities.
	if len(r.Responsibilities) > 0 {
		b.WriteString("\n## Responsibilities\n\n")
		for _, resp := range r.Responsibilities {
			fmt.Fprintf(&b, "- %s\n", resp)
		}
	}

	// Anti-responsibilities — what this agent does NOT do, derived
	// from the target roles of reports_to edges. Bullets are bucketed
	// by target in a single pass: targets records first-seen order,
	// byTarget groups the bullets. Preamble and bullet block then
	// render from the same ordered slice, so the two orderings cannot
	// drift.
	if len(antiResps) > 0 {
		b.WriteString("\n## What You Don't Do\n\n")
		targets := make([]string, 0, len(antiResps))
		byTarget := make(map[string][]antiResponsibility, len(antiResps))
		for _, ar := range antiResps {
			if _, ok := byTarget[ar.TargetRole]; !ok {
				targets = append(targets, ar.TargetRole)
			}
			byTarget[ar.TargetRole] = append(byTarget[ar.TargetRole], ar)
		}
		fmt.Fprintf(&b, "You report to %s. These are not yours:\n\n", joinWithOxford(targets))
		for _, tgt := range targets {
			for _, ar := range byTarget[tgt] {
				fmt.Fprintf(&b, "- %s (%s)\n", ar.Responsibility, ar.TargetRole)
			}
		}
	}

	// Talents. A leading newline guarantees a blank line between the
	// previous section (Responsibilities, anti-responsibilities, or
	// Writing Style) and this line so the label never hugs a bullet.
	if len(id.Talents) > 0 {
		fmt.Fprintf(&b, "\nTalents: %s\n", strings.Join(id.Talents, ", "))
	}

	return b.String()
}

// extractDescription returns the first prose sentence from markdown
// content, suitable for the frontmatter description field. Skips
// headings, bullets, and other non-prose lines.
func extractDescription(content string) string {
	return firstContentSentence(content)
}

// yamlQuote wraps s in double quotes, escaping internal backslashes
// and double quotes. This prevents YAML-significant characters like
// # and : from corrupting frontmatter values.
func yamlQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
