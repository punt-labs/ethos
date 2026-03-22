# Build Plan: Hook Standards Compliance

Audit date: 2026-03-22. Standard: `punt-kit/standards/hooks.md`.

## Current State

Ethos has 5 hook shell scripts totaling ~387 lines. All business logic lives
in shell — there are no Go hook handlers (`cmd/ethos/hook.go` does not exist).
The `suppress-output.sh` script (205 lines) is the worst offender: it contains
10 per-tool formatters in a shell if/elif chain. Additionally, the tool name
checks in `suppress-output.sh` reference old individual tool names
(`list_talents`, `get_personality`) that no longer exist — the MCP tools were
consolidated into `talent`, `personality`, `writing_style`, `session`, and `ext`
with a `method` parameter. This means two-channel display is broken for all
consolidated tools.

### MCP Tools (9 total)

| Tool | Type | Methods |
|------|------|---------|
| `whoami` | Identity | — |
| `list_identities` | Identity | — |
| `get_identity` | Identity | — |
| `create_identity` | Identity | — |
| `talent` | Attribute | create, list, show, delete, add, remove |
| `personality` | Attribute | create, list, show, delete, set |
| `writing_style` | Attribute | create, list, show, delete, set |
| `session` | Session | iam, roster, join, leave |
| `ext` | Extension | get, set, del, list |

### Audit Findings (9 issues)

| # | Severity | Issue |
|---|----------|-------|
| 1 | CRITICAL | `suppress-output.sh` is 205 lines of business logic |
| 2 | CRITICAL | Tool name mismatch — two-channel display unreachable for consolidated tools |
| 3 | VIOLATION | Session/subagent hooks contain business logic (79, 44, 30, 29 lines) |
| 4 | VIOLATION | JSON extraction via grep/cut instead of jq or CLI |
| 5 | VIOLATION | No per-tool sentinel file check |
| 6 | VIOLATION | Missing delete method handlers for talent, personality, writing_style |
| 7 | WARNING | No open-pipe regression tests |
| 8 | WARNING | SessionStart forks ethos binary 3-4 times (potential >1s cold start) |
| 9 | WARNING | No functional hook tests in quality gates |

---

## Changes

### Step 1: Add `ethos hook` CLI subcommand group

**Files**: `cmd/ethos/hook.go` (new), `cmd/ethos/main.go` (register)

Add a hidden `hook` command group with subcommands that accept JSON on stdin
and emit structured JSON on stdout. This is the Go equivalent of the Python
`hooks.py` + `_hook_entry.py` pattern from the standard.

Subcommands:

| Subcommand | Hook Event | Replaces |
|------------|-----------|----------|
| `ethos hook session-start` | SessionStart | `session-start.sh` lines 20-66 |
| `ethos hook session-end` | SessionEnd | `session-end.sh` lines 18-27 |
| `ethos hook subagent-start` | SubagentStart | `subagent-start.sh` lines 18-41 |
| `ethos hook subagent-stop` | SubagentStop | `subagent-stop.sh` lines 18-26 |
| `ethos hook format-output` | PostToolUse | `suppress-output.sh` lines 15-204 |

Each subcommand:

- Reads JSON from stdin using a non-blocking reader (equivalent to the
  Python `_read_hook_input()` pattern but in Go — no blocking on EOF)
- Contains the business logic currently in shell
- Returns structured JSON to stdout
- Is testable with table-driven tests

**Verification**: `go build ./cmd/ethos/` compiles. `ethos hook --help` lists
the subcommands.

### Step 2: Add non-blocking stdin reader

**Files**: `internal/hook/stdin.go` (new), `internal/hook/stdin_test.go` (new)

Go equivalent of the Python `_read_hook_input()` pattern. Uses `select`-style
polling with a timeout to avoid blocking when Claude Code leaves the pipe open.

```go
func ReadInput(r io.Reader, timeout time.Duration) (map[string]any, error)
```

Tests:

- Valid JSON returns parsed map
- Empty input returns empty map
- Open pipe without EOF returns within timeout (200ms max)
- Malformed JSON returns empty map (no error)

**Verification**: `go test ./internal/hook/` passes, including the open-pipe
regression test.

### Step 3: Implement `ethos hook session-start`

**Files**: `internal/hook/session_start.go` (new),
`internal/hook/session_start_test.go` (new)

