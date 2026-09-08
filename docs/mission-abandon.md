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
2. Zero *blocking* entries exist under the mission's
   `.punt-labs/ethos/missions/<id>/delegations/` directory
   (`countBlockingDelegations`, `internal/mission/store.go`). This was
   originally "zero entries at any verdict" — see the two carve-outs
   below, both added after the gate as first designed proved too broad
   in practice (DES-076, DESIGN.md):
   - **`verdict: aborted` is excluded unconditionally**, independent of
     any disclaim. A delegation refused before its worker ever ran (the
     `max_delegation_depth` guard, or the content-hash verifier gate)
     never represents real work — there is nothing to have judged, and
     nothing `close` could meaningfully close. This exclusion applies
     even to a delegation recorded via the dispatch-sidecar path
     (`BoundVia`); it is keyed on `Verdict`, not on provenance.
   - **An explicitly disclaimed entry is excluded** — see "Disclaiming a
     captured delegation" below. Unlike the aborted exclusion, this one
     is not automatic: it requires the operator to name the specific
     delegation and prove its provenance.
3. Zero result artifacts exist, for **any** round, not only the
   mission's current round. A result recorded for an earlier round
   the mission has since advanced past is still recoverable work. This
   gate has no exception, override, or disclaim path — see
   "What `--disclaim` does NOT do" below.

Any failing condition refuses the transition with a specific error
naming which condition failed and pointing at `mission close` (or
`--disclaim`, when the delegation is provably a sidecar capture) as the
remediation.

**Why the check is on existence, not verdict — except for `aborted` and
an explicit disclaim.** A delegation record that already closed with a
`pass` verdict still proves a worker ran — discarding that mission's
history via abandon rather than close would lose the audit trail
linking the work to its outcome. Abandon is for missions where nothing
happened at all, OR where something happened but is proven, by one of
the two carve-outs above, not to be real work: a refusal before the
worker ran (`aborted`), or a real spawn later proven to have been
misattributed to the wrong mission by the dispatch-sidecar bug
(disclaim).

## Disclaiming a captured delegation

A delegation record does not always mean the mission it is filed under
actually did the work. DES-076 (DESIGN.md) describes a class of bugs
where the active-mission dispatch sidecar misattributed an unrelated
`Agent()` spawn to a mission, writing a delegation record for work that
mission never asked for. `mission abandon --disclaim <delegation-id>`
(CLI) / `disclaim: [<delegation-id>, ...]` (MCP `abandon` method) is the
mechanically-gated exception that lets such a mission still retire.

**The disclaim gate**, checked per named delegation ID
(`Store.DisclaimDelegation`), is NOT a blanket override:

- `BoundVia` must equal the sidecar-dispatch provenance value recorded
  at spawn time — a delegation bound via explicit `MISSION_ID` or
  parent-delegation inheritance is refused by name; only a dispatch-
  sidecar capture is eligible.
- The delegation must already be closed (`Verdict != open`) — a
  delegation still in flight cannot be disclaimed out from under its
  running worker.
- A delegation cannot be disclaimed twice.

Every disclaim is applied to every named delegation ID BEFORE `Abandon`
itself runs, and is permanently recorded on the delegation's own record
and the mission's append-only event log — a disclaim cannot be undone.
If any named delegation fails its gate, or `Abandon` itself then fails
(e.g. a result artifact still exists), the error names every delegation
ID that had already been disclaimed before the failure, since those
commitments cannot be retried as a clean unit with the rest of the call.

**What `--disclaim` does NOT do.** It only ever affects Gate 2
(delegation existence). `Abandon`'s result-artifact gate (Gate 3 above)
is completely untouched by disclaim — a mission with a submitted result
still refuses regardless of any disclaim. There is still no override
flag for that gate, for the same reason `close` has none: it is the
whole point, and weakening it would let a leader retire a mission that
has recoverable work sitting on disk.

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
# CLI, no delegations at all
ethos mission abandon m-2026-08-06-002 --reason "created via dispatch, worker never spawned"

# CLI, disclaiming a dispatch-sidecar capture (repeatable for more than one)
ethos mission abandon m-2026-08-06-002 --reason "captured by the dispatch sidecar bug" \
  --disclaim d-2026-08-06-001

# MCP
mission(method="abandon", mission_id="m-2026-08-06-002",
        reason="captured by the dispatch sidecar bug",
        disclaim=["d-2026-08-06-001"])
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
| Every delegation on record was refused before its worker ran (`verdict: aborted`) | `mission abandon` — no `--disclaim` needed, the aborted exclusion is automatic |
| A delegation is real work, but was misattributed to this mission by the dispatch-sidecar bug (DES-076) | `mission abandon --disclaim <delegation-id>`, once its provenance and closed status are confirmed |
| You are not sure whether a worker was spawned | Run `ethos mission log <id>` — if the only event is `create`, it's safe to abandon; if there are `result`, `reflect`, or `round_advanced` events, or `close` has ever been attempted, use `close` after submitting a result |

The safety invariant in one sentence: zero *blocking* delegations
(after the automatic `aborted` exclusion and any proven-safe
`--disclaim`) and zero results means nothing to lose, which means it is
safe to retire without a verdict.
