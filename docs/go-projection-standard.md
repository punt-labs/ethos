# Go Projection Standard

How a Go project exposes its capabilities across 4 surface areas: CLI,
slash commands, MCP tools, and Go library. Ethos is the reference
implementation.

## Principle: One Capability, Four Projections

Every capability in the system exists as a Go function first. The CLI, MCP
tool, and slash command are projections of that function onto different
interfaces. They share the same verbs, the same parameters (modulo
idiomatic conventions), and the same behavior.

```text
Go Function (source of truth)
├── CLI command    (human in terminal)
├── MCP tool       (AI agent)
├── Slash command  (Claude Code user)
└── Go API         (other Go programs)
```

The CLI is the complete product. MCP tools mirror CLI capabilities. Slash
commands are 1:1 wrappers around MCP tools. The Go API is the superset —
it includes internal functions that don't need external surface area.

## Naming Conventions

### Verbs — Identity Operations

| Verb | CLI | MCP method | Go API | Notes |
|------|-----|-----------|--------|-------|
| get | `show <handle>` | `get` | `Store.Load(handle) (*Identity, error)` | CLI "show" (Unix), MCP "get" (REST), Go "Load". |
| list | `list` | `list` | `Store.List() (*ListResult, error)` | Same across surfaces. `ListResult` contains `Identities` slice + `Warnings`. |
| create | `create` | `create` | `Store.Save(id *Identity) error` | Save is an upsert — works for both new and existing identities. |
| iam | `iam <persona>` | `iam` | `Session.Store.Join(id, participant)` | Declares persona in current session. |

Identity has no `Delete` operation — identities are never programmatically
deleted (manual file removal only).

### Verbs — Attribute Operations (talent, personality, writing-style)

| Verb | CLI | MCP method | Go API | Notes |
|------|-----|-----------|--------|-------|
| show | `show <slug>` | `show` | `attribute.Store.Load(slug)` | Attributes use "show" on both CLI and MCP. |
| list | `list` | `list` | `attribute.Store.List()` | Same across surfaces. |
| create | `create <slug>` | `create` | `attribute.Store.Save(attr)` | CLI opens editor or reads `--file`. MCP accepts content string. |
| delete | `delete <slug>` | `delete` | `attribute.Store.Delete(slug)` | Same across surfaces. |
| set | `set <handle> <slug>` | `set` | `identity.Store.Update(handle, fn)` | Sets single-value attribute on identity. Personality, writing-style only. |
| add | `add <handle> <slug>` | `add` | `identity.Store.Update(handle, fn)` | Adds to list attribute. Talents only. |
| remove | `remove <handle> <slug>` | `remove` | `identity.Store.Update(handle, fn)` | Removes from list attribute. Talents only. |

**Note on show vs get**: Identity uses `get` as MCP method (REST
convention). Attribute resources use `show` as MCP method (matching CLI).
This is intentional — attribute `show` displays markdown content, which
is closer to "show me the file" than "get a record." Do not normalize
attribute `show` to `get`.

### Resource Names

| Resource | CLI command | MCP tool name | Go package | Slash command |
|----------|-----------|--------------|-----------|--------------|
| Identity | `identity` (canonical), `whoami`/`list`/`show` (shortcuts) | `identity` | `identity` | `/ethos:identity` |
| Talent | `talent` | `talent` | `attribute` (kind=Talents) | `/ethos:talent` |
| Personality | `personality` | `personality` | `attribute` (kind=Personalities) | `/ethos:personality` |
| Writing style | `writing-style` | `writing_style` | `attribute` (kind=WritingStyles) | `/ethos:writing-style` |
| Extension | `ext` | `ext` | `identity` (ext methods) | `/ethos:ext` |
| Session | `session` | `session` | `session` | `/ethos:session` |

**Convention clash**: CLI uses hyphens (`writing-style`), MCP tool names
use underscores (`writing_style`). This is unavoidable — CLI conventions
require hyphens, MCP/JSON conventions use underscores. The mapping is
mechanical and must be documented in the project's CLAUDE.md.

### Parameters

| Surface | Convention | Example |
|---------|-----------|---------|
| CLI flags | `--long-name`, `-s` short | `--agent-id`, `-f` |
| CLI positional | `<required>`, `[optional]` | `<handle>`, `[key]` |
| MCP parameters | `snake_case` | `agent_id`, `session_id` |
| Go function args | short descriptive names per Go convention | `handle string`, `slug string`, `fn func(*Identity) error` |

### Consolidated Tool Pattern

When a resource has multiple operations, use a single tool with a `method`
parameter rather than separate tools per operation:

```text
# Bad: 4 separate tools
whoami, list_identities, get_identity, create_identity

# Good: 1 tool with method dispatch
identity { method: whoami | list | get | create | iam }
```

This applies to MCP tools and slash commands. Method names are not
restricted to CRUD verbs — action verbs (`speak`, `send`, `search`,
`purge`, `iam`) are valid method names when they describe the operation
accurately.

CLI commands can have top-level shortcuts for the most common operations
(e.g., `ethos whoami` as a shortcut for `ethos identity whoami`), but
the consolidated form (`ethos identity whoami`) should also work.
Shortcuts should be hidden in `--help` to avoid duplication — the
canonical resource group is the primary form.

## Surface-Specific Idioms

### CLI (cobra)

- Resource group commands as primary (`ethos identity get`, `ethos talent list`)
- Top-level shortcuts for brand commands only (`ethos whoami`)
- Other shortcuts hidden (`Hidden: true`) for backward compatibility
- `--json` persistent flag on root command for machine-readable output
- `--json` output matches MCP response fields
- `--reference` flag where MCP has `reference` parameter
- Help text IS the manual — complete with examples
- Shell completion via `ethos completion <bash|zsh|fish>`
- Hidden commands for internal operations (`hook`, `resolve-agent`)
- Exit codes: 0 success, 1 error
- Errors to stderr, output to stdout

### MCP Tools

- One tool per resource (consolidated method dispatch)
- `method` parameter required, enum-validated
- Named parameters, `snake_case`
- Return **formatted text**, not raw JSON (DES-020). The PostToolUse
  hook formats all output for LLM consumption. Raw JSON wastes context
  tokens and increases model error rate.
- Error messages via `NewToolResultError` with context
- No interactive input — all parameters explicit
- Admin tools (`doctor`) can be standalone (not method-dispatched)
  and return structured check results formatted as text tables

### Slash Commands

- 1:1 wrapper around MCP tools
- Parse `$ARGUMENTS` to extract method and parameters
- Default method when no argument provided (e.g., `whoami` for identity)
- `allowed-tools` in frontmatter scopes to the exact MCP tool
- Dev variants (`-dev.md`) reference dev plugin MCP namespace

### Go Library API

- `internal/` for everything (no public API yet)
- `Store` structs with CRUD methods
- `IdentityStore` interface for polymorphism (LayeredStore)
- Key return types: `*Identity`, `*ListResult` (contains `Identities`
  slice + `Warnings`), `*Attribute`, `*Roster`
- Error wrapping with `fmt.Errorf("context: %w", err)`
- Options pattern for Load (`Reference(true)`)
- Table-driven tests with testify

## Output Standard

### Two-Channel Display (MCP)

Every MCP tool response goes through a PostToolUse hook that formats it
for Claude Code:

- **Panel** (`updatedMCPToolOutput`): count summary or compact display
- **Context** (`additionalContext`): formatted table or field list

Pattern:

- List operations: panel = `"N resources"`, context = columnar table
- Get/show operations: panel = formatted field list, context = same
- Create/delete/set: panel = confirmation message
- Tables use `FormatTable` with `▶` prefix and 3-space data indent

### CLI Output

- Default: human-readable text (tables, field lists)
- `--json`: JSON object/array, same fields as MCP response
- Errors: `ethos: <context>: <what failed>` to stderr
- Tables match the MCP two-channel format where possible

## Consistency Checklist

For every new capability:

- [ ] Go function exists in the appropriate package
- [ ] CLI command registered in cobra with Args validation and help text
- [ ] MCP tool method added (or new tool if new resource)
- [ ] Slash command `.md` file created (prod + dev variants)
- [ ] Two-channel display formatter added for list/show operations
- [ ] `make check` passes
- [ ] Verb matches across all surfaces (or mapping documented)
- [ ] Parameters follow surface-specific naming conventions
- [ ] `--json` output matches MCP response fields
- [ ] `--reference` flag added where MCP has `reference` parameter
- [ ] Tests cover the Go function, not just the CLI wrapper

## Anti-Patterns

- **Separate MCP tools per operation** — use consolidated method dispatch
- **CLI-only capabilities** — if it's useful on CLI, agents need it too
- **Raw JSON in MCP context** — always format for readability
- **Different verbs for same operation** — `show` on CLI, `get` on MCP
  is acceptable for identity (convention clash). `roster` vs `show` for
  the same session operation is not.
- **Scattered top-level commands** — group under the resource name.
  Keep one brand shortcut (`whoami`), hide the rest.
- **Missing `--json`** — every command that produces output needs it,
  and the fields must match MCP response
- **Forcing CRUD verbs on action operations** — `speak`, `send`, `purge`,
  `iam` are valid method names. Not everything is get/list/create/delete.
