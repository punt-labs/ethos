# Mission Write-Set Staleness and Recovery

Design for ethos-9x07 (P1): a mission with an overly broad `write_set`
and no visible activity for six days is blocking all new mission
creation in the same tree, in a repo where the leader cannot prove the
stuck mission has zero delegations. This is a design doc only — no
code changes. Implementation is a separate mission, dispatched after
the leader reviews this design.

> **Implementation note (post-review).** "Design part 2" below
> describes `ForceReleaseWriteSet` literally clearing `WriteSet` and
> `ExtractInto`. Local review caught that this refuses for every
> archetype requiring a non-empty `write_set` -- most of them,
> including `implement`, the default and the archetype the reported
> incident mission actually used -- so the mechanism as designed here
> could not fix the case it exists for. The shipped implementation
> instead adds a `WriteSetReleasedAt` marker: `WriteSet`/`ExtractInto`
> stay on the contract unchanged (satisfying validation for every
> archetype), and `checkWriteSetConflicts` skips any mission with the
> marker set. See `docs/mission-force-release-write-set.md` for the
> as-shipped design; this document is preserved as the original
> design record, not updated in place.

## Problem

`ethos-9x07`, reported live in `punt-labs/lux`: `m-2026-07-31-005`
(worker `rmh`, evaluator `gvr`) declared a `write_set` covering almost
all of `src/punt_lux/` and `tests/`. Its only log activity is a single
`reflect` + round-advance on 2026-07-31 00:01 PDT; zero results have
ever been submitted; it sat open for six days. Because
`Store.checkWriteSetConflicts` (`internal/mission/store.go:2214`)
refuses any new `Create` whose `write_set` overlaps an *open*
mission's `write_set` — via `findWriteSetConflicts`
(`internal/mission/conflict.go:72`) — this one mission now blocks every
unrelated `mission create` that touches the same tree, regardless of
which worker the new contract names.

Today there is exactly one way out: `Store.Abandon`
(`internal/mission/store.go:1081`). Its gate is two preconditions, both
of which must hold:

1. Zero entries under `delegations/` for the mission
   (`internal/mission/store.go:1161`, `countDelegations`).
2. Zero result artifacts for any round (`internal/mission/store.go:1178`).

Gate 2 is what actually applies to `m-2026-07-31-005` — no result was
ever submitted. But Abandon's own doc (`docs/mission-abandon.md`) is
explicit that gate 1 exists because "any entry — even one that already
closed pass or fail — proves a worker was actually spawned, which
means there may be recoverable work." The bug as reported is broader
than this one mission: **the leader has no way to check gate 1 without
already knowing the answer.** A delegation record lives under
`.punt-labs/ethos/missions/<id>/delegations/` in the repo tree; the
leader reported this from a *different* session (`claude:tty16`) and
had no visible signal that told them, before running `abandon` and
finding out the hard way, whether a delegation record exists. Worse:
when one *does* exist — a worker was genuinely spawned once, produced
nothing, and the session is long gone — Abandon refuses permanently,
by design, and there is no other terminal or non-terminal recovery
path. `Close` refuses too (no result, `checkResultGateLocked`). The
mission is administratively stuck and its `write_set` claim never
expires.

A second, related gap: even the contract's own `updated_at` field is
not a reliable staleness signal. `Store.AppendReflection`
(`internal/mission/store.go:1778`) writes the reflection to its
sibling YAML and appends a `reflect` event to the log
(`internal/mission/store.go:1836`), but never touches the contract's
`UpdatedAt` field — that only changes on `Store.Update` or a terminal
transition. A mission that reflected once and then went dark shows the
same `updated_at` as it did at creation; the true last-activity
timestamp is buried in the JSONL event log
(`Store.LoadEvents`, `internal/mission/log.go:194`), which nothing
today surfaces as a queryable signal.

`ethos-swoh` (handle-overlap false-positive in `warnHandleOverlap`,
`cmd/ethos/mission.go:1004`) is a related but distinct bug in the same
neighborhood — it fires a warning whenever a handle appears in *any*
other open mission, even one with no `write_set` intersection. It is
being fixed independently, mechanically, in a parallel mission. It is
noted here for context only; this design does not touch
`warnHandleOverlap` or `checkRoleOverlap`
(`internal/mission/store.go:2085`), and no fix for it is proposed
below.

## Constraint: write-sets are a convention, not a sandbox

