# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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
