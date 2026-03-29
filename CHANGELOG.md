# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/punt-labs/ethos/compare/v0.3.4...HEAD
[0.2.0]: https://github.com/punt-labs/ethos/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/punt-labs/ethos/releases/tag/v0.1.0
