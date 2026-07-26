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
  `internal/mcp/team_tools.go:13` (team). It already drifts, and the drift
  is in the **handlers**, not only the declarations: the role tool declares
  three fields (`role_tools.go:22-30`) *and* its `handleCreateRole` reads
  only those three (`role_tools.go:49-65`), silently dropping `model`,
  `tools`, `safety_constraints`, `output_format`. The team `create` handler
  never reads `collaborations` (`team_tools.go:75-103`) — they arrive only
  via the separate `add_collab` method.
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
(`internal/schema`) built by reflect-plus-overlay: field names and
required-ness come live from the Go structs by reflection, and the registry
overlays only descriptions, enums, type labels, and patterns. The CLI
table, the `--json` JSON Schema, the MCP `inputSchema` **and its `create`
handlers**, the `validate-content` enum check, and the seeded READMEs all
read from that registry. Names and required-ness cannot drift because they
are reflected, not copied; enums are guarded by a test; description prose is
authored and unguarded. The MCP handlers are extended to read every field
the schema advertises, so the tool cannot advertise a field it drops.

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
   model               no        enum + pattern             Claude model: opus, sonnet, haiku, inherit, or any claude-* ID. Empty inherits.
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
    "model": {"description": "Claude model; empty inherits.", "anyOf": [{"enum": ["opus", "sonnet", "haiku", "inherit"]}, {"type": "string", "pattern": "^claude-"}]},
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
| `model` | no | enum + pattern | Alias `opus`/`sonnet`/`haiku`/`inherit` **or** any `claude-*` string; empty inherits; `role.go:45`. Not a closed enum. |
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

## The schema registry — reflect plus overlay

A new package `internal/schema` holds the registry. It does **not** restate
the field set. Field *names* and *required-ness* come from the Go structs by
reflection — one source, never copied. The registry supplies only what a
struct tag cannot hold: the description, the enum values, the human type
label, and the pattern. This is a reflect-plus-overlay design: reflection is
the field set, the overlay is the prose and constraints.

```go
// internal/schema — descriptions, enums, and constraints overlaid onto the
// reflected struct shape. Field names and required-ness come from the
// structs; this package supplies only what struct tags cannot carry.
package schema

// Overlay is the metadata a struct tag cannot hold, keyed by wire name.
type Overlay struct {
    Type        string             // human type label: "string (slug)", "list of object"
    Enum        []string           // closed-enum values, when the field is a closed enum
    Pattern     string             // regexp for slug/handle fields
    Description string             // one sentence, human-facing (authored, unguarded)
    Fields      map[string]Overlay // nested-object overlays (Member, Collaboration, SafetyConstraint)
}

// Entity binds a Go struct to its overlay. Struct is reflected for field
// names and required-ness (a field is required when its yaml tag lacks
// ,omitempty); Overlay is merged in by wire name.
type Entity struct {
    Name    string             // "Identity"
    Wire    string             // "identity" — the subcommand and JSON title stem
    Struct  any                // identity.Identity{} — reflected, never persisted
    Overlay map[string]Overlay
}

// The registry: one Entity per typed entity.
var Identity = Entity{Name: "Identity", Wire: "identity", Struct: identity.Identity{}, Overlay: /* ... */}
var Role     = Entity{Name: "Role", Wire: "role", Struct: role.Role{}, Overlay: /* ... */}
var Team     = Entity{Name: "Team", Wire: "team", Struct: team.Team{}, Overlay: /* ... */}

// Registry maps wire name to Entity for CLI and MCP dispatch.
var Registry = map[string]Entity{"identity": Identity, "role": Role, "team": Team}
```

`Field` is the *merged* view — reflection for name and required, overlay for
the rest — produced on demand, never stored:

```go
type Field struct {
    Name, Type, Pattern, Description string
    Required bool
    Enum     []string
    Fields   []Field // nested object fields
}

func (e Entity) Fields() []Field                            // reflect Struct, merge Overlay by wire name

func (e Entity) Table() (headers []string, rows [][]string) // for hook.FormatTable
func (e Entity) JSONSchema() map[string]any                 // draft 2020-12 object
func (e Entity) MarkdownTable() string                      // the README fields block
```

`Fields()` walks the struct once: for each field with a real YAML tag (not
`-`), it takes the wire name and the required bit (tag lacks `,omitempty`)
from reflection, then merges the overlay entry of the same wire name for
type/enum/pattern/description. A nested struct field
(`[]team.Member`, `[]team.Collaboration`, `[]role.SafetyConstraint`)
recurses, merging `Overlay.Fields`. The three render methods consume
`Fields()`, so the CLI table, the JSON Schema, and the README block are the
same field set in three formats.

