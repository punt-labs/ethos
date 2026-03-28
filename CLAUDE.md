# CLAUDE.md

## No "Pre-existing" Excuse

There is no such thing as a "pre-existing" issue. If you see a problem — in code you wrote, code a reviewer flagged, or code you happen to be reading — you fix it. Do not classify issues as "pre-existing" to justify ignoring them. Do not suggest that something is "outside the scope of this change." If it is broken and you can see it, it is your problem now.

## Project Overview

Identity binding for humans and AI agents. Ethos unifies a name, voice (Vox), email (Beadle), GitHub handle (Biff), writing style, personality, and skills into a single identity that other tools optionally read. Written in Go.

Ethos is a sidecar — it publishes identity state to a known filesystem location. Vox, Beadle, and Biff work without ethos; when ethos is installed, they gain richer identity context.

## Standards

This project follows [Punt Labs standards](https://github.com/punt-labs/punt-kit). When this CLAUDE.md conflicts with punt-kit standards, this file wins (most specific wins).

### Standards-First Workflow

Before specifying any work for the team, the COO must consult the relevant punt-kit standards. This is not optional — it is a blocking prerequisite for writing specs.

- **New MCP tool** → check DES-018 (PostToolUse formatted output, not raw JSON). Every tool needs a formatter in `format_output.go` before shipping.
- **New CLI command** → check [cli standard](https://github.com/punt-labs/punt-kit/blob/main/standards/cli.md). Exit codes, help text, output format.
- **New hook** → check [hooks standard](https://github.com/punt-labs/punt-kit/blob/main/standards/hooks.md). Shell gate pattern, error handling.
- **New slash command** → check existing command files for pattern. Both `name.md` and `name-dev.md` variants required.
- **Any Go code** → check [go standard](https://github.com/punt-labs/punt-kit/blob/main/standards/go.md). Error wrapping, naming, test patterns.
- **Release work** → check [release-process standard](https://github.com/punt-labs/punt-kit/blob/main/standards/release-process.md).

Failure to consult standards before specifying work is a process failure, not a coding error. The COO is accountable for knowing and applying all standards to every piece of work delegated to the team.

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

## Scratch Files

Use `.tmp/` at the project root for scratch and temporary files — never `/tmp`. The `TMPDIR` environment variable is set via `.envrc` so that `tempfile` and subprocesses automatically use it. Contents are gitignored; only `.gitkeep` is tracked.

## Quality Gates

Run before every commit. The Makefile is the source of truth (`make help`).

```bash
make check                             # All gates: lint + docs + test
```

Expands to `make lint docs test`:

- `go vet ./...`
- `staticcheck ./...`
- `shellcheck hooks/*.sh install.sh`
- `npx markdownlint-cli2 "**/*.md"`
- `go test -race -count=1 ./...`

## Architecture

### Package Map

| Package | Responsibility |
|---------|---------------|
| `cmd/ethos/` | CLI entry point: identity, attribute, session, and admin commands |
| `internal/identity/` | Core identity model, validation, CRUD, attribute resolution |
| `internal/attribute/` | Generic CRUD for named markdown files (skills, personalities, writing styles) |
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

- **Sidecar, not dependency.** Other tools read ethos state from the filesystem. They do not import ethos.
- **Same schema for humans and agents.** The `kind` field is the only structural difference.
- **Agent definition is a channel binding.** Like voice or email — the `.md` file defines tools and workflow, ethos defines who.

## Go Standards

This project follows [Go standards](https://github.com/punt-labs/punt-kit/blob/main/standards/go.md). Module path: `github.com/punt-labs/ethos`.

## Development Workflow

### Branch Discipline

All code changes go on feature branches. Never commit directly to main.

| Prefix | Use |
|--------|-----|
| `feat/` | New features |
| `fix/` | Bug fixes |
| `refactor/` | Code improvements |
| `test/` | Test coverage |
| `docs/` | Documentation only |
| `chore/` | Maintenance and housekeeping |

### Commits

One logical change per commit. Quality gates pass before every commit.

Format: `type(scope): description`

### Delegation Discipline

The COO does not write code. All code changes — initial implementation, review-cycle fixes, lint fixes, test updates — are delegated to specialist agents:

| Specialist | Agent | Domain |
|-----------|-------|--------|
| Brian K | `bwk` | Go implementation |
| Andrej K | `kpz` | ML inference, hardware abstraction, ONNX |
| Doug M | `mdm` | CLI design |
| Dan B | `djb` | Security review |

**Workflow**: spec → delegate → review → fix-spec → delegate → review → ship.

- Write detailed specs: files to read, files to modify, acceptance criteria.
- After code review agents report findings, consolidate into a fix spec and send back to the implementation agent. Do not apply fixes yourself.
- There is no threshold below which a code change is "too small to delegate."
- The only files the COO edits directly: `CHANGELOG.md`, `CLAUDE.md`, `DESIGN.md`, `README.md`, memory files, plan files.

### Collaboration Model

Two collaboration mechanisms, used for different scopes:

| Mechanism | Scope | When to use |
|-----------|-------|-------------|
| **Agent Teams** | Within this repo | Spawn teammates for parallel work on implementation, review, testing. Each teammate is a full Claude Code session with its own context, MCP servers, and tools. Use `TeamCreate` to create a team, `TaskCreate` for work items. |
| **Biff** | Across repos | Coordinate with agents working in other repos (quarry, punt-kit, etc.). Use `/write` to send messages, `/read` to check inbox, `/who` to see active teammates. |

**Agent Teams** (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`):

- Team lead (COO) creates the team, spawns teammates, coordinates work
- Teammates load CLAUDE.md, MCP servers, and skills independently — they do NOT inherit conversation history
- Shared task list with automatic dependency resolution
- Teammates can work in plan mode (read-only until lead approves)
- Quality gates via `TeammateIdle` and `TaskCompleted` hooks

**Biff** (cross-repo):

- Start every session with `/loop 2m /biff:read` to poll for incoming messages
- Async messaging — messages queue until the recipient reads them
- Use for design reviews, status updates, and coordination across repos
- Never ignore biff messages — check `/read` when prompted by hooks

### Workflow Tiers

Match the workflow to the bead's scope. The deciding factor is **design ambiguity**, not size.

| Tier | Tool | When | Tracking |
|------|------|------|----------|
| **T1: Feature Dev** | `/feature-dev` | Epics, cross-cutting work, competing design approaches | Beads with dependencies |
| **T2: Feature Dev** | `/feature-dev` | Features, multi-file, clear goal but needs exploration | Beads + TodoWrite |
| **T3: Direct** | Plan mode or manual | Tasks, bugs, obvious implementation path | Beads |

**Decision flow:**

1. Is there design ambiguity needing multi-perspective input? → **T1: Feature Dev**
2. Does it touch multiple files and benefit from codebase exploration? → **T2: Feature Dev**
3. Otherwise → **T3: Direct** (plan mode if >3 files, manual if fewer)

**Escalation only goes up.** If T3 reveals unexpected scope, escalate to T2. If T2 reveals competing design approaches, escalate to T1. Never demote mid-flight.

### Code Review

1. **Create PR** via `mcp__github__create_pull_request`.
2. **Request Copilot review** via `mcp__github__request_copilot_review`.
3. **Watch for feedback** — `gh pr checks <number> --watch` in background.
4. **Read all feedback** via MCP tools. Address every finding.
5. **Fix, re-push, repeat.** Expect 2–6 cycles.
6. **Merge only when clean** — zero new comments, all checks green.

### Documentation Discipline

- **CHANGELOG**: Entries in the PR branch, before merge. Follow Keep a Changelog.
- **README**: Update when user-facing behavior changes.
- **DESIGN.md**: Log decisions with rejected alternatives.

### Issue Tracking

This project uses **bd** (beads) for issue tracking. Run `bd onboard` to get started.

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --status in_progress  # Claim work
bd close <id>         # Complete work
bd sync               # Sync with git
```

### Session Close Protocol

When ending a work session, complete ALL steps. Work is NOT complete until `git push` succeeds.

```bash
git status              # Check for uncommitted work
git add <files>         # Stage changes
git commit -m "..."     # Commit
bd sync                 # Sync beads
git push                # Push to remote
git status              # MUST show "up to date with origin"
```

Rules:

- File issues for remaining work before closing
- Run `make check` if code changed
- Close finished beads, update in-progress items
- NEVER stop before pushing — that leaves work stranded locally
- If push fails, resolve and retry until it succeeds

## Standards References

- [GitHub](https://github.com/punt-labs/punt-kit/blob/main/standards/github.md)
- [Workflow](https://github.com/punt-labs/punt-kit/blob/main/standards/workflow.md)
- [CLI](https://github.com/punt-labs/punt-kit/blob/main/standards/cli.md)
- [Shell](https://github.com/punt-labs/punt-kit/blob/main/standards/shell.md)
- [Hooks](https://github.com/punt-labs/punt-kit/blob/main/standards/hooks.md)
- [Plugins](https://github.com/punt-labs/punt-kit/blob/main/standards/plugins.md)
