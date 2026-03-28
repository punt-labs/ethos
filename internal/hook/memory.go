package hook

import (
	"fmt"
	"strings"
)

// BuildMemorySection assembles a markdown section explaining quarry memory
// usage for an agent. Returns empty string if the identity has no quarry
// extension or no memory_collection configured.
func BuildMemorySection(ext map[string]map[string]string, handle string) string {
	if ext == nil {
		return ""
	}
	quarry, ok := ext["quarry"]
	if !ok {
		return ""
	}
	collection, ok := quarry["memory_collection"]
	if !ok || collection == "" {
		return ""
	}

	var b strings.Builder

	b.WriteString("## Memory\n\n")
	b.WriteString("You have persistent memory stored in quarry, a local semantic search\n")
	b.WriteString("engine. Your memories survive across sessions and machines.\n")

	b.WriteString("\n### Working Memory\n\n")
	fmt.Fprintf(&b, "Collection: %q\n\n", collection)
	b.WriteString("To recall prior knowledge:\n")
	fmt.Fprintf(&b, "  /find <query> — or use the quarry find tool with collection=%q, agent_handle=%q\n\n", collection, handle)
	b.WriteString("To persist something you learned:\n")
	fmt.Fprintf(&b, "  /remember <content> — or use the quarry remember tool with collection=%q, agent_handle=%q, memory_type=fact|observation|procedure|opinion\n\n", collection, handle)
	b.WriteString("Memory types:\n")
	b.WriteString("- fact: objective, verifiable information (\"the API rate limit is 100 req/s\")\n")
	b.WriteString("- observation: neutral summary of an entity or system\n")
	b.WriteString("- procedure: how-to knowledge (\"when deploying, run migrations first\")\n")
	b.WriteString("- opinion: subjective assessment with confidence")

	if expertise, ok := quarry["expertise_collections"]; ok && expertise != "" {
		b.WriteString("\n\n### Expertise\n\n")
		fmt.Fprintf(&b, "Your expertise corpus is in collections: %s.\n", expertise)
		b.WriteString("Search these for deep domain knowledge. Expertise does not decay over time.")
	}

	return b.String()
}
