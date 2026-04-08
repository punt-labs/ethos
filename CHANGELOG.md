# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Bounded rounds with mandatory reflection** (Phase 3.4,
  `ethos-07m.8`) — `Contract` gains a `current_round` field, and a
  new `Reflection` type plus the `Store.AdvanceRound` gate make
  `Budget.Rounds` enforceable. Reflections are typed (round, author,
  converging, signals, recommendation enum, reason) and append-only:
  `Store.AppendReflection` refuses duplicate rounds, refuses
  reflections whose round does not match the contract's
  `current_round`, and refuses reflections on closed missions. The
  round-advance gate refuses to bump `current_round` when (a) the
  current round has no reflection on disk, (b) the reflection
  recommends `stop` or `escalate` (the leader's `reason` is surfaced
  verbatim), or (c) advancing would exceed `Budget.Rounds`.
  Reflections live alongside the contract in a sibling
  `<id>.reflections.yaml` file so the contract itself stays pure.
  Three new `mission` subcommands wire this to operators —
  `ethos mission reflect <id> --file <path>` to record a reflection,
  `ethos mission advance <id>` to bump the round, and
  `ethos mission reflections <id>` to dump the round-by-round log.
  The `mission` MCP tool gains matching `reflect`, `advance`, and
  `reflections` methods, each with a formatter in
  `internal/hook/format_output.go` (DES-020). The new
  `round_advanced` and `reflect` events are appended to the
  per-mission JSONL log so the round lifecycle is auditable. The
  Phase 3.1/3.2/3.3 primitives are untouched: existing `Validate`
  rules still hold, the cross-mission write_set conflict scan still
  runs, and the frozen-evaluator hash and verifier-spawn gate are
  unchanged. To unblock a `stop`/`escalate` refusal or a
  budget-exhausted refusal, close the mission via
  `ethos mission close <id>` (optionally `--status failed` or
  `--status escalated`) and create a replacement contract with a
  revised scope or budget.
- **Frozen evaluator with content hash pinning** (Phase 3.3,
  `ethos-07m.7`) — `Store.ApplyServerFields` now resolves the
  evaluator handle through the live identity, role, and team stores
  and pins a sha256 of the resolved content (DES-033) into
  `Contract.Evaluator.Hash`. The hash covers personality, writing
  style, talents (in declaration order), and every (team, role)
  assignment for the evaluator (sorted lexicographically). Resolution
  failure is fatal: a mission whose evaluator cannot be loaded fails
  create with no on-disk artifacts. The SubagentStart hook
  (`HandleSubagentStartWithDeps`) recomputes the hash on every
  verifier spawn and refuses spawns whose pinned hash disagrees with
  the current content. The mismatch error aggregates every drifted
  open mission into a single multi-line block, showing the pinned
  and current rollup hash prefixes alongside a per-section breakdown
  (personality, writing_style, each talent, each role) so the
  operator can see which source file they edited. To recover from a
  drift failure, revert the edit to the evaluator's identity content
  or close and relaunch the mission(s) with the new content. Pre-3.3
  missions with empty `Evaluator.Hash` are allowed through with a
  stderr warning so the upgrade path remains clean.
  Closed/failed/escalated missions are out of the gate's purview.
  New `internal/mission/hash.go` defines the deterministic hash
  function, the `IdentityLoader`/`RoleLister` interfaces, and the
  `EvaluatorHashBreakdown` struct returned by
  `ComputeEvaluatorHashBreakdown`; `NewLiveHashSources` adapts the
  live stores. CLI and MCP create paths build the same `HashSources`
  so contracts launched from either surface produce identical hashes.
- **Write-set admission control** (Phase 3.2, `ethos-07m.6`) —
  `Store.Create` rejects a new mission whose `write_set` overlaps any
  currently-open mission's `write_set`. Overlap is segment-prefix on
  cleaned paths: `internal/foo` blocks `internal/foo/bar.go` but
  `internal/foo` does NOT block `internal/foobar`. Closed, failed,
  and escalated missions are out of the registry. A new
  directory-level create lock (`<missionsDir>/.create.lock`)
  serializes the conflict scan and the write so two concurrent
  Creates with disjoint mission IDs cannot both pass the scan and
  both write. The error names every blocking mission by ID, worker,
  and overlapping path — one line per blocker. CLI exit code is 1;
  MCP returns a structured tool error. To unblock, close the named
  mission or re-scope the new contract's write_set to disjoint paths.
