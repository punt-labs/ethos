package seed

import "embed"

//go:embed sidecar/roles/*.yaml
var Roles embed.FS

//go:embed sidecar/talents/*.md
var Talents embed.FS

//go:embed sidecar/personalities/*.md
var Personalities embed.FS

//go:embed sidecar/writing-styles/*.md
var WritingStyles embed.FS

//go:embed sidecar/archetypes/*.yaml
var Archetypes embed.FS

//go:embed sidecar/pipelines/*.yaml
var Pipelines embed.FS

//go:embed sidecar/agents/*.md
var Agents embed.FS

//go:embed all:sidecar/bundles
var Bundles embed.FS

// No top-level sidecar skill is seeded to the global ~/.claude/skills/
// namespace anymore: every ethos skill (baseline-ops and the gstack bundle
// skills included) ships through the plugin as ethos:<skill> (plugin/skills/)
// per standards/plugins.md §Namespace scope, and generated agents reference
// them by that namespaced name. The Skills FS embeds only the README so the
// directive stays valid; topLevelSkillSlugs filters to directories, so it
// yields no deployable skill and seedSkills deploys nothing. The former
// flat copies are cleaned up on upgrade via retiredTopLevelSkills.
//
//go:embed sidecar/skills/README.md
var Skills embed.FS

//go:embed sidecar/identities/README.md sidecar/talents/README.md sidecar/personalities/README.md sidecar/writing-styles/README.md sidecar/roles/README.md sidecar/teams/README.md sidecar/skills/README.md sidecar/agents/README.md sidecar/README.md
var Readmes embed.FS
