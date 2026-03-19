# ethos

> Identity binding for humans and AI agents.

[![Release](https://img.shields.io/github/v/release/punt-labs/ethos)](https://github.com/punt-labs/ethos/releases/latest)
[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/punt-labs/ethos)](https://goreportcard.com/report/github.com/punt-labs/ethos)

Ethos gives humans and AI agents a shared identity model — name, voice,
email, GitHub handle, writing style, personality, and skills — stored as
YAML and readable by any tool in the Punt Labs ecosystem. Vox reads the
voice binding, Beadle reads the email, Biff reads the GitHub handle.
Everything works without ethos; ethos makes it richer.

**Platforms:** macOS, Linux

## Quick Start

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/v0.1.0/install.sh | sh
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
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/v0.1.0/install.sh -o install.sh
shasum -a 256 install.sh
cat install.sh
sh install.sh
```

</details>

## Features

- **Unified identity** --- one YAML file per human or agent, same schema for both
- **Channel bindings** --- voice (Vox), email (Beadle), GitHub (Biff), Claude Code agent definition
- **Sidecar architecture** --- publishes state to the filesystem; other tools read it optionally
- **Session roster** --- tracks all participants (human + agents) in a session with parent-child tree
- **Resolution chain** --- repo-local config overrides global active identity
- **CLI + MCP + Plugin** --- accessible from terminal, AI agents, and Claude Code

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

`--json` is a global flag — valid before or after the subcommand.
Use `--` to stop flag parsing (e.g., `ethos create -f -- --json` treats
`--json` as a filename). Use `ethos <command> --help` for per-command usage.

## Identity Schema

```yaml
name: Mal Reynolds
handle: mal
kind: human                    # or "agent"
email: mal@serenity.ship        # beadle binding
github: mal                  # biff binding
voice:                         # vox binding
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

## Storage

| Scope | Path | Tracked? |
|-------|------|----------|
| Identities | `~/.punt-labs/ethos/identities/<persona>.yaml` | No (personal) |
| Extensions | `~/.punt-labs/ethos/identities/<persona>.ext/<tool>.yaml` | No |
| Active identity | `~/.punt-labs/ethos/active` | No |
| Sessions | `~/.punt-labs/ethos/sessions/<session-id>.yaml` | No (ephemeral) |
| Repo config | `.punt-labs/ethos/config.yaml` | Yes |
| Repo agents | `.punt-labs/ethos/agents/<name>.yaml` | Yes |

## How Other Tools Use Ethos

Ethos is a sidecar. Other tools read identity state at known paths and
store tool-specific attributes via the extension mechanism. No import
dependency exists.

**Core identity fields** (owned by ethos): name, handle, kind, email,
github, voice, agent, writing\_style, personality, skills.

**Extensions** (owned by each tool): any tool can read/write namespaced
key-value pairs in `<persona>.ext/<tool>.yaml`. For example, Beadle
stores its GPG key ID, Biff stores a preferred TTY name, Vox stores a
default mood. Ethos assembles the merged view when you load an identity
but never interprets extension contents.

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
