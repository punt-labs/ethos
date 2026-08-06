# Agents

Personaless, structured-output Claude Code review-checklist agents (DES-070).
Each file is a complete Claude Code subagent definition — YAML frontmatter
(`name`, `description`, `model`, `color`) plus a system prompt body — deployed
verbatim to a consuming repo's `.claude/agents/` by `ethos seed`.

These are not identities. They have no personality, no writing style, no
talents, and are never bound to an ethos handle — unlike the persona-bound
roles in `sidecar/roles/`, which combine with a personality and writing style
via `generate_agents.go` to synthesize a named specialist for the mission
worker/evaluator system. A review-checklist agent is invoked directly by a
leader as a local review pass (Claude Code's normal subagent-invocation
surface); it is never dispatched via `ethos mission dispatch --worker
<handle>`.

- `code-reviewer.md` — general code-quality and CLAUDE.md-compliance review.
- `silent-failure-hunter.md` — error handling, swallowed exceptions, fallback
  logic.
- `invariant-completeness-reviewer.md` — verifies that a claimed invariant,
  exhaustiveness property, or regression-guarding test actually holds, rather
  than trusting the prose that asserts it.

Each agent states explicitly, in its own scope section, what belongs to the
other two — the three partition cleanly so results stay comparable across
repos and a PR only needs to fix a finding once, not reconcile overlapping
reports.

Deployed by the installer to a consuming repo's `.claude/agents/`, tracked in
the seed manifest like every other seeded category — so a hand-edit (an
operator narrowing `code-reviewer`'s scope for their codebase, for example)
survives the next `ethos seed`.
