# Entity schema command

Design for a `schema` subcommand on the three typed entities — identity,
role, team — backed by a single schema registry that every schema-shaped
consumer reads.

## Problem

An agent or human who wants to know the *shape* of a typed entity — what
fields exist, which are required, what values are legal — has no command to
ask. The knowledge is scattered across five places that already disagree:

- **The Go structs** are the real authority: `internal/identity/identity.go:13`
  (`Identity`), `internal/role/role.go:28` (`Role`) with
  `internal/role/role.go:14` (`SafetyConstraint`), and
  `internal/team/team.go:30` (`Team`) with `Member` (`team.go:16`) and
  `Collaboration` (`team.go:22`).
- **The MCP `inputSchema`** is hand-written `WithString`/`WithArray`
  descriptions in `internal/mcp/tools.go:156` (identity),
  `internal/mcp/role_tools.go:12` (role), and
  `internal/mcp/team_tools.go:13` (team). It already drifts: the role tool
  exposes only `name`, `responsibilities`, `permissions`
  (`role_tools.go:22-30`) and omits `model`, `tools`, `safety_constraints`,
  `output_format` entirely.
- **`validate-content`** encodes required-ness and enums a third time, by
  calling the package validators — `Identity.Validate`
  (`identity.go:44`), `team.Validate` (`team.go:126`) — from
  `cmd/validate-content/main.go`.
- **The seeded per-category READMEs** carry a fields table, but only for
  identities (`internal/seed/sidecar/identities/README.md`). Roles have a
  README with no table (`internal/seed/sidecar/roles/README.md`); teams
  have no seeded directory and no README at all.
- **The enums** live inline: `kind` in `identity.go:54`, collaboration
  `type` in `team.go:43`, role `model` in `role.go:45`.

Five representations, hand-maintained, already out of sync. The identity
README lists nine fields; the identity MCP tool lists nine; the role MCP
tool lists three of seven; teams have no human-readable field reference
outside the source.

## Decision

Add a `schema` subcommand to each of the three typed entities, alongside
their existing `create`/`list`/`show` verbs:

```text
ethos identity schema [--json]
ethos role schema [--json]
ethos team schema [--json]
```

Default output is a human-readable field table. `--json` emits a JSON
Schema (draft 2020-12) object. Both render from one **schema registry**
(`internal/schema`) that is the single source of truth. The CLI table, the
`--json` JSON Schema, the MCP `inputSchema`, the `validate-content` field
metadata, and the seeded READMEs all read from that registry, and a guard
test proves the registry matches the Go structs field-for-field so they
cannot drift.

Attributes (personality, talent, writing-style) are excluded. They are
markdown files — a title and prose body, not a typed field set — so they
have no schema to render.

### CLI behavior

Default (human) output columns: `FIELD`, `REQUIRED`, `TYPE`,
`DESCRIPTION`. Rendered with `hook.FormatTable`
(`internal/hook/format_output.go:177`), the same table renderer `ethos
doctor` already uses, so the output matches house style and pipes cleanly.

```text
$ ethos role schema
▶  FIELD               REQUIRED  TYPE                       DESCRIPTION
   name                yes       string (slug)              Role name; lowercase alphanumeric with hyphens.
   model               no        enum                       Claude model: opus, sonnet, haiku, inherit, or a claude-* ID. Empty inherits.
   responsibilities    no        list of string            What the role is accountable for.
   permissions         no        list of string            Permission grants for the role.
   tools               no        list of string            Tool names available to the role.
   safety_constraints  no        list of object            Tool-usage restrictions: {tool, message}.
   output_format       no        string (markdown)         Body of the generated agent's Output Format section.
```

`--json` emits a JSON Schema object for the same entity, suitable for
piping into a validator or an editor's schema store:

