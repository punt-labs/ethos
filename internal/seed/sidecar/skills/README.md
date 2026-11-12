# Seeded skills

This directory holds the one skill `ethos seed` deposits to the global
`~/.claude/skills/` namespace: `baseline-ops`. It is preloaded into every
generated agent by bare slug (`skills: [baseline-ops]`), so it must resolve
under that bare name rather than a plugin prefix.

The **discoverable** skills a human or agent invokes by name
(`optimize-team`, `mission`, `create-from-project`) ship through the plugin
instead, under `plugin/skills/<name>/`, so they resolve as `ethos:<skill>`
per `standards/plugins.md` §"Namespace scope" — not in the flat global
namespace. They are not seeded here.

## baseline-ops

Operational discipline for sub-agents that lose Claude Code's default
system prompt. Covers dedicated tool usage, verification, scope
discipline, commits, security, and output formatting.

Referenced in agent frontmatter as `skills: [baseline-ops]`.

See [baseline-ops/SKILL.md](baseline-ops/SKILL.md) for the full content.
