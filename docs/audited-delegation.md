# Audited Delegation

Reference for the DES-054 audited-delegation feature surface. Every
`Agent` tool call is now bound to a delegation record. Tool calls
under that delegation are tagged in the session audit log so a single
spawn — and every sub-spawn it produces — can be reconstructed
cross-session.

This document is the working reference: command surface, contract
fields, storage layout, refusal reasons. For the design rationale see
DESIGN.md (DES-054). For low-level shipped changes see
`CHANGELOG.md` under `## [Unreleased]`.

## Why

A bare `Agent(...)` call produced no durable trace. There was no way
to answer:

- which sub-agent was spawned at 14:32 and what did it touch?
- did that spawn stay inside its parent's write-set?
- how deep did the spawn chain go before it timed out?
- where is the audit log for a spawn that ran in a session three days ago?

Audited delegation closes that loop. Every `Agent` call now produces a
delegation record and tags subsequent audit entries with its
`delegation_id`. Tier B (contract-bound) spawns additionally honour
preconditions, depth limits, and the `MISSION_ARTIFACTS_DIR`
write-target convention.

## Two Tiers

| Tier | When | Audit trail | Enforcement |
|------|------|-------------|-------------|
| A | `Agent(...)` with no `MISSION_ID` env | `delegation_id` tagged on every tool call | None — advisory stderr line, spawn always allowed |
| B | `MISSION_ID` env set, OR inherited from a parent contract | Same audit tagging + per-delegation `record.yaml` on disk | Preconditions, depth limit, write-set, hash gate |

Tier A is the "ad-hoc" path — a spawn nobody wrote a contract for.
Tier B is the contract-bound path — `ethos mission dispatch`, or a
child whose parent contract authorized its spawn via `delegations:`.

### Telling them apart

In the audit log, every entry under a Tier B spawn carries
`contract_id`. Tier A entries carry `delegation_id` but no
`contract_id`. On disk:

| Tier | What persists | Where |
|------|---------------|-------|
| A | `delegation_id` only; audit log entries are tagged with it | `<repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<session-id>/audit.jsonl` |
| B | Per-delegation `record.yaml` (and optional `prompt.md`) under the mission tree, plus the same audit tagging | `<repo>/.punt-labs/ethos/missions/<mission-id>/delegations/<delegation-id>/` |

Tier A does **not** write a `record.yaml`. Its forensic trail is
exclusively the audit log filtered by `delegation_id`.

Tier A's advisory line (suppress with `ETHOS_QUIET_ADVICE=1`):

```text
ethos: ad-hoc Agent spawn (no mission contract). Consider 'ethos mission dispatch' for governed delegation. (set ETHOS_QUIET_ADVICE=1 to silence)
```

## Dispatch

`Agent` tool calls route through the `PreToolUse` hook. Routing rule:

1. `MISSION_ID` env set → Tier B by explicit dispatch.
2. `MISSION_ID` unset, `PARENT_DELEGATION_ID` set → try Tier B by
   inheritance (walks the parent contract's `delegations[]` for a
   matching `spawn_pattern`; falls through to Tier A on any miss or
   error).
3. Neither set → Tier A.

On every path the hook emits an `additional_env` block consumed by
the spawned worker:

| Env var | Tier A | Tier B |
|---------|--------|--------|
| `DELEGATION_ID` | set | set |
| `PARENT_DELEGATION_ID` | set (= `DELEGATION_ID`) | set (= `DELEGATION_ID`) |
| `PARENT_SESSION_ID` | set | set |
| `MISSION_ID` | unset | set |
| `MISSION_ARTIFACTS_DIR` | unset | `<repo>/.punt-labs/ethos/missions/<mission-id>/delegations/<delegation-id>/` |

The worker inherits these. Any `Agent` call the worker subsequently
makes carries `PARENT_DELEGATION_ID` and `PARENT_SESSION_ID` into the
child spawn — that's how the chain reconstructs.

## Tier B Inheritance

A parent contract with a `delegations:` entry can lend itself to a
child spawn:

```yaml
delegations:
  - role: implementation
    spawn_pattern: "bwk|rsc"
    inherits_contract: true
  - role: review
    spawn_pattern: "djb"
    inherits_contract: true
```

Walk semantics:

- `PARENT_DELEGATION_ID` resolves the immediate parent record.
- The hook loads the parent's contract and scans `delegations[]` in
  order; first entry where `MatchSpawnPattern(spawn_pattern, child)`
  returns true AND `inherits_contract: true` lends its `mission` to
  the child.
