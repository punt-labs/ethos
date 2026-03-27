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
		fmt.Fprintf(&b, "You are %s (%s), %s.", id.Name, id.Handle, first)
	} else {
		fmt.Fprintf(&b, "You are %s (%s).", id.Name, id.Handle)
	}

	if id.PersonalityContent != "" {
		b.WriteString("\n\n## Personality\n\n")
		b.WriteString(id.PersonalityContent)
	}

	if id.WritingStyleContent != "" {
		b.WriteString("\n\n## Writing Style\n\n")
		b.WriteString(id.WritingStyleContent)
	}

	// Talents are listed as slugs, not full content, to stay within context
	// budget. Full talent content is available on demand via the MCP tool.
	if len(id.Talents) > 0 {
		b.WriteString("\n\n## Talents\n\n")
		b.WriteString(strings.Join(id.Talents, ", "))
	}

	return b.String()
}

// firstContentSentence returns the first sentence from markdown content,
// skipping headings and blank lines. Collects continuation lines until a
// blank line or a line ending with a period. Returns empty string if no
// content line exists.
func firstContentSentence(content string) string {
	var parts []string
	collecting := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !collecting {
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			collecting = true
		} else if trimmed == "" {
			break
		}
		parts = append(parts, trimmed)
		if strings.HasSuffix(trimmed, ".") {
			break
		}
	}
	return strings.Join(parts, " ")
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
				fmt.Fprintf(&b, "%s\n", summary)
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
