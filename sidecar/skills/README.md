# Skills

Claude Code skills shipped in this directory. To make them available
to Claude Code, copy the desired skill folders into ~/.claude/skills/.
Skills provide preloaded content injected into sub-agent context at
startup via the `skills` frontmatter field.

## baseline-ops

Operational discipline for sub-agents that lose Claude Code's default
system prompt. Covers dedicated tool usage, verification, scope
discipline, commits, security, and output formatting.

Referenced in agent frontmatter as `skills: [baseline-ops]`.

See [baseline-ops/SKILL.md](baseline-ops/SKILL.md) for the full content.
