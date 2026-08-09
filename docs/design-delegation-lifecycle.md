# Delegation lifecycle and attribution: closing ethos-14r7

**Status**: Implemented on `task/delegation-lifecycle-attribution`.
Bead `ethos-14r7`. Mission `m-2026-08-09-019` (design);
`m-2026-08-09-024` (implementation, review rounds).

## Summary

Two recommendations, both mechanical, both small:

1. **Lifecycle.** Do not add a new sweep, a refuse-on-close gate, or a
   backfill job. `Store.Close` already sweeps every open delegation
   under a mission to a terminal verdict
   (`closeDelegationSkeletons`, store.go:1013–1067). That sweep is
   correct and does not touch `Mission.status` once terminal, so it
   does not threaten terminal-states-are-final. The three delegations
   the bead found stuck open are not a sweep failure — they are
   delegations that were **written after the sweep already ran**,
   which is a write-time attribution bug, not a close-time lifecycle
   bug. Fix the write path (recommendation 2) and the existing sweep
   is complete for the invariant that matters: after this fix ships,
   a write against a closed mission's `delegations/` directory is
   either refused outright (facet 2's status re-check) or, in the
   narrow TOCTOU window where a write is already in flight when
   `Close` starts, caught by `Close`'s own exclusive-lock sweep before
   `Close` returns. A skeleton can briefly land on disk in that
   window, but no open-verdict delegation record persists under a
   closed mission once `Close` has returned — provided the exclusive
   lock is actually acquired. If `AcquireMissionLockExclusive` itself
   fails, `Close` falls back to an unlocked sweep rather than skipping
   it outright (open delegations WILL still be closed), but a
   concurrent `dispatchTierB` may race in a late delegation write in
   that fallback branch, and a narrow TOCTOU window remains
   (store.go:1059–1065). This is a deliberate trade-off — a partial
   sweep beats a skipped sweep — not a claim that the fallback branch
   closes the window completely.

2. **Attribution.** `internal/hook/pretooluse_dispatch.go`'s
   `dispatchAgent` has two paths into `dispatchTierB`, and only one of
   them checks whether the mission is still open before writing a
   delegation record. Make both paths check, uniformly, and make the
   result of a failed check the same as every other attribution
   failure in this file: fall through to Tier A (ungoverned,
   session-scoped), never block the spawn. This single change closes
   both concrete misattribution patterns in the bead — the post-close
   writes and (once `ethos-5jsf` ships alongside it) the stale-binding
   write — using a mechanism (Tier A) that already exists and needs no
   new vocabulary.

`ethos-r2f9` is a different bug in a different subsystem (git-hook
deployment staleness) and shares no code path with this one — see
below. `ethos-5jsf` (MCP `mission create` does not rebind the session)
**is** the same root-cause family as facet 2, and is effectively a
companion fix this design depends on for full coverage — see below.

## Reading the field evidence

Bead `ethos-14r7`, from Bugbot+Copilot on PR #358's freeze
(2026-07-22):

- Three Tier B delegation records sit at `verdict: open`, no
  `closed_at`, though their parent missions closed.
- `d-2026-07-22-040`'s own prompt names `m-003`; the record is filed
  under `m-002` — `m-002` was still the session's bound mission when
  the resume fired.
- `d-043` and `d-044` have `created_at` **after** `m-003`'s
  `closed_at` — post-close review-cycle resumes attributed to a
  mission that had already closed.

Every one of these is a **write-time** problem: a delegation record
landed in the wrong mission's directory, or in a closed mission's
directory, at the moment `WriteDelegationSkeleton` ran. None of them
is caused by `Store.Close` failing to sweep — the sweep ran, correctly,
at the moment each mission closed. The records in the bead did not
exist yet when their mission's sweep fired.

## Facet 1: lifecycle

### What already exists

`Store.Close` (store.go:929–1034) transitions the contract, then —
outside the contract's exclusive lock, non-fatally — calls
`closeDelegationSkeletons(s.repoRoot, missionID, delegationVerdict,
closedAt)` (store.go:1018–1025). That function walks
`<repo>/.punt-labs/ethos/missions/<id>/delegations/*/record.yaml`,
and for every record still at `verdict: open`, calls
`CloseDelegationSkeleton` with a verdict derived from the mission's own
result (`pass` if the satisfying result's verdict is not `fail`, else
`fail`) and the same `closedAt` timestamp used on the mission
(store.go:1036–1067). This has shipped since PR #334
(commit `046507d`, 2026-05-25) — nearly two months before the PR #358
freeze that produced the bug report.

So "sweep it closed" is not a proposal; it is the current, working
design for every delegation that exists at the moment `Close` runs.
The design question this mission actually needs to answer is why that
sweep did not cover the three records in the bead.

### Why the existing sweep misses these three records

`closeDelegationSkeletons` is a **one-shot walk at Close time**. It
has no way to see a `record.yaml` that gets written five minutes, or
five days, later. If a delegation write lands in a mission's
`delegations/` directory after `Close` has already walked and returned,
that record is permanently invisible to the sweep — not because the
sweep is buggy, but because there is no second sweep, ever, for a
terminal mission. `d-043`/`d-044`'s `created_at` values are after
`m-003`'s `closed_at`: those records could not have existed when
`m-003`'s sweep ran, by definition.

The only two ways to make a one-shot sweep complete are: (a) prevent
anything from being written to a closed mission's directory ever
again, or (b) re-run the sweep periodically or on every read. (b) is a
poll-and-patch band-aid over a write-time bug, and it does not fully
close the gap either — a delegation written between two poll intervals
is still visibly wrong for a window. (a) is a direct fix at the point
of failure: refuse the write. That is recommendation 2.

### Recommendation

**Keep `closeDelegationSkeletons` exactly as it is.** Do not add a
refuse-close-until-delegations-resolved gate, and do not add a
periodic or read-time re-sweep. Fix the write path instead (facet 2);
once every write against a closed mission's `delegations/` directory
either is refused by facet 2's status re-check or, for a write already
in flight when `Close` starts, is caught by `Close`'s own
exclusive-lock sweep (see the locking hardening below), the existing
sweep guarantees no open-verdict delegation record persists under a
closed mission — even though, in that narrow window, a skeleton can
briefly land before being swept to a terminal verdict. This full
guarantee depends on the exclusive lock actually being acquired: if
`AcquireMissionLockExclusive` fails, `Close` falls back to an unlocked
sweep rather than skipping it outright — open delegations WILL still
be closed, but a concurrent `dispatchTierB` may still race in a late
delegation write, and a narrow TOCTOU window remains
(store.go:1059–1065). That fallback branch is a deliberate trade-off —
a partial sweep beats a skipped sweep — with a documented residual
TOCTOU, not a claim of full coverage.

Two alternatives considered and rejected:

- **Refuse close until every delegation resolves.** Close already has
  a hard gate — the result-artifact gate
  (`checkResultGateLocked`, store.go:2828). Adding a second gate on
  delegation state would mean a mission with a valid, worker-submitted
  result cannot close because a background verifier subagent hasn't
  hit `SubagentStop` yet. That is friction with no payoff: the
  existing sweep already has a defensible answer for what an
  in-flight delegation's terminal verdict should be (derive it from
  the mission's own result), and a hard refusal only delays reaching
  that same answer.
- **Periodic/backfill re-sweep of "closed missions with open
  delegations."** This treats the symptom. It needs new scheduling
  machinery ethos does not have anywhere else (there is no daemon,
  no cron equivalent — every ethos operation is invoked by a hook or
  a CLI command), and it does not fully close the window even where
  it runs. The write-time fix removes the need for it entirely.

One implementation hardening, not a design change: `Close`'s contract
transition is serialized by `s.lockPath` (store.go:426–428, a
`<globalMissionsDir>/<id>.lock` flock acquired in `withLock`,
store.go:1768–1795), but `closeDelegationSkeletons` runs **after**
that lock is released and takes no lock of its own
(store.go:1013–1025, 1038–1067). The delegation write path takes a
**different** lock — `AcquireMissionLock`, a shared flock on
`<repo>/.punt-labs/ethos/missions/<id>/.lock` (delegation.go:434–461,
`MissionLockPath`/`AcquireMissionLock`; used by `dispatchTierB`,
pretooluse_dispatch.go:226–231). These two locks do not exclude each
other, so there is a narrow TOCTOU: a dispatch that reads
`status: open` a moment before `Close` commits can still be mid-write
when `Close`'s sweep walks the directory. `AcquireMissionLock`'s own
doc comment anticipated exactly this need ("a separate writer that
needs the mission tree quiescent — for example a hypothetical mission
close that wants no in-flight skeletons — can take `LOCK_EX` on the
same file and will wait for every shared holder to release",
delegation.go:419–422). Recommend `Close` take that `LOCK_EX` around
its `closeDelegationSkeletons` call, so any concurrent
`dispatchTierB` holding the shared lock finishes its write (and gets
caught by the facet-2 status re-check below) before the sweep
enumerates the directory. This is a small, already-designed-for
addition, not new architecture.

### Terminal-states-are-final

`docs/spec-mission-lifecycle.tex` models `Mission` — `status`,
`currentRound`, `budgetRounds`, `worker`, `evaluatorHandle`,
`evaluatorHash`, `delegationCount`, `resultRounds`, `reflected`,
`repoRootSet` (§State) — and proves (§Theorems, `TerminalIsFinal`)
that once `status` leaves `stOpen`, no modelled operation can move it
again. A `Delegation` record's own fields — `Verdict`, `ClosedAt`
(delegation.go:88–103) — are not part of that schema at all; the spec
says so explicitly (§Non-goals: "the real `Contract` struct does not
itself carry delegation or result data — those live in sibling
files", also stated at §Traceability's `Mission` row). Sweeping a
delegation record's `Verdict` from `open` to `pass`/`fail` after
`Close` mutates a **different file**, not `Mission.status`. It is
compatible with terminal-states-are-final by construction: nothing in
this design proposes a second write to the mission contract after
`Close` commits, and nothing here reopens `status` from any of its
five terminal or open values.

The failure mode this design does need to avoid is a **new** write to
`Mission.status` after `Close` — e.g., a "reopen to attach a
late-arriving delegation" idea. This design does not do that. Facet 2
refuses the late-arriving delegation instead, and files it somewhere
that carries no relationship to the closed mission's contract at all.

## Facet 2: attribution

### The two write paths, and the asymmetry between them

`dispatchAgent` (pretooluse_dispatch.go:55–64) picks one of two ways
to reach `dispatchTierB`:

```go
missionID := os.Getenv("MISSION_ID")
if missionID != "" {
    return dispatchTierB(w, sessionID, missionID, toolInput)   // case 1
}
if missionID := readActiveMissionForDispatch(sessionID); missionID != "" {
    return dispatchTierB(w, sessionID, missionID, toolInput)   // case 2
}
return dispatchTierBOrTierA(w, sessionID, toolInput)
```

**Case 2** (`readActiveMissionForDispatch`, pretooluse_dispatch.go:80–109)
reads the session's active-mission sidecar and calls
`staleBindingReason` (pretooluse_dispatch.go:121–134), which loads the
contract and refuses the binding — falling through to no-mission Tier A
— when `c.Status != mission.StatusOpen`. This check exists and is
correct; its doc comment even names the exact failure mode: "A sidecar
naming a mission that is no longer open is stale and is refused with a
warning (ethos-7vo3) ... filing a fresh delegation under a mission
whose results are already in is a false audit trail."

**Case 1** (`dispatchTierB`, pretooluse_dispatch.go:200–312) has no
such check. It calls `store.Load(missionID)` (line 206) purely to
confirm the mission exists and is parseable — a `Load` error blocks
the spawn with a named reason — and then proceeds straight to
allocating a delegation ID, acquiring locks, and calling
`WriteDelegationSkeleton` (line 251). A mission that loads
successfully but is `closed`, `failed`, `escalated`, or `abandoned`
passes this check exactly like an open one. Nothing downstream checks
status either — `enforceDelegationDepth` cares about ancestor chain
length, not mission state.

This is the mechanism behind `d-043`/`d-044`: a `MISSION_ID` env value
set for one turn of a resumed subagent process is inherited, by
ordinary OS process-environment inheritance, by every subsequent tool
call that process makes — including tool calls made long after the
operator (or the leader) closed that mission in a completely different
part of the session. Case 1 treats the mere presence of that
env var as license to write, with no re-validation against the
mission's current state. Case 2 already re-validates on every read.
The fix is to make case 1 do what case 2 already does.

### `d-040`'s distinct mechanism

`d-040`'s prompt names `m-003`, but the record is filed under `m-002`
because — per the bead — "`m-002` was still bound when the resume
fired." This is case 2, not case 1: the session's active-mission
sidecar (`<globalRoot>/sessions/<id>/active-mission`,
`ActiveMissionPath`, active.go:31–39) had not yet been rebound to
`m-003` at the moment the resume's `Agent` call fired. The sidecar is
session-wide, single-valued (mission.go's `ActiveMissionBinding` has
exactly one `MissionID`, active.go:82–87) — it answers "which mission
is this session currently working on," not "which mission does this
specific prompt belong to." A prompt that names `m-003` explicitly in
its own text is strong, direct evidence of intent that the sidecar
mechanism has no way to read or reconcile against.

The CLI's `mission create`/`dispatch` commands rebind the sidecar the
moment a fresh mission is minted precisely to keep this window short
(`bindDispatchedMission`, cmd/ethos/mission.go:1992–2068 — see its own
comment: "a leader who created m-B while still bound to m-A filed m-B's
delegation under m-A (observed: d-078 under m-017)... Creating or
dispatching a mission is the leader naming one explicitly, so it is the
moment the binding must follow"). That mechanism exists, is correct,
and is exactly what this bead needs — **but it is not wired into every
surface that can create a mission.** See `ethos-5jsf` below.

### Recommendation

Give `dispatchTierB` the same guard `staleBindingReason` already has,
and apply it uniformly to both callers:

- Immediately after `store.Load(missionID)` succeeds
  (pretooluse_dispatch.go:206), check `c.Status != mission.StatusOpen`.
- On a non-open mission, do **not** block the spawn (blocking on an
  attribution problem contradicts every other Tier A/Tier B fallback
  rule already in this file — Tier A dispatch is explicitly
  non-blocking by design, pretooluse_dispatch.go:143–151). Instead,
  fall through to `dispatchTierBOrTierA`'s existing no-mission path —
  the same Tier A destination `readActiveMissionForDispatch` already
  falls back to on a stale sidecar. Log the same shape of stderr
  warning `staleBindingReason`'s caller already emits (naming the
  session, the stale mission, and the remedy — run `ethos mission
  claim` or dispatch the mission you mean).
- Re-validate status a second time immediately before
  `WriteDelegationSkeleton`, while holding `AcquireMissionLock`
  (already acquired at line 226) — this is the TOCTOU half of the
  fix described in facet 1's hardening note: if `Close` takes the
  same lock exclusively while sweeping, and `dispatchTierB` re-checks
  status while holding it shared, the two operations cannot interleave
  into a write that lands after the mission is already closed.

This directly answers the bead's own proposed direction — "refuse to
attribute to a closed mission (file under a session-level
continuations area)" — using the option that requires zero new
storage concept: Tier A **is** the session-level continuations area.
It already writes to
`<repo>/.punt-labs/ethos/sessions/<date>-<session-id>/adhoc/<NNN>/`
(delegation.go:74–78), it is already audited, and it already carries
no relationship to any mission contract. The alternative the bead
raises — annotate the delegation as a "post-close continuation" and
still file it under the closed mission — is rejected: it requires a
new verdict/reason vocabulary, it still writes a new file into a
`delegations/` directory whose parent mission's audit trail should be
finished as of `ClosedAt`, and it buys nothing over just filing the
work where it truthfully belongs.

### Should a `SendMessage`-resume inherit no mission unless one is actively bound and open?

Yes, and the fix above makes that the actual behavior without
special-casing "resume" as a concept ethos would have to detect.
Ethos cannot distinguish "the operator just exported `MISSION_ID` for
this exact command" from "this process has been carrying `MISSION_ID`
in its environment for three days across many resumed turns" — env
vars carry no timestamp or provenance. The only sound, uniform rule is
the one case 2 already enforces: a mission attribution is trusted only
at the moment it is used, checked fresh against the mission's current
status, every time. Once case 1 gets the same check, "inherited stale
env" and "inherited stale sidecar" both degrade to the identical,
already-correct outcome — Tier A, no mission, spawn proceeds.

## Relationship to `ethos-r2f9` and `ethos-5jsf`

### `ethos-r2f9` — not the same root cause

I read `ethos-r2f9`'s bead and its design doc
(`docs/design-hook-drift-detection.md`, merged in #451). Both are
unambiguously about a different subsystem: git hook scripts
(`commit-msg`, `pre-commit`) are deployed as **static file copies** at
`ethos enable` time, and nothing re-syncs a later bug fix into an
already-enabled repo's `.git/hooks/`. The fix was `CheckHookCurrency`
in `internal/doctor` — content-hash comparison between the deployed
hook and what the current binary would deploy. Nothing in that bead or
its doc touches `internal/mission`, the active-mission sidecar, or
`PreToolUse-on-Agent` dispatch.

The mission brief's context note also names a specific claim I could
not substantiate: "SubagentStart's join key vs whoami's lookup key."
I read both. `resolveFromSession` (`internal/resolve/resolve.go:163–193`)
resolves the acting persona via `os.Getenv("ETHOS_AGENT_ID")`, falling
back to `process.FindClaudePID()`, and looks that value up with
`roster.FindParticipant(agentID)`. `HandleSubagentStartWithDeps`
(`internal/hook/subagent_start.go:108,154–163`) joins the roster with
`p := session.Participant{AgentID: agentID, ...}` where `agentID` is
read directly from the hook's own JSON input (`input["agent_id"]`) —
not independently derived. Both call sites key the roster by the same
kind of value (an agent/process identifier), and I found no mismatch
between how the join writes it and how `resolveFromSession` reads it.
If a mismatch exists, it is not visible in the current code on this
path, and it is not described in `r2f9`'s current bead text or its
merged design doc. I am flagging this rather than asserting a shared
root cause I cannot show: **`ethos-r2f9` and `ethos-14r7` are separate
bugs in separate subsystems. Fixing this design does not touch, and
would not close, `r2f9`'s hook-staleness class, and `r2f9`'s fix
(already merged) does nothing for delegation attribution.**

### `ethos-5jsf` — same root-cause family, and a real dependency

`ethos-5jsf` is exactly the mechanism behind `d-040`. The CLI's
`runMissionCreate`/`runMissionDispatch` call `bindDispatchedMission`
(cmd/ethos/mission.go:1946, 2023–2068) to rebind the session's
active-mission sidecar the instant a new mission is minted, closing
the window where the sidecar still names the previous mission. The MCP
tool's equivalent, `handleCreateMission`
(`internal/mcp/mission_tools.go:105–151`), does **not** — it ends at
`h.missionStore.Create(&c)` and `jsonResult(&c)` (lines 147–150), with
no call to `WriteActiveMissionOrigin` or any rebind helper. (Contrast
with `handleCloseMission`, which correctly mirrors the CLI's
close-time unbind: `h.clearClosedMissionBindings(id)`,
mission_tools.go:259, calling through to `mission.ClearMissionBindings`.
Only the create side has the gap.)

This is the same causal shape as facet 2: attribution is keyed off a
session-level sidecar, and that sidecar is only correct if **every**
surface that changes what mission a session is working on — create,
dispatch, claim, release, close — keeps it current. `5jsf` is a hole
on the "keep it current when a new mission starts" side of that
contract; this design's facet-2 fix is a hole on the "don't trust it
past the point the target mission stops being open" side. Both holes
belong to the same invariant: *the active-mission sidecar must never
be allowed to outlive its own accuracy.* Fixing only this mission's
facet-2 change and leaving `5jsf` open still leaves `d-040`'s exact
pattern reachable through the MCP surface — a leader that creates a
mission via MCP, then immediately resumes a spawn, still writes under
whatever mission the sidecar named a moment ago, because MCP `create`
never told the sidecar it's stale. **Recommend `ethos-5jsf` ship
alongside, or immediately after, this design's facet-2 fix** — mirror
`bindDispatchedMission`'s call into `handleCreateMission` (and confirm
`mission dispatch`'s MCP path, if one exists, does the same). Without
it, this design closes the post-close-write pattern (`d-043`/`d-044`)
completely but leaves the stale-sidecar pattern (`d-040`) only
partially closed — case 1/case 2 parity still helps, because a sidecar
naming a mission that's gone from open to closed is now caught, but a
sidecar that's simply *wrong* (names a still-open mission that isn't
the one the prompt means) has no signal to catch it at all; that gap
is `5jsf`'s to close.

## Forward-fix, not a backfill

The delegation records already on disk in the PR #358 freeze —
`d-2026-07-22-040`, `d-043`, `d-044`, and the third open record the
bead names — stay exactly as written. They faithfully reflect what the
tooling actually did at the time: `d-040` really was filed under
`m-002` by the dispatch logic that existed then, and `d-043`/`d-044`
really were created after `m-003` closed, by the dispatch logic that
existed then. Rewriting them after the fact to what they "should" have
said would replace an accurate historical record with a fabricated
one — worse for audit trust than a handful of records that are visibly
inconsistent and now explained. This design changes only the code path
that produces **new** records; it defines no migration, no backfill
script, and no retroactive annotation of existing `record.yaml` files.

## Implementation sketch (design only)

For the implementing mission, not this one:

1. `internal/hook/pretooluse_dispatch.go`: extract
   `staleBindingReason`'s status check into a shared helper (or call it
   directly — it already takes just a `missionID`) and call it from
   `dispatchTierB` right after the existing `store.Load` at line 206.
   On non-open, do not call `writeAgentBlock`; instead return
   whatever `dispatchTierBOrTierA` (or `dispatchTierA` directly) would
   have returned, with a stderr line matching
   `readActiveMissionForDispatch`'s existing wording.
2. Same file: re-check status once more immediately before
   `WriteDelegationSkeleton` (line 251), while `AcquireMissionLock`
   (line 226) is held, to close the TOCTOU window against a concurrent
   `Close`.
3. `internal/mission/store.go`: in `Close`, acquire
   `AcquireMissionLock(s.repoRoot, missionID)` in `LOCK_EX` mode around
   the `closeDelegationSkeletons` call (store.go:1018–1025), so it
   waits for any in-flight shared holder (a concurrent `dispatchTierB`)
   to finish and be caught by step 2's re-check before it enumerates
   the directory.
4. `internal/mcp/mission_tools.go`: `handleCreateMission` calls the
   MCP-side equivalent of `bindDispatchedMission` after
   `h.missionStore.Create(&c)` succeeds (line 147) — this is
   `ethos-5jsf`'s fix, tracked separately but needed for facet 2 to be
   complete on the MCP surface.
5. Tests: table-driven cases in `pretooluse_dispatch_test.go` for both
   case 1 (env `MISSION_ID` naming a closed/failed/escalated/abandoned
   mission → Tier A, spawn allowed, stderr warning) and the existing
   case 2 behavior (regression coverage, already partially present via
   `TestDispatchAgent_ActiveMissionSidecarMalformedRefuses` and
   neighboring tests — confirm naming conventions before adding new
   cases). A `Store.Close` test asserting the exclusive-lock ordering
   requires a synchronization harness (goroutine racing a slow
   `WriteDelegationSkeleton` against `Close`); the existing
   `store_test.go`/`delegation_test.go` files are the right home.

No code in this document. The implementing mission owns exact diffs,
review, and `make check`.
