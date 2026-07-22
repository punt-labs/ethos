# Ethos Agent Guide

How to use ethos from an AI agent session — CLI, MCP tools, hooks, and extending identities with custom attributes.

## Prerequisites: Agent Definitions

Ethos auto-generates `.claude/agents/<handle>.md` files at **SessionStart**
from team data resolved in this order: repo-local `.punt-labs/ethos/`,
active bundle, global `~/.punt-labs/ethos/`.

**New repo (preferred) — activate a bundle:**

```bash
ethos seed                     # deploys embedded gstack to global
ethos team activate gstack     # writes active_bundle: gstack
```

Or add your own bundle:

```bash
ethos team add-bundle git@github.com:myorg/team.git --apply
ethos team activate myorg
```

**Legacy — team submodule at `.punt-labs/ethos/`:**

```bash
git submodule add git@github.com:punt-labs/team.git .punt-labs/ethos
```

For an existing clone, run `git submodule init && git submodule update`.
To convert a legacy submodule to the bundles layout, use
`ethos team migrate` (dry-run by default; `--apply` to execute).

Then restart Claude Code to trigger the SessionStart hook. Verify with
`ls .claude/agents/` — you should see one `.md` file per agent identity
in the resolved team.

**Troubleshooting**: if `subagent_type` fails with "agent type not found":

1. Run `ls .claude/agents/<handle>.md` — if the file exists, **restart
   Claude Code**. Agent types are discovered at session start; files
   added after launch are not visible until restart.
2. If the file is missing, check the team source: `ethos team active`
   (bundle layout) or `git submodule status` (legacy layout — a
   leading `-` means uninitialized). Then restart Claude Code to
   trigger the SessionStart hook, which generates agent files from
   resolved team data.

## Concepts

Ethos provides two things:

1. **Identity registry** — YAML files at `~/.punt-labs/ethos/identities/<handle>.yaml`. One file per human or agent. Same schema for both.
2. **Session roster** — who is present in the current Claude Code session (human, primary agent, subagents), their personas, and parent-child relationships. Managed automatically by hooks.

Tools integrate with ethos at whatever coupling level fits:

- **Filesystem** — read YAML at known paths. Zero dependency on the ethos binary.
- **CLI** — call `ethos whoami`, `ethos show`, etc. from hooks and scripts. Requires the binary.
- **MCP server** — connect to `ethos serve` for structured identity operations during a session.

## Repo Enablement

`install.sh` is machine scope only (binary, plugin, seed). Turning ethos on
in a specific repo is `ethos enable`; `install.sh` delegates to it
automatically when run inside a work tree.

```bash
ethos enable            # deposit guide + marker + import line; chain the hooks
ethos enable --json     # per-step report
ethos disable           # remove import line + marker; unchain hooks (non-destructive)
ethos disable --force   # unchain even when a sibling worktree is still enabled
```

`enable` does four things and is idempotent (re-running is the upgrade path):

1. deposits the vendored agent guide `.punt-labs/ethos/CLAUDE.md` and its
   `.vendored-manifest` (the vendored zone — never touches identities, teams,
   sessions, or other repo-owned data);
2. writes the enabled marker `.punt-labs/ethos/enabled` — after the deposit,
   so a marker present always implies a complete guide;
3. adds the `@.punt-labs/ethos/CLAUDE.md` import line to the repo `CLAUDE.md`;
4. chains the `ETHOS DES-058 SEAL` and `ETHOS DES-054 TRAILER` sections into
   the `pre-commit` and `commit-msg` hooks.

`enable` and `setup` stay separate — neither calls the other. `enable` prints
a "run `ethos setup`" hint when the repo has no identity config.

`disable` removes the import line, deletes the marker, and unchains the hooks,
but leaves the vendored guide and all config and audit data dormant on disk.
It runs no final seal; unsealed audit lines stay in the gitignored local zone
and seal on a later re-enable.

**The enabled marker is the signal, not directory presence.** The chained
hook scripts gate on it — `[ -f "$REPO_ROOT/.punt-labs/ethos/enabled" ]` —
so a disabled repo's hook does no commit-time work while still preserving a
host hook's failing fall-through. `ethos doctor` keys its seal-hook check on
the marker: a never-enabled or disabled repo passes ("not enabled here"); a
repo with the hook chained but no marker WARNs (gated-but-unenabled); an
enabled repo FAILs if the seal is missing or inactive.

## Identity Operations

### CLI

```bash
# Shortcuts — canonical forms in parentheses.
ethos whoami                          # Show active identity   (ethos identity whoami)
ethos iam mal                         # Declare active persona (ethos session iam)
ethos create                          # Interactive identity creation
ethos create -f persona.yaml          # Create from YAML file
ethos list                            # List all identities   (ethos identity list)
ethos show mal                        # Full identity          (ethos identity show)
ethos show mal --json                 # JSON output
```

### MCP Tools

When running as a Claude Code plugin, ethos registers an MCP server (named `ethos`, plugin key `self` -- tool names follow the pattern `mcp__plugin_ethos_self__<tool>`) with 11 tools using method-dispatch.

**All tools use a consolidated `method` parameter:**