### What is drift-guarded, and what is not

State it plainly: **field names, required-ness, and closed-enum membership
are drift-guarded; description prose is authored and unguarded.**

- **Field names and required-ness** cannot drift — they are read live from
  the struct by reflection every render, not copied into the registry. Add,
  rename, or re-`omitempty` a struct field and every rendering follows on the
  next build with no registry edit.
- **Enums** are guarded by a test: `internal/schema/schema_test.go` asserts
  each overlay `Enum` equals the exported package slice it mirrors
  (`identity.KindValues`, `role.ModelAliases`, `team.CollaborationTypes`).
  Add a collaboration type in `team.go` and forget the overlay: the test
  fails.
- **Overlay coverage** is guarded: the test walks each struct by reflection
  and asserts every persisted field (and every nested-struct field) has an
  overlay entry with a non-empty `Type` and `Description`, and that no
  overlay entry names a field the struct lacks (no phantom entries). A new
  struct field with no overlay fails the test.
- **Not guarded: whether the prose is *accurate*.** The test proves a
  description exists, not that it is correct. A maintainer can write a
  misleading sentence and CI will pass. That is the one hand-maintained
  surface; it is small (one clause per field) and reviewed like any other
  doc.

The `role.model` case is deliberately partial: `ValidateModel`
(`role.go:45`) accepts four aliases **or** any `claude-*` string, so the
overlay `Enum` lists only the four aliases and the guard asserts only those.
The pattern half (`^claude-`) is authored in the overlay `Pattern` and is
not enum-guarded — documented here as a known partial, not an oversight.

### How each consumer reads the registry

| Consumer | Reads | Replaces today's |
|----------|-------|------------------|
| CLI `identity/role/team schema` (human) | `Entity.Table()` → `hook.FormatTable` | — (new) |
| CLI `... schema --json` | `Entity.JSONSchema()` → `writeJSON` | — (new) |
| MCP `inputSchema` | registry `Fields()` → generated `WithString`/`WithArray`/`Enum` options | hand-written blocks in `tools.go:156`, `role_tools.go:12`, `team_tools.go:13` |
| MCP `create` handlers | registry `Fields()` to know which fields to read | partial hand-written arg reads |
| `validate-content` | registry enum/required metadata, cross-checked against the validators | duplicated enum/required knowledge |
| Seeded READMEs | `Entity.MarkdownTable()`, checked against the committed file | hand-maintained tables |

**MCP is in scope, and the handler is the real work.** Generating the
`inputSchema` from the registry is not enough — and on its own it would be
wrong. Today's gap is not in the schema declaration, it is in the
**handlers**: `handleCreateRole` (`role_tools.go:49-65`) reads only `name`,
`responsibilities`, and `permissions`, dropping `model`, `tools`,
`safety_constraints`, and `output_format` even if a caller sends them.
`handleCreateTeam` (`team_tools.go:75-103`) reads `name`, `repositories`,
and `members` but never `collaborations` — those arrive only through the
separate `add_collab` method (`team_tools.go:183`). A registry-generated
schema that advertised all fields while the handler silently ignored four of
them would be a worse lie than the honest three-field schema shipping today.

So the schema and the handler move together, and the schema advertises only
what the handler reads:

- **`handleCreateRole`** is extended to read all seven role fields —
  `model`, `tools`, `safety_constraints` (an array of `{tool, message}`
  objects, parsed like `parseMembersArg`), and `output_format` — and to
  validate `model` via `role.ValidateModel` before Save.
- **`handleCreateTeam`** is extended to accept a `collaborations` array of
  `{from, to, type}` objects in `create`. **`add_collab` stays** as a
  post-create mutation, so both paths work: create a team with its
  collaborations in one call, or add one later. `Team.Validate` already
  enforces collaboration referential integrity (`team.go:126`), so the
  create path gets the same checks for free.
- **Then** the three tool builders replace their hand-listed
  `WithString`/`WithArray` blocks with a loop over `Entity.Fields()`,
  emitting one option per field with the registry's description and enum. A
  small type mapping (`schema.Field.Type` → `WithString` vs.
  `WithArray`+`WithStringItems` vs. object) lives in `internal/mcp`. Because
  the loop runs over the same `Fields()` the handler now reads, the schema
  and the handler cannot advertise different field sets.

The method-dispatch shape (`create`, `list`, `show`, …) is unrelated to the
field schema and is unchanged; only the field-bearing `create` method gains
fields. `internal/mcp` is named in the implementation write-set. There is no
DES-020 formatter change: `schema` is a CLI-only surface with no MCP tool.

