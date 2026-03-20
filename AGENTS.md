# Ethos Agent Guide

How to use ethos from an AI agent session — CLI, MCP tools, hooks, and extending identities with custom attributes.

## Concepts

Ethos provides two things:

1. **Identity registry** — YAML files at `~/.punt-labs/ethos/identities/<handle>.yaml`. One file per human or agent. Same schema for both.
2. **Session roster** — who is present in the current Claude Code session (human, primary agent, subagents), their personas, and parent-child relationships.

Ethos is a sidecar. Other tools (Vox, Beadle, Biff) read ethos state from the filesystem. They do not import ethos. The file format is the contract.

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

When running as a Claude Code plugin, ethos registers an MCP server (`self`) with 12 tools. The plugin auto-allows `mcp__plugin_ethos_self__*` in `settings.json` on first session.

**Identity tools:**

| Tool | Parameters | Description |
|------|-----------|-------------|
| `whoami` | optional `handle` | Show active identity, or set it |
| `list_identities` | — | List all identities with active status |
| `get_identity` | `handle` | Full identity including extensions |
| `create_identity` | `name`, `handle`, `kind` + optional fields | Create a new identity |

**Example — read identity from MCP:**

```text
Call mcp__plugin_ethos_self__get_identity with handle="mal"
```

Returns JSON with all core fields plus the `ext` map (extensions from all tools).

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

| Tool | Parameters | Description |
|------|-----------|-------------|
| `session_iam` | `session_id`, `agent_id`, `persona` | Declare persona for a participant |
| `session_roster` | `session_id` | Return full roster with tree |
| `session_join` | `session_id`, `agent_id` + optional `persona`, `parent`, `agent_type` | Add participant |
| `session_leave` | `session_id`, `agent_id` | Remove participant |

### Slash Commands

| Command | Description |
|---------|-------------|
| `/whoami` | Show or set active identity |
| `/whoami mal` | Switch to identity "mal" |
| `/iam archie` | Declare persona in current session |
| `/session` | Show session participants |

### Roster Structure

```yaml
session: ba3bb20f
started: 2026-03-18T14:30:00Z
participants:
  - agent_id: mal                   # $USER — the human
    persona: mal
    parent: ~                       # root of the tree
    ext:
      biff: { tty: s001 }

  - agent_id: "19147"              # Claude PID
    persona: archie
    parent: mal
    ext:
      biff: { tty: s004 }

  - agent_id: a5734dd              # subagent ID
    persona: code-reviewer
    parent: "19147"
    agent_type: code-reviewer
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

| Tool | Parameters | Description |
|------|-----------|-------------|
| `ext_get` | `persona`, `namespace`, optional `key` | Read one key or all keys |
| `ext_set` | `persona`, `namespace`, `key`, `value` | Write a key-value pair |
| `ext_del` | `persona`, `namespace`, optional `key` | Delete key or namespace |
| `ext_list` | `persona` | List all namespaces |

### Merged View

When you read an identity (via `ethos show`, `get_identity`, or `Load()`), extensions appear under the `ext` map:

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

Any tool can read its own namespace file directly. This is the sidecar pattern — stable paths, no import dependency.

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

## Identity Resolution

When a tool asks "who is active?", ethos checks two locations in order:

1. **Repo-local config** — `.punt-labs/ethos/config.yaml` in the repo root. If it has an `active` field, use it.
2. **Global active** — `~/.punt-labs/ethos/active`. Plain text file with the handle.
3. **Error** — no active identity configured.

This mirrors Git: `.git/config` overrides `~/.gitconfig`.

## Hooks

Ethos registers 5 hooks in `hooks/hooks.json`:

| Hook | Script | Purpose |
|------|--------|---------|
| `SessionStart` | `session-start.sh` | Create roster, deploy commands, auto-allow MCP tools, inject identity context |
| `SubagentStart` | `subagent-start.sh` | Add subagent to roster, auto-match persona |
| `SubagentStop` | `subagent-stop.sh` | Remove subagent from roster |
| `SessionEnd` | `session-end.sh` | Delete roster and PID-keyed session file |
| `PostToolUse` | `suppress-output.sh` | Suppress raw MCP tool output (matched to `mcp__plugin_ethos(-dev)?_self__.*`) |

### Session Discovery

Hooks receive `session_id` on stdin. Non-hook callers (Biff, Vox) discover the session ID through a PID-keyed file:

```text
~/.punt-labs/ethos/sessions/current/<claude-pid>
```

The `SessionStart` hook writes this file. Any descendant process walks the process tree to the topmost `claude` ancestor PID, reads this file, and gets the session ID.

## Identity Schema Reference

```yaml
name: Mal Reynolds                    # required
handle: mal                           # required, unique, used as filename
kind: human                           # required: "human" or "agent"
email: mal@serenity.ship              # beadle channel binding
github: mal                           # biff channel binding
voice:                                # vox channel binding
  provider: elevenlabs
  voice_id: "abc123def456"
agent: .claude/agents/mal.md          # claude code agent binding
writing_style: |                      # prose style directives
  Direct. Short sentences. Data over adjectives.
personality: |                        # behavioral directives
  Principal engineer. Formal methods, accountability.
skills:                               # capability tags
  - formal-methods
  - product-strategy
```

The `agent` field is a channel binding — like voice or email. Ethos defines *who*. The agent `.md` file defines *what tools and workflow*.

## Storage Layout

| Scope | Path | Git-tracked? |
|-------|------|-------------|
| Identities | `~/.punt-labs/ethos/identities/<handle>.yaml` | No |
| Extensions | `~/.punt-labs/ethos/identities/<handle>.ext/<ns>.yaml` | No |
| Active identity | `~/.punt-labs/ethos/active` | No |
| Sessions | `~/.punt-labs/ethos/sessions/<id>.yaml` | No |
| Session locks | `~/.punt-labs/ethos/sessions/<id>.lock` | No |
| Current session | `~/.punt-labs/ethos/sessions/current/<pid>` | No |
| Repo config | `.punt-labs/ethos/config.yaml` | Yes |
| Repo agents | `.punt-labs/ethos/agents/<name>.yaml` | Yes |