| Tool | Methods | Key Parameters |
|------|---------|----------------|
| `identity` | whoami, list, get, create | `handle`, `reference` |
| `session` | roster, join, leave, iam | `session_id`, `agent_id`, `persona` |
| `talent` | create, list, show, delete, add, remove | `slug`, `content`, `handle` |
| `personality` | create, list, show, delete, set | `slug`, `content`, `handle` |
| `writing_style` | create, list, show, delete, set | `slug`, `content`, `handle` |
| `ext` | get, set, del, list | `handle`, `namespace`, `key`, `value` |
| `team` | list, show, create, delete, add_member, remove_member, add_collab, for_repo | `name`, `identity`, `role`, `from`, `to`, `collab_type`, `repo` |
| `role` | list, show, create, delete | `name`, `responsibilities`, `permissions` |
| `mission` | create, show, list, close, reflect, reflections, advance, result, results, log | `mission_id`, `contract`, `reflection`, `result`, `status` |
| `adr` | create, list, show | `id`, `title`, `status`, `body` |
| `doctor` | *(none — standalone)* | *(none)* |

**Example — read identity from MCP:**

```text
Call mcp__plugin_ethos_self__identity with method="get", handle="mal"
```

Returns JSON with all core fields, resolved attribute content (`writing_style_content`, `personality_content`, `talent_contents`), and the `ext` map. Pass `reference: true` for slugs only.

## Session Roster

The session roster tracks all participants in a Claude Code session.

### How It Works

Sessions are managed automatically by hooks — no manual setup required.

1. **SessionStart** hook creates the roster with two participants: the human user (root) and the primary Claude agent.
2. **SubagentStart** hook adds each subagent to the roster.
3. **SubagentStop** hook removes the subagent.
4. **SessionEnd** hook tears down the roster.

### CLI

```bash
ethos iam archie                      # Declare "I am archie" in this session
ethos session                         # Show current session roster
ethos session purge                   # Clean up stale rosters
```

### MCP Tools

| Tool | Method | Parameters | Description |
|------|--------|-----------|-------------|
| `session` | iam | `persona` | Declare persona in current session |
| `session` | roster | `session_id` (optional) | Return full roster with tree |
| `session` | join | `agent_id`, optional `persona`, `parent`, `agent_type` | Add participant |
| `session` | leave | `agent_id` | Remove participant |

### Slash Commands

| Command | Description |
|---------|-------------|
| `/ethos:identity` | Manage identities -- whoami, list, get, create |
| `/ethos:session` | Manage session roster -- roster, join, leave, iam |
| `/ethos:ext` | Manage tool-scoped extensions -- get, set, del, list |
| `/ethos:talent` | Manage talents -- create, list, show, delete, add, remove |
| `/ethos:personality` | Manage personalities -- create, list, show, delete, set |
| `/ethos:writing-style` | Manage writing styles -- create, list, show, delete, set |
| `/ethos:team` | Manage teams -- list, show, create, delete, members, collaborations |
| `/ethos:role` | Manage roles -- list, show, create, delete |

### Roster Structure

```yaml
session: ba3bb20f
started: 2026-03-18T14:30:00Z
repo: punt-labs/ethos               # org/repo from git remote
host: dev-machine                   # short hostname
participants:
  - agent_id: mal                   # $USER — the human
    persona: mal
    parent: ~                       # root of the tree
    joined: 2026-03-18T14:30:00Z
    ext:
      biff: { tty: s001 }

  - agent_id: "19147"              # Claude PID
    persona: archie
    parent: mal
    joined: 2026-03-18T14:30:01Z
    ext:
      biff: { tty: s004 }

  - agent_id: a5734dd              # subagent ID
    persona: code-reviewer
    parent: "19147"
    agent_type: code-reviewer
    joined: 2026-03-18T14:31:15Z
    ext: {}
```

The tree structure encodes authority: root → primary agent → subagents. Any participant can walk the tree to find its initiator, delegates, or siblings.

### Persona Auto-Matching

When a subagent starts, the hook does a case-sensitive `ethos show "$AGENT_TYPE"` to check if an ethos identity exists with that exact handle. Identity handles are restricted to lowercase alphanumeric plus hyphens, so auto-matching only works for lowercase `agent_type` values.

```bash
# Create personas for common agent types
ethos create -f code-reviewer.yaml    # auto-matches agent_type "code-reviewer"
ethos create -f explore.yaml          # auto-matches agent_type "explore"
```

A subagent can override the default via `ethos iam <different-persona>`.

## Extending Identities

Ethos never adds consumer-specific fields to the identity schema. Instead, it provides a generic extension mechanism — namespaced key-value storage that any tool can use.

### The Problem

Vox needs a `default_mood` on each persona. Beadle needs a `gpg_key_id`. Biff needs a `preferred_tty`. If ethos added these as core fields, every new consumer would require an ethos schema change, a new release, and cross-repo coordination.

### How Extensions Work

Extensions are stored as separate YAML files alongside the identity:

```text
~/.punt-labs/ethos/identities/
  mal.yaml                     # ethos owns — core identity fields
  mal.ext/                     # extension directory
    beadle.yaml                # beadle owns — GPG key, IMAP config
    biff.yaml                  # biff owns — preferred TTY
    vox.yaml                   # vox owns — default mood
```

Each file is a flat YAML map. Ethos never reads or interprets the contents — it only assembles the merged view when you ask for an identity.

### CLI

```bash
# Write an extension key
ethos ext set mal vox default_mood calm

# Read one key
ethos ext get mal vox default_mood
# → calm

# Read all keys in a namespace
ethos ext get mal vox
# → default_mood: calm

# List all namespaces
ethos ext list mal
# → beadle biff vox

# Delete a key
ethos ext delete mal vox default_mood

# Delete an entire namespace
ethos ext delete mal vox
```