Before any design choice: `prfaq.tex` §`faq:not-building`
(`\label{faq:not-building}`) states this outright — "Write-sets are
conventions: declared in the mission contract, surfaced to the agent,
and verified in review. Ethos does not enforce them at the filesystem
level. This is deliberate... If agent runtimes add scoped filesystem
access in the future, ethos can emit the constraints for them to
enforce. Until then, convention-plus-review is the only mechanism that
works across all runtimes." The Feature Appendix's Won't-Do list
carries the same line as `feat:no-runtime-enforce`: "write-set and
safety constraints are surfaced to the agent and verified in review,
not enforced at the filesystem level. Kernel-level sandboxing is a
different product." `feat:writeset` describes what admission control
actually is: "segment-prefix conflict scan refuses overlapping file
claims between open missions" — a claim-registry that blocks
*creation* of a conflicting contract, not a runtime sandbox that
blocks a write.

That framing sets the boundary for both pieces of this design:

- **Staleness detection surfaces information. It never acts.** No
  cron, no hook, no auto-abandon, no auto-narrowing. A human (today,
  always the leader) decides.
- **Recovery is a leader-invoked, explicit, audited operation** — the
  same shape as `Store.Abandon`: a distinct method with its own gate,
  its own event type, a mandatory reason, and no override flag that
  weakens an existing gate. It does not fabricate a verdict and does
  not silently discard the original mission's record. It also does not
  cross into filesystem enforcement — it changes what the *admission
  check* (`checkWriteSetConflicts`) sees as claimed, nothing more.

## Design part 1: staleness as a queryable signal

Add a pure function, `mission.Staleness`, next to `NewListEntry`
(`internal/mission/filter.go:52`) rather than inside `Store` — it needs
only data the caller already has (contract, events, results,
delegation count), and keeping it a pure function makes it trivially
testable without a store fixture:

```go
// StalenessInfo summarizes how long a mission has gone without
// activity, for display and for --stale filtering. It is a read-only
// projection: nothing here mutates a contract or triggers an action.
type StalenessInfo struct {
    LastActivityAt   string // RFC3339; max(created_at, updated_at, latest event ts)
    AgeDays          int    // days since LastActivityAt, floor
    HasResults       bool   // len(results) > 0, any round
    DelegationCount  int    // entries under delegations/, best-effort
    DelegationsKnown bool   // false when repoRoot is unset and the count could not be checked
}

func Staleness(c *Contract, events []Event, results []Result, delegationCount int, delegationsKnown bool, now time.Time) StalenessInfo
```

`LastActivityAt` is the max of `CreatedAt`, `UpdatedAt`, and every
event's `TS` in the log — this is the fix for the buried-timestamp gap
above: a `reflect`-only mission's real last-activity date becomes
visible instead of reading as "updated six days ago is actually
wrong, it's whatever created_at says."

`DelegationsKnown` exists because `countDelegations`
(`internal/mission/store.go:1246`) requires `repoRoot`; a caller
without one (e.g. a bare `Store` in a unit test, or a future
cross-repo query) must not silently report zero and let a leader read
absence-of-evidence as evidence-of-absence — the exact failure mode
`Store.Abandon`'s own comment at `internal/mission/store.go:1144`
already warns about for gate 1. Surfacing "unknown" rather than
guessing zero is the same fail-closed posture, applied to a read-only
signal instead of a gate.

### Surface: `mission show` and `mission list --stale-days`

- `ethos mission show <id>` (`cmd/ethos/mission.go:1072`,
  `runMissionShow`) gains an unconditional staleness line in text
  mode and a `staleness` object in `--json` mode — no flag needed,
  since a leader looking at one mission always wants to know its age.
  Example text output:

  ```text
  Staleness:   6d since last activity (2026-07-31 00:01 PDT), 0 results, delegations: 1
  ```

- `ethos mission list` (`cmd/ethos/mission.go:1196`, `runMissionList`)
  gains a `--stale-days N` flag. It filters to open missions whose
  `AgeDays >= N` **and** `HasResults == false` — matching the
  contract's phrasing ("age + zero-results threshold"). A mission with
  results is not stale in the sense this feature cares about; it is
  mid-review, which is a different, already-visible state. No default
  threshold is baked into the tool as a hard cutoff for anything
  *automatic* — `--stale-days` is a query parameter the leader chooses
  per situation, not a constant the system enforces. The command
  documentation suggests 3–7 days as a reasonable starting range,
  matching the six-day report in `ethos-9x07`, but does not mandate it.
  `MCP`'s `mission` tool gains the same `stale_days` parameter on the
  `list` method, sharing `runMissionList`'s filter logic so the two
  surfaces cannot drift (`internal/mcp/format_output.go` gets the
  matching formatter per DES-020, as this repo's own CLAUDE.md
  requires for any new MCP-visible output shape).