```text
$ ethos role schema --json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "Role",
  "type": "object",
  "required": ["name"],
  "additionalProperties": false,
  "properties": {
    "name": {"type": "string", "description": "Role name; lowercase alphanumeric with hyphens.", "pattern": "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$"},
    "model": {"type": "string", "description": "Claude model...", "enum": ["opus", "sonnet", "haiku", "inherit"]},
    "responsibilities": {"type": "array", "items": {"type": "string"}, "description": "..."},
    "permissions": {"type": "array", "items": {"type": "string"}, "description": "..."},
    "tools": {"type": "array", "items": {"type": "string"}, "description": "..."},
    "safety_constraints": {"type": "array", "items": {"type": "object", "required": ["tool", "message"], "properties": {"tool": {"type": "string"}, "message": {"type": "string"}}}, "description": "..."},
    "output_format": {"type": "string", "description": "..."}
  }
}
```

Field naming in every representation uses the YAML/JSON wire name
(`writing_style`, `safety_constraints`), not the Go field name, because
that is what a user writes in a file and what the MCP tool accepts.

Exit codes: `0` on success, `2` on usage error (unknown flag), per the CLI
standard. `schema` takes no arguments and never touches the store, so it
cannot fail on I/O.

### The three schemas, grounded in the structs

**Identity** — `internal/identity/identity.go:13-41`. Only the nine
persisted fields (those with a real YAML tag) are in the schema. The
`yaml:"-"` fields (`WritingStyleContent`, `PersonalityContent`,
`TalentContents`, `Warnings`, `Ext`, lines 26-40) are resolved at load
time, never written by a user, and are excluded.

| Field | Required | Type | Notes |
|-------|----------|------|-------|
| `name` | yes | string | Display name. `identity.go:45` requires it. |
| `handle` | yes | string (slug) | Pattern `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`; `identity.go:10,51`. |
| `kind` | yes | enum | `human` or `agent`; `identity.go:54`. |
| `email` | no | string | Must contain `@`, no whitespace; `identity.go:62`. |
| `github` | no | string | GitHub handle (Biff binding). |
| `agent` | no | string (path) | Path to Claude Code agent `.md`. |
| `writing_style` | no | string (slug) | References `writing-styles/<slug>.md`. |
| `personality` | no | string (slug) | References `personalities/<slug>.md`. |
| `talents` | no | list of string (slug) | Each references `talents/<slug>.md`. |

**Role** — `internal/role/role.go:28-36`.

| Field | Required | Type | Notes |
|-------|----------|------|-------|
| `name` | yes | string (slug) | Slug rules; `role.go:39`. |
| `model` | no | enum | `opus`, `sonnet`, `haiku`, `inherit`, or `claude-*`; empty inherits; `role.go:45`. |
| `responsibilities` | no | list of string | |
| `permissions` | no | list of string | |
| `tools` | no | list of string | |
| `safety_constraints` | no | list of object | `{tool, message}`; both required; `role.go:14`. |
| `output_format` | no | string (markdown) | Free-form; trusted source, unvalidated; `role.go:19-27`. |

**Team** — `internal/team/team.go:30-35`.

| Field | Required | Type | Notes |
|-------|----------|------|-------|
| `name` | yes | string (slug) | Slug rules; `team.go:38`. |
| `repositories` | no | list of string | Repository paths. |
| `members` | yes | list of object | `{identity, role}`, both required; at least one member; `team.go:16`, `team.go:66-85`. |
| `collaborations` | no | list of object | `{from, to, type}`; `team.go:22`. |

Nested object `Member` (`team.go:16`): `identity` (required, string),
`role` (required, string). Nested object `Collaboration` (`team.go:22`):
`from` (required, string), `to` (required, string), `type` (required,
enum: `reports_to`, `collaborates_with`, `delegates_to`; `team.go:43`).

Required-ness is derived structurally: a field is required exactly when its
YAML tag has no `,omitempty`. `Identity.Name`/`Handle`/`Kind`,
`Role.Name`, and `Team.Name`/`Members` are required; everything else is
optional. The registry's `required` bit for each field is checked against
the struct tag by the guard test (below), so this derivation cannot rot.

## The schema registry — single source of truth

A new package `internal/schema` holds the registry. It is a small, flat
data structure — data before algorithm — that describes each entity once.