### MCP Tools

| Tool | Method | Parameters | Description |
|------|--------|-----------|-------------|
| `ext` | get | `handle`, `namespace`, optional `key` | Read one key or all keys |
| `ext` | set | `handle`, `namespace`, `key`, `value` | Write a key-value pair |
| `ext` | del | `handle`, `namespace`, optional `key` | Delete key or namespace |
| `ext` | list | `handle` | List all namespaces |

### Merged View

When you read an identity (via `ethos show`, `identity` method `get`, or `Load()`), extensions appear under the `ext` map:

```yaml
name: Mal Reynolds
handle: mal
kind: human
email: mal@serenity.ship
ext:
  beadle:
    gpg_key_id: 3AA5C34371567BD2
  biff:
    preferred_tty: tty1
  vox:
    default_mood: calm
```

### Direct File Access (Sidecar Contract)

Tools don't need to go through ethos to read their extensions. The file path is the contract:

```text
~/.punt-labs/ethos/identities/<handle>.ext/<namespace>.yaml
```

Any tool can read its own namespace file directly — stable paths, no import dependency. This is the filesystem integration pattern; tools that prefer structured access use the CLI or MCP server instead.

### Validation Constraints

| Field | Pattern | Limit |
|-------|---------|-------|
| Namespace | `^[a-z][a-z0-9-]*$` | 32 chars |
| Key | `^[a-z][a-z0-9_]*$` | 64 chars |
| Value | Any YAML scalar | 4096 bytes |
| Keys per namespace | — | 64 |
| Namespaces per persona | — | 32 |

### Two Scopes

Extensions exist at two independent scopes:

1. **Persona-level** (durable) — files in `<handle>.ext/`. Persist across sessions.
2. **Session-participant-level** (ephemeral) — the `ext` map on each participant in the session roster. Deleted when the session ends.

Use persona-level for defaults (Vox's preferred voice, Beadle's GPG key). Use session-level for runtime state (Biff's current TTY, Vox's active voice).

### Extension Session Context

Any extension can provide a `session_context` field containing markdown
instructions that ethos injects into agent context at session start, before
compaction, and when sub-agents spawn. This is how tools integrate behavioral context without
requiring ethos-side code changes.

```yaml
# claude.ext/quarry.yaml
memory_collection: memory-claude
session_context: |
  ## Memory

  You have persistent memory stored in quarry. Your memories survive
  across sessions and machines.

  To recall prior knowledge: /find <query>
  To persist something you learned: /remember <content>
```

Ethos iterates over all extensions, collects `session_context` values,
and emits them in sorted order after the persona block. No parsing, no
interpretation — the content is yours to define.

To set session context for your tool:

```bash
ethos ext set <handle> <your-tool> session_context "$(cat context.md)"
```

Or via MCP:

```text
Call mcp__plugin_ethos_self__ext with method="set", handle="claude",
  namespace="your-tool", key="session_context", value="..."
```

## Teams and Roles

Teams bind identities to roles for a set of repositories. Roles define
responsibilities and tool permissions. Both are first-class ethos concepts
with CLI, MCP, and layered resolution (repo-local overrides global).

### Querying Teams

```bash
ethos team list                        # List all teams
ethos team show engineering            # Show members, roles, collaborations
ethos team for-repo punt-labs/ethos    # Which team works on this repo?
```

Via MCP:

```text
Call mcp__plugin_ethos_self__team with method="show", name="engineering"
Call mcp__plugin_ethos_self__team with method="for_repo", repo="punt-labs/ethos"
```

### Querying Roles

```bash
ethos role list                        # List all roles
ethos role show go-specialist          # Show role responsibilities and tools
```

Via MCP:

```text
Call mcp__plugin_ethos_self__role with method="show", name="go-specialist"
```

### Team Schema

```yaml
name: engineering
repositories:
  - punt-labs/ethos
  - punt-labs/biff
members:
  - identity: claude
    role: coo
  - identity: bwk
    role: go-specialist
collaborations:
  - from: go-specialist
    to: coo
    type: reports_to
```

### Role Schema

```yaml
name: go-specialist
model: sonnet
responsibilities:
  - Go implementation following Kernighan's principles
  - Tests with race detection and full coverage
tools:
  - Read
  - Write
  - Edit
  - Bash
  - Grep
  - Glob
```

The `tools` field is the source of truth for sub-agent tool restrictions.
DES-026 uses it to generate agent definition frontmatter.

Note: the MCP `role create` method accepts `responsibilities` and `permissions` but does not expose `tools`. To set `tools`, edit the role YAML file directly.

### How Agents Use Team Context

You don't need to query teams manually. The SessionStart and PreCompact
hooks automatically inject team context into your session — member names,
roles, responsibilities, and the collaboration graph. This context
survives compaction because PreCompact re-injects it.

Use the team and role MCP tools when you need to look up specific
details: who has a particular role, which repos a team covers, or what
tools a role permits.

## Missions

A mission is a typed delegation contract between a leader, a worker,
and an evaluator. It binds a write-set (which files the worker may
touch), success criteria, a round budget, and an optional ticket
reference into a single artifact that ethos enforces at runtime.

### Creating Missions

Two paths: YAML file for full control, or `dispatch` for quick
one-liners.

**From a YAML file:**

```bash
ethos mission create --file contract.yaml
```

