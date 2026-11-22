# ethos

> Identity, mission contracts, and per-tool-call audit for AI agent delegation.

[![License](https://img.shields.io/github/license/punt-labs/ethos)](LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/punt-labs/ethos/test.yml?label=CI)](https://github.com/punt-labs/ethos/actions/workflows/test.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/punt-labs/ethos/v4.svg)](https://pkg.go.dev/github.com/punt-labs/ethos/v4)
[![Working Backwards](https://img.shields.io/badge/Working_Backwards-hypothesis-lightgrey)](./prfaq.pdf)

Ethos is a command-line tool, an MCP (Model Context Protocol) server, and a
Claude Code plugin for developers who hand work to AI coding agents in a Git
repository. It gives each agent a named identity and role, runs delegated work
under a written mission contract that limits which files the agent may change,
and logs every tool call the agent makes. It is a single Go binary that runs
locally, with no server, network telemetry, or cloud service. Private local usage
records under `~/.punt-labs/ethos` never leave the machine; disable them with
`ETHOS_TELEMETRY=off` or `PUNT_TELEMETRY=off`.

**Platforms:** macOS, Linux (amd64, arm64).

## Quick Start

Install ethos:

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/cb596af/install.sh | sh
```

Create your identity and team:

```bash
~/.local/bin/ethos setup
```

The installer places the `ethos` binary in `~/.local/bin`, adds that
directory to `PATH` in your shell profile, copies starter roles, talents and
skills into `~/.punt-labs/ethos/`, and, when `claude`
and `git` are available, installs the Claude Code plugin. When you run it
inside a Git repository it also runs `ethos enable` there, and it finishes
with `ethos doctor`. `ethos setup` is an interactive wizard that creates an
identity for you, an agent identity named `claude`, and a team of specialist
agents. After you open a new terminal (or `source` your shell profile), the
bare `ethos` command works. Full walkthrough: [Onboarding](docs/onboarding.md).

<details>
<summary>Install without the Claude Code plugin</summary>

For a harness other than Claude Code, or a Claude Code install where
organization policy blocks plugins:

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/cb596af/install.sh | sh -s -- --no-plugin
```

Where arguments cannot pass through the pipe, set `ETHOS_NO_PLUGIN=1`:

```bash
curl -fsSL https://raw.githubusercontent.com/punt-labs/ethos/cb596af/install.sh | ETHOS_NO_PLUGIN=1 sh
```

`--no-plugin` skips only the marketplace registration and plugin install. The
binary, `PATH` setup, starter content, `ethos enable` in the current
repository, and the health check still run. Run the installer again without
the flag to add the plugin later.
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

Run `ethos setup --file config.yaml`. When `email` is omitted, setup uses
`git config user.email`. If that is also unset, setup fails and says how to
fix it, instead of creating an identity that cannot be resolved.
</details>

## Features

A **mission** is one unit of delegated work. Its **contract** is a YAML file
that names the worker agent, the evaluator (the agent that reviews the work),
the write set (the files the worker may change), the success criteria, and the
number of review rounds allowed.

| Feature | What it does |
|---------|-------------|
| Mission contracts | Typed contracts with write sets, success criteria and a round limit. The evaluator's identity is hash-pinned when the mission starts, so it cannot be swapped mid-mission. |
| Write-set enforcement | The PreToolUse hook blocks an edit to a file outside the write set before the edit runs |
| Audit trail | Logs every tool call with its delegation ID and mission ID, and keeps each delegation's contract, results and prompt on disk. Email addresses, credentials and home-directory paths are removed from files written to Git-tracked directories. |
| Git trailers | The commit-msg hook adds `Mission:` and `Delegation:` trailers to commits. For squash merges, `ethos mission publish`, `ethos provenance message` and `ethos provenance verify` carry and check the attribution in the merge commit. |
| Traceability UI | `ethos ui` opens a local web dashboard to browse repository files and view mission and delegation details |
| Preconditions | Block a tool call until a required earlier step has happened ("must read DESIGN.md before editing") |
| Agent identities | Each identity has a personality, a writing style and talents, written into the generated agent file |
| Teams and roles | Roles limit which tools an agent can use; teams record who reports to whom and what each role is not responsible for |
| Pipeline templates | Chain missions into multi-stage workflows from 8 built-in templates, plus 5 in the gstack team bundle |
| Lifecycle hooks | 7 Claude Code hook events: SessionStart, PreToolUse, PostToolUse, SubagentStart, SubagentStop, PreCompact, SessionEnd |
| Repo-only identities | `ethos vendor` copies a complete identity set into the repository; `resolution: repo-only` stops lookups in your home directory |
| Symlink rejection | Mission files and lock paths that are symbolic links are refused |
| Depth limits | `max_delegation_depth` limits how deeply delegations can nest |
| Mission queries | `ethos find missions` filters closed missions by date, worker and status |
| Integration | CLI, MCP server (12 tools), and plain files that other tools read; biff, vox, beadle and quarry use ethos identities when it is installed |
| Automatic storage upgrade | Converts older store layouts the first time any command, hook or MCP call runs; `ethos doctor` reports the result in its `Storage` row. See [Storage Layout](docs/storage-layout.md). |

## What It Looks Like

### Traceability: Line of Code to Full History

The commit-msg hook adds `Mission:` and `Delegation:` trailers to the commit.
The prompt and audit log are kept locally under `.punt-labs/local/ethos/` in
the main checkout of the repository, so a fresh clone does not have them.
Run these commands from the main checkout:

```text
$ git blame -L 42,42 internal/hook/generate_agents.go
abc1234 (bwk  2026-05-25  42) func projectFilePatterns(repoRoot string) string {

$ git log --format='%(trailers)' abc1234
Mission: m-2026-05-25-002
Delegation: d-2026-05-25-011

$ cat .punt-labs/local/ethos/trace/missions/m-2026-05-25-002/delegations/d-2026-05-25-011/prompt.md
"Fix the hardcoded Go file-extension patterns. Detect project type
at generation time (go.mod → Go, pyproject.toml → Python)..."

$ ethos audit show --delegation d-2026-05-25-011 --format text
2026-05-25T12:49:23Z  Read   <repo>/internal/hook/generate_agents.go
2026-05-25T12:49:55Z  Edit   <repo>/internal/hook/generate_agents.go
2026-05-25T12:51:06Z  Bash   go test -run TestGenerateAgentFiles ...
2026-05-25T12:53:25Z  Bash   make check
2026-05-25T12:53:37Z  Bash   git commit ...
```

Illustrative: actual SHAs and IDs vary per repository.

The `record` setting in `.punt-labs/ethos.yaml` controls what is shared. With
the default, `record: blame`, prompts and audit logs stay in
`.punt-labs/local/ethos/`, which Git ignores; the contract, results and
delegation records are written to `.punt-labs/ethos/` for you to commit. With `record: full`,
ethos also copies prompts, reflections and sealed audit logs recorded while
`record: full` was set into `.punt-labs/ethos/`, so they are committed with
the code. Sealing is done by
the pre-commit hook, which runs `ethos audit seal` to move pending log lines
into fixed archive files. Switching modes does not publish older private
history or remove history that was already published.

For a squash merge that covers one or more missions, use `ethos mission
publish`, `ethos provenance message` and `ethos provenance verify`. They
generate the merge commit body and check the merged commit afterward. See
the [merge example](docs/example/07-pr-and-merge.md).

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

When the worker agent is spawned, the PreToolUse hook enforces the write
set: an Edit to a file outside the contract is blocked before it runs.

### Specialist Agents

```text
[ethos] Subagent bwk spawned: go-specialist
[ethos] Personality: kernighan (simplicity, clarity, generality)
[ethos] Tools: Read, Write, Edit, Bash, Grep, Glob
[ethos] Anti-responsibilities: strategic direction (that's the COO's job)
```

Ethos generates each `.claude/agents/<handle>.md` file from the identity,
role and team data. The identity's personality, writing style and talents
become text in the agent's prompt. The role sets which tools the agent can
use.

## Commands

The most common commands are below. Every command accepts `--json`. Full
reference: [Agent Guide](docs/agent-guide.md).

| Command | What it does |
|---------|--------------|
| `ethos enable` / `disable` | Turn ethos on or off in this repository (see [Enable / Disable](#enable--disable)) |
| `ethos setup` | Create identities and a team (interactive wizard) |
| `ethos whoami` | Show your resolved identity |
| `ethos identity schema` | Show the field reference for an identity (also `role schema`, `team schema`) |
| `ethos session start` / `end` | Open or close a session from any harness: `eval "$(ethos session start)"` |
| `ethos session iam <persona>` | Declare your persona in the active session |
| `ethos doctor` | Check installation health |
| `ethos vendor [handle...]` | Copy a complete identity set into this repository (see [Repo-Only Identities](#repo-only-identities)) |
| `ethos mission create` / `dispatch` | Create a mission contract from a YAML file or from flags |
| `ethos mission claim` / `release` | Bind the current session to a mission, so agents it spawns run under that contract, or clear the binding |
| `ethos mission show <id>` | Show a contract, its results and reflections |
| `ethos audit show --delegation <id>` | Show every tool call made under a delegation |
| `ethos audit seal [--dry-run]` | Move pending audit lines into fixed local archive files (the pre-commit hook runs it) |
| `ethos find missions` | Query closed missions by date, worker and status |
| `ethos ui` | Open the traceability dashboard in a browser |

## Setup

Configuration beyond the Quick Start.

### Outside Claude Code (Codex, Plain Terminal)

Inside Claude Code, the plugin's hooks open a session for you. In any other
harness, open one when the shell or harness starts:

```bash
eval "$(ethos session start --persona bwk)"
```

Then `ethos whoami` reports the persona, `ethos session` shows who is in the
session, and `ethos session end` closes it. `session start` is safe to run
again; it exports `ETHOS_SESSION`, plus `ETHOS_AGENT_ID` when `--persona` is
given. Details: [Harness-Neutral Sessions](docs/harness-sessions.md).

### Native OpenCode MCP Identity

This is a pilot and is off by default. With `ethos serve
--opencode-session-metadata`, MCP calls from OpenCode resolve identity from
the OpenCode session ID sent with each request, after you add that session
to an ethos session roster. The session ID is used for routing, not
authentication. Setup and limitations: [OpenCode MCP
Identity](docs/opencode-mcp-identity.md).

### Repo-Only Identities

By default ethos looks up an identity in the repository, then the active team
bundle, then `~/.punt-labs/ethos/`. Because of that last step, an identity
missing from the repository is found in your home directory without warning,
and the gap shows up only on a CI runner or a fresh clone. `ethos vendor`
copies every identity, role, team and attribute that the chosen identities
need into the repository:

```bash
ethos vendor --team engineering --apply --prune
```

Then set `resolution: repo-only` in `.punt-labs/ethos.yaml`. A missing
reference then fails with an error that names it, and `ethos doctor` reports
it. Full walkthrough: [Team Setup](docs/team-setup.md).

### Git Worktrees

All worktrees of a repository share one ethos store in the main checkout.
Removing a linked worktree keeps its prompts, reflections and audit logs,
even if the session did not end cleanly. Deleting the main checkout, or its
`.punt-labs/local/ethos/` directory, deletes them. Set `ETHOS_REPO_ROOT` to
use a different store location. Details: [Storage
Layout](docs/storage-layout.md).

### Enable / Disable

The installer works at machine scope. `ethos enable` turns ethos on in one
repository: it adds the ethos agent guide and its `@.punt-labs/ethos/CLAUDE.md`
import to the repository's `CLAUDE.md`, adds ethos steps to the `pre-commit`
and `commit-msg` Git hooks, and allows the ethos CLI, MCP server and skills in
`.claude/settings.json`, with `ask` rules that prompt before state-changing
operations. `ethos disable` removes these and leaves your identities, missions
and audit data in place. Both commands are safe to run again. Details:
[Enable / Disable](docs/enable-disable.md).

## How This Is Different

| Tool | What it does | Where ethos differs |
|------|--------------|---------------------|
| [SoulSpec](https://soulmd.dev) | Structured agent personas | Ethos adds mission contracts, an audit trail and team structure to personas |
| [Mastra](https://mastra.ai) | Typed Zod schemas and pre-delegation hooks | Ethos adds persistent identities, file-level write sets and a fixed evaluator |
| [CrewAI](https://www.crewai.com) | Role-based agent orchestration | Ethos uses typed YAML contracts with write sets enforced before each tool call, and links commits to missions |
| [Claude Managed Agents](https://platform.claude.com/docs/en/managed-agents/overview) | Hosted agent harness for long-running and asynchronous work | Ethos runs locally with any harness and keeps contracts and audit logs in the repository |

Ethos imports SoulSpec personas (`ethos import --from soulspec`) and exports
to SoulSpec (`ethos export --to soulspec`). Export is lossy: the enforcement
data has no place in SoulSpec's markdown.

## Documentation

| Guide | Audience |
|-------|----------|
| [Onboarding](docs/onboarding.md) | Install, setup, first delegation |
| [Team Setup](docs/team-setup.md) | Configuring roles, teams, bundles, vendor and repo-only resolution |
| [Harness-Neutral Sessions](docs/harness-sessions.md) | Running ethos outside Claude Code (Codex, plain terminal) |
| [Enable / Disable](docs/enable-disable.md) | Per-repository enable and disable, Git hook chaining, `ethos doctor` |
| [Audited Delegation](docs/audited-delegation.md) | Ad hoc and contract-bound agent spawns, claim and release, audit trail, Git trailers |
| [Traceability Data Assets](docs/traceability-data-assets.md) | Every file ethos produces, organized by scope |
| [Storage Layout](docs/storage-layout.md) | Where ethos stores each file, the automatic upgrade, recording modes, publication and export, and the test behind each rule |
| [Traceability Use Cases](docs/traceability-use-cases.md) | 10 forensic and compliance scenarios with query paths |
| [Archetypes and Pipelines](docs/archetypes-and-pipelines.md) | Mission templates and multi-stage workflows |
| [Architecture](docs/architecture.tex) | System design (LaTeX) |
| [Agent Guide](docs/agent-guide.md) | CLI, MCP, hooks, storage layout |
| [OpenCode Setup](docs/opencode-setup.md) | Native OpenCode agents, models, tools, and verification |
| [Design Decisions](DESIGN.md) | Architecture decision records with rationale and rejected alternatives |
| [Contributing](CONTRIBUTING.md) | Building ethos and running the quality gates |
| [Changelog](CHANGELOG.md) | Release history |
| [Roadmap](docs/ETHOS-ROADMAP.md) | What has shipped and what is planned |

## License

MIT