```go
// internal/schema — the one description of every typed entity's shape.
package schema

type Field struct {
    Name        string   // wire name: "writing_style"
    Required    bool     // true when the struct tag lacks ,omitempty
    Type        string   // "string", "list of string", "enum", "list of object"
    Enum        []string // legal values when Type == "enum"
    Pattern     string   // regexp for slug/handle fields, "" otherwise
    Description string   // one sentence, human-facing
    Fields      []Field  // nested object fields (Member, Collaboration, SafetyConstraint)
}

type Entity struct {
    Name   string  // "Identity"
    Wire   string  // "identity" — the subcommand and JSON title stem
    Fields []Field
}

// The registry. Descriptions and enums are authored here; names, types,
// and required bits are proved against the Go structs by schema_test.go.
var Identity = Entity{ /* ... */ }
var Role     = Entity{ /* ... */ }
var Team     = Entity{ /* ... */ }

// Registry maps wire name to Entity for the CLI and MCP dispatch.
var Registry = map[string]Entity{"identity": Identity, "role": Role, "team": Team}
```

Two render functions live beside the data, so every consumer emits
identical output:

```go
func (e Entity) Table() (headers []string, rows [][]string) // for hook.FormatTable
func (e Entity) JSONSchema() map[string]any                  // draft 2020-12 object
func (e Entity) MarkdownTable() string                       // the README fields block
```

### Why the registry is authored, not pure reflection

Go struct tags carry the wire name and `,omitempty` (so *name* and
*required* are reflectable) but not the description, the enum values, or
the human type label. Those must be written somewhere. Pure reflection
cannot produce them; a fully hand-written table drifts. The registry is the
middle path: descriptions and enums are authored once in `internal/schema`,
and a **guard test proves the reflectable half against the structs** so the
authored half is the only thing a maintainer touches and the field set can
never silently diverge.

### The anti-drift guard (the teeth)

`internal/schema/schema_test.go` walks each Go struct by reflection and
asserts, for every entity:

1. Every persisted struct field (YAML tag present and not `-`) appears in
   the registry `Entity`, matched by wire name.
2. No registry field lacks a corresponding struct field (no phantom
   fields).
3. `Field.Required == (tag has no ",omitempty")` for each field.
4. Nested structs (`Member`, `Collaboration`, `SafetyConstraint`) are
   walked the same way against `Field.Fields`.

Add a field to `Identity` and forget the registry: the test fails on rule 1.
Delete a field and leave it in the registry: rule 2 fails. Flip a field to
`omitempty` and forget: rule 3 fails. The struct stays the authority; the
registry stays in lockstep; CI enforces it. Enum values (`kind`, `model`,
collaboration `type`) are exported from their packages as slices and the
test asserts the registry enum equals the package's authoritative set, so
adding a collaboration type in `team.go:43` and forgetting the registry
also fails.

### How each consumer reads the registry

| Consumer | Reads | Replaces today's |
|----------|-------|------------------|
| CLI `identity/role/team schema` (human) | `Entity.Table()` → `hook.FormatTable` | — (new) |
| CLI `... schema --json` | `Entity.JSONSchema()` → `writeJSON` | — (new) |
| MCP `inputSchema` | registry `Field` list → generated `WithString`/`WithArray`/`Enum` options | hand-written blocks in `tools.go:156`, `role_tools.go:12`, `team_tools.go:13` |
| `validate-content` | registry enum/required metadata, cross-checked against the validators | duplicated enum/required knowledge |
| Seeded READMEs | `Entity.MarkdownTable()`, checked against the committed file | hand-maintained tables |

**MCP `inputSchema`.** The three tool builders keep their method dispatch
(`create`, `list`, `show`, …) — that shape is unrelated to field schema.
Only the field-bearing method (`create`) changes: instead of hand-listing
`WithString("email", Description("..."))`, the builder loops the registry
`Entity.Fields` and emits one option per field, pulling the description and
enum from the registry. This closes the role-tool gap
(`role_tools.go:22-30` exposes three of seven fields) for free — the loop
emits all seven. A small mapping (`schema.Field.Type` → `WithString` vs.
`WithArray`+`WithStringItems`) lives in `internal/mcp`, reading the
registry, so descriptions never diverge from the CLI again.