```yaml
# contract.yaml
leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  ticket: ethos-7al
write_set:                  # modify existing files (and create within)
  - internal/session/store.go
  - internal/session/store_test.go
extract_into:               # create new files only — never modify (DES-052)
  - internal/session/
success_criteria:
  - purge removes stale entries
  - test covers TTL edge case
budget:
  rounds: 2
  reflection_after_each: true
```

**From CLI flags (dispatch):**

```bash
ethos mission dispatch \
  --worker bwk \
  --evaluator djb \
  --write-set "internal/session/store.go,internal/session/store_test.go" \
  --extract-into "internal/session/" \
  --criteria "purge removes stale entries,test covers TTL edge case" \
  --context "Follow-up from PR #280" \
  --ticket ethos-7al \
  --type implement \
  --budget 2
```

Required flags: `--worker`, `--evaluator`, `--write-set`, `--criteria`.
Optional: `--extract-into`, `--context`, `--ticket`, `--type`
(default: implement), `--budget` (default: 2). Leader is resolved from
the repo's configured `agent:` value; if unset or outside a repo,
falls back to `"claude"`.

**Asymmetric semantics (DES-052).** `write_set` authorizes both
modification of existing files and creation of new files at or under
its listed paths; `extract_into` authorizes only the creation of new
files under listed directories. A new file at path P is permitted
when an `extract_into` directory is a prefix of P **or** P is
covered by `write_set`; modifying an existing file always requires a
`write_set` match — `extract_into` never grants modify rights, even
for files it created. Entries in `extract_into` are directory-shaped:
a file-shaped entry (anything with a code-file extension) is rejected
by rule 17 at validate time. See DES-052 in DESIGN.md for the full
design, including the closed six-rule cross-mission admission table.

Via MCP:

```text
Call mcp__plugin_ethos_self__mission with method="create", contract={...}
```

### Showing and Listing

```bash
ethos mission show <id>               # Contract, results, reflections
ethos mission show <id> --json        # JSON payload
ethos mission list                    # All missions
ethos mission list --status open      # Filter by status
ethos mission list --pipeline <id>    # Pipeline missions in stage order
```

Via MCP:

```text
Call mcp__plugin_ethos_self__mission with method="show", mission_id="m-2026-04-14-001"
Call mcp__plugin_ethos_self__mission with method="list", status="open"
```

### Result Artifacts

Before closing a mission, the worker submits a structured result:

```bash
ethos mission result <id> --file result.yaml
ethos mission results <id>            # List all results for a mission
```

Via MCP:

```text
Call mcp__plugin_ethos_self__mission with method="result", mission_id="...", result={...}
Call mcp__plugin_ethos_self__mission with method="results", mission_id="..."
```

### Reflections and Round Advancement

After each round, the leader records a reflection and optionally
advances to the next round:

```bash
ethos mission reflect <id> --file reflection.yaml
ethos mission advance <id>
ethos mission reflections <id>
```

Via MCP:

```text
Call mcp__plugin_ethos_self__mission with method="reflect", mission_id="...", reflection={...}
Call mcp__plugin_ethos_self__mission with method="advance", mission_id="..."
Call mcp__plugin_ethos_self__mission with method="reflections", mission_id="..."
```

### Closing

Closing requires a valid result artifact for the current round:

```bash
ethos mission close <id>
ethos mission close <id> --status failed
ethos mission close <id> --status escalated
```

Closing auto-appends a summary line to `.punt-labs/ethos/missions.jsonl` for
commit-ready traceability. The JSONL file is append-only and intended
to be committed.

Via MCP:

```text
Call mcp__plugin_ethos_self__mission with method="close", mission_id="...", status="closed"
```

### Audit Trail

Every mission operation appends to a per-mission JSONL event log:

```bash
ethos mission log <id>                # Full event history
ethos mission log <id> --event create,close   # Filter by event type
ethos mission log <id> --since 2026-04-14T00:00:00Z
ethos mission log <id> --json
```

Via MCP:

```text
Call mcp__plugin_ethos_self__mission with method="log", mission_id="..."
```

### Linting

Advisory pre-delegation linter with heuristic checks:

```bash
ethos mission lint contract.yaml
ethos mission lint contract.yaml --json
```

### Traceability UI

```bash
ethos ui              # start localhost server, open browser
ethos ui --port 9876  # explicit port
```

Three views: dashboard (mission list with counts), mission detail
(contract + delegations + results + event log), delegation detail
(record + dispatch prompt + full audit trail table). Pure Go —
`html/template` with `go:embed`, Tailwind via CDN. No npm, no build
step. Ctrl-C stops the server.

### Migration from v3.11

Two one-time relocation commands move legacy global artifacts into
the DES-054 per-repo tree. Both are idempotent, both honour
`--dry-run`, and both refuse to touch artifacts that belong to a
different repo's work tree.

```bash
ethos audit migrate                    # legacy global session audit logs
ethos audit migrate --dry-run --verbose
ethos mission migrate                  # all migratable missions
ethos mission migrate <mission-id>     # a single mission
ethos mission migrate --to-repo --dry-run
```

Cross-repo policy: a session whose id has no matching repo-tree
session directory, or a mission whose `contract_id` is not referenced
by any audit entry in `<repo>/.punt-labs/ethos/sessions/`, stays where it is —
it belongs to another checkout.

## Audited Delegation

Every `Agent` tool call is allocated a `delegation_id` and the spawn's
subsequent audit entries are tagged with it. Two tiers differ in what
they write:

| Tier | When | Persistence | Enforcement |
|------|------|-------------|-------------|
| A | `Agent(...)` with no `MISSION_ID` | `delegation_id` only; tool calls land in the per-session `audit.jsonl` tagged with it | Advisory stderr line; spawn always allowed |
| B | `MISSION_ID` set, OR inherited from a parent contract | Same audit tagging plus a per-delegation `record.yaml` (and optional `prompt.md`) under the mission tree | Preconditions, depth limit, write-set, hash gate |

The forensic answer to "what did this spawn do" comes from
`ethos audit show --delegation <id>` for either tier; the Tier B
`record.yaml` adds the contract, verdict, and prompt-hash binding.

The `PreToolUse` hook routes on `Agent` calls:

1. `MISSION_ID` env set → Tier B by explicit dispatch.
2. `MISSION_ID` unset, `PARENT_DELEGATION_ID` set → try Tier B by
   inheritance; walk the parent contract's `delegations[]` for a
   matching `spawn_pattern`. Falls through to Tier A on any miss or
   error (inheritance is non-blocking).
3. Neither set → Tier A.

On every path the hook emits an `additional_env` block consumed by
the spawned worker: `DELEGATION_ID`, `PARENT_DELEGATION_ID`,
`PARENT_SESSION_ID`. Tier B also emits `MISSION_ID` and
`MISSION_ARTIFACTS_DIR` (the per-delegation directory the worker
writes results into).

Refusal reasons (Tier B only — Tier A is always allow):

- `resolving mission store: <err>` — global root unreachable
- `resolving MISSION_ID "<id>": <err>` — malformed or missing contract
- `allocating delegation id: <err>` — counter file unwriteable
- `acquiring mission lock for "<id>": <err>` — per-mission flock failure
- `resolving global root for delegation lock: <err>` — UserHomeDir failed
- `acquiring delegation lock for "<id>": <err>` — per-delegation flock failure
- `writing delegation skeleton for "<id>": <err>` — atomic record write failed
- `resolving max_delegation_depth: <err>` — repo config error
- `walking parent_delegation chain for "<id>": <err>` — corrupt or missing ancestor
- `max_delegation_depth <limit> exceeded by depth <proposed> for "<id>"` — chain too deep

Every refusal closes the skeleton (if already written) with
`verdict=aborted` so depth refusals are queryable via
`ethos audit show --delegation <id>`.

### Contract Fields

DES-054 phase 3 adds three optional fields to the mission Contract:

```yaml
preconditions:                          # admission gates
  - form: implicit                      # target file_path must have been Read
    message: "Read the target file before editing it"
  - form: explicit
    require_read:
      - DESIGN.md
      - ${inputs.references.0}          # ${inputs.X} substitution
    message: "Read DESIGN.md and the first reference before writing"

strict_preconditions: true              # default; *bool — explicit false opts into warn-mode

delegations:                            # per-spawn templates
  - role: implementation
    spawn_pattern: "bwk|rsc"            # anchored regex against agent_type
    inherits_contract: true             # child runs Tier B under this contract
    extract_into:                       # new-file-creation allowlist for the child
      - internal/session/
  - role: review
    spawn_pattern: "djb"
    inherits_contract: true
```

**Preconditions** are evaluated by the `PreToolUse` hook before
every non-Read tool call under a Tier B contract. *Implicit* form
requires the tool input's target paths to have been Read in this
session. *Explicit* form requires every `require_read` entry — after
`${inputs.X}` substitution — to have been Read. Supported keys:
`${inputs.ticket}`, `${inputs.files.N}`, `${inputs.references.N}`.

A *violated* predicate always blocks with the contract's `message`.
An *unevaluable* predicate (malformed substitution, missing input,
unreadable audit log) blocks under `strict_preconditions: true`
(default) and warns + allows under `strict_preconditions: false`.

**Delegations** authorize child `Agent` spawns to inherit this
contract. `spawn_pattern` is admission-time-validated as an anchored
regex (`^(?:<pattern>)$`); a malformed pattern is rejected at
`mission create`, not at runtime match time.

### Commands

```bash
ethos audit show --delegation <id>             # forensic query across sessions
ethos audit show --delegation <id> --format text
ethos audit migrate                            # legacy → repo-tree audit logs
ethos audit migrate --dry-run --verbose
ethos mission migrate --to-repo                # legacy → repo-tree missions
ethos mission migrate <mission-id> --to-repo
```

`ethos audit show` walks `<repo>/.punt-labs/ethos/sessions/<date>-<id>/audit.jsonl`
(with legacy global fallback) and prints every entry whose
`delegation_id` matches. JSON output is one entry per line and is
itself a valid audit-log fragment — pipes cleanly into `jq`.

### Commit-Msg Trailer Hook

`hooks/commit-msg.sh` appends `Mission: <id>` and `Delegation: <id>`
git trailers when `MISSION_ID` and `DELEGATION_ID` are set in the
environment. Idempotent. `install.sh` installs it as the repo's
`.git/hooks/commit-msg` automatically (no clobber if an unrelated
hook is already present). Queryable with
`git log --grep="Mission: m-2026-05-23-001"`.

### max_delegation_depth

Default ceiling 16. Walks `parent_delegation` on every Tier B
dispatch and refuses when adding the new spawn would exceed the
limit. Override per repo:

```yaml
# .punt-labs/ethos.yaml
max_delegation_depth: 32
```

