package hook

import (
	"fmt"
	"strings"

	"github.com/punt-labs/ethos/internal/identity"
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
	first := firstContentLine(id.PersonalityContent)
	if first != "" {
		fmt.Fprintf(&b, "You are %s (%s), %s", id.Name, id.Handle, first)
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

// BuildCondensedPersona assembles a compact persona summary for PreCompact.
// Returns empty string if id is nil or has no personality/writing style content.
func BuildCondensedPersona(id *identity.Identity) string {
	if id == nil {
		return ""
	}
	if id.PersonalityContent == "" && id.WritingStyleContent == "" {
		return ""
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("Active persona: %s (%s)", id.Name, id.Handle))

	if id.Personality != "" && id.PersonalityContent != "" {
		rules := extractRules(id.PersonalityContent, 3)
		if rules != "" {
			lines = append(lines, fmt.Sprintf("Personality: %s -- %s", id.Personality, rules))
		} else {
			lines = append(lines, fmt.Sprintf("Personality: %s", id.Personality))
		}
	}

	if id.WritingStyle != "" && id.WritingStyleContent != "" {
		rules := extractRules(id.WritingStyleContent, 3)
		if rules != "" {
			lines = append(lines, fmt.Sprintf("Writing: %s -- %s", id.WritingStyle, rules))
		} else {
			lines = append(lines, fmt.Sprintf("Writing: %s", id.WritingStyle))
		}
	}

	if len(id.Talents) > 0 {
		lines = append(lines, fmt.Sprintf("Talents: %s", strings.Join(id.Talents, ", ")))
	}

	return strings.Join(lines, "\n")
}

// firstContentLine returns the first non-heading, non-blank line from
// markdown content. Returns empty string if no such line exists.
func firstContentLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

// extractRules pulls up to n list-item lines ("- ...") from markdown
// content, joining them with "; ". Returns empty string if none found.
func extractRules(content string, n int) string {
	var rules []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		rules = append(rules, strings.TrimPrefix(trimmed, "- "))
		if len(rules) >= n {
			break
		}
	}
	return strings.Join(rules, "; ")
}
