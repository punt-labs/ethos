# ethos

> Persistent identity and structured delegation for human-agent teams.

[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/punt-labs/ethos)](https://github.com/punt-labs/ethos/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/punt-labs/ethos)](https://goreportcard.com/report/github.com/punt-labs/ethos)
[![Working Backwards](https://img.shields.io/badge/Working_Backwards-hypothesis-lightgrey)](./prfaq.pdf)

## What ethos is

Every Claude Code session starts anonymous. The agent doesn't know who
it is, who you are, or how your team works. When you delegate to a
sub-agent, the contract is prose. When something goes wrong, there's no
audit trail. Every tool you pair with Claude — messaging, voice, email —
reinvents the same identity fragment.

Ethos is a local runtime that fills three gaps:

- **Identity** — one schema for humans and agents. Personality, writing
  style, talents, role, email, voice config. Loads automatically at
  session start and survives context compaction.
- **Delegation** — typed mission contracts with file-level write-sets,
  frozen evaluators, bounded rounds, and an append-only audit log.
- **Integration** — any tool reads ethos through filesystem, CLI, or MCP.
  No dependency required. Extensions attach per-tool config without
  schema changes.

All state is local YAML — user-global under `~/.punt-labs/ethos/`,
with optional repo-local overrides under `.punt-labs/ethos/` for
team-shared identities and config. Starter teams ship as
**bundles** — self-contained directories activated per repo — so
new users can adopt a production-ready team in one command. No
server, no cloud, no telemetry.

**Platforms:** macOS, Linux (amd64, arm64).

## Quick start

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/cbc3064/install.sh | sh
ethos identity create
```

The installer places the `ethos` binary in `~/.local/bin`, seeds starter
roles and talents, and registers the Claude Code plugin.
`ethos identity create` prompts for your name, handle, personality,
writing style, and talents, then writes the identity to
`~/.punt-labs/ethos/identities/<handle>.yaml`.

Run `ethos identity create` a second time to create an agent identity
(set `kind: agent`). Then point your repo at that agent:

```bash
mkdir -p .punt-labs
cat > .punt-labs/ethos.yaml <<'EOF'
agent: claude
EOF
```

(Optional) Activate a starter team — `gstack` ships embedded and
deploys on `ethos seed`:

```bash
ethos team activate gstack
```

Start Claude Code. Ethos hooks load your agent's identity at session
start, re-inject it through compaction, and give every sub-agent its
own persona.

<details>
<summary>Manual install (Go required)</summary>

```bash
go install github.com/punt-labs/ethos/cmd/ethos@latest
mkdir -p ~/.punt-labs/ethos/identities
ethos doctor
```

</details>

## What it looks like

At session start, ethos injects identity from the resolved YAML:

```text
[ethos] Identity: claude (agent)
[ethos] Personality: principal-engineer
[ethos] Writing style: concise-quantified
[ethos] Talents: engineering, security
[ethos] Working context: branch feat/rewrite, 2 dirty files
```

When you delegate to a sub-agent, it loads a matched persona with tool
restrictions from its role:

```text
[ethos] Subagent bwk spawned: go-specialist
[ethos] Tools allowed: Read, Write, Edit, Bash, Grep, Glob
[ethos] Safety: Bash must not run destructive commands (rm -rf, git push --force)
```

For a real mission — issue ticket claim to merged PR in 12m55s, with
every command and output — see [docs/example/](docs/example/). The
walkthrough uses [beads](https://github.com/punt-labs/beads) as the
issue tracker, but ethos accepts any tracker ID (Linear, Jira, GitHub
issues, etc.) via the `inputs.ticket` contract field.

## Scale up as you need

Each layer works alone. Add the next when you want more structure.

| Layer | What you get | Guide |
|-------|--------------|-------|
| Identity | Consistent agent persona across sessions. Hooks fire automatically. | This page |
| Team | Roles with tool restrictions, team graph with `reports_to` edges, auto-generated `.claude/agents/` files with anti-responsibilities | [Team setup](docs/team-setup.md) |
| Missions | Typed delegation contracts with write-sets, bounded rounds, frozen evaluators, audit logs. Closing a mission auto-appends a summary to `.ethos/missions.jsonl` for commit-ready traceability. | [Archetypes and pipelines](docs/archetypes-and-pipelines.md) |

## How it integrates

Any tool reads ethos at the coupling level that fits:

| Pattern | How | Dependency |
|---------|-----|------------|
| Filesystem | Read YAML at `~/.punt-labs/ethos/` | None |
| CLI | `ethos whoami --json`, `ethos identity get <handle> --json` | Binary |
| MCP | `ethos serve` exposes 10 tools | Binary |

Three Punt Labs tools integrate today: Biff (team messaging), Vox
(voice), and Beadle (email). Each works without ethos and gains
identity context when present. Extensions let any tool attach per-tool
config under `~/.punt-labs/ethos/identities/<handle>.ext/<tool>.yaml`
without schema changes.

Integration guides: [filesystem](docs/integration/filesystem.md),
[CLI](docs/integration/cli.md), [MCP](docs/integration/mcp.md).

## Commands

Essentials below. Every command accepts `--json`. Full reference in
[AGENTS.md](AGENTS.md#commands).

| Command | What it does |
|---------|--------------|
| `ethos whoami` | Show your resolved identity |
| `ethos identity create` | Create a new identity (interactive) |
| `ethos doctor` | Check installation health |
| `ethos identity list` / `get <handle>` | Query identities |
| `ethos mission create --file <path>` | Create a mission contract |
| `ethos mission dispatch` | Create a mission from CLI flags (no YAML needed) |
| `ethos mission show <id>` | Show contract, results, reflections |
| `ethos mission pipeline list` / `show <name>` | Query pipeline templates |
| `ethos mission pipeline instantiate <name> --var key=value` | Create N missions from a pipeline |
| `ethos mission lint <contract.yaml>` | Advisory pre-delegation linter |

## How this is different

| Tool | What it does | Where ethos differs |
|------|--------------|---------------------|
| [SoulSpec](https://soulmd.dev) | Structured agent personas in `.md` files | Agent-only; no human identity, no teams, no delegation contracts, no CLI/MCP integration surface |
| [Mastra](https://mastra.ai) | Typed Zod schemas and pre-delegation hooks | No persistent identity, no write-set boundaries, no frozen evaluator |
| [CrewAI](https://www.crewai.com) | Role-based agent orchestration | Prose delegation, no typed contracts, no persistent identity, no reflection gates |
| [Claude Managed Agents](https://docs.anthropic.com/en/docs/claude-code/managed-agents) | Hosted stateful agent sessions | Vendor-specific; "persona" means deployment config, not rich identity; no human developer identity |

Ethos ingests SoulSpec and CLAUDE.md on the way in, exports to both
formats on the way out (lossy — structural enforcement drops because
markdown cannot represent it). Coexistence rather than competition.

## Status

v3.7.0 — all five planned phases shipped. 23+ KLOC production Go,
37+ KLOC tests, A+ Go Report Card. Identity, teams, mission contracts,
write-set admission, frozen evaluators, bounded rounds, audit logs,
archetypes, pipelines, automatic mission traceability, and mission
dispatch one-liner are in daily use by Punt Labs. The gstack starter
team (6 agents + 5 pipeline templates) ships as optional content
via the team submodule.

Gstack also ships as an embedded, activatable **team bundle**:
`ethos seed` deploys it to the global store and
`ethos team activate gstack` turns it on per repo. Existing users on
the legacy `punt-labs/team` submodule can move to the bundles layout
with `ethos team migrate` (dry-run by default).

Remaining work is adoption-driven: reducing mission ceremony,
customer validation interviews, cross-tool integration.
See the [roadmap](docs/ETHOS-ROADMAP.md).

## Documentation

[Live example](docs/example/) ·
[Team setup](docs/team-setup.md) ·
[Archetypes and pipelines](docs/archetypes-and-pipelines.md) ·
[Gstack starter team](docs/gstack-getting-started.md) ·
[Persona animation](docs/persona-animation.md) ·
[Agent definitions](docs/agent-definitions.md) ·
[Architecture](docs/architecture.tex) ·
[Agent guide (CLI, MCP, hooks)](AGENTS.md) ·
[Design decisions](DESIGN.md) ·
[Changelog](CHANGELOG.md) ·
[Roadmap](docs/ETHOS-ROADMAP.md)

## Development

```bash
make check    # All quality gates (vet, staticcheck, markdownlint, shellcheck, tests)
make build    # Build binary
make install  # Install to ~/.local/bin
make help     # List all targets
```

Contributors: see [CLAUDE.md](CLAUDE.md) for the development lifecycle.

## License

MIT
