# Mission Abandon

`ethos mission abandon <id-or-prefix> --reason <text>` retires a
mission that was created (via `mission create` or `mission dispatch`)
but never actually had a worker spawned against it. It is a distinct
command from `mission close`, not a bypass of it.

## Problem

`ethos mission close` is gated: it refuses the terminal transition
unless a result artifact exists for the mission's current round
(`internal/mission/store.go`, `checkResultGateLocked`). That gate is
correct and stays correct — a leader's final verdict on a mission must
be backed by the worker's structured output, not prose left in chat,
and there is no override flag because the whole point of the gate is
that it cannot be bypassed.

But that gate has a side effect: a mission contract that was written
and persisted, then never actually dispatched to a worker (the create
event fires, no delegation ever spawns, no result is ever submitted),
can never close. `mission create` refuses a new mission whose
`write_set` overlaps any *open* mission — including this dead one —
so the dead mission permanently reserves its `write_set` for every
future `mission create` call that would touch the same paths. There
was previously no legitimate way to retire it: `close` correctly
refuses (no result), and there was no other terminal transition.

This is not a hypothetical. Confirmed live in one session:
`m-2026-08-06-002`'s predecessors `m-2026-05-23-006` and
`m-2026-05-23-007` hit exactly this pattern — single create event,
zero delegations, close refused — and the same shape blocked real work
concurrently in the `quarry` and `lux` repos the same night.

## Design

`Store.Abandon(missionID, reason)` (`internal/mission/store.go`) is a
new, separately gated operation. It is not a flag on `Close`.

**The gate.** Abandon succeeds only when all of the following hold:

1. The mission is currently `open` (not already terminal — closed,
   failed, escalated, or already abandoned).
2. Zero entries exist under the mission's
   `.punt-labs/ethos/missions/<id>/delegations/` directory, at any
   verdict. Any entry — even one that already closed pass or fail —
   proves a worker was actually spawned, which means there may be
   recoverable work; that is `close`'s territory, not abandon's.
3. Zero result artifacts exist, for **any** round, not only the
   mission's current round. A result recorded for an earlier round
   the mission has since advanced past is still recoverable work.

Any failing condition refuses the transition with a specific error
naming which condition failed and pointing at `mission close` as the
remediation. There is no override flag, for the same reason `close`
has none: the gate is the whole point. Weakening it would let a
leader retire a mission that has recoverable work sitting on disk.

**Why the check is on existence, not verdict.** A delegation record
that already closed with a `pass` verdict still proves a worker ran —
discarding that mission's history via abandon rather than close would
lose the audit trail linking the work to its outcome. Abandon is only
for missions where nothing happened at all.

**Why a distinct terminal status.** `Store.Abandon` transitions the
contract to `status: abandoned` — a value distinct from `closed`,
`failed`, and `escalated`. Those three all carry a verdict backed by a
result artifact; `abandoned` means the opposite: no work, no verdict,
nothing to have judged. Keeping the value distinct lets `mission list
--status=abandoned` and the trace log (`missions.jsonl`) separate
"this mission finished" from "this mission never started" without an
auditor cross-referencing the event log for every closed row.

**Why this actually fixes the blocking bug.** `checkWriteSetConflicts`
only considers missions whose `Status == StatusOpen`
(`internal/mission/store.go`). Abandon moves the mission's status out
of `open` in the same locked section that commits the `abandon` event.
No change to the conflict checker was needed — excluding abandoned
missions from future conflict scans is a consequence of the status
transition, not a separate fix.

**Why `Close` explicitly refuses `--status abandoned`.** `abandoned`
is a member of `validStatuses` (needed for `Validate` to accept a
persisted abandoned contract), which would otherwise let a caller pass
`ethos mission close <id> --status abandoned` and route around
Abandon's stricter gate entirely — closing a mission with a real
result on record straight into the "nothing to see here" status.
`Store.Close` refuses `status == StatusAbandoned` explicitly, alongside
its existing refusal of `status == StatusOpen`.

## Usage

```bash
# CLI
ethos mission abandon m-2026-08-06-002 --reason "created via dispatch, worker never spawned"

# MCP
mission(method="abandon", mission_id="m-2026-08-06-002",
        reason="created via dispatch, worker never spawned")
```

`--reason` (CLI) / `reason` (MCP) is required and is recorded on the
`abandon` event's `details.reason` field — the audit trail records
*why* the mission was retired, not just that it was. `ethos mission
log <id>` renders it as `reason="..."` alongside the timestamp and
actor, the same way `close` renders `status=`/`verdict=`/`round=`.

## When to use abandon vs. close

| Situation | Command |
|---|---|
| A worker was spawned, ran, and submitted a result | `mission close` |
| A worker was spawned but hasn't submitted a result yet | Neither — submit a result first, then `mission close` |
| A mission contract exists but no worker was ever spawned against it | `mission abandon` |
| You are not sure whether a worker was spawned | Run `ethos mission log <id>` — if the only event is `create`, it's safe to abandon; if there are `result`, `reflect`, or `round_advanced` events, or `close` has ever been attempted, use `close` after submitting a result |

The safety invariant in one sentence: zero delegations and zero
results means nothing to lose, which means it is safe to retire
without a verdict.