This alone fixes the "no visible signal" half of `ethos-9x07`: a
leader in any repo can run `ethos mission list --status=open
--stale-days=3` and see every mission that looks dead, including ones
where `DelegationsKnown` is false — so the "can I trust this?" question
is answered by the tool, in the output, rather than by trusting a
human's memory of what `updated_at` actually measures.

## Design part 2: force-release write-set

`Store.Abandon` is - and stays - the *only* path when the leader can
prove zero delegations and zero results: it is a stronger claim than
what this design adds, and its own gate must not be weakened to
accommodate the harder case. What is missing is the case in the bug
report: delegations may exist (`DelegationsKnown` false, or
`DelegationCount > 0`), no result was ever submitted, and the leader
has independently concluded — from staleness data, from the worker's
session being gone, from asking around — that the mission's
`write_set` claim should stop blocking new work while the mission
itself stays exactly as it is on record.

### `Store.ForceReleaseWriteSet(missionID, reason string) (*Contract, error)`

Modeled directly on `Store.Abandon` (`internal/mission/store.go:1081`),
same file, same locking discipline (`withLock`), same
`NewPathRedactor`-before-lock pattern for the reason field (see the
rationale at `internal/mission/store.go:1088`–1118, which applies
unchanged here — `reason` is leader-supplied free text that lands in a
git-tracked, publicly-pushed event log).

**What it does.** Sets `WriteSet` and `ExtractInto` to empty on the
contract, in the same locked section, via the existing `writeContract`
path — not a general-purpose `Update()` call, because `Update`'s event
(`internal/mission/store.go:857`) records only `{event: "update",
actor}` with no `Details`, which would leave an auditor unable to see
what changed or why without diffing contract file history. A dedicated
method emits a dedicated, detailed event instead (below).

**What it does not do.**

- Does not change `Status`. The mission stays `open`. This is the
  central design choice: force-release is not a terminal transition,
  so it makes no verdict claim and does not compete with `Close`'s
  result gate or `Abandon`'s delegation gate. If the worker
  resurfaces, or the leader later gets certainty the mission is truly
  dead, `Abandon` or `Close` (after a result lands) still apply on top
  — force-release is strictly a resource-release step, not a
  substitute lifecycle terminus.
- Does not touch `SuccessCriteria`, `Budget`, `Worker`, `Evaluator`, or
  any other planning field. It narrows exactly the two fields that
  `checkWriteSetConflicts` reads (`WriteSet`, `ExtractInto`,
  `findWriteSetConflicts` at `internal/mission/conflict.go:72`) — the
  admission-control claim, not the mission's declared intent. This
  keeps the operation inside the "convention, verified in review"
  frame from `prfaq.tex`: it revokes a *claim on future admission*, it
  does not rewrite history or retroactively make the original contract
  say something different than what the leader wrote at launch.
- Does not require `DelegationsKnown` or a specific `DelegationCount`
  value as a precondition. Staleness and delegation state are
  judgment inputs the leader consults via `mission show` /
  `mission list --stale-days` before deciding — see "no automatic
  gate" below — not a mechanical gate this method re-derives and
  enforces. The existing zero-delegation, zero-result case is already
  served, better, by `Abandon` (a real terminal state, not a
  half-measure); a leader who *can* prove zero-delegation-and-results
  should use `Abandon`, not this operation, and the CLI help text says
  so explicitly.

**The gate.** Deliberately thin, matching Abandon's "every failure
happens before any mutation" discipline
(`internal/mission/store.go:1099`–1113):

1. Mission is currently `open` (same check as Abandon and Close — a
   terminal mission's `write_set` is already excluded from
   `checkWriteSetConflicts` by virtue of `Status != StatusOpen`, so
   there is nothing to release).
2. `reason` is required, non-empty after trimming, and free of control
   characters (same validation `Abandon` applies).
3. `WriteSet` and `ExtractInto` are not both already empty — refuses a
   no-op release with a clear error rather than silently succeeding
   and writing a misleading event.

