# Roles

Starter role definitions for common agent archetypes. Each role defines
tools, responsibilities, and model preference. Referenced from team
member assignments.

Roles are deployed by the installer to `~/.punt-labs/ethos/roles/`.
Teams override or extend with project-specific roles.

`model` (and every other field) is a one-time starter default, not a
synced value. `ethos seed` writes these files once; a file the user
has since edited is preserved on every later seed run (see
`internal/seed/seed.go`'s upgrade-vs-edited distinction). This repo's
own `.punt-labs/ethos/roles/` happens to agree with these defaults for
several role names today, but nothing enforces that: this repo tunes
its own model tiers independently, same as any other repo seeded from
this content. Do not add a test coupling the two.

See [ETHOS-ROADMAP.md](../../docs/ETHOS-ROADMAP.md) for context on
role archetypes and the persona/role/mission model.

## Fields

This table is generated from the schema registry and checked by
validate-content. Edit `internal/schema` and rebuild — do not hand-edit.

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `name` | yes | string (slug) | Role name; lowercase alphanumeric with hyphens. |
| `model` | no | enum + pattern | Claude model: opus, sonnet, haiku, inherit, or any claude-* ID. Empty inherits. |
| `responsibilities` | no | list of string | What the role is accountable for. |
| `permissions` | no | list of string | Permission grants for the role. |
| `tools` | no | list of string | Tool names available to the role. |
| `safety_constraints` | no | list of object | Tool-usage restrictions: {tool, message}. |
| `output_format` | no | string (markdown) | Body of the generated agent's Output Format section. |
