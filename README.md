# ethos

> Identity, mission contracts, and per-tool-call audit for AI agent delegation.

[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/punt-labs/ethos/v4.svg)](https://pkg.go.dev/github.com/punt-labs/ethos/v4)
[![Working Backwards](https://img.shields.io/badge/Working_Backwards-validated-blue)](./prfaq.pdf)

Ethos runs typed mission contracts for AI agents — a write-set, a frozen
evaluator, bounded review rounds, and a per-tool-call audit trail.
Commit trailers link recorded mission/delegation associations; full prompts
and tool traces need not be shared with the code.
Missions dispatch against a persistent layer of identities and teams that
give each agent a role, tool restrictions, and a delegation graph. It ships
as a single Go binary with a Claude Code plugin, an MCP server, and a
filesystem layout other tools read directly. It runs locally — no server, no
telemetry, no cloud.

**Platforms:** macOS, Linux (amd64, arm64).

## Quick Start

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/8ebb77f5/install.sh | sh
~/.local/bin/ethos setup
```

The installer places the `ethos` binary in `~/.local/bin`, seeds
starter content into `~/.punt-labs/ethos/`, and — when `claude` and
`git` are available — registers the Claude Code plugin. `ethos setup`
walks a 60-second wizard that creates your CEO identity, a paired COO
agent (`claude`), and a specialist team. Once your shell rc adds
`~/.local/bin` to `PATH` (open a new terminal, or `source` the rc),
the bare `ethos` command works. Full walkthrough:
[Onboarding](docs/onboarding.md).

<details>
<summary>Install without the Claude Code plugin</summary>

For a non-Claude harness, or a Claude install where org policy blocks
plugins:

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/8ebb77f5/install.sh | sh -s -- --no-plugin
```

Where arguments cannot pass through the pipe, set `ETHOS_NO_PLUGIN=1`:

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/8ebb77f5/install.sh | ETHOS_NO_PLUGIN=1 sh
```

`--no-plugin` skips only the marketplace-register and plugin-install
steps; the binary, PATH setup, directories, seed content, per-repo
`ethos enable`, and health check all still run. Re-run without the flag
to add the plugin later.
</details>

<details>
<summary>Non-interactive setup from a YAML file</summary>

```yaml
name: Mal Reynolds
handle: mal
email: mal@serenity.ship   # optional; defaults to git config user.email
github: mal                # optional
writing_style: concise-quantified
```

Run `ethos setup --file config.yaml`. When `email` is omitted, setup
uses `git config user.email`; if that is also unset, setup fails with
a remedy rather than creating an identity nothing can resolve.
</details>

## Features

**Storage repair status:** DES-080 redaction shipped in v4.20.0. DES-079's
source/local boundary, shared decisions, publication and squash provenance
remain pending final entry-point acceptance. The
[storage and publication contract](docs/storage-layout.md) defines the required
behavior and maps each promise to implementation and outstanding acceptance.
An ADR's settled status is not a release claim.

| Feature | What it does |
|---------|-------------|
| Mission contracts | Typed delegation with write-sets, frozen evaluators, bounded rounds, success criteria |
| Audit trail | Per-tool-call log tagged with delegation ID + contract ID; PII-redacted paths |
| Git trailers | `Mission:`/`Delegation:` associations on locally hooked commits; DES-079 squash manifests and actual-commit verification pending |
| Traceability UI | Browse code with ethos blame; mission + delegation detail views |
| Preconditions | Gate tool calls on prior reads ("must read DESIGN.md before editing") |
| Expert identities | Personalities, writing styles, talents bound to named agents |
| Team structure | Roles with tool restrictions, reports-to graph, anti-responsibilities |
| Pipeline templates | Multi-stage mission workflows from 8 built-in templates (plus bundle-specific ones) |
| Lifecycle hooks | 7 events (SessionStart, PreToolUse, PostToolUse, SubagentStart, SubagentStop, PreCompact, SessionEnd) |
| Write-set enforcement | PreToolUse blocks unauthorized file modifications at runtime |
| Self-standing repos | `ethos vendor` snapshots a resolvable identity set; `resolution: repo-only` drops the global fallback |
| Symlink rejection | Uniform policy across all mission loaders and lock paths |
| Depth limits | Configurable ceiling on nested delegation chains |
| Query surface | `ethos find missions` with date/worker/status filters |
| Composable integration | CLI, MCP (12 tools), filesystem reads; works with biff, vox, beadle, quarry |

## What It Looks Like

### Traceability: Line of Code to Full History

For a single recorded association, local commit-msg hooks append
`Mission:`/`Delegation:` trailers. This example also assumes the full prompt
and audit detail are available locally; a fresh blame-only clone need not have
them. Under the pending DES-079 layout, resolve the canonical store root S first:

```text
$ git blame -L 42,42 internal/hook/generate_agents.go
abc1234 (bwk  2026-05-25  42) func projectFilePatterns(repoRoot string) string {

$ git log --format='%(trailers)' abc1234
Mission: m-2026-05-25-002
Delegation: d-2026-05-25-011

$ cat "$S/.punt-labs/local/ethos/trace/missions/m-2026-05-25-002/delegations/d-2026-05-25-011/prompt.md"
"Fix the hardcoded Go file-extension patterns. Detect project type
at generation time (go.mod → Go, pyproject.toml → Python)..."

$ ethos audit show --delegation d-2026-05-25-011 --format text
2026-05-25T12:49:23Z  Read   <repo>/internal/hook/generate_agents.go
2026-05-25T12:49:55Z  Edit   <repo>/internal/hook/generate_agents.go
2026-05-25T12:51:06Z  Bash   go test -run TestGenerateAgentFiles ...
2026-05-25T12:53:25Z  Bash   make check
2026-05-25T12:53:37Z  Bash   git commit ...
```

Illustrative — actual SHAs and IDs vary per repo.

DES-079 requires live logs and immutable archives under
`S/.punt-labs/local/ethos/` in **both** recording modes. Default `record: blame`
archives without querying or changing the index; `record: full` adds eligible
published copies, including sealed session audits, while retaining the local
copy. Eligibility is captured per immutable entry. Switching modes neither
promotes old private history nor removes published history. Invalid `record`
values fail visibly; omitted means `blame`. These are pending repair requirements,
superseding the historical tracked-by-default seal in DES-058.

For multi-mission/delegation squash merges, selected `mission publish`,
`provenance message` and `provenance verify` are pending command contracts.
The supported route supplies a generated body and pins the reviewed head to
GitHub's squash command, then fetches and verifies the actual merge commit.
Local hooks do not run remotely. Attribution must preserve ambiguity rather
than guess the first delegation. See the [merge example](docs/example/07-pr-and-merge.md).

`ethos ui` opens a localhost dashboard for code and mission detail. DES-079
requires it to distinguish unavailable local detail from non-submission or
unreadable data. Shared round decisions still govern advancement when full
reflection prose is unavailable here.

### Contract-Bound Delegation

```yaml
leader: claude
worker: bwk
evaluator:
  handle: djb
write_set:
  - internal/hook/generate_agents.go
  - internal/hook/generate_agents_test.go
success_criteria:
  - detect project type at generation time
  - tests cover Go, Python, and generic fallback
budget:
  rounds: 2
  reflection_after_each: true
```

When a verifier agent is spawned, the PreToolUse hook enforces the
write-set — an Edit to a file outside the contract is blocked before
it executes.

### Specialist Agents

```text
[ethos] Subagent bwk spawned: go-specialist
[ethos] Personality: kernighan (simplicity, clarity, generality)
[ethos] Tools: Read, Write, Edit, Bash, Grep, Glob
[ethos] Anti-responsibilities: strategic direction (that's the COO's job)
```

Each specialist has a personality that constrains and focuses the
model's output, a writing style, domain talents, and a role that
determines which tools they can use. Generated from identity +
role + team data as `.claude/agents/*.md`.

## Commands

Essentials below. Every command accepts `--json`. Full reference in
[Agent Guide](docs/agent-guide.md#commands).

| Command | What it does |
|---------|--------------|
| `ethos enable` / `disable` | Turn ethos on/off in this repo (see below) |
| `ethos setup` | Set up identities and team (60-second wizard) |
| `ethos whoami` | Show your resolved identity |
| `ethos identity schema` | Show the field reference for an entity (also `role schema`, `team schema`) |
| `ethos session start` / `end` | Open/close a session from any harness — `eval "$(ethos session start)"` |
| `ethos iam <persona>` | Declare your persona in the active session |
| `ethos doctor` | Check installation health |
| `ethos vendor [handle...]` | Snapshot a complete, self-standing identity set into this repo (see below) |
| `ethos mission create` / `dispatch` | Create a mission contract |
| `ethos mission claim` / `release` | Bind session to mission for Tier B dispatch |
| `ethos mission show <id>` | Show contract, results, reflections |
| `ethos audit show --delegation <id>` | Full tool-call trace for a delegation |
| `ethos audit seal [--dry-run]` | Seal pending lines into immutable chunks; DES-079 requires local archives by default (acceptance pending) |
| `ethos audit quarantine <chunk>` | Retire a corrupt sealed chunk and recover what the live file holds |
| `ethos session purge [--force] [--ack <id>]` | Clean up stale sessions; guard/acknowledge unsealed audit lines |
| `ethos find missions` | Query closed missions by date, worker, status |
| `ethos ui` | Open traceability dashboard in browser |

## Setup

Configuration beyond the Quick Start.

### Outside Claude Code (Codex, plain terminal)

Ethos is harness-neutral. Inside Claude Code, hooks open a session for
you. Anywhere else, open one explicitly at shell or harness init:

```bash
eval "$(ethos session start --persona bwk)"
```

Then `ethos whoami` reports the persona, `ethos session` shows the
roster, `ethos session end` tears it down. `session start` is idempotent
and exports `ETHOS_SESSION` (plus `ETHOS_AGENT_ID` when `--persona` is
supplied). Design + resolution rules:
[Harness-neutral sessions](docs/harness-sessions.md).

### Native OpenCode MCP identity

With `ethos serve --opencode-session-metadata`, OpenCode MCP calls can resolve
identity from request-local session metadata, without setting a shared server
identity. Default MCP behavior remains unchanged, including when metadata is
malformed. Metadata is routing context, not authentication.
Explicitly seat the OpenCode session
ID as an Ethos participant first; `whoami`, implicit roster lookup, and `iam`
then use that exact caller and fail if its binding is missing or invalid.
Repair a missing participant by joining the existing roster, not recreating it.
See the [identity pilot guide](docs/opencode-mcp-identity.md) for setup and
limitations. This does not implement automatic lifecycle, mission, or fork
integration.

### Self-Standing Repos: `vendor` and `resolution: repo-only`

Default resolution is repo → active bundle → global, and the global
fallback catches whatever the repo lacks — which is why a partially-
vendored repo does not know it is partial. `ethos vendor` computes the
identity closure (attributes, roles, teams, *and other members of those
teams* — the edge that pulls in people reachable no other way) and
writes a set that resolves on its own:

```bash
ethos vendor --team engineering --apply --prune
```

Then set `resolution: repo-only` in `.punt-labs/ethos.yaml` to drop
the global layer — missing references become hard errors naming each
one, instead of silently resolving from a home directory a CI runner
or fresh clone does not have. `ethos doctor` is the gate. DES-079 requires
repository overlays at `S/.punt-labs/local/ethos/identities/<handle>.ext/<namespace>.local.yaml`,
preserving local precedence and base-only writes. Global sibling overlays stay
unchanged. Vendor never copies overlays; they are not a vault and can reach model
context. Full walkthrough: [Team Setup](docs/team-setup.md).

### Git Worktrees

Ethos resolves the canonical store root **S** through Git's common directory,
separately from the invoking checkout **C**. Missions share S's record tree.
DES-079 additionally requires all repository live logs, retained trace, locks,
overlays, receipts, scratch and staging under `S/.punt-labs/local/ethos/`.
Generated agents, enablement deposits, host-file edits and publication/index
effects target C; publication copies are not another mutable mission authority.
Removing a linked C distinct from S must preserve prompts, reflections, chunks
and unsealed tails even without SessionEnd. Deleting S or its local data is not
covered. Override store resolution with validated `ETHOS_REPO_ROOT`.

### Enable / Disable

`install.sh` is machine scope. Per-repo enablement is `ethos enable`,
which deposits the vendored agent guide, writes the `enabled` marker,
adds the `@.punt-labs/ethos/CLAUDE.md` import to the repo `CLAUDE.md`,
and chains `ETHOS DES-058 SEAL` + `ETHOS DES-054 TRAILER` sections into
the `pre-commit` and `commit-msg` hooks. `ethos disable` reverses it
non-destructively — leaves the vendored guide, config, and audit data
on disk, dormant. Both idempotent. `enable` and `setup` stay separate.
State table, hook-chaining mechanics, `ethos doctor`'s marker vs
currency check distinction, and the sibling-worktree `--force` case:
[Enable / Disable](docs/enable-disable.md).

## What Ethos Does

Ethos records and bounds agent delegation along three axes.

**Control.** Typed mission contracts with file-level write-sets
enforced at runtime, frozen evaluators (hash-pinned so nobody swaps
the reviewer mid-mission), bounded review rounds, preconditions
that gate tool calls on prior reads, and delegation depth limits.
The agent can only do what the contract authorizes.

**Auditability.** Every delegation produces artifacts on disk —
contract, results, delegation records, the exact dispatch prompt and a
per-tool-call trail tagged with the delegation ID. DES-079 requires shared
compact round decisions and corrections, retained local detail, and explicit
complete blame publication. A blame-only clone must retain demonstrable
attribution after the supported squash route, without depending on another
machine's archive. Full export explicitly shares one mission's retained trace,
including its unsealed tail, but excludes session audits, agent artifacts and
transcripts. Publication and full-export acceptance remain pending.

**Performance.** Named specialist agents with encapsulated domain
expertise — a Go specialist grounded in Kernighan's principles, a
security reviewer built on Bernstein's methodology. Personalities,
writing styles, and talents are prompt-scaffolding that
constrains the model's output within a role's stated domain. Roles
restrict tool access. Teams define delegation topology. The
configuration is reusable, versioned, and revisable.

## How This Is Different

| Tool | What it does | Where ethos differs |
|------|--------------|---------------------|
| [SoulSpec](https://soulmd.dev) | Structured agent personas | Agent-only; no contracts, no audit trail, no team structure |
| [Mastra](https://mastra.ai) | Typed Zod schemas and pre-delegation hooks | No persistent identity, no write-set boundaries, no frozen evaluator |
| [CrewAI](https://www.crewai.com) | Role-based agent orchestration | Prose delegation; no typed contracts, no write-set enforcement, no traceability |
| [Claude Managed Agents](https://docs.anthropic.com/en/docs/claude-code/managed-agents) | Hosted stateful sessions | Vendor-specific; no forensic audit trail, no contract binding |

Ethos ingests SoulSpec on the way in (`ethos import --from soulspec`)
and exports on the way out (lossy — enforcement drops because
markdown cannot represent it).

## Documentation

| Guide | Audience |
|-------|----------|
| [Onboarding](docs/onboarding.md) | Install, setup, first delegation |
| [Team Setup](docs/team-setup.md) | Configuring roles, teams, bundles, vendor + repo-only |
| [Harness-Neutral Sessions](docs/harness-sessions.md) | Running ethos outside Claude Code (Codex, plain terminal) |
| [Enable / Disable](docs/enable-disable.md) | Per-repo enable/disable, hook chaining, `ethos doctor` |
| [Audited Delegation](docs/audited-delegation.md) | Tier A/B dispatch, claim/release, audit trail, git trailers |
| [Traceability Data Assets](docs/traceability-data-assets.md) | Every artifact ethos produces, organized by scope |
| [Storage and Publication Contract](docs/storage-layout.md) | DES-079 S/C ownership, decisions, mode switches, publication/export and pending acceptance; supersedes older path claims |
| [Traceability Use Cases](docs/traceability-use-cases.md) | 10 forensic/compliance scenarios with query paths |
| [Archetypes and Pipelines](docs/archetypes-and-pipelines.md) | Mission templates and multi-stage workflows |
| [Architecture](docs/architecture.tex) | System design (LaTeX) |
| [Agent Guide](docs/agent-guide.md) | CLI, MCP, hooks, storage layout |
| [OpenCode Setup](docs/opencode-setup.md) | Native development agents, models, tools, and verification |
| [Design Decisions](DESIGN.md) | ADRs with rationale and rejected alternatives |
| [Changelog](CHANGELOG.md) | Release history |
| [Roadmap](docs/ETHOS-ROADMAP.md) | What's shipped and what's next |

## Development

Project instructions live in [AGENTS.md](AGENTS.md). Native OpenCode setup and
the task/review workflow are documented in [OpenCode Setup](docs/opencode-setup.md)
and [OpenCode Workflow](docs/opencode-workflow.md). `make check-opencode` validates
the committed configuration offline; `make doctor-opencode` and
`make smoke-opencode` verify the live workstation and actual tool execution.

Run all quality gates (vet, staticcheck, shellcheck, markdownlint, validate-content, tests, Z-spec fuzz type-check):

```bash
make check
```

Build the binary:

```bash
make build
```

Install to `~/.local/bin`:

```bash
make install
```

List all targets:

```bash
make help
```

Contributors: see [CLAUDE.md](CLAUDE.md) for the development
lifecycle.

## License

MIT
