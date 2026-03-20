# ethos

> Identity binding for humans and AI agents.

[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/punt-labs/ethos)](https://github.com/punt-labs/ethos/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/punt-labs/ethos)](https://goreportcard.com/report/github.com/punt-labs/ethos)
[![Working Backwards](https://img.shields.io/badge/Working_Backwards-hypothesis-lightgrey)](./prfaq.pdf)

Ethos stores persistent identity for humans and AI agents — name, email,
GitHub handle, voice, writing style, personality, and skills — as one YAML
file per persona. Agentic coding tools (Claude Code, OpenCode, Codex) start
each session without knowing who the user is or distinguishing one agent from
another. Ethos provides that context. Any tool can read it via the filesystem,
CLI, or MCP server. Same schema for humans and agents, extensible by any
application.

**Platforms:** macOS, Linux

## Quick Start

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/2725279/install.sh | sh
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
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/2725279/install.sh -o install.sh
shasum -a 256 install.sh
cat install.sh
sh install.sh
```

</details>

## Features

- **Same schema for humans and agents** — one YAML file per persona, `kind: human` or `kind: agent`
- **Three integration patterns** — filesystem (zero dependency), CLI (shell/hooks), MCP server (structured protocol)
- **Extensible** — any tool attaches its own attributes via `<persona>.ext/<tool>.yaml`
- **Session roster** — tracks all participants (human + agents) in a session with parent-child tree
- **Persona auto-matching** — subagents get personas automatically when the handle matches the agent type
- **Resolution chain** — repo-local config overrides global active identity
- **Channel bindings** — an identity *has* a voice the way it *has* an email: voice (Vox), email (Beadle), GitHub (Biff), Claude Code agent definition

## What It Looks Like

```text
$ ethos create
Name: Mal Reynolds
Handle [mal-reynolds]: mal
Kind (human/agent) [human]:
Email (optional): mal@serenity.ship
GitHub username (optional): mal
Voice provider (optional, e.g. elevenlabs):
Agent definition path (optional):
Writing style (optional, one line): Direct. Short sentences. Data over adjectives.
Personality (optional, one line): Principal engineer. Formal methods, accountability.
Skills (optional, comma-separated): formal-methods, product-strategy
Set as active identity (first identity created)
Created identity "mal" (Mal Reynolds)

$ ethos whoami
Mal Reynolds (mal)

$ ethos list
* mal            Mal Reynolds
  river            River Tam
```

## Commands

| Command | What it does |
|---------|-------------|
| `ethos whoami [--json]` | Show the active identity |
| `ethos whoami <handle> [--json]` | Set the active identity |
| `ethos create` | Create a new identity (interactive) |
| `ethos create -f <path>` | Create from a YAML file |
| `ethos list [--json]` | List all identities |
| `ethos show <handle> [--json]` | Show identity details |
| `ethos version [--json]` | Print version |
| `ethos doctor [--json]` | Check installation health |
| `ethos ext` | Manage tool-scoped extensions |
| `ethos iam <persona>` | Declare persona in current session |
| `ethos session` | Show current session participants |
| `ethos session create` | Create a new session roster |
| `ethos session join` | Add a participant to a session |
| `ethos session leave` | Remove a participant from a session |
| `ethos session purge` | Clean up stale session rosters |
| `ethos serve` | Start MCP server (stdio) |
| `ethos uninstall` | Remove plugin (`--purge` to remove binary + data) |

`--json` is a global flag — valid before or after the subcommand.
Use `--` to stop flag parsing (e.g., `ethos create -f -- --json` treats
`--json` as a filename). Use `ethos <command> --help` for per-command usage.

## Identity Schema

```yaml
name: Mal Reynolds
handle: mal
kind: human                    # or "agent"
email: mal@serenity.ship        # email binding
github: mal                  # GitHub binding
voice:                         # voice binding
  provider: elevenlabs
  voice_id: "abc123"
agent: .claude/agents/mal.md # claude code agent binding
writing_style: |
  Direct. Short sentences. Data over adjectives.
personality: |
  Principal engineer. Formal methods, accountability.
skills:
  - formal-methods
  - product-strategy
```

Same schema for agents — only `kind` differs:

```yaml
name: Code Reviewer
handle: code-reviewer
kind: agent
writing_style: |
  Formal. Cite line numbers. Flag security issues first.
personality: |
  Thorough, direct, zero tolerance for silent failures.
skills:
  - code-review
  - security-analysis
agent: .claude/agents/code-reviewer.md
```

When a `code-reviewer` subagent spawns, ethos auto-matches it to this
persona by handle. The agent inherits the writing style, personality,
and channel bindings defined here.

**Auto-matching convention:** the ethos handle must exactly match the
agent type string (case-sensitive, lowercase). Handles are restricted to
`[a-z0-9-]`. If a subagent doesn't get a persona, check that you created
an identity with a handle matching the agent type.

## Storage

| Scope | Path | Tracked? |
|-------|------|----------|
| Identities | `~/.punt-labs/ethos/identities/<persona>.yaml` | No (personal) |
| Extensions | `~/.punt-labs/ethos/identities/<persona>.ext/<tool>.yaml` | No |
| Active identity | `~/.punt-labs/ethos/active` | No |
| Sessions | `~/.punt-labs/ethos/sessions/<session-id>.yaml` | No (ephemeral) |
| Repo config | `.punt-labs/ethos/config.yaml` | Yes |
| Repo agents | `.punt-labs/ethos/agents/<name>.yaml` | Yes |

## Per-Project Identity

Override the global active identity for a specific repo by creating
`.punt-labs/ethos/config.yaml` in the repo root:

```yaml
active: claude
```

This pins the identity to `claude` in this repo regardless of the global
`ethos whoami` setting. Tracked in git — the whole team shares the same
project identity. Useful when a repo's agent should always use a specific
persona (e.g., beadle always sends email as `claude`).

## Integration

Tools integrate with ethos at whatever coupling level fits:

| Pattern | How | Dependency |
|---------|-----|------------|
| **Filesystem** | Read YAML at `~/.punt-labs/ethos/identities/<handle>.yaml` | None |
| **CLI** | Call `ethos whoami --json` or `ethos show <handle> --json` from hooks/scripts | Binary installed |
| **MCP server** | Connect to `ethos serve` for structured identity operations | Binary installed |

**Core identity fields** (owned by ethos): name, handle, kind, email,
github, voice, agent, writing\_style, personality, skills.

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
