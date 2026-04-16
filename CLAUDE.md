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
./ethos mission lint <contract.yaml>    # Advisory pre-delegation linter
./ethos mission pipeline list           # List available pipeline templates
./ethos mission pipeline show <name>    # Show pipeline stages and defaults
./ethos mission pipeline instantiate <name> --var key=value  # Create N missions from a pipeline template
```

Use `.tmp/` for scratch files — `TMPDIR` is set via `.envrc` so subprocesses use it automatically.

## Use Ethos to Build Ethos

This project uses its own pipeline and mission system for ALL work —
design, implementation, review, documentation. Not just code.

**Product design work** uses the `product` pipeline:
```bash
ethos mission pipeline instantiate product \
  --var feature=<name> --var target=<path> \
  --leader claude --worker ghr --evaluator adt
```
Stage 1 (prfaq) → ghr for product thinking. Stage 2 (design) → edt +
mdm for UX + CLI. Stages 3-6 → bwk for implementation, tests, review,
docs. Design is reviewed before code starts.

**Engineering work** uses `standard` or `quick` pipelines. Delegate to
specialists: bwk (Go), mdm (CLI), djb (security), rmh (Python).

**Never solo-design in plan mode.** Instantiate a pipeline, delegate to
specialists, review their output. The system exists for this purpose.

**Dogfood before shipping.** Build the binary, run the commands, walk
the user journey. `make check` passing is necessary but not sufficient.
A feature that fails when used is not shipped — it's fiction.

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
| `internal/hook/` | Hook handlers (SessionStart, PreCompact, SubagentStart/Stop, SessionEnd, PostToolUse) and format output |
| `internal/doctor/` | Installation health checks |
| `internal/role/` | Role model, CRUD, layered store |
| `internal/team/` | Team model, CRUD, layered store, referential integrity enforcement |
| `internal/mission/` | Mission contracts, pipelines, archetypes, write-set enforcement, result artifacts, event log |
| `internal/bundle/` | Team bundle discovery, resolution, validation (three-layer: repo → bundle → global) |
| `internal/seed/` | Embedded starter content (roles, talents, archetypes, pipelines, bundles) deployed by `ethos seed` |
| `internal/mcp/` | MCP tool definitions and handlers (10 tools) |

### Storage Layout

| Scope | Path | Git-tracked? |
|-------|------|-------------|
| Repo identities | `.punt-labs/ethos/identities/<handle>.yaml` | Yes |
| Repo talents | `.punt-labs/ethos/talents/<slug>.md` | Yes |
| Repo personalities | `.punt-labs/ethos/personalities/<slug>.md` | Yes |
| Repo writing styles | `.punt-labs/ethos/writing-styles/<slug>.md` | Yes |
| Repo config | `.punt-labs/ethos.yaml` | Yes |
| Repo roles | `.punt-labs/ethos/roles/<name>.yaml` | Yes |
| Repo teams | `.punt-labs/ethos/teams/<name>.yaml` | Yes |
| Repo agents | `.punt-labs/ethos/agents/<name>.md` | Yes |
| Global identities | `~/.punt-labs/ethos/identities/<handle>.yaml` | No |
| Extensions (global) | `~/.punt-labs/ethos/identities/<handle>.ext/<tool>.yaml` | No |
| Global talents | `~/.punt-labs/ethos/talents/<slug>.md` | No |
| Global personalities | `~/.punt-labs/ethos/personalities/<slug>.md` | No |
| Global writing styles | `~/.punt-labs/ethos/writing-styles/<slug>.md` | No |
| Global roles | `~/.punt-labs/ethos/roles/<name>.yaml` | No |
| Global teams | `~/.punt-labs/ethos/teams/<name>.yaml` | No |
| Global bundles | `~/.punt-labs/ethos/bundles/<name>/` | No |
| Repo bundles | `.punt-labs/ethos-bundles/<name>/` | Yes |
| Missions | `~/.punt-labs/ethos/missions/<id>.yaml` | No |
| Mission traces | `<repo>/.ethos/missions.jsonl` | Yes |
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
- **Three-layer resolution.** All layered stores resolve repo-local → active bundle → global. Bundle layer is read-only. `active_bundle` in `.punt-labs/ethos.yaml` selects the bundle; when unset and `.punt-labs/ethos/` exists as a directory, legacy two-layer behavior is preserved (DES-051).
- **Pipeline instantiation is atomic.** If any stage fails validation, zero missions are created. If a stage fails during Create (phase 2), all prior stages are rolled back.

## Go Standards

Module path: `github.com/punt-labs/ethos`. Follows [Go standards](https://github.com/punt-labs/punt-kit/blob/main/standards/go.md).

## Operational Constraints

- **Never self-install.** Do not run `make install` from inside Claude Code. The ethos binary is loaded by Claude Code's process tree; macOS will not allow overwriting a running binary. Ask the user to run `make install` from their shell. Use `.tmp/ethos` for testing.
