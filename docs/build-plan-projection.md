# Build Plan: Surface Projection Consistency

> **HISTORICAL — SHIPPED**: This build plan is preserved for reference.
> It describes the surface projection consistency work (CLI, MCP, slash
> commands, Go API naming and behavior alignment per
> [`go-projection-standard.md`](go-projection-standard.md) and DES-020).
> That work has landed; the current CLI and MCP surface definitions are
> in [`architecture.tex`](architecture.tex) §CLI Surface and §MCP Tool
> Surface.
>
> **Current build planning lives in
> [`ETHOS-ROADMAP.md`](ETHOS-ROADMAP.md)**. Do not use this file as a
> source of truth for current priorities.

---

Close naming and behavioral gaps across CLI, MCP, slash commands, and
Go API per `docs/go-projection-standard.md` and DES-020 (formatted text,
not raw JSON).

## Beads

### Bead 1: Quick fixes (typo, flags, columns)

Small independent fixes that can ship together.

| Change | What | Files |
|--------|------|-------|
| Fix "Skill slug" → "Talent slug" | MCP tool description typo | `internal/mcp/attribute_tools.go` |
| Add `--reference` to `ethos whoami` | CLI parity with MCP `reference=true` | `cmd/ethos/main.go` |
| Add WRITING column to CLI `list` | CLI parity with MCP `writing_style` field | `cmd/ethos/main.go` |
| Add `ethos session roster` alias | Canonical verb alignment | `cmd/ethos/session.go` |

**Agent**: mdm (CLI specialist)

### Bead 2: CLI identity command group

Add `ethos identity <method>` subcommand group that matches MCP structure.
Hide old top-level shortcuts except `whoami`.

| Command | Maps to | Visible |
|---------|---------|---------|
| `ethos identity whoami` | `runWhoami` | Yes |
| `ethos identity list` | `runList` | Yes |
| `ethos identity get <handle>` | `runShow` | Yes |
| `ethos identity create` | `runCreate` | Yes |
| `ethos whoami` | `runWhoami` | Yes (brand shortcut) |
| `ethos list` | `runList` | Hidden |
| `ethos show` | `runShow` | Hidden |
| `ethos create` | `runCreate` | Hidden |

**Agent**: mdm
**Files**: `cmd/ethos/main.go`

### Bead 3: Move iam from identity to session

`iam` calls `session.Store.Join()` — it belongs on the session tool.

MCP changes:

- Add `iam` method to `session` tool enum
- Move `handleIam` from identity handler to session handler
- Remove `iam` from `identity` tool enum
- Add `persona` and `session_id` params to session tool (if not already)

CLI changes:

- `ethos session iam <persona>` — canonical form
- `ethos iam <persona>` — hidden shortcut (backward compat)

Slash command changes:

- Remove `iam` from identity command docs
- Add `iam` to session command docs (both prod + dev variants)

Hook formatter changes:

- Move `iam` case from `formatIdentity` to `formatSession` in
  `format_output.go`

**Agent**: bwk (MCP tool changes) + mdm (CLI + commands)
**Depends on**: Bead 2 (identity group must exist first so hidden
`ethos identity iam` doesn't get registered)

### Bead 4: Session list + doctor MCP

Two independent features that add missing surface coverage.

**Session list** (`ethos session list`):

- Show summary per session: ID, participant count, primary persona
- Load each session with `session.Store.Load()` for detail
- `--json` output

**Doctor MCP tool**:

- Standalone MCP tool (not method-dispatched — admin, not resource)
- Returns formatted text (DES-020): check name, status, detail per line
- PostToolUse formatter: panel = pass/fail count, context = check table
- Verify `ethos doctor --json` works first

**Agent**: mdm (CLI session list) + bwk (doctor MCP tool)

### Bead 5: Documentation

Final documentation pass after all code changes.

- Update CLAUDE.md with surface projection section (verb mappings,
  convention clashes, canonical forms)
- Add DES-021: session create/delete are CLI-only (hook internals,
  deferred decision for MCP promotion)
- Update README.md CLI command reference
- Update AGENTS.md tool reference

**Agent**: me (COO — cross-cutting docs)
**Depends on**: Beads 1-4

## Dependency Order

```text
Bead 1 (quick fixes) ─────────────┐
Bead 2 (identity group) ──────────┤── independent
Bead 4 (session list + doctor) ───┘
         │
         ↓
Bead 3 (move iam) ────── depends on Bead 2
         │
         ↓
Bead 5 (docs) ─────────── after all code
```

Beads 1, 2, 4 can run in parallel.
Bead 3 depends on Bead 2.
Bead 5 is last.

## PR Strategy

Single PR for all 5 beads. Each bead is one commit.

## Reviewer Summary

All changes incorporate findings from:

- mdm surface audit (9 inconsistencies)
- bwk Go review (3 errors, 4 misleading items)
- Architect review (5 concerns, 3 gaps)
- DES-020 (formatted text, not raw JSON)
