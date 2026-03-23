# ethos

> Identity binding for humans and AI agents.

[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/punt-labs/ethos)](https://github.com/punt-labs/ethos/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/punt-labs/ethos)](https://goreportcard.com/report/github.com/punt-labs/ethos)
[![Working Backwards](https://img.shields.io/badge/Working_Backwards-hypothesis-lightgrey)](./prfaq.pdf)

Ethos stores persistent identity for humans and AI agents — name, email,
GitHub handle, voice, writing style, personality, and talents — as one YAML
file per persona. Agentic coding tools (Claude Code, OpenCode, Codex) start
each session without knowing who the user is or distinguishing one agent from
another. Ethos provides that context. Any tool can read it via the filesystem,
CLI, or MCP server. Same schema for humans and agents, extensible by any
application.

**Platforms:** macOS and Linux (amd64, arm64)

## Quick Start

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/2b7f919/install.sh | sh
```

<details>
<summary>Manual install (if you already have Go)</summary>

```bash
go install github.com/punt-labs/ethos/cmd/ethos@latest
mkdir -p ~/.punt-labs/ethos/identities
ethos doctor
```

</details>

<details>
<summary>Verify before running</summary>

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/2b7f919/install.sh -o install.sh
shasum -a 256 install.sh
cat install.sh
sh install.sh
```

</details>

## Features

- **Same schema for humans and agents** — one YAML file per persona, `kind: human` or `kind: agent`
- **Composable attributes** — talents, personalities, and writing styles are reusable `.md` files referenced by slug
- **Three integration patterns** — filesystem (zero dependency), CLI (shell/hooks), MCP server (identity, attributes, extensions, sessions)
- **Extensible** — any tool attaches its own attributes via `<persona>.ext/<tool>.yaml`
- **Session roster** — tracks all participants (human + agents) in a session with parent-child tree
- **Persona auto-matching** — subagents get personas automatically when the handle matches the agent type
- **Resolution chain** — identity resolved from iam declaration, git config, or OS user (DES-011)
- **Channel bindings** — an identity *has* a voice the way it *has* an email: voice (Vox), email (Beadle), GitHub (Biff), Claude Code agent definition

## What It Looks Like

```text
$ ethos personality create principal-engineer -f principal-engineer.md
Created personality "principal-engineer"

$ ethos talent create go-engineering -f go-engineering.md
Created talent "go-engineering"

$ ethos create
Name: Mal Reynolds
Handle [mal-reynolds]: mal
Kind (human/agent) [human]:
Email (optional): mal@serenity.ship

Personality:
  1. principal-engineer
  n. [create new]
  (empty to skip)
Choice: 1

Skills (select multiple, comma-separated):
  1. go-engineering
  n. [create new]
  (empty to skip)
Choice: 1

Created identity "mal" (Mal Reynolds)

$ ethos whoami
Mal Reynolds (mal)

$ ethos show mal
Name:         Mal Reynolds
Handle:       mal
Kind:         human
Email:        mal@serenity.ship
Personality:  principal-engineer

# Principal Engineer
Direct, accountable, evidence-driven...

Skills:       go-engineering

--- go-engineering ---
# Go Engineering
Systems design, correctness over speed...
```

## Commands

### Identity

| Command | What it does |
|---------|-------------|
| `ethos whoami [--json]` | Show the caller's identity (resolved from iam/git/OS) |
| `ethos create` | Create a new identity (interactive wizard) |
| `ethos create -f <path>` | Create from a YAML file |
| `ethos list [--json]` | List all identities |
| `ethos show <handle> [--json]` | Show identity with resolved attribute content |
| `ethos show <handle> --reference` | Show identity with attribute slugs only |

### Attributes

| Command | What it does |
|---------|-------------|
| `ethos talent create <slug>` | Create a talent (opens `$EDITOR` or `--file`) |
| `ethos talent list` | List all talents |
| `ethos talent show <slug>` | Show talent content |
| `ethos talent add <handle> <slug>` | Add talent to an identity |
| `ethos talent remove <handle> <slug>` | Remove talent from an identity |
| `ethos personality create <slug>` | Create a personality |
| `ethos personality list` | List all personalities |
| `ethos personality show <slug>` | Show personality content |
| `ethos personality set <handle> <slug>` | Set personality on an identity |
| `ethos writing-style create <slug>` | Create a writing style |
| `ethos writing-style list` | List all writing styles |
| `ethos writing-style show <slug>` | Show writing style content |
| `ethos writing-style set <handle> <slug>` | Set writing style on an identity |

### Session

| Command | What it does |
|---------|-------------|
| `ethos iam <persona>` | Declare persona in current session |
| `ethos session` | Show current session participants |
| `ethos session purge` | Clean up stale session rosters |

### Extensions

| Command | What it does |
|---------|-------------|
| `ethos ext set <persona> <ns> <key> <value>` | Write an extension key |
| `ethos ext get <persona> <ns> [key]` | Read extension key(s) |
| `ethos ext del <persona> <ns> [key]` | Delete key or namespace |
| `ethos ext list <persona>` | List extension namespaces |

### Admin

| Command | What it does |
|---------|-------------|
| `ethos version [--json]` | Print version |
| `ethos doctor [--json]` | Check installation health |
| `ethos serve` | Start MCP server (stdio) |
| `ethos uninstall` | Remove plugin (`--purge` to remove binary + data) |

`--json` is a global flag — valid before or after the subcommand.

## MCP Tools

When running as a Claude Code plugin, ethos registers an MCP server with
9 tools. The `talent`, `personality`, `writing_style`, `ext`, and `session`
tools use a `method` parameter for verb dispatch; the others are single-action
tools.

All tools have corresponding slash commands under `/ethos:*`.

| Tool | Methods | Slash command |
|------|---------|---------------|
| `identity` | whoami, list, get, create, iam | `/ethos:identity` |
| `talent` | create, list, show, delete, add, remove | `/ethos:talent` |
| `personality` | create, list, show, delete, set | `/ethos:personality` |
| `writing_style` | create, list, show, delete, set | `/ethos:writing-style` |
| `ext` | get, set, del, list | `/ethos:ext` |
| `session` | roster, join, leave | `/ethos:session` |

## Identity Schema

```yaml
name: Mal Reynolds
handle: mal
kind: human                       # or "agent"
email: mal@serenity.ship           # Beadle binding
github: mal                        # Biff binding
voice:                             # Vox binding
  provider: elevenlabs
  voice_id: "abc123"
agent: .claude/agents/mal.md       # Claude Code agent binding
writing_style: concise-quantified  # slug → writing-styles/concise-quantified.md
personality: principal-engineer    # slug → personalities/principal-engineer.md
talents:                            # slugs → talents/<slug>.md
  - go-engineering
  - product-strategy
```

Attributes (`writing_style`, `personality`, `talents`) are slugs that reference
`.md` files in the attribute directories. `ethos show` resolves them to full
content by default. Multiple identities can share the same attribute files.

Same schema for agents — only `kind` differs:

```yaml
name: Code Reviewer
handle: code-reviewer
kind: agent
personality: principal-engineer
talents:
  - code-review
  - security-analysis
agent: .claude/agents/code-reviewer.md
```

When a `code-reviewer` subagent spawns, ethos auto-matches it to this
persona by handle. The agent inherits the personality, talents, and
channel bindings defined here.

**Auto-matching convention:** the ethos handle must exactly match the
agent type string (case-sensitive, lowercase). Handles are restricted to
`[a-z0-9-]`. If a subagent doesn't get a persona, check that you created
an identity with a handle matching the agent type.

## Storage

| Scope | Path | Tracked? |
|-------|------|----------|
| Identities | `~/.punt-labs/ethos/identities/<persona>.yaml` | No (personal) |
| Extensions | `~/.punt-labs/ethos/identities/<persona>.ext/<tool>.yaml` | No |
| Skills | `~/.punt-labs/ethos/talents/<slug>.md` | No |
| Personalities | `~/.punt-labs/ethos/personalities/<slug>.md` | No |
| Writing styles | `~/.punt-labs/ethos/writing-styles/<slug>.md` | No |
| Sessions | `~/.punt-labs/ethos/sessions/<session-id>.yaml` | No (ephemeral) |
| Repo config | `.punt-labs/ethos/config.yaml` | Yes |
| Repo agents | `.punt-labs/ethos/agents/<name>.yaml` | Yes |

## Identity Resolution

Human and agent identities are resolved automatically — no manual
"set active" step required.

**Human resolution** (stops at first match):

1. `iam` declaration — explicit persona set via `ethos iam`
2. `git config user.name` — matched against identity `github` field
3. `git config user.email` — matched against identity `email` field
4. `$USER` — matched against identity `handle` field

**Agent resolution** — per-repo `.punt-labs/ethos/config.yaml`:

```yaml
agent: claude
```

Tracked in git — the whole team shares the same agent configuration.
When the agent field is unset, the primary agent has no persona.

## Integration

Tools integrate with ethos at whatever coupling level fits:

| Pattern | How | Dependency |
|---------|-----|------------|
| **Filesystem** | Read YAML at `~/.punt-labs/ethos/identities/<handle>.yaml` | None |
| **CLI** | Call `ethos whoami --json` or `ethos show <handle> --json` from hooks/scripts | Binary installed |
| **MCP server** | Connect to `ethos serve` for identity CRUD, attribute management, extensions, and session roster | Binary installed |

**Core identity fields** (owned by ethos): name, handle, kind, email,
github, voice, agent, writing\_style, personality, talents.

**Attributes** (reusable `.md` files): talents, personalities, and writing
styles are plain markdown documents stored in dedicated directories. Any
identity can reference them by slug. Create with `ethos talent create`,
`ethos personality create`, or `ethos writing-style create`.

**Extensions** (owned by each tool): any tool can read/write namespaced
key-value pairs in `<persona>.ext/<tool>.yaml`. A voice tool stores its
voice ID, an email tool stores a GPG key, a messaging tool stores routing
preferences. Ethos assembles the merged view but never interprets
extension contents.

```bash
ethos ext set mal beadle gpg_key_id 3AA5C34371567BD2
ethos ext get mal beadle gpg_key_id
ethos show mal   # includes ext map with all tool namespaces
```

## Development

```bash
make check    # All quality gates: vet + staticcheck + markdownlint + shellcheck + tests
make build    # Build binary
make format   # Auto-format code
make dist     # Cross-compile for all platforms
make help     # List all targets
```

## License

MIT
