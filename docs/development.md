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
make check                              # All quality gates (vet, staticcheck, shellcheck, markdownlint, validate-content, tests)
./ethos version                         # Print version
./ethos doctor                          # Check installation health
./ethos setup                           # Interactive repo setup wizard
./ethos whoami                          # Show caller's identity (iam/git/OS)
./ethos resolve-agent                   # Show default agent from repo config
./ethos serve                           # Start MCP server (stdio transport)
./ethos iam <persona>                   # Declare persona in current session
./ethos session                         # Show current session participants
./ethos session purge                   # Clean up stale sessions
./ethos adr list                        # List architecture decision records
./ethos import --from soulspec <file>   # Import identity from SoulSpec
./ethos export --to soulspec <handle>   # Export identity to SoulSpec or claude-md
./ethos mission lint <contract.yaml>    # Advisory pre-delegation linter
./ethos mission claim <id>              # Bind session to mission for Tier B dispatch
./ethos mission release                 # Clear active-mission binding
./ethos find missions                   # Query closed missions (--since, --worker, --status, --format)
./ethos audit show --delegation <id>    # Tool-call trace for a delegation
./ethos ui                              # Open traceability dashboard in browser
./ethos mission dispatch --worker bwk --evaluator djb --write-set "..." --criteria "..."
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

The `--worker` flag sets the default for all stages. The leader
reassigns workers per stage at delegation time: Stage 1 (prfaq) → ghr,
Stage 2 (design) → edt + mdm, Stages 3-6 → bwk. Design is reviewed
before code starts.

**Engineering work** uses `standard` or `quick` pipelines. Two specialists per domain — within each row, the worker and evaluator must be distinct handles. Across rows, `bwk` and `rsc` trade worker/evaluator on Go internals vs. module/dependency work — bringing complementary perspectives without ever putting a handle in conflict with itself on a single task.

| Task type | Worker | Evaluator |
|-----------|--------|-----------|
| Go internals (resolve, store, hooks) | `bwk` (Kernighan) | `rsc` (Cox) — toolchain, supply-chain |
| Go module / dependency / vuln tooling | `rsc` | `bwk` |
| CLI / command surface | `mdm` (McIlroy) | `rop` (Pike) — Plan 9 minimalism |
| MCP tool definition / format_output | `mdm` | `rop` |
| Cryptographic primitives (key handling, signing) | `bwk` | `djb` (Bernstein) |
| Threat modeling — multi-tenant identity, session trust | `claude` (leader) | `bcs` (Schneier) |
| Infra / CI / release / homebrew tap | `adb` (Lovelace) | `kth` (Hightower) |
| Mission / pipeline schema design | `claude` (leader) | `mcg` (Cagan) — frameworks for empowered teams |
| Onboarding / `ethos seed` / first-run UX | `claude` (leader) | `dna` (Norman) — affordances, mental models |
| Bundle layout / resolution | `bwk` | `rsc` (compatibility / migration cost) |
| Persona-animation (SessionStart, PreCompact) | `bwk` | `mdm` |
| Z spec for the contract or session schemas | `jms` (Spivey) | `jra` (Abrial) |

**Never solo-design in plan mode.** Instantiate a pipeline, delegate to specialists, review their output. The system exists for this purpose. The full org roster is available via `ethos identity list`.

**Dogfood before shipping.** Build the binary, run the commands, walk
the user journey. `make check` passing is necessary but not sufficient.
A feature that fails when used is not shipped — it's fiction.

**`write_set` is not a sandbox.** It is enforced mechanically only for
verifier spawns, and only for `Write`/`Edit` — see DES-069 and
`docs/workflow.md` §"What `write_set` does and does not enforce"
before scoping an MCP grant or trusting a write-set to fence a worker.

**Local review runs three ethos-seeded agents.** `ethos seed` deploys
`code-reviewer` (style/bugs/CLAUDE.md compliance), `silent-failure-hunter`
(error handling), and `invariant-completeness-reviewer` to
`.claude/agents/` (DES-070). Run all three on every diff before opening a
PR:

1. `code-reviewer` — style, bugs, CLAUDE.md-rule compliance.
2. `silent-failure-hunter` — swallowed errors, unjustified fallbacks,
   inadequate logging.
3. `invariant-completeness-reviewer` — verifies claims a PR makes about
   itself (exclusivity, exhaustiveness, "cannot drift," a test that
   claims to guard a general case) against what the code actually does,
   a review dimension neither of the other two covers by design. Added
   after DES-069's PR #431 review cycle surfaced three real defects of
   exactly this shape that both third-party local agents missed because
   the defects were outside their stated scope, not because of a
   capability gap.

