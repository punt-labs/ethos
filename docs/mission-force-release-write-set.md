# Mission Force-Release Write-Set

`ethos mission force-release-write-set <id-or-prefix> --reason <text>`
(CLI/MCP surface: follow-on, not yet available — see below) marks an
open mission's `write_set`/`extract_into` claim as released
(`WriteSetReleasedAt`) so `checkWriteSetConflicts` stops treating it
as claiming those paths. It does not clear the fields themselves or
change the mission's status or any other field -- see "Design" below
for why leaving `write_set` intact is the point, not an oversight.

## Problem

`ethos-9x07`, reported live in `punt-labs/lux`: a mission
(`m-2026-07-31-005`, worker `rmh`, evaluator `gvr`) declared a
`write_set` covering almost all of `src/punt_lux/` and `tests/`. Its
only log activity was a single `reflect` + round-advance; zero results
were ever submitted; it sat open for six days. Because
`checkWriteSetConflicts` refuses any new `mission create` whose
`write_set` overlaps an *open* mission's `write_set`, this one mission
blocked every unrelated `mission create` touching the same tree.

`ethos mission abandon` (see `docs/mission-abandon.md`) is the correct
recovery *only* when the leader can prove zero delegations and zero
results. That was not provable here — a worker may genuinely have been
spawned once, produced nothing, and the session is long gone. Abandon
refuses in that case, by design, and there was no other recovery path:
`close` refuses too (no result), and the mission's `write_set` claim
never expired.

## Design

**Staleness detection surfaces information. It never acts.** No cron,
no hook, no auto-abandon, no auto-narrowing. `mission.Staleness`
(`internal/mission/staleness.go`) is a pure function computing a
mission's last-activity age (the max of `created_at`, `updated_at`,
and every event log timestamp — not just `updated_at`, since
`AppendReflection` never touches that field, so a reflect-only mission
looks exactly as fresh as it did at creation) and reports whether the
delegation count could actually be determined (`DelegationsKnown` —
`false` when no `repoRoot` is available, so a caller never reads
absence-of-evidence as evidence-of-absence).

**Recovery is a leader-invoked, explicit, audited operation** — the
same shape as `Store.Abandon`: a distinct method
(`Store.ForceReleaseWriteSet`) with its own gate, its own event type,
a mandatory reason, and no override flag that weakens Abandon's
existing gate. It does not fabricate a verdict and does not change
`Status`, `SuccessCriteria`, `Budget`, `Worker`, `Evaluator`, or any
other planning field — only `WriteSetReleasedAt` (the release marker)
and `UpdatedAt` (releasing a claim is itself a modification, like any
other Store write). It revokes a *claim on future admission*, nothing
more. This keeps the operation inside the "convention, verified in
review" frame `prfaq.tex` §`faq:not-building` describes for write-sets
generally: ethos does not enforce write-sets at the filesystem level,
and this operation doesn't change that — it only changes what the
*admission check* sees as claimed.

**Why `WriteSet`/`ExtractInto` stay on the contract, unchanged.** The
first implementation of this method literally cleared both fields —
and turned out to be unable to release the exact class of mission
`ethos-9x07` reports: `Contract.Validate` rule 11 requires a non-empty
`write_set` for every archetype except the read-only report/inbox
ones, and `implement` (the default every mission gets, and what the
reported incident mission was) is not one of them. A gate refusing to
leave the contract unloadable is correct, but it meant force-release
could only ever succeed for a minority of missions — not the incident
it exists to fix. `WriteSetReleasedAt` solves this at the root: the
declared `write_set` stays valid and on the record (rule 11 is
satisfied unchanged, validation never has an emptied-field case to
refuse), and `checkWriteSetConflicts` is the only place that treats a
released mission differently — skipping it outright, the same way it
already skips missions whose `Status != StatusOpen`.

**The gate**, every failure before any mutation:

1. The mission is currently `open` (a terminal mission's `write_set`
   is already excluded from `checkWriteSetConflicts`).
2. `reason` is required, non-empty, control-character-free.
3. The mission is not already released (`WriteSetReleasedAt` is
   empty), and `WriteSet`/`ExtractInto` are not both already empty —
   either case refuses a no-op release rather than silently
   succeeding and writing a misleading event.

**No automatic age or delegation-count gate**, deliberately: staleness
is a heuristic judgment (how old is "too old" depends on the mission
and the worker's cadence), not a correctness invariant like Abandon's
gates. The leader consults `mission show` / `mission list
--stale-days` (once that CLI surface lands) before deciding; this
method does not re-derive or enforce that judgment.

**Audit trail.** A dedicated event type, `write_set_released`, with
full `Details` — not the generic `update` event, which records only
`{event: "update", actor}` with no detail:

```yaml
event: write_set_released
actor: <leader handle>
ts: <RFC3339>
details:
  reason: <redacted reason text>
  write_set_at_release: [<entries>]
  extract_into_at_release: [<entries>]
  staleness_snapshot:
    last_activity_at: <RFC3339>
    age_days: <int>
    has_results: false
    delegation_count: <int or "unknown">
```

`write_set_at_release` / `extract_into_at_release` are a point-in-time
snapshot for the audit trail -- the live contract still carries these
fields unchanged, so this is not a backup of something deleted, just
a record of what was claimed at the moment the release happened.
`staleness_snapshot` records the evidence the leader had at decision
time — informational, not gating — so a later reader can judge whether
the call was reasonable given what was known.

## Status: core mechanism only

This mission (`internal/mission/staleness.go`,
`Store.ForceReleaseWriteSet`) ships the store-level mechanism and its
tests. The CLI (`ethos mission force-release-write-set`) and MCP
(`mission` tool, `force_release_write_set` method) surfaces are a
follow-on — two other missions held `cmd/ethos/mission.go` and
`internal/mcp` at dispatch time. Until that surface lands, the
mechanism is reachable only via the `internal/mission` package
directly.

## Why not the alternatives

See `docs/mission-writeset-staleness.md`'s "Rejected alternatives"
section for the full analysis: mechanical filesystem enforcement
(rejected by `prfaq.tex`'s Won't-Do list), auto-abandon on a timer
(the exact "silently reclaim a write-set" pattern this design avoids),
weakening `Abandon`'s zero-delegation gate (would reopen a settled
invariant), a general `mission update` command (too broad a surface),
and routing the operation through `mission reflect` (would either
fabricate a round-matched reflection or loosen `Reflection`'s
round-match invariant for an unrelated purpose).
