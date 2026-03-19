# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `/session` and `/iam` slash commands with `-dev` variants for plugin parity
- MCP tool permission auto-allow in SessionStart hook (matches biff pattern)
- Persistent PATH setup in installer — appends `~/.local/bin` to shell profile

### Fixed

- `ethos doctor` no longer fails on fresh install when no active identity exists (WARN instead of FAIL)
- `whoami.md` command: `allowed-tools` corrected from bare string to array
- PostToolUse suppress hook now returns meaningful per-tool summaries instead of generic "Done."

### Changed

- SessionStart hook detects dev mode and skips top-level command deployment when running as `ethos-dev`

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

[Unreleased]: https://github.com/punt-labs/ethos/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/punt-labs/ethos/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/punt-labs/ethos/releases/tag/v0.1.0