- No match → walk the parent's `parent_delegation` to the next
  ancestor. Continues until either a match is found or the chain
  ends.
- Bounded by `max_delegation_depth` (default 16) with a cycle guard.

`spawn_pattern` is anchored at both ends — `^(?:<pattern>)$`. A
pattern of `bw.` matches the three-character agent type `bwk`, not
`bwk-junior`.

Inheritance is **non-blocking**. Every error along the walk (record
not on disk, malformed regex on a non-current entry, contract load
failure, walk overflow) writes a stderr warning and falls through to
Tier A. The spawn still runs.

## Preconditions

Tier B contracts can require that specific files be Read in the
session before any non-Read tool call:

```yaml
preconditions:
  - form: implicit
    message: "Read the target file before editing it"

  - form: explicit
    require_read:
      - DESIGN.md
      - ${inputs.references.0}
    message: "Read DESIGN.md and the first reference before writing"

strict_preconditions: true   # default; explicit false opts into warn-mode
```

### Forms

**Implicit** — the tool input's target paths must have been Read in
this session. Paths come from `file_path`, `notebook_path`, and any
`files`/`paths` array entries on the call.

**Explicit** — each entry in `require_read` must have been Read in
this session, after `${inputs.X}` substitution against the
contract's `inputs:` block.

### Substitution

Supported `${inputs.X}` keys:

| Key | Resolves to |
|-----|-------------|
| `${inputs.ticket}` | `inputs.ticket` |
| `${inputs.files.N}` | `inputs.files[N]` |
| `${inputs.references.N}` | `inputs.references[N]` |

Anything else (`${env.X}`, `${vars.Y}`, malformed `${...}`) is
rejected at evaluate time. `${inputs.files.7}` against a four-element
`files` list errors with `index out of range [0,4)`.

### strict_preconditions

Controls behaviour when a predicate is **unevaluable** (malformed
substitution, missing input, unreadable audit log) — not when it is
*violated*. A violated predicate always blocks.

| Value | Unevaluable predicate |
|-------|----------------------|
| `true` (default, including unset) | block with `message` |
| `false` | warn to stderr, allow |

`*bool` semantics: the on-disk shape distinguishes "field unset" (default
strict) from "field explicitly false" (explicit opt-out for migration
from pre-DES-054 contracts).

### Validation

`ethos mission create` and `mission lint` enforce these rules on
preconditions:

- `form` must be `implicit` or `explicit`.
- `message` must be non-empty. A failed gate is never silently named.
- `explicit` requires non-empty `require_read`.
- Each `require_read` entry follows the same per-entry rules as
  `write_set`, with `${inputs.X}` placeholders permitted in the path.

## Delegations Templates

The `delegations:` field on a Tier B contract authorizes child spawns
under the same contract:

```yaml
delegations:
  - role: implementation
    spawn_pattern: "bwk"
    inherits_contract: true
    extract_into:
      - internal/session/
  - role: review
    spawn_pattern: "djb|rop"
    inherits_contract: true
```

Per-entry fields:

| Field | Required | Meaning |
|-------|----------|---------|
| `role` | yes | Human-readable role this template authorizes |
| `spawn_pattern` | yes | Anchored regex; match against the child's `agent_type` |
| `inherits_contract` | no (default false) | If true, child runs Tier B under this contract |
| `extract_into` | no | New-file-creation allowlist for the child (DES-052 axis) |

Patterns are validated at admission time:
`Contract.Validate` compiles each `spawn_pattern` under the same
`^(?:...)$` form `MatchSpawnPattern` uses at runtime, so a malformed
regex is rejected at `mission create` rather than at match time. An
empty pattern is allowed (never matches).

## Commands

### `ethos audit show --delegation <id>`

Cross-session forensic query. Walks the repo-tree session audit logs
(with legacy global fallback) and filters by `delegation_id`.

```text
Usage: ethos audit show --delegation <id> [--format json|text]

Flags:
  --delegation <id>   delegation id to filter on (required)
  --format <fmt>      "json" (one JSONL line per entry, default) or
                      "text" (ts<TAB>tool<TAB>file_path-or-preview)

Exit codes:
  0  success (including zero matching entries)
  2  must run inside a repo
```

```bash
$ ethos audit show --delegation d-2026-05-23-007 --format text
2026-05-23T14:32:11Z    Read    internal/hook/preconditions.go
2026-05-23T14:32:54Z    Read    internal/hook/preconditions_test.go
2026-05-23T14:33:08Z    Edit    internal/hook/preconditions.go
2026-05-23T14:33:42Z    Bash    {"command":"go test ./internal/hook/...","timeout":120000}
```