Value `0` means default; negative surfaces as a refusal (no silent
clamp).

Full feature reference, including audit-log shape, the lock model,
forensic query recipes, and the migration runbook:
[docs/audited-delegation.md](docs/audited-delegation.md).

## Pipelines

Pipeline templates are multi-stage workflows composed from archetypes.
Each stage becomes a mission with dependency ordering.

### Listing and Showing

```bash
ethos mission pipeline list                   # All 13 templates
ethos mission pipeline list --json
ethos mission pipeline show gstack-debug      # Stages, archetypes, defaults
ethos mission pipeline show gstack-debug --json
```

### Instantiating

```bash
ethos mission pipeline instantiate gstack-debug \
  --var target=internal/session/ \
  --leader gstack-architect \
  --worker gstack-implementer \
  --evaluator gstack-reviewer
```

Creates one mission per stage with `depends_on` references linking
them in order. Use `--dry-run` to preview the contracts without
creating them.

Full flag list: `--var key=value` (repeatable), `--leader`, `--worker`,
`--evaluator`, `--id` (override auto-generated pipeline ID),
`--dry-run`.

### Available Templates

| Template | Stages | Purpose |
|----------|--------|---------|
| `quick` | 2 | Minimal implement + review |
| `standard` | 5 | Implement, test, review, document, coverage |
| `full` | 9 | PR/FAQ through coverage |
| `product` | 3 | PR/FAQ before engineering |
| `formal` | 4 | Z-Spec before implementation |
| `docs` | 3 | Documentation-only |
| `coe` | 3 | Cause of error investigation |
| `coverage` | 3 | Targeted test improvement |
| `gstack-plan` | 4 | Idea validation to architecture lock |
| `gstack-ship` | 5 | Code review to released and documented |
| `gstack-design` | 4 | Design system to production HTML/CSS |
| `gstack-debug` | 3 | Root cause investigation to verified fix |
| `gstack-review` | 4 | Multi-perspective review pipeline |

## Archetypes

Archetypes are typed subtypes for missions. Each defines default
constraints: whether an empty write-set is allowed, write-set glob
patterns, and required fields.

| Archetype | Empty write-set | Typical use |
|-----------|-----------------|-------------|
| `implement` | No | Code changes, feature work |
| `design` | No | Architecture, system design |
| `review` | No | Code review, findings reports |
| `test` | No | Test writing, coverage improvement |
| `report` | Yes | Status reports, audit summaries |
| `task` | No | Generic bounded tasks |
| `inbox` | Yes | Triage, email-triggered work |
| `investigate` | No | Root cause analysis, debugging |
| `audit` | No | Security audits, compliance checks |
| `orchestrate` | No | Multi-agent coordination |

Set the archetype via `--type` on dispatch or the `type` field in
contract YAML. Defaults to `implement`.

Each archetype also carries `extract_into_constraints` — a glob
allowlist that bounds the directories a leader may name in
`extract_into` (DES-052). For example, `design` constrains
extraction to `docs/**`; `investigate` permits `docs/**` and
`.tmp/**`. The default table lives in DESIGN.md under DES-052.

## Gstack Starter Team

Ethos ships with a gstack starter team: 6 agents (architect,
implementer, reviewer, qa, security, product) with personalities
ported from the gstack builder framework's principles -- Boil the
Lake, Search Before Building, User Sovereignty. 5 pipeline templates
(gstack-plan, gstack-ship, gstack-design, gstack-debug,
gstack-review) and 3 archetypes (investigate, audit, orchestrate)
support the team's workflows.

Configure your repo to use the gstack team:

```yaml
# .punt-labs/ethos.yaml
agent: gstack-architect
team: gstack
```

See [Gstack starter team](docs/gstack-getting-started.md) for the
full setup guide.

## Repo Configuration

Ethos reads per-repo configuration from `.punt-labs/ethos.yaml`:

```yaml
agent: claude             # handle of the primary agent identity
team: engineering         # team name for session context
active_bundle: gstack     # optional: bundle for layer 2 resolution
```

| Field | Default | Description |
|-------|---------|-------------|
| `agent` | *(none)* | Handle of the primary agent identity. When set, ethos injects this persona at session start. When omitted, no agent persona is injected. |
| `team` | *(none)* | Team name. When set, ethos injects members, roles, and collaboration graph into session context. |
| `active_bundle` | *(none)* | Bundle name for layer 2 resolution. Omit if not using bundles. |

### Three-layer resolution

Ethos resolves identities, roles, and teams through three layers.
First match wins:

1. **Repo-local** — `.punt-labs/ethos/` in the repo (files checked in
   directly, or a shared team repo mounted as a git submodule)
2. **Active bundle** — `.punt-labs/ethos-bundles/<name>/` (repo-local)
   or `~/.punt-labs/ethos/bundles/<name>/` (global), controlled by the
   `active_bundle` field in `.punt-labs/ethos.yaml`
3. **Global** — `~/.punt-labs/ethos/`

Each layer holds the same subdirectories (`identities/`,
`personalities/`, `writing-styles/`, `talents/`, `roles/`, `teams/`).
When no `active_bundle` is set, layer 2 is skipped.

Attribute content (personalities, writing styles, talents) resolves
starting from the layer where the identity was found, then falls
through to lower-precedence layers. A bundle-sourced identity looks
up its personality in the bundle first, then global — not repo-local.
To override attributes for a bundle identity, override the identity
itself in repo-local.

Three ways to provide team data:

- **Shared team submodule** (layer 1) — `git submodule add` your org's
  team repo at `.punt-labs/ethos/`. All repos share one source of truth.
- **Repo-local files** (layer 1) — check project-specific identities
  directly into `.punt-labs/ethos/`. For teams unique to one project.
- **Bundle** (layer 2) — set `active_bundle` in `.punt-labs/ethos.yaml`.
  Ethos ships starter bundles (foundation, gstack) for new orgs. Custom
  bundles added via `ethos team add-bundle <git-url>`.

These compose: a repo can override bundle identities with repo-local
files, or use a shared submodule with global fallbacks.

Full setup guide: [docs/team-setup.md](docs/team-setup.md).

## Identity Resolution

Human and agent identities are resolved automatically — no manual
"set active" step required.

**Human resolution** (stops at first match):

| Step | Source | Match field |
|------|--------|-------------|
| 1 | `iam` declaration | Explicit persona set via `ethos session iam` |
| 2 | `git config user.name` | Identity `github` field |
| 3 | `git config user.email` | Identity `email` field |
| 4 | `$USER` | Identity `handle` field |

> Step 2 resolves when `git config user.name` is set to the GitHub
> username (a common convention for developers). If `git config
> user.name` contains a display name like "Jane Freeman" rather than
> a GitHub handle, this step will not match.

**Agent resolution** — per-repo `.punt-labs/ethos.yaml`:

```yaml
agent: claude
```

When `agent:` is unset, the primary agent has no persona.

## Hooks

Ethos registers 6 hooks in `hooks/hooks.json`:

| Hook | Script | Purpose |
|------|--------|---------|
| `SessionStart` | `session-start.sh` | Create roster, inject identity context and persona (personality + writing style + talents) |
| `PreCompact` | `pre-compact.sh` | Re-inject full persona block + team context before context compression -- prevents behavioral drift |
| `SubagentStart` | `subagent-start.sh` | Add subagent to roster, auto-match and inject persona |
| `SubagentStop` | `subagent-stop.sh` | Remove subagent from roster |
| `SessionEnd` | `session-end.sh` | Delete roster and PID-keyed session file |
| `PostToolUse` | `suppress-output.sh` | Suppress raw MCP tool output (matched to `mcp__plugin_ethos(-dev)?_self__.*`) |

### Session Discovery

Hooks receive `session_id` on stdin. Non-hook callers (Biff, Vox) discover the session ID through a PID-keyed file:

```text
~/.punt-labs/ethos/sessions/current/<claude-pid>
```

The `SessionStart` hook writes this file. Any descendant process walks the process tree to the topmost `claude` ancestor PID, reads this file, and gets the session ID.

## Persona Animation

Claude Code agent definitions (`.claude/agents/<handle>.md`) define *what* the agent does — tools, scope, principles. Ethos identities define *who* the agent is — personality, writing style, temperament. They complement each other. Persona animation connects the two: ethos hooks inject behavioral content from the identity into the agent's context at lifecycle events.

### How It Works

Three hooks inject persona content automatically:

1. **SessionStart** — loads the primary agent's identity and injects full personality content, writing style content, and talent slugs into the session context. This replaces the old one-line identity confirmation with a structured persona block.

2. **PreCompact** — re-injects the full persona block + team context before context compression. A condensed version was tried and rejected -- behavioral drift returned within 2-3 compaction cycles.

3. **SubagentStart** — when a subagent spawns, ethos auto-matches its `agent_type` to an identity handle. If a match is found, the subagent's personality and writing style content are injected into its context at spawn time.

Talent slugs are listed but not expanded inline — full talent content is available on demand via `/ethos:talent show <slug>`.

### Creating an Agent with Persona

1. **Create the ethos identity** with personality and writing style:

   ```yaml
   # .punt-labs/ethos/identities/bwk.yaml
   name: Brian K
   handle: bwk
   kind: agent
   personality: kernighan
   writing_style: kernighan-prose
   talents:
     - engineering
   ```

2. **Create the Claude Code agent definition** at `.claude/agents/bwk.md`. This file defines tools, scope, and working principles — the operational spec.

3. **Match the names.** The agent definition's `name:` in frontmatter must match the ethos identity's `handle:`. This is how SubagentStart finds the persona.

4. **On spawn**, ethos hooks inject the personality and writing style content into the agent's context automatically. The agent .md file does not need to call `ethos show` — the identity is already present.

### What Goes Where

| File | Defines | Example content |
|------|---------|----------------|
| `.claude/agents/bwk.md` | What the agent does | Tools, principles, working style, scope |
| `.punt-labs/ethos/identities/bwk.yaml` | Who the agent is | Personality slug, writing style slug, talents |
| `personalities/kernighan.md` | How the agent thinks | Temperament, design principles, debugging approach |
| `writing-styles/kernighan-prose.md` | How the agent writes | Sentence structure, naming conventions, comment style |

See [docs/persona-animation.md](docs/persona-animation.md) for the full design.

## Identity Schema Reference

```yaml
name: Mal Reynolds                    # required
handle: mal                           # required, unique, used as filename
kind: human                           # required: "human" or "agent"
email: mal@serenity.ship              # beadle channel binding
github: mal                           # biff channel binding
agent: .claude/agents/mal.md          # claude code agent binding
writing_style: concise-quantified     # slug → writing-styles/concise-quantified.md
personality: principal-engineer       # slug → personalities/principal-engineer.md
talents:                               # slugs → talents/<slug>.md
  - formal-methods
  - product-strategy
```