**`validate-content`.** The package validators (`Identity.Validate`,
`team.Validate`, `role.ValidateModel`) remain the runtime authority — they
run at Save and Load and must not move. Required-ness needs no
cross-check: the registry reflects it live from the tags, so it is the tags.
The registry's job in `validate-content` is enum fidelity: it gains a check
that each overlay `Enum` equals the exported package slice
(`identity.KindValues`, `team.CollaborationTypes`, and `role.ModelAliases`
for the alias half of `model`), run in CI against live content. Runtime
validation stays in the packages; the registry guarantees the published
enum set matches what the validators enforce.

**Seeded READMEs.** `validate-content` compares the fields-table block of
each seeded README against `Entity.MarkdownTable()`. Drift fails CI. All
three typed entities get a checked table:

- The identity README (`internal/seed/sidecar/identities/README.md`) already
  has a table — it becomes the checked block, verbatim from
  `Team`/`Identity`… `MarkdownTable()`.
- A fields table is added to the role README
  (`internal/seed/sidecar/roles/README.md`), which today has prose but no
  table.
- A new team README (`internal/seed/sidecar/teams/README.md`) is seeded —
  see the next section — carrying the generated team table.

The check is a comparison, not a write, so there is no generated file that
can lag behind the registry and no `go generate` step: the committed README
and the registry are proven equal on every CI run, and a stale README fails
the build.

### Teams get a new seeded directory and README

Teams have no `internal/seed/sidecar/teams/` directory and no README today,
so there is nowhere a user reads the team field set outside the source. This
feature closes that gap two ways:

- `ethos team schema` becomes the canonical team schema surface — the
  subcommand answers the question the missing README could not, and `--json`
  gives teams a machine-readable schema they never had.
- A new `internal/seed/sidecar/teams/README.md` is seeded for parity with
  identities and roles, carrying the team fields table generated from
  `schema.Team.MarkdownTable()` and held in sync by the same
  `validate-content` comparison. The seed deploys it to
  `~/.punt-labs/ethos/teams/` alongside the other category directories.

Both the subcommand and the README render from the one registry, so the two
team-facing surfaces cannot disagree.

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
  identity struct must be edited in three places for one field change. The
  same three-place burden let the role MCP `create` handler drift to reading
  four fewer fields than the struct defines. A checked README plus a
  registry-driven handler removes the hand-maintenance.

- **Pure reflection with no overlay.** Struct tags carry name and
  required-ness but not descriptions, enums, or human type labels.
  Reflection alone produces a table with empty descriptions. Rejected in
  favor of reflect-plus-overlay: reflection supplies the field set (so names
  and required-ness cannot drift), the overlay supplies the prose and
  enums. This is the adopted design, not a wholesale rejection of
  reflection — only of reflection *alone*.

- **A fully authored registry restating the field set.** The first draft of
  this design copied every field name and required bit into the registry and
  used a guard test to prove the copy matched the struct. rop's review
  showed the copy is pure duplication: reflection already knows the field set
  live, so copying it in only to test the copy adds a maintenance surface for
  no gain. Reflect-plus-overlay deletes the copy.

- **A build-time code generator writing the READMEs (`go generate`).**
  Adds a generated file to review and a generation step to CI, and lets the
  committed file drift from the generator until someone re-runs it. The
  comparison check in `validate-content` is simpler: it needs no generated
  artifact and fails the instant the committed file and the registry
  disagree.

## Scope decisions (operator-ruled)

The operator ruled that this feature ships whole — no deferral of known
work. The findings from rop's review are folded in above; the resulting
scope decisions:

1. **MCP handlers are in scope and change first.** `handleCreateRole` reads
   all seven role fields; `handleCreateTeam` accepts a `collaborations`
   array (with `add_collab` retained). The generated `inputSchema`
   advertises only fields the handlers read. `internal/mcp` is in the
   implementation write-set.

2. **Reflect-plus-overlay, no field-set copy.** Field names and required-ness
   are reflected from the structs; the registry overlays only descriptions,
   enums, type labels, and patterns. The guard test proves overlay coverage
   and enum equality, not a duplicated field set.

3. **Enums are exported as package slices.** `identity.KindValues`,
   `role.ModelAliases`, `team.CollaborationTypes` replace today's inline
   literals (`identity.go:54`, `role.go:49`, `team.go:43`) so the guard and
   `validate-content` can assert against one authoritative set. In scope.

