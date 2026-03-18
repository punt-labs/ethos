# ethos

> Identity binding for humans and AI agents.

[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)

Ethos gives humans and AI agents a shared identity model — name, voice,
email, GitHub handle, writing style, personality, and skills — stored as
YAML and readable by any tool in the Punt Labs ecosystem. Vox reads the
voice binding, Beadle reads the email, Biff reads the GitHub handle.
Everything works without ethos; ethos makes it richer.

**Platforms:** macOS, Linux

## Quick Start

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/80f42bb/install.sh | sh
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
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/80f42bb/install.sh -o install.sh
shasum -a 256 install.sh
cat install.sh
sh install.sh
```

</details>

## Features

- **Unified identity** --- one YAML file per human or agent, same schema for both
- **Channel bindings** --- voice (Vox), email (Beadle), GitHub (Biff), Claude Code agent definition
- **Sidecar architecture** --- publishes state to the filesystem; other tools read it optionally
- **Resolution chain** --- repo-local config overrides global active identity
- **CLI + MCP + Plugin** --- accessible from terminal, AI agents, and Claude Code

## What It Looks Like

```text
$ ethos create
Name: Jim Freeman
Handle [jim-freeman]: jfreeman
Kind (human/agent) [human]:
Email (optional): jim@punt-labs.com
GitHub username (optional): jfreeman
Voice provider (optional, e.g. elevenlabs):
Agent definition path (optional):
Writing style (optional, one line): Direct. Short sentences. Data over adjectives.
Personality (optional, one line): Principal engineer. Formal methods, accountability.
Skills (optional, comma-separated): formal-methods, product-strategy
Set as active identity (first identity created)
Created identity "jfreeman" (Jim Freeman)

$ ethos whoami
Jim Freeman (jfreeman)

$ ethos list
* jfreeman         Jim Freeman
  wei              Wei
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
| `ethos serve` | Start MCP server (stdio) |

`--json` is a global flag — valid before or after the subcommand.
Use `--` to stop flag parsing (e.g., `ethos create -f -- --json` treats
`--json` as a filename). Use `ethos <command> --help` for per-command usage.

## Identity Schema

```yaml
name: Jim Freeman
handle: jfreeman
kind: human                    # or "agent"
email: jim@punt-labs.com       # beadle binding
github: jfreeman               # biff binding
voice:                         # vox binding
  provider: elevenlabs
  voice_id: "abc123"
agent: .claude/agents/jim.md   # claude code agent binding
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
| Identities | `~/.punt-labs/ethos/identities/<handle>.yaml` | No (personal) |
| Active identity | `~/.punt-labs/ethos/active` | No |
| Repo config | `.punt-labs/ethos/config.yaml` | Yes |
| Repo agents | `.punt-labs/ethos/agents/<name>.yaml` | Yes |

## How Other Tools Read Ethos

Ethos is a sidecar. Other tools check for identity state at known paths and
use it if present. No import dependency exists.

| Tool | What it reads | How |
|------|--------------|-----|
| Vox | `voice.provider`, `voice.voice_id` | Reads active identity YAML before speaking |
| Beadle | `email` | Uses as sender address |
| Biff | `github` | Shows in `/who` and `/finger` |
| Claude Code | `agent` | References the `.md` agent definition |

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
