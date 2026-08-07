# ethos

> A responsible agent harness — control, auditability, and
> performance for AI agent delegation.

[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/punt-labs/ethos.svg)](https://pkg.go.dev/github.com/punt-labs/ethos)
[![Working Backwards](https://img.shields.io/badge/Working_Backwards-hypothesis-lightgrey)](./prfaq.pdf)

Ethos binds a name, personality, writing style, domain expertise, email,
GitHub handle, and voice into one identity that other tools read, and adds
typed mission contracts that record and bind what an agent is asked to do.
It ships as a single Go binary with a Claude Code plugin, an MCP server, and
a filesystem layout other tools read directly. It runs locally — no server,
no telemetry, no cloud.

**Platforms:** macOS, Linux (amd64, arm64).

## The Problem

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

## What Ethos Does

Ethos records and bounds agent delegation along three axes:

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

## Quick Start

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/8b04ad0/install.sh | sh
```

Then set up your identity and team:

```bash
ethos setup
```

To install the CLI without the Claude Code plugin — for a non-Claude
harness, or a Claude install where org policy blocks plugins — pass
`--no-plugin`:

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/8b04ad0/install.sh | sh -s -- --no-plugin
```

Or, where arguments cannot pass through the pipe, set `ETHOS_NO_PLUGIN=1`:

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/8b04ad0/install.sh | ETHOS_NO_PLUGIN=1 sh
```

`--no-plugin` skips only the marketplace-register and plugin-install
steps; the binary, PATH setup, directories, seed content, per-repo
`ethos enable`, and health check all still run. Re-run the installer
without the flag to add the plugin later.

The installer places the `ethos` binary in `~/.local/bin` and,
when `claude` and `git` are available, registers the Claude Code
plugin. `ethos setup` asks for your name, handle, email, GitHub
handle (optional), and working style, then creates your identity as
**CEO**, a paired **COO** agent (`claude`), repo config, and a
specialist team (architect, implementer, reviewer, security) that
reports to the COO, plus agent definition files. Out of the box the
org is you (CEO) → `claude` (COO) → specialists — see
[DESIGN.md](DESIGN.md) DES-064. The email prompt defaults to your `git config user.email`;
your identity carries that email so ethos can resolve you by it. Start
Claude Code — the agent knows who it is, who you are, and how to
delegate. See [Onboarding](docs/onboarding.md) for the full walkthrough.

Non-interactive setup reads the same fields from a YAML file:

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

`ethos setup` requires starter content to be seeded first. The
installer runs `ethos seed` for you, so the quick start above just
works. If you build from source or run `setup` on a machine that was
never seeded, run `ethos seed` first — `setup` otherwise fails with an
error naming the missing attribute and telling you to seed.

Run inside a repo, `ethos seed` also deploys three review-checklist
agents to that repo's `.claude/agents/`: `code-reviewer`,
`silent-failure-hunter`, and `invariant-completeness-reviewer`. These
are personaless Claude Code subagents for local review — invoke them
directly on a diff, never via `ethos mission dispatch`. Outside a repo,
`ethos seed` deploys only the global content above.

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
modified after creation, so branch merges are conflict-free.

`ethos enable` chains the seal hook into the pre-commit hook git actually
runs — resolved with `git rev-parse --git-path hooks`, so it lands in
`.git/hooks/` normally, the common hooks dir inside a worktree, or the
`core.hooksPath` directory (e.g. `.husky/`) when one is configured. When a
hook is already there — the beads hook, for one — it appends a
marker-delimited `ETHOS DES-058 SEAL` section rather than skipping, so the
seal coexists with the host hook. The chained script gates on the enabled
marker: it does nothing at commit time unless `.punt-labs/ethos/enabled`
exists, so a disabled repo's hook is inert (and still preserves a host
hook's failing fall-through). Re-running `enable` is idempotent. `ethos
doctor` resolves the same path and reports on the seal hook only when the
repo is enabled — a never-enabled or disabled repo passes; a repo with the
hook chained but no marker WARNs. See [enable / disable](#enable--disable).

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

### Outside Claude Code (Codex, plain terminal)

Ethos is harness-neutral. Inside Claude Code a session is created for you by
hooks. Anywhere else, open one explicitly — one line at shell or harness
init:

Start a session — this exports `ETHOS_SESSION` and `ETHOS_AGENT_ID`:

```bash
eval "$(ethos session start --persona bwk)"
```

Report the declared persona:

```bash
ethos whoami
```

Show the roster:

```bash
ethos session
```

Tear the session down:

```bash
ethos session end
```

`session start` is idempotent (a live `ETHOS_SESSION` is reported, not
re-minted), and resolution is `--session` > `ETHOS_SESSION` > the Claude
process walk. Outside a session, `iam` and `mission claim` fail with an
error naming `ethos session start`.

### Self-Standing Repos: `vendor` and `resolution: repo-only`

By default ethos resolves repo → active bundle → global, and the global
fallback catches whatever the repo lacks. That is what you want on a
developer's machine, and it is also why a repo that vendors a partial
identity set does not know it is partial: the gaps resolve from
`~/.punt-labs/ethos/` and nothing says so.

`ethos vendor` produces a set that resolves on its own:

Plan — show the closure and its blast radius:

```bash
ethos vendor bwk
```

Write it into `.punt-labs/ethos/`:

```bash
ethos vendor bwk --apply
```

Vendor a whole team, pruning anything outside the closure:

```bash
ethos vendor --team engineering --apply --prune
```

It follows references to a fixed point — attributes, roles, the teams an
identity belongs to, and *those* teams' other members. That last edge is
what pulls in people reachable no other way, and without it a vendored
team names members the set does not contain. It also means the closure is
the connected component of the team graph: naming one handle in a dense
org can vendor most of the roster. Vendor therefore plans by default and
reports the gap between what you asked for and what it would write.

Then make the repo layer authoritative, in `.punt-labs/ethos.yaml`:

```yaml
resolution: repo-only    # default is `layered`
```

The global layer now leaves every read, and a missing reference is a hard
error naming each one and the file it should occupy — instead of silently
resolving from a home directory a CI runner or a fresh clone does not
have. `ethos doctor` is the gate:

```text
Repo-only completeness   PASS  29 identities resolve with no global fallback
```

Extensions come along, because a vendored agent without its memory wiring
looks correct and behaves as if it had amnesia. Anything secret goes in
`<handle>.ext/<namespace>.local.yaml`, which vendor never copies and
`.gitignore` covers; `ethos ext set --local` writes it, and reads see base
and `.local` merged. Vendor refuses (non-zero) to copy a key whose *name*
reads as a credential — `api_token` blocks, `gpg_key_id` does not, values
are never inspected — with `--allow-ext-key <ns>/<key>` as a per-key
override and no blanket force.

`.local` is a git-exclusion mechanism, not a vault: the merged view still
reaches the model at runtime.

`ethos export` is a different job — a lossy conversion of one identity
into a foreign format — and is unchanged.

### Git Worktrees and the Store Root

The repo-layer store lives at `<repo>/.punt-labs/ethos/`. Ethos resolves it
through the git common dir, so an agent working in a linked worktree
(`<repo>/.claude/worktrees/x`) still addresses the store in the main work
tree — a mission created from either checkout is visible from the other, and
a leader dispatching from a worktree binds the same store the CLI wrote to,
so delegation resolves rather than failing "MISSION_ID not found". Only the
mission store crosses to the main tree; per-checkout state (the enable
marker, generated `.claude/agents/`, audit trail, and the files a verifier
inspects) stays in the worktree. When no git repository is in scope, the
mission store warns to stderr that it is operating on the global store
(`~/.punt-labs/ethos/`) rather than silently switching.

Set `ETHOS_REPO_ROOT` to force the store location when auto-resolution is
wrong. It overrides the git walk for every command and hook:

```bash
ETHOS_REPO_ROOT=/path/to/repo ethos mission list
```

### Enable / Disable

`install.sh` is machine scope only — it installs the binary, registers the
plugin, and seeds global content. Per-repo enablement is `ethos enable`,
run inside a repo (and delegated to automatically by `install.sh` when it is
run inside a work tree). `enable`:

1. deposits the vendored agent guide `.punt-labs/ethos/CLAUDE.md` and its
   `.vendored-manifest`;
2. writes the enabled marker `.punt-labs/ethos/enabled`;
3. adds the `@.punt-labs/ethos/CLAUDE.md` import line to the repo `CLAUDE.md`;
4. chains the `ETHOS DES-058 SEAL` and `ETHOS DES-054 TRAILER` sections into
   the `pre-commit` and `commit-msg` hooks.

It is idempotent — re-running is the upgrade path — and prints a "run `ethos
setup`" hint when the repo has no identity config. `enable` and `setup` stay
separate: neither calls the other.

`ethos disable` reverses it non-destructively: it removes the import line,
deletes the marker, and unchains both hooks, but leaves the vendored guide
and all config and audit data on disk, dormant. It refuses when a sibling
worktree is still enabled (the git hooks are shared across worktrees); pass
`--force` to unchain anyway. It runs no final seal — any unsealed audit
lines stay in the gitignored local zone and seal on a later re-enable.

The three states a repo can be in:

| State | `enabled` marker | Import line | Hooks | `doctor` seal check |
|-------|------------------|-------------|-------|---------------------|
| Enabled | present | present | chained + active | FAIL if seal missing/inactive |
| Dormant / Absent | absent | absent | absent | PASS (not enabled here) |
| Gated-but-unenabled | absent | absent | chained (inert) | WARN |

The chained hook scripts gate on the marker: they do no commit-time work
unless `.punt-labs/ethos/enabled` exists, so a dormant repo's hook is inert
while still preserving a host hook's failing fall-through.

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

Run all quality gates (vet, staticcheck, markdownlint, shellcheck, tests):

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