4. **`role.model` is a partial enum.** JSON Schema emits
   `anyOf[{enum:[opus,sonnet,haiku,inherit]},{pattern:"^claude-"}]`, not a
   closed enum, matching `ValidateModel` (`role.go:45`). The guard asserts
   only the alias slice; the pattern half is authored and documented as a
   known partial.

5. **All three READMEs are generated/checked, including a new team README.**
   `internal/seed/sidecar/teams/README.md` is seeded, and identity/role/team
   README tables are held in sync by the `validate-content` comparison.

6. **Attribute referents are noted in descriptions.** The `writing_style`,
   `personality`, and `talents` fields describe the file they reference
   (e.g. "references `writing-styles/<slug>.md`"). One clause; answers the
   obvious next question. Attributes themselves remain out of scope — they
   have no field schema.

7. **JSON Schema dialect is draft 2020-12.** No known downstream consumer
   pins an older draft. If one surfaces, the registry emits that draft
   instead — a one-line change in `JSONSchema()`.

### One open question remains

- **`safety_constraints` in the MCP `create` schema.** The role `create`
  handler will parse `safety_constraints` as an array of `{tool, message}`
  objects. MCP array-of-object schema is expressible but verbose in the
  `mcp-go` builder (`WithArray` with no rich item typing today; see the
  `members` field on the team tool, `team_tools.go:42-44`, which advertises
  a bare array). Recommend: advertise it as an array with an object-shape
  description string, matching how `members` is handled today, and revisit
  if `mcp-go` gains richer item typing. Confirm this matches the team
  `members`/`collaborations` treatment.

## ADR draft (for DESIGN.md)

> ## DES-066: Entity schema command — reflect-plus-overlay registry (PROPOSED)
>
> **Status**: Proposed. Full design in `docs/entity-schema-command.md`.
>
> ### Problem
>
> The shape of each typed entity (identity, role, team) is described in
> several hand-maintained places that already disagree: the Go structs
> (`internal/identity/identity.go:13`, `internal/role/role.go:28`,
> `internal/team/team.go:30`), the MCP `inputSchema` blocks
> (`internal/mcp/tools.go:156`, `role_tools.go:12`, `team_tools.go:13`), the
> `validate-content` checks, the inline enums (`identity.go:54`,
> `role.go:45`, `team.go:43`), and the seeded per-category READMEs (only
> `identities/README.md` has a fields table; roles have none; teams have no
> directory). The gap is worse than a stale table: the role MCP `create`
> handler reads three of seven fields (`role_tools.go:49`) and the team
> `create` handler never reads `collaborations` (`team_tools.go:75`), so the
> tools silently drop input. There is no command an agent can run to learn
> an entity's fields, required-ness, or legal values.
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
> A new `internal/schema` package uses reflect-plus-overlay: field names and
> required-ness are read live from the Go structs by reflection (so they
> cannot drift and are never copied), and the registry overlays only what
> tags cannot carry — descriptions, enums, human type labels, and patterns.
> Render methods (`Table`, `JSONSchema`, `MarkdownTable`) drive every
> consumer: the CLI table, the `--json` schema, the MCP `inputSchema`, the
> `validate-content` enum check, and the seeded READMEs (compared against
> `MarkdownTable()` — a role table is added and a new `teams/README.md` is
> seeded, both checked). The MCP handlers change first and the schema
> advertises only what they read: `handleCreateRole` gains all seven fields,
> `handleCreateTeam` accepts a `collaborations` array (`add_collab`
> retained), then both tool builders generate their options from the
> registry. `internal/mcp` is in the write-set.
>
> Drift guarantees are stated plainly: field names, required-ness, and
> closed-enum membership are guarded (names/required by live reflection;
> enums by a test against exported package slices `identity.KindValues`,
> `role.ModelAliases`, `team.CollaborationTypes`); description prose is
> authored and unguarded (the test proves a description exists, not that it
> is correct). `role.model` is a partial enum: JSON Schema emits
> `anyOf[{enum:[aliases]},{pattern:"^claude-"}]` per `ValidateModel`
> (`role.go:45`), and only the alias slice is guarded. Each entity's
> `--help` gains one line pointing at its schema subcommand; `--help` stays
> about usage, `schema` about shape.
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
>   (role handler drifted to four fewer fields than the struct).
> - **Pure reflection, no overlay** — tags lack descriptions and enums;
>   adopted in part (reflection for the field set) but not alone.
> - **Fully authored registry restating the field set** — pure duplication
>   of what reflection already knows; reflect-plus-overlay deletes the copy.
> - **`go generate` for READMEs** — adds a generated artifact and a CI step;
>   a comparison check needs neither.
