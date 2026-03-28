# Ethos

Identity binding for humans and AI agents. Ethos unifies a name, voice (Vox), email (Beadle), GitHub handle (Biff), writing style, personality, and talents into a single identity that other tools read. Written in Go.

Ethos publishes identity state via CLI, MCP, and the filesystem. Vox, Beadle, and Biff work without ethos; when ethos is installed, they gain richer identity context.

## Standards Checklist

When this CLAUDE.md conflicts with punt-kit standards, this file wins.

Before specifying work, check the relevant standard:

- **New MCP tool** → DES-020: every tool needs a formatter in `format_output.go` before shipping
- **New CLI command** → [cli standard](https://github.com/punt-labs/punt-kit/blob/main/standards/cli.md)
- **New hook** → [hooks standard](https://github.com/punt-labs/punt-kit/blob/main/standards/hooks.md)
- **New slash command** → existing command files for pattern; both `name.md` and `name-dev.md` required
- **Any Go code** → [go standard](https://github.com/punt-labs/punt-kit/blob/main/standards/go.md)
- **Release work** → [release-process standard](https://github.com/punt-labs/punt-kit/blob/main/standards/release-process.md)

## Build & Run

```bash
make build                              # Build ethos binary
make install                            # Build and install to ~/.local/bin
make check                              # All quality gates (vet, staticcheck, shellcheck, markdownlint, tests)
./ethos version                         # Print version
./ethos doctor                          # Check installation health
./ethos whoami                          # Show caller's identity (iam/git/OS)
./ethos resolve-agent                   # Show default agent from repo config
./ethos serve                           # Start MCP server (stdio transport)
./ethos iam <persona>                   # Declare persona in current session
./ethos session                         # Show current session participants
./ethos session purge                   # Clean up stale sessions
```

Use `.tmp/` for scratch files — `TMPDIR` is set via `.envrc` so subprocesses use it automatically.

## Quality Gates

The Makefile is the source of truth (`make help`).

```bash
make check                             # All gates: lint + docs + test
```

Expands to `make lint docs test`: `go vet`, `staticcheck`, `shellcheck hooks/*.sh install.sh`, `markdownlint`, `go test -race -count=1 ./...`.

## Architecture

### Package Map

| Package | Responsibility |
|---------|---------------|
| `cmd/ethos/` | CLI entry point: identity, attribute, session, and admin commands |
| `internal/identity/` | Core identity model, validation, CRUD, attribute resolution |
| `internal/attribute/` | Generic CRUD for named markdown files (talents, personalities, writing styles) |
| `internal/process/` | Process tree walker: find topmost Claude ancestor PID |
| `internal/session/` | Session roster model, store with flock-based concurrency |
| `internal/resolve/` | Identity resolution chain: repo-local → global → error |
| `internal/mcp/` | MCP tool definitions and handlers (9 tools) |

### Storage Layout

| Scope | Path | Git-tracked? |
|-------|------|-------------|
| Repo identities | `.punt-labs/ethos/identities/<handle>.yaml` | Yes |
| Repo talents | `.punt-labs/ethos/talents/<slug>.md` | Yes |
| Repo personalities | `.punt-labs/ethos/personalities/<slug>.md` | Yes |
| Repo writing styles | `.punt-labs/ethos/writing-styles/<slug>.md` | Yes |
| Repo config | `.punt-labs/ethos.yaml` | Yes |
| Repo agents | `.punt-labs/ethos/agents/<name>.yaml` | Yes |
| Global identities | `~/.punt-labs/ethos/identities/<handle>.yaml` | No |
| Extensions (global) | `~/.punt-labs/ethos/identities/<handle>.ext/<tool>.yaml` | No |
| Global talents | `~/.punt-labs/ethos/talents/<slug>.md` | No |
| Global personalities | `~/.punt-labs/ethos/personalities/<slug>.md` | No |
| Global writing styles | `~/.punt-labs/ethos/writing-styles/<slug>.md` | No |
| Sessions | `~/.punt-labs/ethos/sessions/<session-id>.yaml` | No |

### Identity Schema

```yaml
name: Mal Reynolds
handle: mal
kind: human                           # or "agent"
email: mal@serenity.ship               # beadle binding
github: mal                            # biff binding
agent: .claude/agents/mal.md           # claude code agent binding
writing_style: concise-quantified      # slug → writing-styles/concise-quantified.md
personality: principal-engineer        # slug → personalities/principal-engineer.md
talents:                               # slugs → talents/<slug>.md
  - formal-methods
  - product-strategy
```

### Design Invariants

- **Multiple integration surfaces.** Other tools integrate with ethos via its CLI, via MCP, or by reading its filesystem state directly. Ethos is consumed through these surfaces, not as a Go library API.
- **Same schema for humans and agents.** The `kind` field is the only structural difference.
- **Agent definition is a channel binding.** Like voice or email — the `.md` file defines tools and workflow, ethos defines who.
- **No consumer-specific fields.** Never add fields for a specific consumer (Beadle, Biff, Vox). Use the generic extension mechanism — any tool can read/write arbitrary key-value pairs scoped to a namespace. Ethos validates constraints but does not know what the keys mean.
- **Preserve identity content.** Source `.md` files are the authority on personality, writing style, and talent content. Hooks and persona blocks may restructure (strip leading headings, fold the first paragraph into the opening line, list talents as slugs) but must not discard or summarize the underlying meaning.

## Go Standards

Module path: `github.com/punt-labs/ethos`. Follows [Go standards](https://github.com/punt-labs/punt-kit/blob/main/standards/go.md).

## Operational Constraints

- **Never self-install.** Do not run `make install` from inside Claude Code. The ethos binary is loaded by Claude Code's process tree; macOS will not allow overwriting a running binary. Ask the user to run `make install` from their shell. Use `.tmp/ethos` for testing.