**`validate-content`.** The package validators (`Identity.Validate`,
`team.Validate`, `role.ValidateModel`) remain the runtime authority — they
run at Save and Load and must not move. The registry's role here is to make
the *documented* required/enum set provably equal to what the validators
enforce: `validate-content` gains a check that the registry's required bits
and enums match the struct tags and the exported enum slices (the same
assertion as the guard test, run in CI against live content). Runtime
validation stays in the packages; the registry guarantees the published
schema describes it accurately.

**Seeded READMEs.** `validate-content` compares the fields-table block of
each seeded README against `Entity.MarkdownTable()`. Drift fails CI. The
identity README (`internal/seed/sidecar/identities/README.md`) already has
a table — it becomes the generated block. A fields table is added to the
role README (`internal/seed/sidecar/roles/README.md`), generated the same
way. This keeps the seeded docs and the command in permanent agreement
without a build-time code generator: the check is a comparison, not a
write, so there is no generated file to commit and no `go generate` step.

### Teams have no seeded directory — how team schema is exposed

Teams have no `internal/seed/sidecar/teams/` directory and no README, so
there is no seeded document to keep in sync and nowhere a user currently
reads the team field set. `ethos team schema` **is** the canonical team
schema surface — the subcommand answers the question the missing README
would have. The `--json` output additionally gives teams a machine-readable
schema they never had.

Whether to also seed a `teams/README.md` (generated from
`schema.Team.MarkdownTable()`, for parity with identities and roles) is an
open question for the leader (below). It is not required for the command to
be the source of truth; it would only add a second, generated rendering.

### --help versus schema

Each typed entity's `--help` gains one line pointing at its schema
subcommand, and no more. `--help` stays about *usage* (what verbs exist,
what flags they take); `schema` is about *shape* (what fields an entity
has). Keeping them separate stops `--help` from bloating into a field
reference and gives the field reference a stable home.

- `ethos identity --help` → add: `Run 'ethos identity schema' for the identity field reference.`
- `ethos role --help` → add: `Run 'ethos role schema' for the role field reference.`
- `ethos team --help` → add: `Run 'ethos team schema' for the team field reference.`

Wired via each group command's `Long` field (`identityCmd`,
`identity_group.go:7`; `roleCmd`, `role.go:30`; `teamCmd`, `team.go:43`).

## Rejected alternatives

- **A top-level `ethos schema <entity>` command.** Operator ruled for the
  per-entity subcommand shape. It also reads worse: `ethos team schema`
  sits with `ethos team create`/`show`/`list`, so a user exploring `ethos
  team --help` discovers it. A separate top-level command hides the schema
  from the entity's own help and invents a fourth noun position the CLI
  does not otherwise use.

- **A `--schema` flag on `show`/`create`.** Overloads a verb: `show`
  displays one stored instance, `create` writes one. Schema is neither — it
  is a property of the type, not of any instance. A flag that ignores the
  command's argument and store is a hidden mode, not a flag.

- **Enrich `create --help`.** Conflates usage with shape and gives the
  field reference no machine-readable form. `--help` text is not pipeable
  to a JSON Schema validator; it also cannot be reused by the MCP tool or
  the READMEs, so the drift problem survives.

- **Hand-maintained per-directory READMEs (the status quo).** The
  observed failure: the identity README, the identity MCP tool, and the
  identity struct must be edited in three places for one field change, and
  the role MCP tool already lost four fields this way. A generated /
  checked README removes the hand-maintenance.

- **Pure reflection with no authored registry.** Struct tags carry name
  and required-ness but not descriptions, enums, or human type labels.
  Reflection alone produces a table with empty descriptions. The authored
  registry supplies the prose; reflection (in the guard test) supplies the
  discipline.

- **A build-time code generator writing the READMEs (`go generate`).**
  Adds a generated file to review and a generation step to CI, and lets the
  committed file drift from the generator until someone re-runs it. The
  comparison check in `validate-content` is simpler: it needs no generated
  artifact and fails the instant the committed file and the registry
  disagree.

## Open questions for the leader

