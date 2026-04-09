# ethos

> AI agents as first-class engineering citizens.

[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/punt-labs/ethos)](https://github.com/punt-labs/ethos/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/punt-labs/ethos)](https://goreportcard.com/report/github.com/punt-labs/ethos)
[![Working Backwards](https://img.shields.io/badge/Working_Backwards-hypothesis-lightgrey)](./prfaq.pdf)

Ethos is a runtime for AI agents that work alongside humans, on three
pillars. **Identity** binds name, persona, role, and channel bindings
into one YAML file per participant — same schema for humans and agents.
**Workflow** turns delegation from free-form prose into typed mission
contracts with write-set admission, frozen evaluators, bounded rounds,
structured result artifacts, and an append-only audit trail.
**Integration** lets any tool — Claude Code, Beadle, Biff, Vox — read
identity state and configure itself, so the same agent shows up
correctly in code reviews, email, chat, and voice.

**Platforms:** macOS and Linux (amd64, arm64)

## Status at a Glance

The single source of truth for what is live and what is planned. Prose
elsewhere is written in the present tense; this table is where status
lives.

| Subsystem | Phase | Status | ADR / Bead |
|-----------|-------|--------|------------|
| Identity, sessions, persona animation, teams, roles, starter content | 1 | SHIPPED | v2.7.0 |
| Mission contract | 3.1 | SHIPPED | DES-031, `ethos-07m.5` |
| Write-set admission | 3.2 | SHIPPED | DES-032, `ethos-07m.6` |
| Frozen evaluator | 3.3 | SHIPPED | DES-033, `ethos-07m.7` |
| Bounded rounds with reflection | 3.4 | SHIPPED | DES-034, `ethos-07m.8` |
| Verifier isolation | 3.5 | SHIPPED | DES-035, `ethos-07m.9` |
| Result artifacts and close gate | 3.6 | SHIPPED | DES-036, `ethos-07m.10` |
| Event log reader API | 3.7 | SHIPPED | DES-037, `ethos-07m.11` |
| Anti-responsibility generation, role-based hooks, structured output, baseline-ops injection | 2.1–2.4 | PLANNED | `ethos-9ai.1`–`.4` |
| Mission skill (`/mission`) | 2.5–2.6 | PLANNED | `ethos-9ai.5` |
| SessionStart working context, role-based pre-tool safety, session audit log | 4.1–4.3 | PLANNED | `ethos-gcq.1`–`.3` |
| ethos + biff: identity-aware messaging | Integration | PLANNED | `ethos-wb4` |
| ethos + beadle: identity-aware email | Integration | PLANNED | `ethos-g2f` |

Phase 1 and Phase 3 ship today. The four architecture rules — roles as
interfaces, centralized understanding with decentralized execution,
hooks as enforcement, no ambient context inheritance — are
runtime-enforced for the first time in the project's history.

## Quick Start

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/590ae88/install.sh | sh
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
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/590ae88/install.sh -o install.sh
shasum -a 256 install.sh
cat install.sh
sh install.sh
```

</details>

The installer seeds starter content automatically: roles and talents to
`~/.punt-labs/ethos/`, and the baseline-ops skill to `~/.claude/skills/`.
To re-seed or update after install: `ethos seed` (or `ethos seed --force`
to overwrite customizations).

## What It Looks Like

A complete delegation. The leader writes a contract; ethos pins the
evaluator and assigns an ID. The worker submits a typed result. The
leader closes the mission. Every state transition lands in the
append-only event log.

```text
$ cat > /tmp/contract.yaml <<'EOF'
leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-07m.11
write_set:
  - internal/mission/log.go
  - internal/mission/log_test.go
success_criteria:
  - LoadEvents reads JSONL log
  - one corrupt line does not erase the tail
budget:
  rounds: 3
  reflection_after_each: true
EOF

$ ethos mission create --file /tmp/contract.yaml

$ ethos mission list
MISSION            STATUS  LEADER  WORKER  EVALUATOR  CREATED
m-2026-04-08-006   open    claude  bwk     djb        Wed Apr  8 11:42

$ ethos mission show m-2026-04-08-006
Mission:    m-2026-04-08-006
Status:     open
Leader:     claude
Worker:     bwk
Evaluator:  djb (pinned Wed Apr  8 11:42)
Hash:       sha256:f3c1...
Budget:     3 round(s), reflection_after_each=true
Round:      1 of 3
```

The worker does its work, then submits a typed result. The mission
cannot close until a result for the current round exists.

```text
$ cat > /tmp/result.yaml <<'EOF'
mission: m-2026-04-08-006
round: 1
author: bwk
verdict: pass
confidence: 0.95
files_changed:
  - path: internal/mission/log.go
    added: 200
    removed: 0
evidence:
  - name: go test ./internal/mission/... -race
    status: pass
EOF

$ ethos mission result m-2026-04-08-006 --file /tmp/result.yaml

$ cat > /tmp/reflection.yaml <<'EOF'
round: 1
author: claude
converging: true
signals:
  - tests passing
  - no new lint findings
recommendation: continue
EOF

$ ethos mission reflect m-2026-04-08-006 --file /tmp/reflection.yaml
$ ethos mission close m-2026-04-08-006
```

After the fact, the leader reads the audit trail. The event log is
append-only; one corrupt line is surfaced as a warning, not a fatal
error.

```text
$ ethos mission log m-2026-04-08-006
Events:
  - Wed Apr  8 11:42  create  by claude
  - Wed Apr  8 12:18  result  by bwk     verdict=pass, round=1
  - Wed Apr  8 12:19  reflect by claude  recommendation=continue, round=1
  - Wed Apr  8 12:20  close   by claude  status=closed, round=1
```

The same primitives are reachable from MCP via the `mission` tool, so a
running Claude Code session drives a mission end-to-end without shelling
out. Filter the log with `--event create,close` or `--since
2026-04-08T00:00:00Z`; pass `--json` for the wrapped
`{"events": [...], "warnings": [...]}` payload.

## Three Pillars

### Pillar 1: Identity — who the agent is

One YAML file per persona, same schema for humans and agents. An
identity binds three layers of context onto one durable anchor:

- **Persona** — personality, writing style, talents. Reusable `.md`
  files referenced by slug. Defines judgment and voice.
- **Role** — tools, responsibilities, model preference. Defines what
  the agent does. Anti-responsibilities — the things the agent should
  push upward — are derived from the team graph's `reports_to` edges,
  not stored on the role itself.
- **Channel bindings** — email (Beadle), GitHub (Biff), voice (Vox
  extension), Claude Code agent definition. Defines where the agent
  shows up.

Resolution is layered: repo-local overrides user-global. Ethos resolves
the caller automatically from iam, git, or OS user. When a subagent
spawns whose handle matches an identity, the persona attaches
automatically.

**Pillar 1 features:**

- **Same schema for humans and agents**, `kind: human` or `kind: agent`
- **Composable attributes** — talents, personalities, and writing styles
  as reusable `.md` files referenced by slug
- **Layered resolution** — repo-local overrides global; resolved from
  iam declaration, git config, or OS user (DES-011, DES-018)
- **Channel bindings** — email (Beadle), GitHub (Biff), Claude Code
  agent definition; voice config in extensions (`ext/vox`)
- **Extensible** — any tool attaches namespaced attributes via
  `<handle>.ext/<tool>.yaml`
- **Session roster** — all participants (human + agents) with
  parent-child tree
- **Persona auto-matching** — subagents get personas when the handle
  matches the agent type
- **Persona animation** — SessionStart, PreCompact, and SubagentStart
  hooks inject personality, writing style, and talent content
- **Agent file generation** — SessionStart generates
  `.claude/agents/<handle>.md` from identity, personality, writing-style,
  and role data
- **Extension session context** — any tool injects per-persona context
  via extension YAML; zero ethos-side code per consumer (DES-022)
- **Starter content** — 10 talents, 6 role archetypes, and the
  baseline-ops skill ship in the box

### Pillar 2: Workflow — how the agent works

The mission primitive (DES-031 through DES-037) turns delegation into a
typed runtime contract. A mission declares a write-set (the file
allowlist the worker may touch), a frozen evaluator (sha256-pinned at
launch), a round budget (bounded, with mandatory reflection between
rounds), typed result artifacts (schema-validated, append-only), and an
append-only JSONL event log.

The store refuses operations that violate the contract: two missions
cannot claim overlapping files, the verifier cannot share a role with
the worker, the mission cannot close without a structured result for
the current round. The CLI and the MCP `mission` tool share the same
admission gates — no surface bypasses them.

**Pillar 2 features:**

- **Mission contract** (DES-031) — typed YAML with leader, worker,
  evaluator, write_set, success_criteria, and budget. Strict
  unknown-field-rejecting decode at every read path.
- **Write-set admission** (DES-032) — segment-prefix conflict scan on
  create refuses overlap with any open mission, naming the blocker.
- **Frozen evaluator** (DES-033) — sha256 of the evaluator's persona and
  role bindings is pinned at launch. SubagentStart refuses verifier
  spawns if the hash drifts, with a per-section breakdown of which
  file changed.
- **Bounded rounds with reflection** (DES-034) — round budget is a
  hard cap. The operator must submit a structured reflection before
  advancing; a `stop` or `escalate` recommendation blocks advance.
- **Verifier isolation** (DES-035) — SubagentStart replaces the normal
  persona injection for verifier spawns with the mission contract and
  a file allowlist; the verifier cannot read the worker's scratch state.
- **Result artifacts and close gate** (DES-036) — every terminal
  transition requires a valid result for the current round. Verdict is
  `pass | fail | escalate`; confidence in `[0.0, 1.0]`; every
  `files_changed` path must live inside the contract's write_set.
- **Append-only event log reader** (DES-037) — `ethos mission log <id>`
  and the MCP `mission log` method read the JSONL audit trail every
  Phase 3.1+ writer appends to. One corrupt line surfaces as a warning,
  not a fatal error. Filters: `--event` (type list) and `--since`
  (RFC3339), AND-composed.

### Pillar 3: Integration — how the agent participates

Ethos publishes identity state through three surfaces — filesystem, CLI,
and MCP — so any tool reads it without taking a Go dependency. The
fourth integration pattern, identity-aware channel binding, lets a
sibling tool configure itself per persona: Beadle sends mail as
`claude@punt-labs.com` with the right GPG key, Biff routes messages to
the right session, Vox speaks with the right voice — all from the same
identity record.

Operational hooks (Phase 4) inject working context at session start (git
branch, uncommitted changes, recent issues), enforce role-based pre-tool
safety constraints (a reviewer cannot Write, a researcher cannot run
destructive Bash), and audit every tool invocation to a session log.

**Pillar 3 features:**

- **Four integration patterns** — filesystem (zero dependency), CLI
  (shell/hooks), MCP server (10 tools), identity-aware channel binding
  (Beadle, Biff, Vox)
- **MCP server** — covers identity, attributes, extensions, sessions,
  teams, roles, and missions
- **Cross-tool identity** — an agent impersonating the COO sends email
  from `claude@punt-labs.com` with the right GPG key, signs commits as
  `Claude Agento`, and speaks with the configured voice
- **Hooks fire reliably across platforms** — DES-029 fixed the Linux
  stdin hang via shell-level forwarding, so SessionStart, PreCompact,
  SubagentStart, SubagentStop, and SessionEnd all work on macOS and
  Linux

## Schema

### Identity contract

```yaml
name: Mal Reynolds
handle: mal
kind: human                       # or "agent"
email: mal@serenity.ship           # Beadle binding
github: mal                        # Biff binding
agent: .claude/agents/mal.md       # Claude Code agent binding
writing_style: concise-quantified  # slug → writing-styles/concise-quantified.md
personality: principal-engineer    # slug → personalities/principal-engineer.md
talents:                            # slugs → talents/<slug>.md
  - go-engineering
  - product-strategy
```

Attributes are slugs that reference `.md` files in the attribute
directories. `ethos identity get` resolves them to content by default.
Multiple identities share the same attribute files.

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

When a `code-reviewer` subagent spawns, ethos auto-matches it by handle
(case-sensitive, lowercase, `[a-z0-9-]`) and the agent inherits the
personality, talents, and channel bindings defined here.

### Mission contract

```yaml
mission_id: m-2026-04-08-006        # server-controlled
status: open                         # server-controlled
leader: claude
worker: bwk
evaluator:
  handle: djb
  pinned_at: 2026-04-08T18:42:01Z   # server-controlled
  hash: sha256:f3c1...               # server-controlled (DES-033)
inputs:
  bead: ethos-07m.11
write_set:
  - internal/mission/log.go
  - internal/mission/log_test.go
success_criteria:
  - LoadEvents reads JSONL log
  - one corrupt line does not erase the tail
budget:
  rounds: 3
  reflection_after_each: true
current_round: 1                     # server-controlled
created_at: 2026-04-08T18:42:01Z    # server-controlled
```

Server-controlled fields are overwritten on every create. The strict
YAML decoder rejects unknown fields, multi-document input, and trailing
content on both read and write — the on-disk trust boundary is symmetric.

## Commands

### Identity

| Command | What it does |
|---------|-------------|
| `ethos whoami [--json]` | Show the caller's identity (resolved from iam/git/OS) |
| `ethos identity whoami` | Same as `ethos whoami` |
| `ethos identity list [--json]` | List all identities |
| `ethos identity get <handle> [--json]` | Show identity with resolved attribute content |
| `ethos identity get <handle> --reference` | Show identity with attribute slugs only |
| `ethos identity create` | Create a new identity (interactive wizard) |
| `ethos identity create -f <path>` | Create from a YAML file |

### Attributes

| Command | What it does |
|---------|-------------|
| `ethos talent create <slug>` | Create a talent (opens `$EDITOR` or `--file`) |
| `ethos talent list` | List all talents |
| `ethos talent show <slug>` | Show talent content |
| `ethos talent delete <slug>` | Delete a talent |
| `ethos talent add <handle> <slug>` | Add talent to an identity |
| `ethos talent remove <handle> <slug>` | Remove talent from an identity |
| `ethos personality create <slug>` | Create a personality |
| `ethos personality list` | List all personalities |
| `ethos personality show <slug>` | Show personality content |
| `ethos personality delete <slug>` | Delete a personality |
| `ethos personality set <handle> <slug>` | Set personality on an identity |
| `ethos writing-style create <slug>` | Create a writing style |
| `ethos writing-style list` | List all writing styles |
| `ethos writing-style show <slug>` | Show writing style content |
| `ethos writing-style delete <slug>` | Delete a writing style |
| `ethos writing-style set <handle> <slug>` | Set writing style on an identity |

### Mission

| Command | What it does |
|---------|-------------|
| `ethos mission create --file <path>` | Create a mission contract from YAML (strict decode) |
| `ethos mission show <id>` | Show contract, reflections, and results for one mission |
| `ethos mission list [--status open\|closed\|failed\|escalated\|all]` | List missions filtered by status |
| `ethos mission close <id> [--status closed\|failed\|escalated]` | Close a mission (gated on a valid result for the current round) |
| `ethos mission result <id> --file <path>` | Submit a structured result for the current round |
| `ethos mission results <id>` | Read the round-by-round result log |
| `ethos mission reflect <id> --file <path>` | Submit a structured reflection for the current round |
| `ethos mission reflections <id>` | Read the round-by-round reflection log |
| `ethos mission advance <id>` | Bump `current_round` (gated on reflection + recommendation + budget) |
| `ethos mission log <id> [--event <list>] [--since <ts>]` | Read the append-only JSONL event log |

All mission subcommands accept `--json`. IDs accept any unambiguous prefix.

### Session

| Command | What it does |
|---------|-------------|
| `ethos session list` | List all sessions (short IDs, REPO column, human-readable dates) |
| `ethos session show [id]` | Show session details — participants, roles, repo, host, joined times |
| `ethos session iam <persona> [--session <id>]` | Declare persona in current or specified session (auto-detected inside Claude Code) |
| `ethos session join --agent-id <id>` | Add a participant to the session |
| `ethos session leave --agent-id <id>` | Remove a participant from the session |
| `ethos session purge` | Clean up stale sessions and PID files |

### Extensions

| Command | What it does |
|---------|-------------|
| `ethos ext set <handle> <ns> <key> <value>` | Write an extension key |
| `ethos ext get <handle> <ns> [key]` | Read extension key(s) |
| `ethos ext del <handle> <ns> [key]` | Delete key or namespace |
| `ethos ext list <handle>` | List extension namespaces |

### Admin

| Command | What it does |
|---------|-------------|
| `ethos version [--json]` | Print version |
| `ethos doctor [--json]` | Check installation health |
| `ethos serve` | Start MCP server (stdio) |
| `ethos completion <bash\|zsh\|fish>` | Generate shell completion script |
| `ethos seed [--force]` | Deploy starter roles, talents, and skills to global directories |
| `ethos uninstall` | Remove plugin (`--purge` to remove binary + data) |

`--json` is a global flag — valid before or after the subcommand.

## MCP Tools

When running as a Claude Code plugin, ethos registers an MCP server with
10 tools. Tools with multiple verbs use a `method` parameter for dispatch.

All tools have corresponding slash commands under `/ethos:*`.

| Tool | Methods | Slash command |
|------|---------|---------------|
| `identity` | whoami, list, get, create | `/ethos:identity` |
| `talent` | create, list, show, delete, add, remove | `/ethos:talent` |
| `personality` | create, list, show, delete, set | `/ethos:personality` |
| `writing_style` | create, list, show, delete, set | `/ethos:writing-style` |
| `session` | roster, iam, join, leave | `/ethos:session` |
| `ext` | get, set, del, list | `/ethos:ext` |
| `team` | list, show, create, delete, add_member, remove_member, add_collab, for_repo | `/ethos:team` |
| `role` | list, show, create, delete | `/ethos:role` |
| `mission` | create, show, list, close, result, results, reflect, reflections, advance, log | `/ethos:mission` |
| `doctor` | *(standalone)* | — |

## Setup

Ethos resolves repo-specific config from `.punt-labs/ethos.yaml` in the
repo root:

```yaml
agent: claude       # primary agent identity handle
team: engineering   # team definition for hook context
```

Team identity data (identities, personalities, writing styles, talents,
roles, teams) lives in `.punt-labs/ethos/`. You can populate this
directory locally or share it across repos as a git submodule.
Running `ethos seed` populates global defaults (6 starter roles, 10
talents, and the baseline-ops skill). Teams override or extend with
repo-local content. See the
[Team Setup Guide](docs/team-setup.md) for how to create and structure
a team from scratch.

## Persona Animation

The agent definition (`.claude/agents/<handle>.md`) defines what the agent does. The ethos identity defines who the agent is. The mission contract defines what the agent must do right now. Hooks connect all three automatically: SessionStart injects the full persona for the primary agent, PreCompact re-injects a condensed persona before context compression, and SubagentStart injects the matched persona when a subagent spawns. For an open mission whose evaluator handle matches the spawning subagent, SubagentStart replaces the normal injection with the Phase 3.5 isolation block — the mission contract byte-for-byte plus a file allowlist derived from the write-set. See [AGENTS.md](AGENTS.md#persona-animation) and [docs/persona-animation.md](docs/persona-animation.md).

## Storage

Identities, attributes, and missions resolve in two layers: **repo-local**
(`.punt-labs/ethos/`) overrides **user-global** (`~/.punt-labs/ethos/`).
Repo-local files are tracked in git; global files are personal. Mission
state is global-only — missions are session-scoped and not meant to
travel across hosts.

| Scope | Path | Tracked? |
|-------|------|----------|
| Repo identities | `.punt-labs/ethos/identities/<handle>.yaml` | Yes |
| Repo talents | `.punt-labs/ethos/talents/<slug>.md` | Yes |
| Repo personalities | `.punt-labs/ethos/personalities/<slug>.md` | Yes |
| Repo writing styles | `.punt-labs/ethos/writing-styles/<slug>.md` | Yes |
| Repo roles | `.punt-labs/ethos/roles/<name>.yaml` | Yes |
| Repo teams | `.punt-labs/ethos/teams/<name>.yaml` | Yes |
| Repo config | `.punt-labs/ethos.yaml` | Yes |
| Repo agents | `.punt-labs/ethos/agents/<name>.md` | Yes |
| Global identities | `~/.punt-labs/ethos/identities/<handle>.yaml` | No |
| Global talents | `~/.punt-labs/ethos/talents/<slug>.md` | No |
| Global personalities | `~/.punt-labs/ethos/personalities/<slug>.md` | No |
| Global writing styles | `~/.punt-labs/ethos/writing-styles/<slug>.md` | No |
| Global roles | `~/.punt-labs/ethos/roles/<name>.yaml` | No |
| Global teams | `~/.punt-labs/ethos/teams/<name>.yaml` | No |
| Extensions | `~/.punt-labs/ethos/identities/<handle>.ext/<tool>.yaml` | No |
| Sessions | `~/.punt-labs/ethos/sessions/<session-id>.yaml` | No (ephemeral) |
| Mission contracts | `~/.punt-labs/ethos/missions/<id>.yaml` | No |
| Mission reflections | `~/.punt-labs/ethos/missions/<id>.reflections.yaml` | No |
| Mission results | `~/.punt-labs/ethos/missions/<id>.results.yaml` | No |
| Mission event log | `~/.punt-labs/ethos/missions/<id>.jsonl` | No (append-only) |

## Identity Resolution

Human and agent identities are resolved automatically — no manual
"set active" step required.

**Human resolution** (stops at first match):

1. `iam` declaration — explicit persona set via `ethos session iam`
2. `git config user.name` — matched against identity `github` field
3. `git config user.email` — matched against identity `email` field
4. `$USER` — matched against identity `handle` field

**Agent resolution** — per-repo `.punt-labs/ethos.yaml`:

```yaml
agent: claude
```

Tracked in git — the whole team shares the same agent configuration.
When the agent field is unset, the primary agent has no persona.

## Integration

Tools integrate with ethos at whatever coupling level fits. The first
three patterns describe how a consumer reads or writes ethos state. The
fourth — identity-aware channel binding — describes how the same
identity record drives configuration in a sibling tool.

| Pattern | How | Dependency |
|---------|-----|------------|
| **Filesystem** | Read YAML at `~/.punt-labs/ethos/identities/<handle>.yaml` | None |
| **CLI** | Call `ethos whoami --json` or `ethos identity get <handle> --json` from hooks/scripts | Binary installed |
| **MCP server** | Connect to `ethos serve` for identity, attributes, extensions, sessions, teams, roles, and missions | Binary installed |
| **Identity-aware channel binding** | Sibling tool reads ethos state and configures itself per persona — Beadle sends mail from the bound email with the right GPG key, Biff routes to the right session, Vox speaks with the right voice | Binary installed + sibling tool |

**Core identity fields** (owned by ethos): name, handle, kind, email,
github, agent, writing\_style, personality, talents.

**Attributes** (reusable `.md` files): talents, personalities, and writing
styles are plain markdown documents stored in dedicated directories. Any
identity can reference them by slug. Create with `ethos talent create`,
`ethos personality create`, or `ethos writing-style create`.

**Extensions** (owned by each tool): any tool can read/write namespaced
key-value pairs in `<handle>.ext/<tool>.yaml`. Vox stores voice config,
Beadle stores a GPG key, Biff stores routing preferences. Ethos assembles
the merged view but never interprets extension contents.

```bash
ethos ext set mal beadle gpg_key_id 3AA5C34371567BD2
ethos ext get mal beadle gpg_key_id
ethos identity get mal   # includes ext map with all tool namespaces
```

## Documentation

[Team Setup Guide](docs/team-setup.md) |
[Agent Definitions](docs/agent-definitions.md) |
[Mission Skill Design](docs/mission-skill-design.md) |
[Persona Animation](docs/persona-animation.md) |
[Agent Teams](docs/agent-teams.md) |
[Quarry Integration](docs/quarry-integration.md) |
[Workflow](docs/workflow.md) |
[Architecture](docs/architecture.tex) |
[Design Decisions](DESIGN.md) |
[Roadmap](docs/ETHOS-ROADMAP.md) |
[Agent Guide](AGENTS.md) |
[Changelog](CHANGELOG.md)

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