JSON output is one entry per line, matching the on-disk audit JSONL
shape — `ethos audit show` output is itself a valid audit-log fragment
and pipes cleanly into `jq`.

### `ethos audit migrate`

One-time relocation of legacy global audit logs into the DES-054 v5
repo-tree layout.

```text
Usage: ethos audit migrate [--dry-run] [--verbose]

Scans ~/.punt-labs/ethos/sessions/*.audit.jsonl (the v3.11 layout)
and copies each file's entries into the v3.12+ repo-tree layout
under <repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<id>/audit.jsonl, then
deletes the legacy file once every entry has landed and been
fsynced.

Idempotent: a second run on the same machine is a no-op. Cross-repo
safe: a legacy session whose id has no matching repo-tree directory
is left in place — it belongs to a different repo's work tree.

Flags:
  --dry-run   show what would change without writing or deleting
  --verbose   print one decision line per session to stdout

Exit codes:
  0  migration completed (including the no-op "nothing to migrate" case)
  1  one or more sessions failed mid-copy; both sources stayed in place
  2  must run inside a repo (no <repo>/.punt-labs/ethos/sessions/ destination)
```

```bash
$ ethos audit migrate --verbose
sess-abc123: migrated 47 entries to .punt-labs/ethos/sessions/2026-05-22-sess-abc123/audit.jsonl
sess-def456: skipped (no matching repo-tree session)
sess-ghi789: already migrated (no-op)
audit migrate: complete
```

### `ethos mission migrate --to-repo`

Atomic per-mission relocation from `~/.punt-labs/ethos/missions/<id>.yaml`
(legacy global) to `<repo>/.punt-labs/ethos/missions/<id>/contract.yaml` (DES-054
repo tree).

```text
Usage: ethos mission migrate [mission-id] [--to-repo] [--dry-run] [--verbose]

Without a mission-id argument, every legacy mission whose contract_id
is referenced by an audit entry in <repo>/.punt-labs/ethos/sessions/ is moved.
Missions belonging to other repos' work trees are left in place —
cross-repo policy.

Idempotent: a mission already migrated is a no-op. Atomic per-mission:
the move stages artifacts in a sibling temp directory and renames into
place; a failure before the rename leaves the legacy tree intact.

Flags:
  --to-repo   migrate into the per-repo .punt-labs/ethos/missions/ tree (default
              and currently the only target)
  --dry-run   show what would change without writing or deleting
  --verbose   print one decision line per mission to stdout

Exit codes:
  0  migration completed (including the no-op "nothing to migrate" case)
  1  one or more missions failed mid-migration; legacy files stayed in place
  2  must run inside a repo
```

```bash
$ ethos mission migrate --dry-run --verbose
m-2026-05-22-001: would migrate (contract + 3 result rounds + 2 reflections)
m-2026-05-22-002: would skip (already migrated)
mission migrate: dry-run complete
```

## Commit-Msg Trailer Hook

`hooks/commit-msg.sh` appends `Mission: <id>` and `Delegation: <id>`
git trailers to every commit message when `MISSION_ID` and
`DELEGATION_ID` are set in the environment. Idempotent — re-running
on a message already carrying the trailer is a no-op.

### Installation

`install.sh` installs the hook automatically when run inside a git
work tree. It copies `hooks/commit-msg.sh` to `.git/hooks/commit-msg`
and refuses to clobber an unrelated existing hook (no-op + warning if
`.git/hooks/commit-msg` does not contain `DES-054`).

Manual install:

```bash
cp hooks/commit-msg.sh .git/hooks/commit-msg
chmod +x .git/hooks/commit-msg
```

### Behaviour

```bash
$ MISSION_ID=m-2026-05-23-001 DELEGATION_ID=d-2026-05-23-007 \
    git commit -m "feat(hook): handle empty session id"
$ git log -1 --format=%B
feat(hook): handle empty session id

Mission: m-2026-05-23-001
Delegation: d-2026-05-23-007
```

The hook prefers `git interpret-trailers` and falls back to a plain
append when git is unavailable, when `mktemp` fails, or when the
rename onto the message file fails. Both paths produce a contiguous
trailer block with no interleaved blank lines.

Querying:

```bash
git log --grep="Mission: m-2026-05-23-001"
git log --grep="Delegation: d-2026-05-23-007" --format=%H
```

## Tier B Refusals

Tier B dispatch can return `decision=block` to the Claude Code hook
runtime. Ten named refusal reasons, each surfaced to the operator:

| Trigger | Reason format |
|---------|---------------|
| Mission store unreachable | `ethos pre-tool-use: resolving mission store: <err>` |
| Malformed / missing `MISSION_ID` | `ethos pre-tool-use: resolving MISSION_ID "<id>": <err>` |
| Delegation ID allocation failure | `ethos pre-tool-use: allocating delegation id: <err>` |
| Mission lock acquire | `ethos pre-tool-use: acquiring mission lock for "<id>": <err>` |
| Global root unreachable (for delegation lock) | `ethos pre-tool-use: resolving global root for delegation lock: <err>` |
| Delegation lock acquire | `ethos pre-tool-use: acquiring delegation lock for "<id>": <err>` |
| Skeleton write failure | `ethos pre-tool-use: writing delegation skeleton for "<id>": <err>` |
| Config resolution error | `ethos pre-tool-use: resolving max_delegation_depth: <err>` |
| Depth-walk error | `ethos pre-tool-use: walking parent_delegation chain for "<id>": <err>` |
| `max_delegation_depth` exceeded | `ethos pre-tool-use: max_delegation_depth <limit> exceeded by depth <proposed> for "<id>"` |

The SubagentStart hash gate (DES-033 evaluator-hash verification) can
also refuse a Tier B verifier spawn. When that refusal fires AFTER
the dispatch hook has already written the skeleton, the
`closeSkeletonOnHashRefusal` helper closes the record with
`verdict=aborted` so no skeleton is leaked at `verdict=open`.

Every depth-refusal path closes the just-written skeleton with
`verdict=aborted` before returning the block. A depth refusal is
visible in `ethos audit show --delegation <id>` as a Tier B record
with `verdict: aborted` and no subsequent tool calls.

## max_delegation_depth

The depth ceiling applied when walking the `parent_delegation`
chain. Default 16 — leaves room for orchestrator → worker → reviewer →
fixer → follow-up chains without absorbing a runaway recursive spawn
pattern.

Override per repo in `.punt-labs/ethos.yaml`:

```yaml
max_delegation_depth: 32
```

A value of `0` means "use the default". A negative value surfaces as
a refusal at dispatch time (no silent clamp). The depth walker uses
`max+1` as a cycle-detection backstop so a chain longer than the
configured limit fails closed rather than spinning.

## Audit Log Shape

Every tool call appends one JSONL line. The DES-054 enrichment added
eight optional fields (`parent_session`, `agent_id`, `agent_type`,
`delegation_id`, `parent_delegation`, `contract_id`, `tool_input`,
`tool_input_hash`) to the v3.11 line shape (existing parsers tolerate
the addition because every new field is `omitempty`):

| Field | Meaning |
|-------|---------|
| `ts` | RFC3339 UTC timestamp |
| `session` | Session ID |
| `parent_session` | Parent session ID (for nested spawns) |
| `agent_id` | Identity handle of the running agent |
| `agent_type` | Claude Code agent type for sub-agent spawns |
| `delegation_id` | Delegation this call falls under |
| `parent_delegation` | Immediate parent delegation in the chain |
| `contract_id` | Mission contract ID (Tier B only) |
| `tool` | Tool name (Read, Edit, Write, Bash, ...) |
| `tool_input` | Full canonical-JSON tool input map |
| `tool_input_hash` | sha256 of canonical-JSON `tool_input` |
| `tool_input_preview` | 200-char grep-friendly preview |

`tool_input_hash` lets a post-hoc query detect two callers writing
the same target — the DES-052 Stat-Write race detector relies on it.

Each line is `f.Sync()`'d. The reader is partial-line-tolerant: a
truncated final line from a crashed writer is skipped, not fatal.

## Storage Layout

### Per-mission (Tier B)

```text
<repo>/.punt-labs/ethos/missions/<mission-id>/
├── contract.yaml
├── log.jsonl
├── results.yaml
├── reflections.yaml
├── .lock                              # per-mission shared flock
└── delegations/
    └── <delegation-id>/
        ├── record.yaml                # delegation skeleton + verdict
        └── prompt.md                  # optional prompt body
```

### Per-session (audit log)

```text
<repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<session-id>/
└── audit.jsonl                        # one line per tool call;
                                       # entries tagged with delegation_id
                                       # (both Tier A and Tier B)
```

Tier A spawns have no on-disk record beyond their audit-log entries.

### Global (counters + flocks)

