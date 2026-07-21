# ethos

> A responsible agent harness — control, auditability, and
> performance for AI agent delegation.

[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)
[![Release](https://img.shields.io/github/v/release/punt-labs/ethos)](https://github.com/punt-labs/ethos/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/punt-labs/ethos)](https://goreportcard.com/report/github.com/punt-labs/ethos)

## The problem

Agents write code you're responsible for, and you can't see what
they did or why.

A developer delegates a task to an AI agent. The agent reads files,
edits code, runs tests, commits. Six months later someone asks: who
authorized this change? What were the instructions? Did the agent
stay within the files it was supposed to touch? Why this approach?

Today the answer is: check the chat history, if you still have it.
There is no durable record connecting a line of code to the contract
that authorized it, the prompt that drove it, and the tool calls
that produced it.

## What ethos does

Ethos makes agent delegation responsible rather than reckless. Three
axes:

**Control.** Typed mission contracts with file-level write-sets
enforced at runtime, frozen evaluators (hash-pinned so nobody swaps
the reviewer mid-mission), bounded review rounds, preconditions
that gate tool calls on prior reads, and delegation depth limits.
The agent can only do what the contract authorizes.

**Auditability.** Every delegation produces artifacts on disk —
contract, delegation record, the exact dispatch prompt, a
per-tool-call audit trail tagged with the delegation ID, and
`Mission:`/`Delegation:` git trailers on commits. `git blame` any
line → commit → trailer → contract → prompt → audit trail. Months
later, you can reconstruct exactly what happened and why.

**Performance.** Named specialist agents with encapsulated domain
expertise — not generic assistants, but a Go specialist grounded in
Kernighan's principles, a security reviewer with Bernstein's
methodology. Personalities, writing styles, and talents shape the
model's output the way a real colleague's expertise would. Roles
restrict tool access. Teams define delegation topology. The
configuration is reusable, measurable, and improvable.

## Quick start

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/ac2c223/install.sh | sh
ethos setup
```

The installer places the `ethos` binary in `~/.local/bin` and,
when `claude` and `git` are available, registers the Claude Code
plugin. `ethos setup` asks 3 questions
(name, handle, working style), then creates your identity, a paired
agent, repo config, a 4-agent team, and agent definition files.
Start Claude Code — the agent knows who it is, who you are, and how
to delegate. See [Onboarding](docs/onboarding.md) for the full walkthrough.

**Platforms:** macOS, Linux (amd64, arm64).

## What it looks like

### Traceability: line of code → full history

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
modified after creation, so branch merges are conflict-free.

Or visually: `ethos ui` opens a localhost dashboard where you browse
the repo, click a line, and see the agent who wrote it, the prompt
they received, and every tool call they made.

### Contract-bound delegation

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

### Specialist agents

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
| Symlink rejection | Uniform policy across all mission loaders and lock paths |
| Depth limits | Configurable ceiling on nested delegation chains |
| Query surface | `ethos find missions` with date/worker/status filters |
| Composable integration | CLI, MCP (11 tools), filesystem reads; works with biff, vox, beadle, quarry |

## Commands

Essentials below. Every command accepts `--json`. Full reference in
[AGENTS.md](AGENTS.md#commands).

| Command | What it does |
|---------|--------------|
| `ethos setup` | Set up identities and team (60-second wizard) |
| `ethos whoami` | Show your resolved identity |
| `ethos doctor` | Check installation health |
| `ethos mission create` / `dispatch` | Create a mission contract |
| `ethos mission claim` / `release` | Bind session to mission for Tier B dispatch |
| `ethos mission show <id>` | Show contract, results, reflections |
| `ethos audit show --delegation <id>` | Full tool-call trace for a delegation |
| `ethos audit seal [--dry-run]` | Seal pending live audit lines into tracked chunks (run by the pre-commit hook) |
| `ethos audit quarantine <chunk>` | Retire a corrupt sealed chunk and recover what the live file holds |
| `ethos session purge [--force] [--ack <id>]` | Clean up stale sessions; guard/acknowledge unsealed audit lines |
| `ethos find missions` | Query closed missions by date, worker, status |
| `ethos ui` | Open traceability dashboard in browser |

## How this is different

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
| [Team Setup](docs/team-setup.md) | Configuring roles, teams, bundles |
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

```bash
make check    # All quality gates (vet, staticcheck, markdownlint, shellcheck, tests)
make build    # Build binary
make install  # Install to ~/.local/bin
make help     # List all targets
```

Contributors: see [CLAUDE.md](CLAUDE.md) for the development
lifecycle.

## License

MIT
