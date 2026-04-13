# ethos

> Identity and workflow for AI agents that work alongside humans.

[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/punt-labs/ethos)](https://github.com/punt-labs/ethos/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/punt-labs/ethos)](https://goreportcard.com/report/github.com/punt-labs/ethos)
[![Working Backwards](https://img.shields.io/badge/Working_Backwards-hypothesis-lightgrey)](./prfaq.pdf)

## The Problem

Every Claude Code session starts blank. The agent doesn't know who it
is, who you are, or how your team works. Sub-agents are generic — they
have tools but no judgment, no voice, no boundaries. When you delegate
work, the contract is free-form prose. When something goes wrong, there's
no audit trail.

## What Ethos Does

Ethos gives every person and AI agent on your team a persistent identity
— personality, writing style, expertise, and role — that loads
automatically at session start and survives context compaction. When you
delegate to a sub-agent, it gets its own identity with distinct expertise
and tool restrictions. When you need structured delegation, ethos provides
typed mission contracts with write-set boundaries, bounded rounds, and
an append-only audit trail.

**You don't do anything at runtime.** Install ethos, create identities,
and forget about it. Hooks handle everything automatically.

**How is this different from SoulSpec?** SoulSpec provides structured
agent personas (SOUL.md, IDENTITY.md, AGENTS.md, STYLE.md) — agent-only, no human
identity, no teams, no delegation contracts, no CLI/MCP integration
surface. Ethos covers humans and agents with the same schema, adds
typed mission contracts for structured delegation, and exposes identity
through filesystem, CLI, and MCP so any tool can read it.

**Platforms:** macOS and Linux (amd64, arm64)

## Quick Start

### 1. Install

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/544d29e/install.sh | sh
```

The installer puts the `ethos` binary in `~/.local/bin` and seeds starter
content: 6 role archetypes, 10 domain talents, and the baseline-ops
skill.

<details>
<summary>Manual install (Go required)</summary>

```bash
go install github.com/punt-labs/ethos/cmd/ethos@latest
mkdir -p ~/.punt-labs/ethos/identities
ethos doctor
```

</details>

### 2. Create Your Identity

```bash
ethos create
```

Or from a YAML file:

```yaml
# ~/.punt-labs/ethos/identities/mal.yaml
name: Mal Reynolds
handle: mal
kind: human
email: mal@example.com
personality: principal-engineer    # slug → personalities/principal-engineer.md
writing_style: concise-quantified  # slug → writing-styles/concise-quantified.md
talents:
  - engineering
```

### 3. Create Your Agent

```yaml
# ~/.punt-labs/ethos/identities/claude.yaml
name: Claude
handle: claude
kind: agent
personality: principal-engineer
writing_style: concise-quantified
talents:
  - engineering
```

### 4. Configure Your Repo

```bash
mkdir -p .punt-labs
cat > .punt-labs/ethos.yaml <<'EOF'
agent: claude
EOF
```

### 5. Start a Session

Start Claude Code. Ethos hooks fire automatically:

- **SessionStart** — loads your agent's personality, writing style,
  team context, and git working state
- **PreCompact** — re-injects identity before context compression so
  your agent doesn't lose its personality in long conversations
- **SubagentStart** — gives each sub-agent its own identity when spawned
- **PostToolUse** — logs every tool invocation to a per-session audit trail

Your agent now knows who it is. Every session.

## How It Works in Practice

Here's a real mission executed on 2026-04-10. Total time: **12 minutes
55 seconds** from claim to merge.

### The task

Eliminate a TOCTOU (time-of-check/time-of-use) bug in the verifier hash
check — two file reads that should be one.

### Create the mission contract

```yaml
leader: claude
worker: bwk                            # Go specialist
write_set:
  - internal/hook/subagent_start.go    # only this file may be modified
success_criteria:
  - make check passes
  - exactly one os.ReadFile per contract
  - same bytes for hash check and rendering
evaluator:
  handle: djb                          # security engineer reviews
budget:
  rounds: 2
```

### The worker delivers

bwk implemented the fix in **2 minutes 10 seconds** and submitted a
typed result: verdict `pass`, confidence `0.95`, 1 file changed
(+50/-23 lines), 4 evidence items.

### The leader reflects

Convergence: yes. All success criteria met on the first round.
Recommendation: continue to code review and close.

### Review, merge, close

The local code reviewer found 1 finding (maintenance comment needed).
Bugbot found 1 finding (same class — accepted tradeoff). Both resolved.
PR merged. Mission closed.

### The audit trail

```text
2026-04-10 07:19 PDT  create   by claude  worker=bwk evaluator=djb
2026-04-10 07:22 PDT  result   by bwk     round=1 verdict=pass
2026-04-10 07:23 PDT  reflect  by claude  round=1 rec=continue
2026-04-10 07:31 PDT  close    by claude  status=closed verdict=pass
```

4 events. 1 round. No rework. Full walkthrough with every command and
output: [docs/example/](docs/example/).

### Where the time went

| Phase | Duration | % |
|-------|----------|---|
| Claim + mission contract | 42s | 5% |
| Worker implementation | 2m10s | 17% |
| Verify + result + reflection | 1m02s | 8% |
| Code review (local) | 1m25s | 11% |
| CI + remote review + merge | 7m36s | 59% |

Coding was 17% of wall time. Review was 70%. That's the right
distribution — the review pipeline is the bottleneck, not the
implementation.

## Teams and Roles

For teams, ethos adds roles and collaboration graphs. Each agent gets
tool restrictions, responsibilities, and anti-responsibilities derived
from the team structure.

```yaml
# teams/engineering.yaml
name: engineering
members:
  - identity: claude
    role: coo
  - identity: bwk
    role: go-specialist
  - identity: djb
    role: security-engineer
collaborations:
  - from: go-specialist
    to: coo
    type: reports_to
```

```yaml
# roles/go-specialist.yaml
name: go-specialist
tools: [Read, Write, Edit, Bash, Grep, Glob]
responsibilities:
  - Go package implementation with tests
  - code review for Go projects
safety_constraints:
  - tool: Bash
    message: "Never run destructive commands (rm -rf, git push --force)"
```

At session start, ethos generates `.claude/agents/<handle>.md` for each
agent — complete with tools, personality, anti-responsibilities ("don't
make architectural decisions — the COO handles that"), and quality-gate
hooks. See the [Team Setup Guide](docs/team-setup.md).

## Status

| Phase | Status | What |
|-------|--------|------|
| 1 — Batteries Included | SHIPPED | Starter talents, roles, persona animation, agent generation |
| 2 — Production Agents | SHIPPED | Anti-responsibilities, role hooks, structured output, mission skill |
| 3 — Workflow Primitives | SHIPPED | Mission contracts, write-set admission, frozen evaluators, bounded rounds, verifier isolation, result artifacts, event log |
| 4 — Operational Excellence | SHIPPED | SessionStart working context, role safety constraints, session audit logging |
| 5 — Ecosystem | PLANNED | Starter team templates, cross-tool integration |

20.1 KLOC production Go. 26.8 KLOC tests (1.33:1 ratio).
Go Report Card: A+.

## Identity Schema

One schema for humans and agents. Only `kind` differs.

```yaml
name: Mal Reynolds
handle: mal
kind: human                       # or "agent"
email: mal@example.com             # Beadle (email) binding
github: mal                        # Biff (messaging) binding
agent: .claude/agents/mal.md       # Claude Code agent definition
writing_style: concise-quantified  # slug → writing-styles/<slug>.md
personality: principal-engineer    # slug → personalities/<slug>.md
talents:                            # slugs → talents/<slug>.md
  - engineering
  - product-strategy
```

Personalities define how the agent thinks. Writing styles define how it
communicates. Talents define domain expertise. Roles define boundaries.
The identity binds them all to one handle.

## Mission Contract Schema

```yaml
leader: claude
worker: bwk
evaluator:
  handle: djb
  # hash and pinned_at are set by ethos at creation
inputs:
  bead: ethos-db7                  # links to your tracking system
  files: [internal/hook/subagent_start.go]  # read context
write_set:                          # write permission envelope
  - internal/hook/subagent_start.go
success_criteria:
  - make check passes
  - exactly one os.ReadFile per contract
budget:
  rounds: 2
  reflection_after_each: true
```

The lifecycle: **create → result → reflect → advance → close**. Each
step is an append-only event in the mission log. The store refuses
operations that violate the contract: overlapping write-sets, self-review,
close without a result, advance without reflection.

**Missions are optional** — identity works standalone. Start with identity,
add missions when you need structured delegation.

**The contract is a spectrum.** An implementation mission has a tight
write-set and specific criteria ("one ReadFile, make check passes"). A
design mission has a broad write-set and open-ended criteria ("evaluate
three approaches, recommend one with tradeoffs"). The agent's expertise
is always present; the contract determines how much latitude they have.

**Archetypes and pipelines.** Missions declare a `type` field that maps
to an archetype (design, implement, test, review, inbox, task, report).
Each archetype carries budget defaults, write-set constraints, and
required fields. Pipelines chain missions into sprint templates -- quick
(2 stages), standard (4 stages), and full (6 stages) -- so multi-phase
work follows a repeatable structure.

## Commands

### Core

| Command | What it does |
|---------|-------------|
| `ethos whoami` | Show your resolved identity |
| `ethos create` | Create a new identity (interactive) |
| `ethos doctor` | Check installation health |
| `ethos seed` | Deploy starter roles, talents, and skills |

### Mission

| Command | What it does |
|---------|-------------|
| `ethos mission create --file <path>` | Create a mission contract |
| `ethos mission show <id>` | Show contract, results, reflections |
| `ethos mission list [--status open]` | List missions |
| `ethos mission result <id> --file <path> [--verify]` | Submit worker result |
| `ethos mission reflect <id> --file <path>` | Submit leader reflection |
| `ethos mission advance <id>` | Advance to next round |
| `ethos mission close <id>` | Close mission (requires result) |
| `ethos mission log <id>` | Read append-only event log |
| `ethos mission pipeline list` | List available sprint templates |
| `ethos mission pipeline show <name>` | Show pipeline stages and defaults |
| `ethos mission lint <contract.yaml>` | Advisory pre-delegation linter |

### Identity and Attributes

| Command | What it does |
|---------|-------------|
| `ethos identity list` | List all identities |
| `ethos identity get <handle>` | Show identity with resolved content |
| `ethos talent create/list/show/add/remove` | Manage talents |
| `ethos personality create/list/show/set` | Manage personalities |
| `ethos writing-style create/list/show/set` | Manage writing styles |

### Session, Team, Role, Extensions

| Command | What it does |
|---------|-------------|
| `ethos session` | Show current session roster |
| `ethos session iam <persona>` | Declare your persona |
| `ethos team list/show` | Query teams |
| `ethos role list/show` | Query roles |
| `ethos ext set/get/del/list` | Manage tool-scoped extensions |

All commands accept `--json` for machine-readable output.

## MCP Tools

When running as a Claude Code plugin, ethos registers 10 MCP tools with
method dispatch. Each has a corresponding `/ethos:*` slash command.

| Tool | Methods |
|------|---------|
| `identity` | whoami, list, get, create |
| `session` | roster, iam, join, leave |
| `talent` | create, list, show, delete, add, remove |
| `personality` | create, list, show, delete, set |
| `writing_style` | create, list, show, delete, set |
| `ext` | get, set, del, list |
| `team` | list, show, create, delete, add_member, remove_member, add_collab, for_repo |
| `role` | list, show, create, delete |
| `mission` | create, show, list, close, result, results, reflect, reflections, advance, log |
| `doctor` | *(standalone)* |

## Storage

Two layers: **repo-local** (`.punt-labs/ethos/`, git-tracked) overrides
**user-global** (`~/.punt-labs/ethos/`, personal).

| What | Repo-local | Global |
|------|------------|--------|
| Identities | `.punt-labs/ethos/identities/` | `~/.punt-labs/ethos/identities/` |
| Personalities | `.punt-labs/ethos/personalities/` | `~/.punt-labs/ethos/personalities/` |
| Writing styles | `.punt-labs/ethos/writing-styles/` | `~/.punt-labs/ethos/writing-styles/` |
| Talents | `.punt-labs/ethos/talents/` | `~/.punt-labs/ethos/talents/` |
| Roles | `.punt-labs/ethos/roles/` | `~/.punt-labs/ethos/roles/` |
| Teams | `.punt-labs/ethos/teams/` | `~/.punt-labs/ethos/teams/` |
| Missions | — | `~/.punt-labs/ethos/missions/` |
| Sessions | — | `~/.punt-labs/ethos/sessions/` |
| Extensions | — | `~/.punt-labs/ethos/identities/<handle>.ext/` |

## Integration

Tools integrate with ethos at whatever coupling level fits:

| Pattern | How | Dependency |
|---------|-----|------------|
| **Filesystem** | Read YAML at known paths | None |
| **CLI** | `ethos whoami --json`, `ethos identity get <handle> --json` | Binary |
| **MCP** | Connect to `ethos serve` during a session | Binary |
| **Channel binding** | Sibling tool reads identity and configures itself per persona | Binary + tool |

**Extensions** let any tool attach config without schema changes:

```bash
ethos ext set claude vox default_voice george
ethos ext set claude beadle gpg_key_id 3AA5C34371567BD2
```

Stored in `~/.punt-labs/ethos/identities/claude.ext/vox.yaml`. Ethos
assembles the merged view but never interprets extension contents.

## Documentation

- [Live Example Walkthrough](docs/example/) — real mission, real outputs, real timing
- [Team Setup Guide](docs/team-setup.md) — creating and structuring a team
- [Agent Guide](AGENTS.md) — CLI, MCP tools, hooks, extending identities
- [Persona Animation](docs/persona-animation.md) — how hooks inject identity
- [Agent Definitions](docs/agent-definitions.md) — generated agent files
- [Agent Teams](docs/agent-teams.md) — multi-agent coordination
- [Workflow](docs/workflow.md) — development lifecycle
- [Architecture](docs/architecture.tex) — system design
- [Design Decisions](DESIGN.md) — ADR archive (DES-001 through DES-040)
- [Roadmap](docs/ETHOS-ROADMAP.md) — phase-by-phase plan
- [Changelog](CHANGELOG.md)

## Development

```bash
make check    # All quality gates: vet + staticcheck + markdownlint + shellcheck + tests
make build    # Build binary
make install  # Build and install to ~/.local/bin
make dev      # Install and symlink plugin cache for development
make undev    # Restore plugin cache from backup
make format   # Auto-format code
make dist     # Cross-compile for all platforms
make help     # List all targets
```

## License

MIT
