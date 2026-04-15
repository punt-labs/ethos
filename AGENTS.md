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

**Troubleshooting**: if `subagent_type` fails with "agent type not found",
run `ethos team active` to confirm the active team source, and
`ls .claude/agents/` to see generated agents. For the legacy layout,
a leading `-` in `git submodule status` indicates an uninitialized
submodule.

## Concepts

Ethos provides two things:

1. **Identity registry** — YAML files at `~/.punt-labs/ethos/identities/<handle>.yaml`. One file per human or agent. Same schema for both.
2. **Session roster** — who is present in the current Claude Code session (human, primary agent, subagents), their personas, and parent-child relationships. Managed automatically by hooks.

Tools integrate with ethos at whatever coupling level fits:

- **Filesystem** — read YAML at known paths. Zero dependency on the ethos binary.
- **CLI** — call `ethos whoami`, `ethos show`, etc. from hooks and scripts. Requires the binary.
- **MCP server** — connect to `ethos serve` for structured identity operations during a session.

## Identity Operations

### CLI

```bash
# Shortcuts — canonical forms in parentheses.
ethos whoami                          # Show active identity   (ethos identity whoami)
ethos iam mal                         # Declare active persona (ethos session iam)
ethos create                          # Interactive identity creation
ethos create -f persona.yaml          # Create from YAML file
ethos list                            # List all identities   (ethos identity list)
ethos show mal                        # Full identity          (ethos identity get)
ethos show mal --json                 # JSON output
```

### MCP Tools

When running as a Claude Code plugin, ethos registers an MCP server (named `ethos`, plugin key `self` -- tool names follow the pattern `mcp__plugin_ethos_self__<tool>`) with 10 tools using method-dispatch.

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
ethos ext del mal vox default_mood

# Delete an entire namespace
ethos ext del mal vox
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

**From CLI flags (dispatch):**

```bash
ethos mission dispatch \
  --worker bwk \
  --evaluator djb \
  --write-set "internal/session/store.go,internal/session/store_test.go" \
  --criteria "purge removes stale entries,test covers TTL edge case" \
  --context "Follow-up from PR #280" \
  --ticket ethos-7al \
  --type implement \
  --budget 2
```

Required flags: `--worker`, `--evaluator`, `--write-set`, `--criteria`.
Optional: `--context`, `--ticket`, `--type` (default: implement),
`--budget` (default: 2). Leader is resolved from the repo's configured
`agent:` value; if unset or outside a repo, falls back to `"claude"`.

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

Closing auto-appends a summary line to `.ethos/missions.jsonl` for
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
| Repo identities | `.punt-labs/ethos/identities/<handle>.yaml` | Yes |
| Repo talents | `.punt-labs/ethos/talents/<slug>.md` | Yes |
| Repo personalities | `.punt-labs/ethos/personalities/<slug>.md` | Yes |
| Repo writing styles | `.punt-labs/ethos/writing-styles/<slug>.md` | Yes |
| Repo roles | `.punt-labs/ethos/roles/<name>.yaml` | Yes |
| Repo teams | `.punt-labs/ethos/teams/<name>.yaml` | Yes |
| Repo config | `.punt-labs/ethos.yaml` | Yes |
| Repo agents | `.punt-labs/ethos/agents/<name>.md` | Yes |
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
