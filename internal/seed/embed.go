package seed

import "embed"

//go:embed sidecar/roles/*.yaml
var Roles embed.FS

//go:embed sidecar/talents/*.md
var Talents embed.FS

//go:embed sidecar/skills/baseline-ops/SKILL.md sidecar/skills/mission/SKILL.md
var Skills embed.FS

//go:embed sidecar/identities/README.md sidecar/talents/README.md sidecar/personalities/README.md sidecar/writing-styles/README.md sidecar/roles/README.md sidecar/skills/README.md sidecar/README.md
var Readmes embed.FS