1. **Seed a `teams/README.md`?** Teams have no seeded directory today. The
   subcommand is the source of truth regardless; a generated team README
   would add parity with identities/roles at the cost of a new seeded file.
   Recommend: yes, for parity — one generated block, checked like the
   others. Leader's call.

2. **Enum exports.** The guard test wants each enum as an exported package
   slice (`identity.KindValues`, `role.ModelAliases`,
   `team.CollaborationTypes`). Today these are inline literals
   (`identity.go:54`, `role.go:49`, `team.go:43`). Exporting them is a
   small, safe refactor the implementation mission would include. Confirm
   it is in scope.

3. **`writing-style`/`personality`/`talent` cross-reference.** These
   identity fields hold slugs pointing at attribute files. Should the
   schema note the referent (as the table above does in "Notes"), or stay
   silent since attributes are out of scope? Recommend: note the referent
   in the description; it costs one clause and answers the obvious next
   question.

4. **JSON Schema dialect.** Draft 2020-12 is proposed. If any downstream
   consumer (an editor schema store, a CI validator) pins an older draft,
   name it and the registry will emit that instead.

## ADR draft (for DESIGN.md)

> ## DES-066: Entity schema command — one registry, five renderings (PROPOSED)
>
> **Status**: Proposed. Full design in `docs/entity-schema-command.md`.
>
> ### Problem
>
> The shape of each typed entity (identity, role, team) is described in five
> hand-maintained places that already disagree: the Go structs
> (`internal/identity/identity.go:13`, `internal/role/role.go:28`,
> `internal/team/team.go:30`), the MCP `inputSchema` blocks
> (`internal/mcp/tools.go:156`, `role_tools.go:12`, `team_tools.go:13`), the
> `validate-content` checks, the inline enums (`identity.go:54`,
> `role.go:45`, `team.go:43`), and the seeded per-category READMEs (only
> `identities/README.md` has a fields table; roles have none; teams have no
> directory). The role MCP tool already exposes three of seven fields. There
> is no command an agent can run to learn an entity's fields, required-ness,
> or legal values.
>
> ### Decision
>
> Add a `schema` subcommand to each typed entity — `ethos identity schema`,
> `ethos role schema`, `ethos team schema` — beside their `create`/`list`/
> `show` verbs (per-entity, not a top-level `ethos schema`). Default output
> is a human field table (`FIELD`, `REQUIRED`, `TYPE`, `DESCRIPTION`) via
> `hook.FormatTable`; `--json` emits a JSON Schema (draft 2020-12).
> Attributes (personality, talent, writing-style) are excluded — they are
> title-plus-prose markdown with no field set.
>
> A new `internal/schema` package is the single source of truth: one
> authored `Entity` per typed entity carrying descriptions and enums, with
> render methods (`Table`, `JSONSchema`, `MarkdownTable`). Every schema
> consumer reads it — the CLI table, the `--json` schema, the MCP
> `inputSchema` (generated by looping registry fields, closing the role-tool
> gap), the `validate-content` metadata, and the seeded READMEs (compared
> against `MarkdownTable()`; a role table is added; a team table is an open
> question). A guard test (`schema_test.go`) reflects over the Go structs
> and asserts the registry matches field-for-field on name, required-ness
> (derived from `,omitempty`), and enum membership, so the five renderings
> cannot drift. Each entity's `--help` gains one line pointing at its schema
> subcommand; `--help` stays about usage, `schema` about shape.
>
> ### Rejected alternatives
>
> - **Top-level `ethos schema`** — operator ruled per-entity; hides schema
>   from the entity's own `--help`.
> - **`--schema` flag on show/create** — overloads verbs; schema is a
>   property of the type, not an instance.
> - **Enrich `create --help`** — conflates usage with shape; no
>   machine-readable form; drift survives.
> - **Hand-maintained READMEs (status quo)** — the observed failure mode
>   (role tool lost four fields).
> - **Pure reflection, no registry** — tags lack descriptions and enums.
> - **`go generate` for READMEs** — adds a generated artifact and a CI step;
>   a comparison check needs neither.