```text
~/.punt-labs/ethos/
├── counters/
│   ├── missions-YYYY-MM-DD            # single int, flock-guarded
│   └── delegations-YYYY-MM-DD
└── delegations/
    └── <delegation-id>.lock           # per-delegation exclusive flock
```

Per-delegation locks live in the global tree; the per-mission shared
lock lives in the repo tree at
`<repo>/.punt-labs/ethos/missions/<mission-id>/.lock`. Two checkouts of the
same repo must lock the same inode for delegation allocation — a
per-checkout lock under `.punt-labs/ethos/` would let two clones write the same
delegation_id.

### Legacy fallback

Reads check the repo tree first, then fall back to the v3.11 global
layout:

| Artifact | Legacy path (read-only fallback) |
|----------|----------------------------------|
| Mission contract | `~/.punt-labs/ethos/missions/<mission-id>.yaml` |
| Mission log | `~/.punt-labs/ethos/missions/<mission-id>.jsonl` |
| Mission results | `~/.punt-labs/ethos/missions/<mission-id>.results.yaml` |
| Mission reflections | `~/.punt-labs/ethos/missions/<mission-id>.reflections.yaml` |
| Session audit log | `~/.punt-labs/ethos/sessions/<session-id>.audit.jsonl` |

`List` deduplicates with repo-wins precedence.

## Migration from v3.11

The migration is opt-in and idempotent. New work lands in the repo
tree automatically (no migration needed). Existing global artifacts
stay readable in place until you migrate them.

Recommended order:

```bash
# 1. Move legacy session audit logs into the repo tree.
ethos audit migrate --verbose

# 2. Move legacy missions into the repo tree.
ethos mission migrate --verbose
```

Both commands are safe to run on a repo that has nothing to migrate
(no-op) and on a repo that has already been migrated (no-op). Both
honour `--dry-run`.

Cross-repo policy: a session or mission whose id is not referenced
by any audit entry in the current repo's `.punt-labs/ethos/sessions/` tree is
treated as "belongs to another checkout" and left alone.

## Cross-Session Forensics

> **Q: What did delegation `d-2026-05-23-007` do?**

```bash
ethos audit show --delegation d-2026-05-23-007 --format text
```

Returns every tool call across every session that ran under that
delegation, in timestamp order.

> **Q: Did this delegation stay inside its write-set?**

```bash
ethos audit show --delegation d-2026-05-23-007 --format json \
    | jq -r 'select(.tool=="Edit" or .tool=="Write") | .tool_input.file_path'
```

Cross-reference against `contract.yaml` `write_set` + `extract_into`.

> **Q: Which commits came out of this delegation?**

```bash
git log --grep="Delegation: d-2026-05-23-007" --format="%H %s"
```

> **Q: What's the full spawn chain ending at this delegation?**

The `parent_delegation` field on each `record.yaml` walks the chain
upward. There is no canned command yet — read records in sequence:

```bash
yq '.parent_delegation' \
    "$repo/.punt-labs/ethos/missions/$mid/delegations/d-2026-05-23-007/record.yaml"
```

## Concurrency Model

Lock acquisition order (LIFO release via `defer`):

```text
per-mission (LOCK_SH, repo tree) → per-delegation (LOCK_EX, global tree)
```

The per-mission flock lives in the repo tree at
`<repo>/.punt-labs/ethos/missions/<mission-id>/.lock`; the per-delegation flock
lives in the global tree at
`~/.punt-labs/ethos/delegations/<delegation-id>.lock` so two checkouts
of the same repo lock the same inode.

The per-mission lock is **shared** so two Tier B spawns under one
mission do not serialize. The per-delegation lock is **exclusive**
so the skeleton write is the sole writer for its ID. A hypothetical
mission-close that wants the tree quiescent can take `LOCK_EX` on
the per-mission lock and will wait for every shared holder to
release.

Atomic write contract for record files (`record.yaml`, `prompt.md`):

```text
os.CreateTemp(dir, "record-*.yaml.tmp")
  → Write
  → Chmod(0o600)
  → Sync         (fsync the bytes; djb gate — half-written is unacceptable)
  → Close
  → Rename       (atomic on the same filesystem)
```

A predictable `.tmp` suffix is avoided so two writers cannot trample
each other's intermediate files. The temp file is removed on every
error path.

## See Also

- `CHANGELOG.md` `## [Unreleased]` — every shipped change, by phase
- `DESIGN.md` (DES-054) — the design rationale and invariants
- `docs/architecture.tex` — the architecture diagram
- `AGENTS.md` `## Missions` — mission contract overview
- `docs/mission-archetypes.md` — archetype constraints
