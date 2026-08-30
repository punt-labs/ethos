# ethos

> Identity, mission contracts, and per-tool-call audit for AI agent delegation.

[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/punt-labs/ethos/v4.svg)](https://pkg.go.dev/github.com/punt-labs/ethos/v4)
[![Working Backwards](https://img.shields.io/badge/Working_Backwards-validated-blue)](./prfaq.pdf)

Ethos runs typed mission contracts for AI agents — a write-set, a frozen
evaluator, bounded review rounds, and a per-tool-call audit trail that
git-trailer-links every commit back to the prompt that authorized it.
Missions dispatch against a persistent layer of identities and teams that
give each agent a role, tool restrictions, and a delegation graph. It ships
as a single Go binary with a Claude Code plugin, an MCP server, and a
filesystem layout other tools read directly. It runs locally — no server, no
telemetry, no cloud.

**Platforms:** macOS, Linux (amd64, arm64).

## Quick Start

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/c513be7/install.sh | sh
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
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/c513be7/install.sh | sh -s -- --no-plugin
```

Where arguments cannot pass through the pipe, set `ETHOS_NO_PLUGIN=1`:

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/c513be7/install.sh | ETHOS_NO_PLUGIN=1 sh
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

| Feature | What it does |
|---------|-------------|
| Mission contracts | Typed delegation with write-sets, frozen evaluators, bounded rounds, success criteria |
| Audit trail | Per-tool-call log tagged with delegation ID + contract ID; PII-redacted paths |
| Git trailers | `Mission:`/`Delegation:` on every commit; blame chain from line to prompt |
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

When commits carry `Mission:`/`Delegation:` git trailers (appended
automatically by the commit-msg hook), the blame chain works like
this:

```text
$ git blame -L 42,42 internal/hook/generate_agents.go
abc1234 (bwk  2026-05-25  42) func projectFilePatterns(repoRoot string) string {

$ git log --format='%(trailers)' abc1234
Mission: m-2026-05-25-002
Delegation: d-2026-05-25-011

$ cat .punt-labs/ethos/missions/m-2026-05-25-002/delegations/d-2026-05-25-011/prompt.md
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

The audit log is written to a machine-local, gitignored live file while a
session runs, so a repo with an active session keeps a clean `git status`.
A `pre-commit` hook runs `ethos audit seal`, which copies the pending lines
into immutable, timestamp-named chunk files under the tracked tree so the
audit record lands in the same commit as the work. Chunks are never
modified after creation, so branch merges are conflict-free. Hook
mechanics, marker sections, and `ethos doctor`'s currency checks: see
[Enable / Disable](#enable--disable) and DES-054/DES-058 in
[DESIGN.md](DESIGN.md).

Or visually: `ethos ui` opens a localhost dashboard where you browse
the repo, click a line, and see the agent who wrote it, the prompt
they received, and every tool call they made.

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
[AGENTS.md](AGENTS.md#commands).

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
| `ethos audit seal [--dry-run]` | Seal pending live audit lines into tracked chunks (run by the pre-commit hook) |
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
or fresh clone does not have. `ethos doctor` is the gate. Secrets in
`<handle>.ext/<namespace>.local.yaml` (vendor never copies, `.gitignore`
covers). Full walkthrough: [Team Setup](docs/team-setup.md).

### Git Worktrees

The repo-layer store lives at `<repo>/.punt-labs/ethos/`. Ethos resolves
it through the git common dir, so an agent in a linked worktree still
addresses the store in the main work tree — a mission created from either
checkout is visible from the other. Only the mission store crosses to
the main tree; per-checkout state (enable marker, generated agents,
audit trail) stays in the worktree. Override with `ETHOS_REPO_ROOT`.

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
contract, delegation record, the exact dispatch prompt, a
per-tool-call audit trail tagged with the delegation ID, and
`Mission:`/`Delegation:` git trailers on commits. `git blame` any
line → commit → trailer → contract → prompt → audit trail.

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
| [Traceability Use Cases](docs/traceability-use-cases.md) | 10 forensic/compliance scenarios with query paths |
| [Archetypes and Pipelines](docs/archetypes-and-pipelines.md) | Mission templates and multi-stage workflows |
| [Architecture](docs/architecture.tex) | System design (LaTeX) |
| [Agent Guide](AGENTS.md) | CLI, MCP, hooks, storage layout |
| [Design Decisions](DESIGN.md) | ADRs with rationale and rejected alternatives |
| [Changelog](CHANGELOG.md) | Release history |
| [Roadmap](docs/ETHOS-ROADMAP.md) | What's shipped and what's next |

## Development

Run all quality gates (vet, staticcheck, shellcheck, markdownlint, validate-content, tests):

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
