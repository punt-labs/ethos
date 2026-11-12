# Seeded skills

This directory no longer holds any skill that `ethos seed` deposits to the
global `~/.claude/skills/` namespace. Every ethos skill now ships through the
plugin under `plugin/skills/<name>/`, so it resolves as `ethos:<skill>` per
`standards/plugins.md` §"Namespace scope" rather than as a bare slug in the
flat global namespace.

This holds for all three kinds:

- `baseline-ops` — operational discipline preloaded into every generated
  agent. Referenced in agent frontmatter as `skills: [ethos:baseline-ops]`
  (`internal/hook/generate_agents.go`).
- The gstack bundle skills (`gstack-plan`, `gstack-design`, `gstack-debug`,
  `gstack-qa`, `gstack-review`, `gstack-ship`) — the role-based pipeline
  skills, referenced as `ethos:<skill>`.
- The discoverable skills a human or agent invokes by name (`optimize-team`,
  `mission`, `create-from-project`).

All of them live under `plugin/skills/<name>/SKILL.md`.

Flat copies deposited by an older `ethos seed` are cleaned up on upgrade via
`retiredTopLevelSkills` (`seed.go`). This `README.md` remains only to keep the
`Skills` embed directive (`embed.go`) matching at least one file; the seed
deploys nothing from it.