Moves logic from `session-start.sh` lines 20-66 into Go:

1. Read stdin JSON, extract `session_id`
2. Run identity resolution (call into `internal/resolve`)
3. Create session roster (call into `internal/session`)
4. Emit `hookSpecificOutput` with `additionalContext`

This eliminates 3-4 binary forks (whoami, resolve-agent, session create,
session write-current) by calling Go functions directly. Addresses finding #8
(cold start performance).

**Verification**: Table-driven tests with mock stores. Measure cold start:
`time ethos hook session-start < test-input.json` — must be <500ms.

### Step 4: Implement `ethos hook session-end`

**Files**: `internal/hook/session_end.go` (new),
`internal/hook/session_end_test.go` (new)

Moves logic from `session-end.sh` lines 18-27 into Go:

1. Read stdin JSON, extract `session_id`
2. Delete session roster
3. Delete current PID file

**Verification**: Table-driven tests. Shell script reduced to 3-line gate.

### Step 5: Implement `ethos hook subagent-start`

**Files**: `internal/hook/subagent_start.go` (new),
`internal/hook/subagent_start_test.go` (new)

Moves logic from `subagent-start.sh` lines 18-41 into Go:

1. Read stdin JSON, extract `agent_id`, `agent_type`, `session_id`
2. Resolve persona from agent_type
3. Join session roster

**Verification**: Table-driven tests. Shell script reduced to 3-line gate.

### Step 6: Implement `ethos hook subagent-stop`

**Files**: `internal/hook/subagent_stop.go` (new),
`internal/hook/subagent_stop_test.go` (new)

Moves logic from `subagent-stop.sh` lines 18-26 into Go:

1. Read stdin JSON, extract `agent_id`, `session_id`
2. Leave session roster

**Verification**: Table-driven tests. Shell script reduced to 3-line gate.

### Step 7: Implement `ethos hook format-output`

**Files**: `internal/hook/format_output.go` (new),
`internal/hook/format_output_test.go` (new)

Moves all logic from `suppress-output.sh` into Go. Fixes:

- **Finding #2**: Uses correct consolidated tool names (`talent`, `personality`,
  `writing_style`, `session`, `ext`) with method dispatch
- **Finding #6**: Adds handlers for delete methods

Tool dispatch table:

| Tool Name | Method | Summary Format | Full Context? |
|-----------|--------|---------------|--------------|
| `whoami` | — | `Name (handle) — kind` + bindings | Yes |
| `list_identities` | — | Comma-separated handles | Yes |
| `get_identity` | — | Multi-line field summary | Yes |
| `create_identity` | — | `Created <name>` | Yes |
| `talent` | list | Comma-separated slugs | Yes |
| `talent` | show | Markdown content | Yes |
| `talent` | create | `Created <slug>` | Yes |
| `talent` | delete | `Deleted <slug>` | No |
| `talent` | add | Simple result | No |
| `talent` | remove | Simple result | No |
| `personality` | list | Comma-separated slugs | Yes |
| `personality` | show | Markdown content | Yes |
| `personality` | create | `Created <slug>` | Yes |
| `personality` | delete | `Deleted <slug>` | No |
| `personality` | set | Simple result | No |
| `writing_style` | list | Comma-separated slugs | Yes |
| `writing_style` | show | Markdown content | Yes |
| `writing_style` | create | `Created <slug>` | Yes |
| `writing_style` | delete | `Deleted <slug>` | No |
| `writing_style` | set | Simple result | No |
| `session` | roster | `Roster loaded` | Yes |
| `session` | iam | Simple result | No |
| `session` | join | Simple result | No |
| `session` | leave | Simple result | No |
| `ext` | get | `Extensions` | Yes |
| `ext` | list | `Extensions` | Yes |
| `ext` | set | Simple result | No |
| `ext` | del | Simple result | No |

**Verification**: Table-driven tests with sample MCP JSON payloads for every
tool × method combination. Verify each produces valid `hookSpecificOutput` JSON
with correct `updatedMCPToolOutput` and `additionalContext` fields.

### Step 8: Reduce shell scripts to thin gates

**Files**: All 5 scripts in `hooks/`

Replace each script with the standard thin gate pattern:

```bash
#!/usr/bin/env bash
# hooks/<event>.sh — Thin gate for <event> hook
[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0
[[ -d "$HOME/.punt-labs/ethos" ]] || exit 0
command -v ethos >/dev/null 2>&1 || exit 0
ethos hook <event> 2>/dev/null || true
```

