package hook

import (
	"fmt"
	"os"
	"strings"

	"github.com/punt-labs/ethos/internal/identity"
	"github.com/punt-labs/ethos/internal/role"
	"github.com/punt-labs/ethos/internal/team"
)

// BuildPersonaBlock assembles a full persona block from a resolved identity.
// Returns empty string if id is nil or has no personality/writing style content.
func BuildPersonaBlock(id *identity.Identity) string {
	if id == nil {
		return ""
	}
	if id.PersonalityContent == "" && id.WritingStyleContent == "" {
		return ""
	}

	var b strings.Builder

	// Opening line: "You are Name (handle), <first meaningful line of personality>."
	first := firstContentSentence(id.PersonalityContent)
	if first != "" {
		// Strip a single trailing period — we add our own.
		first = strings.TrimSuffix(first, ".")
		fmt.Fprintf(&b, "You are %s (%s), %s.", id.Name, id.Handle, first)
	} else {
		fmt.Fprintf(&b, "You are %s (%s).", id.Name, id.Handle)
	}

	if id.PersonalityContent != "" {
		// Skip the first content paragraph since it's already in the
		// opening line — avoids redundant repetition.
		trimmed := strings.TrimRight(stripLeadingHeading(skipFirstParagraph(id.PersonalityContent)), "\n")
		if trimmed != "" {
			// If remaining content already has its own sub-headings,
			// don't add a redundant ## Personality wrapper.
			if strings.HasPrefix(strings.TrimSpace(trimmed), "##") {
				b.WriteString("\n\n")
			} else {
				b.WriteString("\n\n## Personality\n\n")
			}
			b.WriteString(trimmed)
		}
	}

	if id.WritingStyleContent != "" {
		trimmed := strings.TrimRight(stripLeadingHeading(id.WritingStyleContent), "\n")
		if trimmed != "" {
			b.WriteString("\n\n## Writing Style\n\n")
			b.WriteString(trimmed)
		}
	}

	// Talents are listed as slugs, not full content, to stay within context
	// budget. Full talent content is available on demand via the MCP tool.
	if len(id.Talents) > 0 {
		b.WriteString("\n\n## Talents\n\n")
		b.WriteString(strings.Join(id.Talents, ", "))
	}

	return b.String()
}

// stripLeadingHeading removes the first top-level heading (# Title)
// and any blank line immediately after it. Sub-headings (## ...) are
// preserved. Returns content unchanged if it doesn't start with #.
func stripLeadingHeading(content string) string {
	lines := strings.Split(content, "\n")
	i := 0
	// Skip blank lines.
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	// If first non-blank line is a top-level heading, skip it.
	if i < len(lines) && strings.HasPrefix(lines[i], "# ") {
		i++
		// Skip blank lines after heading.
		for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
			i++
		}
	}
	if i >= len(lines) {
		return ""
	}
	return strings.Join(lines[i:], "\n")
}

// skipFirstParagraph removes the first heading and the first prose
// paragraph from markdown content, returning everything after. This
// avoids repeating the opening sentence that's already used in the
// "You are ..." line. Non-prose lines (bullets, headings, indented
// continuations) are skipped during the search but not consumed as
// the "first paragraph" — only actual prose counts.
func skipFirstParagraph(content string) string {
	lines := strings.Split(content, "\n")
	// Phase 1: skip non-prose lines (headings, blanks, bullets) to
	// find first prose line.
	i := 0
	for i < len(lines) {
		if !isNonProse(lines[i]) {
			break
		}
		i++
	}
	// If no prose paragraph exists, preserve the original content
	// (headings and bullets are still meaningful).
	if i >= len(lines) {
		return content
	}
	// Phase 2: skip the first prose paragraph. Stop at blank lines
	// or non-prose lines (bullets, headings) — only consume prose.
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "" || isNonProse(lines[i]) {
			break
		}
		i++
	}
	// Phase 3: skip blank lines between paragraphs.
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) != "" {
			break
		}
		i++
	}
	if i >= len(lines) {
		return ""
	}
	return strings.Join(lines[i:], "\n")
}

