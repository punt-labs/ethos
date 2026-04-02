# Ethos Agent Guide

How to use ethos from an AI agent session — CLI, MCP tools, hooks, and extending identities with custom attributes.

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
ethos whoami                          # Show active identity
ethos whoami mal                      # Set active identity to "mal"
ethos create                          # Interactive identity creation
ethos create -f persona.yaml          # Create from YAML file
ethos list                            # List all identities (* = active)
ethos show mal                        # Full identity with extensions
ethos show mal --json                 # JSON output
```

### MCP Tools

When running as a Claude Code plugin, ethos registers an MCP server (`self`) with 9 tools using method-dispatch.

**All tools use a consolidated `method` parameter:**

| Tool | Methods | Key Parameters |
|------|---------|----------------|
| `identity` | whoami, list, get, create | `handle`, `reference` |
| `session` | roster, join, leave, iam | `session_id`, `agent_id`, `persona` |
| `talent` | create, list, show, delete, add, remove | `slug`, `content`, `handle` |
| `personality` | create, list, show, delete, set | `slug`, `content`, `handle` |
| `writing_style` | create, list, show, delete, set | `slug`, `content`, `handle` |
| `ext` | get, set, del, list | `handle`, `namespace`, `key`, `value` |
| `team` | list, show, create, delete, add-member, remove-member, add-collab, for-repo | `name`, `identity`, `role` |
| `role` | list, show, create, delete | `name`, `content` |
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
instructions that ethos injects into agent context at session start and
before compaction. This is how tools integrate behavioral context without
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

### How Agents Use Team Context

You don't need to query teams manually. The SessionStart and PreCompact
hooks automatically inject team context into your session — member names,
roles, responsibilities, and the collaboration graph. This context
survives compaction because PreCompact re-injects it.

Use the team and role MCP tools when you need to look up specific
details: who has a particular role, which repos a team covers, or what
tools a role permits.

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

**Agent resolution** — per-repo `.punt-labs/ethos.yaml`:

```yaml
agent: claude
```

When `agent:` is unset, the primary agent has no persona.

## Hooks

Ethos registers 5 hooks in `hooks/hooks.json`:

| Hook | Script | Purpose |
|------|--------|---------|
| `SessionStart` | `session-start.sh` | Create roster, inject identity context and persona (personality + writing style + talents) |
| `PreCompact` | `pre-compact.sh` | Re-inject condensed persona before context compression |
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

2. **PreCompact** — re-injects a condensed persona before context compression. Without this, the personality and writing style from SessionStart get summarized away during compaction, causing behavioral drift in long sessions.

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
| Repo agents | `.punt-labs/ethos/agents/<name>.yaml` | Yes |
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