- `mission` subcommand for creating, listing, and closing mission
  contracts — the typed delegation artifact that is the foundation
  of Phase 3 workflow primitives (`ethos-07m.5`). Creation is
  file-only (`--file <path>`); strict YAML decode with unknown-field
  rejection. Non-JSON mode is silent on success; `--json` emits the
  persisted contract.
- `mission` MCP tool with methods create, show, list, close. Full
  formatter in `internal/hook/format_output.go` (DES-020) sharing a
  single `text/tabwriter` layout with the CLI so the two surfaces
  cannot drift.
- `internal/mission/` package: Contract schema, filesystem store with
  flock-protected concurrency, shallow-copy Update semantics (failed
  Update leaves the caller's struct untouched), strict KnownFields
  YAML decode on every read path (Load and Close's loadLocked) so
  attacker-dropped fields cannot slip past the on-disk trust
  boundary, daily ID generator with `[1, 999]` counter bounds that
  reject exhaustion and poisoned counter files, append-only JSONL
  event log, and validation enforcing mission ID format, RFC3339
  timestamps, control-character rejection in identity handles and
  write_set entries, path-traversal and null-byte rejection,
  round budget bounds, and required fields. The Evaluator's
  `pinned_at` is server-controlled at create time on every entry
  point (CLI and MCP).
- `internal/mission/filter.go` — shared `StatusMatches` helper used by
  both the CLI and the MCP list handlers.

## [2.8.0] - 2026-04-04

### Fixed

- **Linux hook stdin hang (DES-029)** — all 6 hook shell scripts hung silently on Linux because Claude Code spawns hooks via `/bin/sh -c`, making `/dev/stdin` inaccessible to the Go binary. Shell scripts now read stdin with `IFS= read -r -t 1` and forward via `printf | binary` over a fresh pipe. Works on both Linux and macOS.
- **Go stdin fallback for non-pollable fds** — `readWithTimeout` in `internal/hook/stdin.go` uses a single `f.Read` (not `io.ReadAll`) when `SetReadDeadline` fails on Linux inherited pipe fds. Defense-in-depth behind the shell-level fix.

### Added

- **Fake Claude spawn regression test** — `TestShellScript_SessionStart` reproduces Claude Code's exact hook execution path using Node.js `spawn(shell: true)`. Fails if any hook script uses `< /dev/stdin`. CI requires `actions/setup-node`.
- **Subprocess integration tests** — 6 tests spawning the real ethos binary for each hook event (SessionStart, PreCompact, SubagentStart, SubagentStop, SessionEnd, open-pipe hang).
- **Linux process tests** — 14 tests for `/proc` filesystem parsing: comm truncation, spaces/parentheses in comm, version-named binary normalization, symlink behavior.
- **DES-029** — ADR documenting the root cause chain: Node.js `spawn(shell: true)` → `/bin/sh -c` → `/dev/stdin` inaccessible on Linux.
- **DES-030** — ADR documenting the subprocess integration test strategy and why in-process `os.Pipe()` tests gave false confidence.

## [2.7.0] - 2026-04-04

### Added

- **Baseline operational skill** — `internal/seed/sidecar/skills/baseline-ops/SKILL.md` provides operational discipline (tool usage, verification, scope, security) for sub-agents that lose the default system prompt
- **6 starter roles** — implementer, reviewer, researcher, architect, security-reviewer, test-engineer with tools, responsibilities, and model preferences; available in `internal/seed/sidecar/roles/` for teams to reference or copy
- **10 starter talents** — go, python, typescript, security, code-review, testing, cli-design, api-design, documentation, devops; substantial domain expertise (200-800 lines each) available in `internal/seed/sidecar/talents/` for teams to reference or copy
- **`model` field on Role** — roles can specify a preferred Claude model (opus, sonnet, haiku, inherit); `GenerateAgentFiles` includes it in agent frontmatter; validated against allowlist on save and load
- **Agent definitions guide** — `docs/agent-definitions.md` covering separation of concerns, anti-responsibilities, tool restrictions, baseline ops, output contracts, scope enforcement, context hygiene, common anti-patterns
- **Team setup guide** — `docs/team-setup.md` for third-party users creating teams from scratch
- **Mission skill design** — `docs/mission-skill-design.md` specifying the `/mission` structured delegation skill
- **ETHOS-ROADMAP.md** — 5-phase roadmap with 24 work items across batteries included, production agents, workflow, operational excellence, and ecosystem
- **Persona/role/mission three-layer model** — documented in agent-definitions.md and README as the core thesis for effective agents
- DES-027 (Teams/Roles as first-class concepts) and DES-028 (Persona animation) ADRs
- **`ethos seed` command** — deploys embedded starter roles, talents, skills, and READMEs to `~/.punt-labs/ethos/` and `~/.claude/skills/`; uses `go:embed` so content is available on all install paths; `--force` to overwrite existing files
- **Installer auto-seeds** — `install.sh` calls `ethos seed` after plugin install to deploy starter content automatically

### Changed

- **architecture.tex** — full rewrite from v0.3.3 to v2.6.1 (18 sections, 1388 lines covering all 11 packages)
- **AGENTS.md** — added team/role MCP tools, extension session context, rewrote stale Identity Resolution and Storage Layout sections
- **CLAUDE.md** — added 4 missing packages and 4 missing storage rows to architecture tables
- **README.md** — added Setup, Documentation, and three-layer model sections; added extension session context to features
- DES-015 status updated from PROPOSED to SETTLED

### Fixed

- **CHANGELOG.md** — Unreleased link corrected from v0.3.4 to v2.6.1; added 20 missing version comparison links
- **persona-animation.md** — version v2.2.2 corrected to v2.3.0; updated for DES-022
- **agent-teams.md** — version v2.2.2 corrected to v2.3.0; removed stale ps reference
- **agent-identity-spec.tex** — updated to v2.6.1; SubagentStart extension gap documented as shipped; staleness check updated to content comparison
- **Sidecar READMEs** — skills→talents in 3 files; path references updated to slugs

## [2.6.1] - 2026-04-01

### Fixed

- `SubagentStart` hook now injects extension `session_context` (quarry memory, vox voice, etc.) into sub-agent context

## [2.6.0] - 2026-03-29

### Added

- SessionStart hook generates `.claude/agents/<handle>.md` from ethos identity, personality, writing-style, and role data — agent definitions stay in sync with ethos source automatically
- `tools` field on role YAML schema — source of truth for sub-agent tool restrictions

## [2.5.0] - 2026-03-29

### Added

- `repo` and `host` fields on session roster — populated at session creation from git remote and hostname
- `joined` timestamp on each participant — set when joining via `iam` or `join`
- `session list` shows REPO column
- `session show` shows JOINED column and repo/host in header

### Changed

- `session list`: short session IDs (8 chars), human-readable timestamps (`Sun Mar 29 14:22`)
- `session show` replaces `session roster` as canonical verb (`roster` kept as hidden alias)
- `session show`: accepts session ID argument (full or prefix), infers role (root/primary/teammate)
- `session iam`: requires `--session` when no Claude Code process tree found

### Removed

- ACTIVE column from `ethos identity list`, MCP identity list, and hook output — only showed local session state, missed identities active on other hosts

## [2.4.1] - 2026-03-28

## [2.4.0] - 2026-03-28

## [2.3.0] - 2026-03-28

### Fixed

- **Agent team PID discovery**: `FindClaudePID()` failed for agent team teammates because Claude Code's version-named binary (e.g., `~/.local/share/claude/versions/2.1.86`) has a version string as its filename. PID discovery on macOS (`kern.procargs2`) and Linux (`/proc/<pid>/exe`) now normalizes versioned Claude binaries to the `claude` comm. Teammates now get working ethos sessions with full persona injection.

### Changed

- **PreCompact hook**: emit full persona block + team context instead of condensed 4-line summary — personality, writing style, role, team members with responsibilities, and collaboration graph all survive context compaction
- **PreCompact handler**: refactored to accept `PreCompactDeps` struct with identity, session, team, and role stores
- **PreCompact formatting**: deduplicate opening sentence from personality section, strip redundant top-level headings, skip bullet-only/indented content in sentence extraction
- **Team context**: include writing style summary and talent slugs for each team member
- **CLAUDE.md**: added delegation discipline, collaboration model (Agent Teams + Biff), and workflow tiers (T1/T2/T3)

### Added

- `BuildTeamContext` function — assembles team context block with member names, roles, responsibilities, and collaborations
- Repo config `team:` field — links `.punt-labs/ethos.yaml` to a team definition for automatic team context in hooks
- `skipFirstParagraph`, `stripLeadingHeading`, `isNonProse` helpers for clean markdown processing in PreCompact output
- `docs/agent-teams.md` — comprehensive documentation of Claude Code agent teams: process model, communication, task list format, team config, hook behavior, PID discovery, session behavior, and lifecycle

## [2.2.1] - 2026-03-26

### Fixed

- **PreCompact hook schema**: use top-level `systemMessage` instead of unsupported `hookSpecificOutput` for PreCompact events — fixes validation errors during context compaction

## [2.2.0] - 2026-03-26

### Added

- **Repo-level config**: `.punt-labs/ethos.yaml` for repo-specific identity config (agent, team bindings). Decoupled from the team submodule path. Backward-compatible fallback to old `.punt-labs/ethos/config.yaml`
- **Team-by-repo lookup**: `ethos team for-repo [repo]` CLI command and `for_repo` MCP method — query which team works on a given repository
- `FindByRepo` on team Store and LayeredStore
- `RepoName()` helper — parses org/repo from git remote URL
- `LoadRepoConfig` and `ResolveTeam` in resolve package
- **Role** as first-class concept: `internal/role/` package, `ethos role` CLI, `role` MCP tool, LayeredStore
- **Team** as first-class concept: `internal/team/` package, `ethos team` CLI with add-member/remove-member/add-collab, `team` MCP tool with 8 methods, Z-spec invariant enforcement (referential integrity, non-empty teams, no self-collaboration, dangling collab cleanup)

### Fixed

- `attribute.Store.isNotFound` TOCTOU race — replaced redundant `os.Stat` with `errors.Is`
- `team.LayeredStore.Load` redundant `Exists` call — replaced with `ErrNotFound` sentinel
- Referential integrity check on role deletion — cannot delete a role referenced by a team
- Z specification for teams/roles/identities domain (`docs/teams.tex`) — type-checks with fuzz, animates with probcli

## [2.1.0] - 2026-03-25

### Added

- **Persona animation**: SessionStart hook injects full personality, writing style, and talent slugs into session context
- **PreCompact hook**: re-injects condensed persona block before context compression so behavioral instructions survive compaction
- **SubagentStart persona injection**: subagents with matched identities (e.g., bwk, mdm) get their persona injected automatically at spawn
- Shared persona builder (`BuildPersonaBlock`, `BuildCondensedPersona`) for consistent formatting across all hooks
- Attribute resolution warnings logged to stderr in all hook handlers

### Changed

- Agent definitions (bwk.md, mdm.md) no longer need manual `ethos show` instructions — persona is injected by hooks
- CLAUDE.md Go standards section replaced with reference to org-wide `punt-kit/standards/go.md`

## [2.0.0] - 2026-03-25

### Changed

- **Breaking**: Renamed `persona` parameter to `handle` in all ext commands (CLI, MCP, and Go API `IdentityStore` `Ext*` methods) for consistency with other identity commands
- **Breaking**: Removed `voice` field from identity YAML — voice config now lives in `ext/vox` (DES-019). Auto-migration on Load handles existing files.
- Identity resolution is now layered: repo-local (`.punt-labs/ethos/`) → user-global (`~/.punt-labs/ethos/`) (DES-018)
- All CLI commands and MCP tools use layered resolution by default

### Added

- `LayeredStore` — two-layer identity store with repo-local priority
- `IdentityStore` interface — enables layered and concrete stores interchangeably
- `FindRepoEthosRoot()` — discovers `.punt-labs/ethos/` in the current git repo
- Repo-local identity, talent, personality, and writing style files for the ethos team (claude, jfreeman, bwk)
- `bwk` agent identity — Go specialist sub-agent based on Kernighan's principles

## [1.2.0] - 2026-03-23

## [1.1.0] - 2026-03-22

### Changed

- Consolidated `whoami`, `list_identities`, `get_identity`, `create_identity` into single `identity` MCP tool with `method` parameter
- Refactored all 5 hook shell scripts to thin gates per punt-kit/standards/hooks.md (387 → 30 lines)
- Moved hook business logic from shell to Go (`internal/hook/` package)
- Fixed two-channel display for consolidated MCP tools (tool name mismatch)

### Added

- `ethos hook` CLI subcommand group (session-start, session-end, subagent-start, subagent-stop, format-output)
- Non-blocking stdin reader with open-pipe-no-EOF safety (`internal/hook/stdin.go`)
- `make dev` / `make undev` targets for plugin cache symlink during development (DES-015)
- Delete method handlers for talent, personality, writing_style in two-channel display
- Per-tool sentinel directory check in hook shell scripts

## [1.0.0] - 2026-03-21

### Changed

- Renamed `skill` → `talent` system-wide (DES-014): MCP tool, CLI subcommand, identity YAML field, storage directory, all documentation
- Identity YAML field: `skills:` → `talents:` (breaking — update identity files manually)
- Storage directory: `~/.punt-labs/ethos/skills/` → `~/.punt-labs/ethos/talents/`
- CLI: `ethos skill` → `ethos talent`
- MCP tool: `skill` → `talent`
- Command: `/ethos:skill` → `/ethos:talent`

## [0.8.0] - 2026-03-21

### Added

- `/ethos:list-identities`, `/ethos:get-identity`, `/ethos:create-identity` slash commands

### Changed

- All slash commands namespaced under `/ethos:*` (DES-012) — no top-level deployment to `~/.claude/commands/`

### Removed

- Top-level command deployment from session-start hook (DES-013)
- `jq` settings mutation from session-start hook (DES-013)
- Users upgrading from v0.7.0 must manually delete ethos files from `~/.claude/commands/`

## [0.7.0] - 2026-03-21

## [0.6.0] - 2026-03-21

### Added

- `ethos resolve-agent` command — prints the default agent handle from repo config
- `Store.FindBy(field, value)` — lookup identities by `handle`, `email`, or `github` field
- `resolve.Resolve(store, sessionStore)` — 4-step identity resolution chain: iam declaration → git user.name → git email → $USER
- `resolve.ResolveAgent(repoRoot)` — reads agent handle from `.punt-labs/ethos/config.yaml`
- `resolve.GitConfig(key)` — reads git config values via `git config` subprocess
- `ethos doctor` checks: "Human identity", "Default agent", "Duplicate fields"
- Per-repo agent config via `.punt-labs/ethos/config.yaml` `agent:` field
- Native process tree walking: `/proc/<pid>/stat` on Linux, `sysctl kern.proc.pid` on macOS (replaces `ps -eo` subprocess)

### Changed

- `ethos whoami` is now read-only — resolves identity from iam/git/OS instead of reading `~/.punt-labs/ethos/active`
- `ethos list` marks all session participants with `*` (multiple markers possible)
- `ethos create` no longer auto-sets first identity as active
- MCP `whoami` tool uses `resolve.Resolve()` instead of `Store.Active()`
- MCP `list_identities` marks session participants (multiple `"active": true` entries possible)
- MCP `create_identity` no longer auto-activates first identity
- Session start hook resolves human and agent personas separately
- Usage text now writes to stderr (was stdout, caused garbage in shell captures)

### Removed

- `~/.punt-labs/ethos/active` file — human identity comes from git/OS
- `ethos whoami <handle>` write path — no "set active" operation
- `Store.Active()` method
- `Store.SetActive()` method
- `ErrNoActive` sentinel error
- `RepoConfig.Active` field — repos are multi-user, human identity is per-user
- `ps -eo pid=,ppid=,comm=` subprocess for process tree walking
- 13 attribute MCP tools (`create_skill`, `get_skill`, etc.) replaced by 3 (`skill`, `personality`, `writing_style`) with `method` parameter
- 4 ext MCP tools (`ext_get`, `ext_set`, etc.) replaced by 1 (`ext`) with `method` parameter
- 4 session MCP tools (`session_iam`, `session_roster`, etc.) replaced by 1 (`session`) with `method` parameter

### Added (slash commands)

- `/ethos:skill` — create, list, show, delete, add, remove
- `/ethos:personality` — create, list, show, delete, set
- `/ethos:writing-style` — create, list, show, delete, set
- `/ethos:ext` — get, set, del, list
- `delete` method on all attribute tools (was not exposed via MCP)

## [0.5.0] - 2026-03-20

### Added

- First-class attribute management: `ethos skill`, `ethos personality`, `ethos writing-style` subcommands with create/list/show/add/remove/set
- `internal/attribute` package — generic CRUD for named markdown files with path containment
- Identity attributes (`writing_style`, `personality`, `skills`) now reference markdown files by slug instead of inline strings
- `Store.Load()` resolves attribute content by default; `Reference(true)` option returns slugs only
- `Store.Update()` for read-modify-write on existing identities
- `Identity.Warnings` field for missing attribute file diagnostics
- `Store.ValidateRefs()` rejects Save when referenced attribute files are missing
- Interactive `ethos create` wizard with pick-from-list and create-new sub-flow for attributes
- 13 new MCP tools: `create_skill`, `get_skill`, `list_skills`, `create_personality`, `get_personality`, `list_personalities`, `create_writing_style`, `get_writing_style`, `list_writing_styles`, `set_personality`, `set_writing_style`, `add_skill`, `remove_skill`
- `reference` parameter on `get_identity` and `whoami` MCP tools
- Installer creates `skills/`, `personalities/`, `writing-styles/` directories

### Changed

- `ethos show` displays resolved attribute content with slug headers
- `ethos show --reference` returns slugs only
- `ethos list` uses reference mode (no attribute file reads)
- `ethos create` interactive prompts replaced with attribute picker wizard

## [0.4.0] - 2026-03-19

### Added

- Architecture specification (`docs/architecture.tex`) — LaTeX document covering package map, identity model, extension system, session roster, resolution chain, MCP tool surface, design invariants, and security boundaries
- `scripts/release-plugin.sh` and `scripts/restore-dev-plugin.sh` per plugins standard
- Homebrew formula: `brew install punt-labs/tap/ethos`

### Changed

- Plugin name on main is now `ethos-dev` (prod name `ethos` set at release time)
- Release tags no longer include `-dev` command files

## [0.3.4] - 2026-03-19

### Fixed

- All hooks rewritten to match biff's proven patterns (DES-009)
- SessionStart: removed `INPUT=$(cat)` stdin read that blocked indefinitely (Claude Code doesn't close pipe promptly)
- Subagent/SessionEnd hooks: replaced `INPUT=$(cat)` with `read -r -t 1` (non-blocking with 1s timeout)
- PostToolUse: kept `INPUT=$(cat)` (pipe closes for this event), removed `set -euo pipefail` for graceful degradation, switched to `jq` for JSON output
- hooks.json: removed empty `"matcher": ""` from all non-PostToolUse hooks
- All hooks: added kill switch, `exit 0`, `hookEventName` in output, `PLUGIN_ROOT` from `dirname` not env var

## [0.3.3] - 2026-03-19

### Fixed

- Removed `set -u` from subagent-start, subagent-stop, session-end, and suppress-output hooks (same bug as session-start fix in v0.3.2)

## [0.3.2] - 2026-03-19

### Fixed

- Installer downloads pre-built release binary instead of `go install` (fixes `ethos dev` version display)
- SessionStart hook: removed `set -u` and bash arrays that crash on bash < 4.4 (fixes `SessionStart:startup hook error`)

## [0.3.1] - 2026-03-19

### Fixed

- Installer now force-updates marketplace before plugin install (prevents stale version)
- Installer verifies installed plugin version matches expected version with mismatch warning

## [0.3.0] - 2026-03-19

### Added

- `/session` and `/iam` slash commands with `-dev` variants for plugin parity
- MCP tool permission auto-allow in SessionStart hook (matches biff pattern)
- Persistent PATH setup in installer — appends `~/.local/bin` to shell profile

### Changed

- Refactored `main()` command dispatch from switch to map (cyclomatic complexity 22 → 10)
- Extracted `voiceValue()`, `joinSkills()`, `showExtensions()` from `runShow()` (complexity 16 → 6)
- Applied `gofmt -s` to `identity_test.go`
- SessionStart hook detects dev mode and skips top-level command deployment when running as `ethos-dev`

### Fixed

- `ethos doctor` no longer fails on fresh install when no active identity exists (PASS with guidance instead of FAIL)
- `whoami.md` command: `allowed-tools` corrected from bare string to array
- PostToolUse suppress hook now returns meaningful per-tool summaries instead of generic "Done."

## [0.2.0] - 2026-03-19

### Added

- `ethos uninstall` command — removes Claude Code plugin; `--purge` removes binary and all identity data with confirmation
- Release and Go Report Card badges in README

### Changed

- Installer rewritten with SSH fallback, marketplace check-before-register, uninstall-before-install, post-install verification, temp dir cleanup trap, and conditional doctor success message
- Replaced personal identity data in docs and tests with Firefly characters (Mal Reynolds, River Tam)
- `warn()` and `fail()` in install.sh now output to stderr

## [0.1.0] - 2026-03-18

### Added

- Session roster for multi-participant identity awareness (DES-007)
  - `internal/session/` package with `Store` (flock-based concurrency), `Roster`, and `Participant` types
  - `internal/process/` package for process tree walking (find topmost Claude ancestor PID)
  - `ethos iam <persona>` command to declare persona in current session
  - `ethos session` commands: `create`, `join`, `leave`, `purge`, and default roster display
  - MCP tools: `session_iam`, `session_roster`, `session_join`, `session_leave`
  - Hooks: `SubagentStart`, `SubagentStop`, `SessionEnd` lifecycle hooks
  - PID-keyed current session files for session ID propagation to non-hook callers
  - Extended `SessionStart` hook to create session roster with root + primary participants
- Initial project scaffolding — Go module, CLI entry point, Makefile, CI workflows
- Identity YAML schema with channel bindings (voice, email, GitHub, agent)
- `ethos version` and `ethos doctor` admin commands
- `ethos create` — interactive and declarative (`--file`/`-f`) identity creation
- `ethos whoami`, `ethos list`, `ethos show` — identity management commands
- MCP server (`ethos serve`) with 4 tools: `whoami`, `list_identities`, `get_identity`, `create_identity`
- `install.sh` — POSIX installer (build from source, plugin registration, doctor)
- GitHub branch protection ruleset with zero bypass actors
- Dependabot, secret scanning, and push protection enabled
- `--json` global flag for machine-readable output on `whoami`, `list`, `show`, `doctor`, `version`
- Subcommand-level `--help`/`-h` with per-command usage text
- `--` separator support in global flag parsing
- 49 tests across 4 packages (84-96% coverage on internal/ packages)

### Fixed

- `show` now displays writing_style, personality, and skills fields
- `show` collapses multi-line YAML values to single-line display
- `show` handles empty VoiceID without trailing slash
- Duplicate identity error no longer references non-existent `edit` command
- Home directory resolution returns errors instead of empty string on `$HOME` failure
- `doctor` distinguishes permission errors from missing directory
- `voice_id` requires `voice_provider` across all creation paths (CLI, MCP, file)
- Malformed identity files produce stderr warnings instead of silent skips
- `Store.Save` uses `O_EXCL` for atomic create (no TOCTOU race)

### Changed

- Consolidated duplicate `Identity` struct into `internal/identity/` with `Store` type
- Extracted MCP handlers from `cmd/ethos/serve.go` to `internal/mcp/tools.go`
- `cmd/ethos/serve.go` reduced from 223 to 25 lines
- `Voice` is a pointer type for proper JSON/YAML omitempty semantics
- Handle regex tightened to disallow trailing hyphens
- `Validate()` expanded with handle format and voice validation
- MCP handlers receive `Store` via injection (no `os.Exit` in handler context)
- ShellCheck added to CI and `make lint`

[Unreleased]: https://github.com/punt-labs/ethos/compare/v2.6.1...HEAD
[2.6.1]: https://github.com/punt-labs/ethos/compare/v2.6.0...v2.6.1
[2.6.0]: https://github.com/punt-labs/ethos/compare/v2.5.0...v2.6.0
[2.5.0]: https://github.com/punt-labs/ethos/compare/v2.4.1...v2.5.0
[2.4.1]: https://github.com/punt-labs/ethos/compare/v2.4.0...v2.4.1
[2.4.0]: https://github.com/punt-labs/ethos/compare/v2.3.0...v2.4.0
[2.3.0]: https://github.com/punt-labs/ethos/compare/v2.2.1...v2.3.0
[2.2.1]: https://github.com/punt-labs/ethos/compare/v2.2.0...v2.2.1
[2.2.0]: https://github.com/punt-labs/ethos/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/punt-labs/ethos/compare/v2.0.0...v2.1.0
[2.0.0]: https://github.com/punt-labs/ethos/compare/v1.2.0...v2.0.0
[1.2.0]: https://github.com/punt-labs/ethos/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/punt-labs/ethos/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/punt-labs/ethos/compare/v0.8.0...v1.0.0
[0.8.0]: https://github.com/punt-labs/ethos/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/punt-labs/ethos/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/punt-labs/ethos/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/punt-labs/ethos/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/punt-labs/ethos/compare/v0.3.4...v0.4.0
[0.3.4]: https://github.com/punt-labs/ethos/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/punt-labs/ethos/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/punt-labs/ethos/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/punt-labs/ethos/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/punt-labs/ethos/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/punt-labs/ethos/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/punt-labs/ethos/releases/tag/v0.1.0