// isNonProse returns true for lines that aren't prose content:
// headings, bullet points (-, *, +), and indented continuation lines
// (which follow bullet points in markdown).
func isNonProse(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return true
	}
	// All three markdown unordered list markers.
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
		return true
	}
	// Indented lines (2+ leading spaces or tab) are bullet continuations.
	if len(line) > 1 && (line[0] == '\t' || (line[0] == ' ' && line[1] == ' ')) {
		return true
	}
	return false
}

// firstContentSentence returns the first sentence from markdown content,
// skipping headings, blank lines, bullet points, and indented continuations.
// Collects continuation lines until a blank line or a line ending with a
// period. Returns empty string if no prose content line exists.
func firstContentSentence(content string) string {
	var parts []string
	collecting := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !collecting {
			if isNonProse(line) {
				continue
			}
			collecting = true
		} else if isNonProse(line) {
			break
		}
		parts = append(parts, trimmed)
		if strings.HasSuffix(trimmed, ".") {
			break
		}
	}
	return strings.Join(parts, " ")
}

// truncateFirstSentence returns the text up to and including the first
// sentence-ending period: a "." followed by a space and an uppercase letter.
// Used for team context summaries where brevity matters.
func truncateFirstSentence(s string) string {
	for i := 0; i < len(s)-2; i++ {
		if s[i] == '.' && s[i+1] == ' ' && s[i+2] >= 'A' && s[i+2] <= 'Z' {
			return s[:i+1]
		}
	}
	return s
}

// BuildTeamContext assembles a team context block from a team definition,
// loading role responsibilities for each member. Returns empty string if
// the team is nil or has no members.
func BuildTeamContext(t *team.Team, roles *role.LayeredStore, identities identity.IdentityStore) string {
	if t == nil || len(t.Members) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Team: %s\n", t.Name)

	for _, m := range t.Members {
		// Load full identity — personality is needed so we know how to
		// work with each team member after compaction (via a summary).
		name := m.Identity
		var id *identity.Identity
		if identities != nil {
			var err error
			id, err = identities.Load(m.Identity)
			if err == nil {
				name = fmt.Sprintf("%s (%s)", id.Name, id.Handle)
			} else {
				fmt.Fprintf(os.Stderr, "ethos: team context: failed to load identity %q: %v\n", m.Identity, err)
			}
		}

		fmt.Fprintf(&b, "\n### %s — %s\n", name, m.Role)

		// Emit personality summary: first sentence gives working style.
		if id != nil && id.PersonalityContent != "" {
			if summary := firstContentSentence(id.PersonalityContent); summary != "" {
				fmt.Fprintf(&b, "%s\n", truncateFirstSentence(summary))
			}
		}

		// Emit writing style summary if available.
		if id != nil && id.WritingStyleContent != "" {
			if ws := firstContentSentence(id.WritingStyleContent); ws != "" {
				fmt.Fprintf(&b, "Writing style: %s\n", truncateFirstSentence(ws))
			}
		}

		// Load role responsibilities.
		if roles != nil {
			if r, err := roles.Load(m.Role); err == nil && len(r.Responsibilities) > 0 {
				for _, resp := range r.Responsibilities {
					fmt.Fprintf(&b, "- %s\n", resp)
				}
			} else if err != nil {
				fmt.Fprintf(os.Stderr, "ethos: team context: failed to load role %q: %v\n", m.Role, err)
			}
		}

		// Emit talents as compact list.
		if id != nil && len(id.Talents) > 0 {
			fmt.Fprintf(&b, "Talents: %s\n", strings.Join(id.Talents, ", "))
		}
	}

	// Emit collaborations as a compact summary.
	if len(t.Collaborations) > 0 {
		b.WriteString("\n### Collaborations\n")
		for _, c := range t.Collaborations {
			fmt.Fprintf(&b, "- %s → %s (%s)\n", c.From, c.To, c.Type)
		}
	}

	return b.String()
}
