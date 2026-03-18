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
# Build and install
make build
make install

# Create your identity
ethos create

# Check who you are
ethos whoami
```

## Features

- **Unified identity** --- one YAML file per human or agent, same schema for both
- **Channel bindings** --- voice (Vox), email (Beadle), GitHub (Biff), Claude Code agent definition
- **Sidecar architecture** --- publishes state to the filesystem; other tools read it optionally
- **Resolution chain** --- repo-local config overrides global active identity
- **CLI + MCP + Plugin** --- accessible from terminal, AI agents, and Claude Code

## Commands

| Command | What it does |
|---------|-------------|
| `ethos whoami` | Show the active identity |
| `ethos whoami <handle>` | Set the active identity |
| `ethos create` | Create a new identity (interactive or from YAML) |
| `ethos list` | List all identities |
| `ethos show <handle>` | Show identity details |
| `ethos version` | Print version |
| `ethos doctor` | Check installation health |
| `ethos serve` | Start MCP server (stdio) |

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
make check    # All quality gates: vet + staticcheck + markdownlint + tests
make build    # Build binary
make format   # Auto-format code
make dist     # Cross-compile for all platforms
```

## License

MIT
