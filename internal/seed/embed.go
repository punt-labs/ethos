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

// baseline-ops is the only top-level sidecar skill still seeded to the global
// ~/.claude/skills/ namespace: it is preloaded into every generated agent by
// bare slug (generate_agents.go), not discovered by name. The discoverable
// skills (optimize-team, mission, create-from-project) ship through the plugin
// as ethos:<skill> (plugin/skills/) per standards/plugins.md §Namespace scope.
//
//go:embed sidecar/skills/baseline-ops/SKILL.md
var Skills embed.FS

//go:embed sidecar/identities/README.md sidecar/talents/README.md sidecar/personalities/README.md sidecar/writing-styles/README.md sidecar/roles/README.md sidecar/teams/README.md sidecar/skills/README.md sidecar/agents/README.md sidecar/README.md
var Readmes embed.FS