**No automatic age or delegation-count gate**, and this is deliberate,
not an oversight: staleness is a heuristic judgment (how old is "too
old" depends on the mission, the worker's cadence, and context the
tool cannot see), and hard-coding a numeric threshold into a mutation
gate would be exactly the "auto-action" the prfaq's exclusion rules
out. The tool's job is to make the judgment inputs visible (part 1)
and make the leader's decision auditable (below) — not to make the
judgment itself. This mirrors `Store.Abandon`'s own design note: its
gates are correctness invariants (would this discard real work?), not
heuristics (does this look old?). A staleness threshold is a
heuristic, so it stays out of the gate.

**Audit trail.** The event is a new type, `write_set_released`, with
full `Details` — this is the piece `Update`'s generic event lacks and
the reason the operation needs its own method rather than reusing
`Update`:

```yaml
event: write_set_released
actor: <leader handle>
ts: <RFC3339>
details:
  reason: <redacted reason text>
  old_write_set: [<entries>]
  old_extract_into: [<entries>]
  staleness_snapshot:
    last_activity_at: <RFC3339>
    age_days: <int>
    has_results: false
    delegation_count: <int or "unknown">
```

`old_write_set` / `old_extract_into` mean the original claim is never
lost to history even though the live contract no longer carries it —
an auditor reading `ethos mission log <id>` sees exactly what was
released and why. `staleness_snapshot` records the *evidence the
leader had* at decision time, even though (per above) it did not gate
the decision — this is what makes the operation reviewable after the
fact: a later reader can judge whether the call was reasonable given
what was known, the same way a post-mortem judges any operator
decision.

### CLI / MCP surface

```bash
ethos mission force-release-write-set <id-or-prefix> --reason "<text>"
```

```text
mission(method="force_release_write_set", mission_id="m-2026-07-31-005",
        reason="worker session ended 6d ago, no delegation activity visible, blocking lux-uoy4")
```

Follows the `mission abandon` flag pattern exactly
(`cmd/ethos/mission.go:809`–810: `--reason` required, mirrored by a
required `reason` MCP parameter) so the two operations read as
siblings, not as one being a shortcut around the other. Text-mode echo
mirrors `abandon`'s: `released: <id> write_set=[] extract_into=[]`.
`ethos mission log <id>` renders the new event type the same way it
already renders `abandon`'s `reason=...` (`cmd/ethos/mission.go`'s
`summarizeEventDetails`), so no new reading convention is needed.

### Why this, and not the alternatives

See "Rejected alternatives" below for the full list; the short version
is that every alternative either (a) reaches into filesystem
enforcement or automatic action, which the prfaq excludes, or (b)
weakens `Abandon`'s existing gate, which its own design note says must
not happen, or (c) reuses the generic `Update()` path and loses the
audit detail that makes the operation reviewable.

## `ethos-swoh` — noted, not addressed here

`ethos-swoh` reports that `warnHandleOverlap`
(`cmd/ethos/mission.go:1004`) warns on *any* handle overlap with an
open mission, even when the two missions' `write_set`s do not
intersect — a false positive against `ethos-z69l`'s actual misbinding
risk (a handle that is worker on one open mission and evaluator on
another). This lives in the CLI layer (`cmd/ethos/mission.go`), not in
`internal/mission/conflict.go` or the `checkWriteSetConflicts` path
this design touches, though the two are adjacent (both are pre-create
advisory/blocking checks over the set of open missions). It is being
fixed independently and mechanically in a parallel mission. This
design does not propose a fix for it and does not modify
`warnHandleOverlap` or `checkRoleOverlap`.

## Rejected alternatives

