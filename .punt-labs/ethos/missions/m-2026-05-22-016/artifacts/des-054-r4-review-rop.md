# DES-054 v5 review — convergence test (rop, round 4)

**Verdict: APPROVE.** v5 converges. Zero new substantive findings.
The three CEO-directed simplifications land cleanly; the r3 IMPL
cleanup (N1) is applied; nothing else needs to move.

## What v5 changed, and whether it earned its keep

The brief asks whether the v5 simplifications are the minimum.
I read v5 against v4 with that question in front.

### 1. Date-keyed two-tree layout (`missions/` + `sessions/`)

**Minimum.** Two cohesion axes exist in the data — per-mission
and per-session — and they do not nest cleanly under either
parent. v4 tried a single tree and ended up dragging session
audit logs into mission directories or vice versa. v5 puts each
artifact in exactly one place, with the date folded into the
key (mission ID already carries the date; session dir name now
prepends it). `ls .ethos/sessions/2026-05-22-*/` and `ls
.ethos/missions/m-2026-05-22-*/` work without an index.

The 1:1 with `~/.claude/projects/<...>/<session-id>.jsonl` is the
right invariant. A forensic operator cross-references by the
shared session-id. Nothing more clever is wanted.

I cannot subtract anything from this layout without losing a
property the design depends on.

### 2. Single audit log per session (no per-delegation `audit.jsonl`)

**Minimum.** v4 had two stores (per-session and per-delegation)
and an invariant (`I10-audit-atomic`) that had to talk about
both. v5 has one store; the invariant collapses to one clause;
per-delegation views are recovered by filtering on
`delegation_id`. The filter is `O(audit_lines_in_session)`, which
in practice is small and bounded; if it ever isn't, that's what
`ethos find` (`ethos-pcra`) is for.

The reduction is real: one fewer file per delegation, one fewer
flock dimension, one fewer atomic-write contract, one fewer
migration story. The cost — a filter on read — is the cheap
direction.

### 3. Sibling-file per-namespace per-date counters

**Minimum.** v4's `counter.yaml` with `schema_version` and a
nested map was the file-format break rsc flagged. v5 keeps the
existing `.counter-YYYY-MM-DD` shape verbatim and adds a
namespace dimension to the filename. New namespace = new sibling
file. Old binaries never touch the new files. No schema, no
version, no migration.

This is the correct shape. A file per (namespace, date) is the
smallest representation that supports rollback, per-date browse,
and incremental adoption. There is no smaller form that doesn't
collide.

## r3 carryover

N1 (open-question §2 stale on `max_delegation_depth`) is resolved
in v5 line 20: "stale `max_delegation_depth` open-question
removed per rop R3 N1". Confirmed by reading §Open questions
in v5 (lines 417–423): only three items remain (advice hook
format, cross-tool surface, advice tone). The stale item is gone.

## Convergence

| Round | Findings (this reviewer) | Status |
|---|---|---|
| 1 | 21 (across reviewers; mine: structural + advice hook + scope rule) | All landed in v2/v2+ |
| 2 | 5 (R1 REQ, R2–R5 IMPL) | All landed in v4 |
| 3 | 1 IMPL (N1, trivial) | Landed in v5 |
| 4 | 0 substantive | — |

The design has converged on the Pike-minimalism axis. The
structures are sized to the data; the invariants encode the
properties without redundancy; the migration is specified at the
boundary cases; the rejected alternatives list reads like a
catalog of the right things to have rejected.

I do not have a finding. I would not invent one to fill space.
Approve, ship phase 1.

— rop