We now use our own seeded agents rather than the third-party
`pr-review-toolkit` plugin for this repo's local review pass — a
dogfooding change, not a claim that ours are better. Both are being run
for one PR cycle (DES-070's transition plan) before the plugin dependency
is dropped from this repo.

## Quality Gates

The Makefile is the source of truth (`make help`).

```bash
make check                             # All gates: lint + docs + test
```

Expands to `make lint docs test validate-content`: `go vet`, `staticcheck`, `shellcheck plugin/hooks/*.sh install.sh`, `markdownlint`, `go test -race -count=1 ./...`, `go run ./cmd/validate-content`.

## Architecture

### Repository Layout

Everything the Claude Code plugin ships lives under `plugin/`, and nothing
else does:

| Path | Contents |
|------|----------|
| `plugin/.claude-plugin/plugin.json` | Plugin manifest. `mcpServers` points at the `ethos` binary on `PATH`, so no compiled code ships. |
| `plugin/commands/` | Slash commands (`name.md` + `name-dev.md` pairs). |
| `plugin/hooks/` | The hook scripts, `hooks.json`, and the `hooks` Go package that embeds the two git-hook scripts. |

The marketplace installs this directory with Claude Code's `git-subdir`
source (`"source": "git-subdir"`, `"path": "plugin"`), which does a blobless
partial clone plus `git sparse-checkout set --cone plugin` — so an install
never fetches `cmd/`, `internal/`, `tests/`, `docs/`, `scripts/`, `.github/`,
or this repo's own `.punt-labs/` and `.claude/` working state. Cone mode does
always materialize the files in the *repo root*, so the large root documents
(`DESIGN.md`, `CHANGELOG.md`, `prfaq.pdf`) still travel with an install; only
whole directories are excluded.

Two rules follow from that, and both are load-bearing:

- **The plugin surface must not reach outside itself at runtime.** A hook
  script may use `$HOME`, the `ethos` binary, and paths under the *consumer's*
  repo root; it may not reference a file elsewhere in this repo, because that
  file will not exist on an installed plugin. `${CLAUDE_PLUGIN_ROOT}` is
  `plugin/`.
- **`make dev` symlinks `$(CURDIR)/plugin`**, not the repo root, so a
  development install sees the same `CLAUDE_PLUGIN_ROOT` layout a real install
  does.

The `hooks` Go package (`plugin/hooks`) is inside the plugin surface because
`go:embed` cannot reach above its own package directory and it embeds
`pre-commit.sh`/`commit-msg.sh`. Its three `.go` files ship with the plugin
and are inert there; Claude Code ignores files it does not recognize.

### Package Map

| Package | Responsibility |
|---------|---------------|
| `cmd/ethos/` | CLI entry point: identity, attribute, session, and admin commands |
| `internal/identity/` | Core identity model, validation, CRUD, attribute resolution |
| `internal/attribute/` | Generic CRUD for named markdown files (talents, personalities, writing styles) |
| `internal/process/` | Process tree walker: find topmost Claude ancestor PID |
| `internal/session/` | Session roster model, store with flock-based concurrency |
| `internal/resolve/` | Identity resolution chain: repo-local → global → error |
| `internal/hook/` | Hook handlers (SessionStart, PreCompact, SubagentStart/Stop, SessionEnd, PreToolUse, PostToolUse), agent generation, format output |
| `internal/doctor/` | Installation health checks |
| `internal/role/` | Role model, CRUD, layered store |
| `internal/team/` | Team model, CRUD, layered store, referential integrity enforcement |
| `internal/mission/` | Mission contracts, pipelines, archetypes, write-set enforcement, result artifacts, event log |
| `internal/bundle/` | Team bundle discovery, resolution, validation (three-layer: repo → bundle → global) |
| `internal/seed/` | Embedded starter content (roles, talents, archetypes, pipelines, bundles) deployed by `ethos seed` |
| `internal/adr/` | ADR model and storage backing the `adr` MCP tool |
| `internal/ui/` | Localhost web UI for traceability data (`ethos ui`). Embedded Go HTTP server + html/template + Tailwind CDN. Reads missions, delegations, audit trails from `.punt-labs/ethos/`. |
| `internal/mcp/` | MCP tool definitions and handlers (12 tools) |
| `internal/mcpclass/` | MCP tool-name classification (read-only / writes-outside-repo / writes-in-repo); single source of truth shared by the DES-069 verifier deny (`internal/hook`) and the build-time grant check (`cmd/validate-content`) |

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
| ADRs | `~/.punt-labs/ethos/adrs/<id>.yaml` | No |
| Repo bundles | `.punt-labs/ethos-bundles/<name>/` | Yes |
| Missions | `<repo>/.punt-labs/ethos/missions/<id>/` | Yes |
| Mission traces | `<repo>/.punt-labs/ethos/missions.jsonl` | Yes |
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
- **Pipeline instantiation validates before creation.** If any stage fails validation (missing worker, evaluator, or archetype), zero missions are created. During Create, missions are persisted sequentially; if a later stage fails, earlier stages remain and must be cleaned up manually before retrying.

## Go Standards

Module path: `github.com/punt-labs/ethos`. Follows [Go standards](https://github.com/punt-labs/punt-kit/blob/main/standards/go.md).

## Operational Constraints

- **Never self-install.** Do not run `make install` from inside Claude Code. The ethos binary is loaded by Claude Code's process tree; macOS will not allow overwriting a running binary. Ask the user to run `make install` from their shell. Use `.tmp/ethos` for testing.
