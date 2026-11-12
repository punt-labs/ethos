// Package pluginskill names the skills ethos ships through its Claude Code
// plugin (plugin/skills/), the single source of truth shared by the agent
// generator (internal/hook) and the seeder (internal/seed).
//
// Only these slugs resolve as ethos:<slug> / ethos-dev:<slug>. Everything
// else a generated agent or a bundle might reference — a user's own skill, a
// third-party bundle skill installed under its bare slug, or an
// already-namespaced slug such as lux:scene — is NOT an ethos plugin skill:
// the generator references it by its own name and the seeder may deploy it
// flat. Prefixing one of those with ethos: would point at a skill ethos does
// not ship, which Claude Code then silently skips.
//
// The set is a literal, not a runtime read of plugin/skills/, because an
// installed consumer resolves the ethos binary off PATH with no plugin
// directory beside it. TestSkillsMatchPluginDir keeps the literal in sync
// with the plugin/skills/ tree so the two cannot drift.
package pluginskill

// skills is the authoritative set of ethos-plugin skill slugs.
var skills = map[string]bool{
	"baseline-ops":        true,
	"create-from-project": true,
	"mission":             true,
	"optimize-team":       true,
	"gstack-plan":         true,
	"gstack-design":       true,
	"gstack-debug":        true,
	"gstack-qa":           true,
	"gstack-review":       true,
	"gstack-ship":         true,
}

// Is reports whether slug is a skill ethos ships through its plugin, and so
// should be referenced as ethos:<slug> rather than by a bare or foreign name.
func Is(slug string) bool {
	return skills[slug]
}

// Slugs returns the ethos-plugin skill slugs. The returned slice is a fresh
// copy the caller may sort or mutate freely; order is unspecified.
func Slugs() []string {
	out := make([]string, 0, len(skills))
	for s := range skills {
		out = append(out, s)
	}
	return out
}