The `agent` field is a channel binding — like email or GitHub. Ethos defines *who*. The agent `.md` file defines *what tools and workflow*. Voice configuration lives in the `ext/vox` namespace, not as a core field.

## Storage Layout

| Scope | Path | Git-tracked? |
|-------|------|-------------|
| Repo config | `.punt-labs/ethos.yaml` | Yes |
| Repo-local (layer 1) | `.punt-labs/ethos/<subdir>/<file>` | Yes |
| Bundle — repo (layer 2) | `.punt-labs/ethos-bundles/<name>/<subdir>/<file>` | Yes |
| Bundle — global (layer 2) | `~/.punt-labs/ethos/bundles/<name>/<subdir>/<file>` | No |
| Global (layer 3) | `~/.punt-labs/ethos/<subdir>/<file>` | No |
| Repo agents | `.claude/agents/<handle>.md` | Yes |
| Global identities | `~/.punt-labs/ethos/identities/<handle>.yaml` | No |
| Extensions | `~/.punt-labs/ethos/identities/<handle>.ext/<ns>.yaml` | No |
| Global talents | `~/.punt-labs/ethos/talents/<slug>.md` | No |
| Global personalities | `~/.punt-labs/ethos/personalities/<slug>.md` | No |
| Global writing styles | `~/.punt-labs/ethos/writing-styles/<slug>.md` | No |
| Global roles | `~/.punt-labs/ethos/roles/<name>.yaml` | No |
| Global teams | `~/.punt-labs/ethos/teams/<name>.yaml` | No |
| Sessions | `~/.punt-labs/ethos/sessions/<id>.yaml` | No |
| Session locks | `~/.punt-labs/ethos/sessions/<id>.lock` | No |
| Current session | `~/.punt-labs/ethos/sessions/current/<pid>` | No |

### DES-054 — date-keyed two-tree mission storage

Mission and session artifacts move into a per-mission and per-session
directory layout. The mission ID encodes the date, so date browsing
is `ls .punt-labs/ethos/missions/m-2026-05-22-*/`. Sessions get the date as a
directory prefix so `ls .punt-labs/ethos/sessions/2026-05-22-*/` lists every
session started that day.

| Scope | Path | Git-tracked? |
|-------|------|-------------|
| Mission contract (repo) | `<repo>/.punt-labs/ethos/missions/<mission-id>/contract.yaml` | Yes |
| Mission event log (repo) | `<repo>/.punt-labs/ethos/missions/<mission-id>/log.jsonl` | Yes |
| Mission results (repo) | `<repo>/.punt-labs/ethos/missions/<mission-id>/results.yaml` | Yes |
| Mission reflections (repo) | `<repo>/.punt-labs/ethos/missions/<mission-id>/reflections.yaml` | Yes |
| Tier B delegation record (repo) | `<repo>/.punt-labs/ethos/missions/<mission-id>/delegations/<delegation-id>/record.yaml` | Yes |
| Tier B delegation prompt (repo) | `<repo>/.punt-labs/ethos/missions/<mission-id>/delegations/<delegation-id>/prompt.md` | Yes |
| Per-mission shared flock | `<repo>/.punt-labs/ethos/missions/<mission-id>/.lock` | No |
| Mission contract (legacy global, read-only fallback) | `~/.punt-labs/ethos/missions/<mission-id>.yaml` | No |
| Session audit log (repo) | `<repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<session-id>/audit.jsonl` | Yes |
| Session audit log (legacy global, read-only fallback) | `~/.punt-labs/ethos/sessions/<session-id>.audit.jsonl` | No |
| Mission ID counter | `~/.punt-labs/ethos/counters/missions-YYYY-MM-DD` | No |
| Delegation ID counter | `~/.punt-labs/ethos/counters/delegations-YYYY-MM-DD` | No |
| Per-delegation exclusive flock | `~/.punt-labs/ethos/delegations/<delegation-id>.lock` | No |

Each Tier B `record.yaml` carries the on-disk
[`Delegation`](docs/audited-delegation.md) shape: `id`, `tier`,
`mission`, `parent_delegation`, `parent_session`, `agent_type`,
`spawn_pattern`, `created_at`, `closed_at`, `verdict`, `prompt_hash`,
`reason`. Atomic write via `os.CreateTemp` + `Chmod(0o600)` + `Sync` +
`Rename` in the record's own directory.

Lock acquisition order (LIFO release via `defer`):

```text
per-mission (LOCK_SH, repo tree) → per-delegation (LOCK_EX, global tree)
```

The per-mission flock lives in the repo tree at
`<repo>/.punt-labs/ethos/missions/<mission-id>/.lock`; the per-delegation flock
lives in the global tree at
`~/.punt-labs/ethos/delegations/<delegation-id>.lock` so two checkouts
of the same repo lock the same inode. The per-mission lock is
**shared** so two Tier B spawns under one mission do not serialize.
The per-delegation lock is **exclusive** so the skeleton write is the
sole writer for its ID.

Counters use the sibling per-namespace per-date pattern: each file
is a single integer, flock-guarded, and adding a new namespace adds
a new sibling file without changing any existing one (DES-054
invariant I9-counter). Session rosters and PID pointers stay under
`~/.punt-labs/ethos/sessions/` because they reference live process
state; only audit logs move into the repo.

Full audited-delegation reference:
[docs/audited-delegation.md](docs/audited-delegation.md).