Stdin passthrough is implicit — the shell script does not consume stdin,
so it flows through to the Go handler via the pipe.

This addresses:

- **Finding #1**: suppress-output.sh drops from 205 lines to 5
- **Finding #3**: session/subagent hooks drop from 30-79 lines to 5
- **Finding #4**: No more grep/cut JSON parsing in shell
- **Finding #5**: Per-tool sentinel check added (line 3)

**Verification**: `shellcheck hooks/*.sh` passes. Each script is ≤6 lines.
`wc -l hooks/*.sh` confirms total line count < 30 (was 387).

### Step 9: Add hook integration tests

**Files**: `internal/hook/integration_test.go` (new)

Integration tests that exercise the full stdin → Go handler → stdout chain:

1. **Open-pipe regression test**: Write JSON to pipe, do NOT close write end.
   Verify handler returns within 200ms. (Finding #7)
2. **Per-tool format test**: For each of the 9 MCP tools × all methods, feed
   sample `PostToolUse` JSON and verify output contains `updatedMCPToolOutput`
   and (where applicable) `additionalContext`. (Finding #9)
3. **Error handling**: Feed malformed JSON, verify graceful degradation.
4. **MCP error passthrough**: Feed `is_error: true`, verify hook returns no
   output (lets Claude Code show the error).

**Verification**: `make check` passes. `go test -v ./internal/hook/` shows
all test cases.

### Step 10: Measure and verify cold start

**Verification only** — no code changes expected.

Measure SessionStart hook cold start with the installed binary:

```bash
echo '{"session_id":"test-bench"}' | time ethos hook session-start
```

Target: <500ms. If >1s, investigate (likely the session store flock or
identity resolution). The consolidation from 4 binary forks to 1 should
bring this well under the threshold.

Record the measurement in the PR description.

---

## Dependency Order

```text
Step 1 ──→ Step 2 ──→ Steps 3-6 (parallel) ──→ Step 7 ──→ Step 8
                                                              │
                                                              ↓
                                                          Step 9 ──→ Step 10
```

- Steps 3-6 can be implemented in parallel (independent handlers)
- Step 7 (format-output) depends on Step 2 (stdin reader) but not on 3-6
- Step 8 (thin gates) depends on all handlers being complete and includes
  the sentinel check (finding #5)
- Step 9 (tests) depends on Step 8 (full chain must be wired)
- Step 10 (measurement) depends on Step 9 (installed binary)

## Commit Strategy

One commit per step. Each commit must pass `make check`.

| Commit | Message |
|--------|---------|
| 1 | `feat(hooks): add hook CLI subcommand group` |
| 2 | `feat(hooks): add non-blocking stdin reader with open-pipe safety` |
| 3 | `refactor(hooks): move session-start logic from shell to Go` |
| 4 | `refactor(hooks): move session-end logic from shell to Go` |
| 5 | `refactor(hooks): move subagent-start logic from shell to Go` |
| 6 | `refactor(hooks): move subagent-stop logic from shell to Go` |
| 7 | `refactor(hooks): move format-output logic from shell to Go, fix tool name mismatch` |
| 8 | `refactor(hooks): reduce shell scripts to thin gates` |
| 9 | `test(hooks): add integration and open-pipe regression tests` |
| 10 | `docs(hooks): record cold-start measurement` |

## PR Strategy

Single PR. The refactor is mechanical — each step preserves behavior (except
Step 7 which fixes broken behavior). The thin-gate reduction in Step 8 is the
breaking change that requires all Go handlers to be complete first.

## Findings → Steps Traceability

| Finding | Step |
|---------|------|
| 1. suppress-output.sh business logic (CRITICAL) | 7, 8 |
| 2. Tool name mismatch (CRITICAL) | 7 |
| 3. Session/subagent business logic (VIOLATION) | 3-6, 8 |
| 4. grep/cut JSON parsing (VIOLATION) | 2, 3-6, 8 |
| 5. No sentinel check (VIOLATION) | 8 |
| 6. Missing delete handlers (VIOLATION) | 7 |
| 7. No open-pipe regression tests (WARNING) | 2, 9 |
| 8. SessionStart cold start (WARNING) | 3, 10 |
| 9. No functional hook tests (WARNING) | 9 |