**Mechanical filesystem enforcement of write_set (e.g., sandbox the
worker's filesystem access to the declared paths).** Rejected outright
by `prfaq.tex` §`faq:not-building` / `feat:no-runtime-enforce`: agent
runtimes do not expose a sandboxing API today, and building
kernel-level containment "would mean shipping a sandbox product, not
an identity product." Nothing in this design or in ethos-9x07 changes
that constraint — the bug is about admission control (can a *new*
mission be *created*), not about what the *existing* worker can write,
which was never enforced and is not proposed to be.

**Auto-abandon on a staleness timer (cron or hook-driven).** Rejected
for the same reason auto-narrowing is: it is exactly the "silently
reclaim or override a write-set" pattern this design is told to avoid.
A mission going quiet for N days is not proof the worker is gone —
long-running human review, an agent waiting on an external event, or a
leader intentionally pausing a mission all look identical to "dead" by
a pure age metric. Every terminal or write_set-mutating transition in
this design requires an explicit, named, reasoned leader action.

**Weaken `Abandon`'s zero-delegation gate (e.g., add a
`--force`/`--i-know-there-might-be-delegations` flag to `Abandon`
itself).** Rejected because `Abandon`'s own design note
(`internal/mission/store.go:1050`–1060, `docs/mission-abandon.md`
"There is no override flag... Weakening it would let a leader retire a
mission that has recoverable work sitting on disk") is explicit that
the gate has no override for exactly this reason. Adding one now would
reopen a settled invariant to solve a problem that a separate,
non-terminal operation solves without touching it. Force-release does
not transition status at all, so it never competes with Abandon's
gate — it is a different verb entirely, not a weaker version of the
same verb.

**A general `ethos mission update --write-set <...>` command exposing
arbitrary contract mutation.** Rejected as too broad for the problem.
`Store.Update` exists as a library method but today nothing in
`cmd/ethos` or `internal/mcp` exposes unrestricted contract mutation
to a caller — there is no "edit" command for `success_criteria`,
`budget`, or any other field, and this design does not create the
first one. A narrow, single-purpose, fully-audited operation that
touches exactly the two admission-control fields is a much smaller
surface to reason about than a generic patch command, and matches the
existing pattern (`Abandon` is a dedicated method, not `Update` with a
status override).

**Route the recovery operation through `mission reflect` (add a
`write_set` override field to the `Reflection` type).** Considered
because the mission contract mentioned "a narrower `mission reflect
--write-set`" as a candidate shape. Rejected: `Reflection`
(`internal/mission/reflection.go:25`) is defined as "the typed handoff
between round N and round N+1" — it is required to be round-matched
(`AppendReflection` refuses a reflection whose `Round` does not equal
`c.CurrentRound`, `internal/mission/store.go:1814`) and append-only per
round. Force-release is not a round handoff and has nothing to do with
the round-advance gate; forcing it through `Reflection` would mean
either fabricating a round-matched reflection just to carry an
unrelated field, or loosening `Reflection`'s round-match invariant for
an unrelated purpose. A dedicated method with a dedicated event type
keeps `Reflection`'s existing contract intact and keeps the new
operation's audit trail readable on its own terms rather than buried
inside round bookkeeping.

**Refuse `mission create` instead of fixing the stuck mission (a
pre-create staleness check that blocks louder rather than unblocks).**
Considered — surfacing the conflicting mission's staleness in the
`checkWriteSetConflicts` error message at create time (e.g. "conflicts
with `m-...`, last active 6d ago") costs little and is a genuine
usability win. Not rejected as a *future* addition, but out of scope
for this design: it does not solve `ethos-9x07`'s actual complaint,
which is that the leader has no way to get *unblocked*, only a more
informative way to stay blocked. Left as a natural follow-on once
`Staleness` (part 1) exists as a shared function — `checkWriteSetConflicts`
could call it to annotate `Conflict` values — but is not required to
close this bug and is not designed further here.

## Summary of surface changes (implementation scope, not built here)

| Surface | Change |
|---|---|
| `internal/mission/filter.go` (or a new `staleness.go`) | `StalenessInfo` type + `Staleness()` function |
| `internal/mission/store.go` | `Store.ForceReleaseWriteSet(missionID, reason string) (*Contract, error)` |
| `internal/mission/mission.go` or `log.go` | new event type constant `EventWriteSetReleased = "write_set_released"` |
| `cmd/ethos/mission.go` | `runMissionShow` staleness line; `runMissionList` `--stale-days` flag; new `missionForceReleaseWriteSetCmd` / `runMissionForceReleaseWriteSet` |
| `internal/mcp/*.go` + `internal/mcp/format_output.go` | `mission` tool: `list` gains `stale_days`; new `force_release_write_set` method; formatter per DES-020 |
| `docs/mission-force-release-write-set.md` | usage doc, mirroring `docs/mission-abandon.md`'s structure |
| Tests | table-driven `Staleness()` unit tests (zero events, reflect-only mission, results present); `Store.ForceReleaseWriteSet` tests mirroring `store_test.go`'s existing `Abandon` coverage (open/terminal, empty reason, no-op refusal, event content, rollback on log-append failure) |
