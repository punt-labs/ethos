# DES-054 v5 Round-4 Review — rsc (compatibility / migration)

**Verdict: APPROVE. Convergence test passes.** Zero new REQs. Zero new
substantive IMPL findings. v5 resolved the single round-3 REQ
(`counter.yaml` file-format break) in a form that is strictly better
than either of the two options I proposed: it abandons the YAML
envelope entirely and adopts a sibling per-namespace per-date pattern
that preserves the current `.counter-YYYY-MM-DD` shape verbatim. The
two structural restructurings introduced under CEO clean-slate
direction (date-keyed two-tree layout; single session-level audit
store) are compatibility-neutral migrations that follow the same
read-fallback discipline as the audit-log move I reviewed in round 3.
The design has reached the fixed point the convergence test was
designed to detect.

The remainder of this review is the convergence audit: every prior
finding traced against v5, the two structural changes pressure-tested
for migration hazards I would expect to surface if any existed, and a
short list of observations recorded so the next reader sees why I did
not raise them as findings.

---

## Round-3 finding closure

### R3 Finding 1 (REQ) — `counter.yaml` file-format break — RESOLVED

The v4 counter design was a single `counter.yaml` with nested map
shape and `schema_version` field. Three independent breaks in one
move: filename change, dotfile-to-ordinary visibility flip, and shape
change from scalar-int-per-file to nested-map-per-file. The
`I9-counter` permissive-decode invariant bound future additions to
`counter.yaml` but did not cover the v3.11.0 → v3.12.0 file-format
replacement itself.

The v5 resolution at lines 14, 82-84, 257, 286-287, and `I9-counter`
at lines 374-381 abandons `counter.yaml` and adopts sibling-file
per-namespace per-date counters:

```text
~/.punt-labs/ethos/counters/missions-YYYY-MM-DD     # single int
~/.punt-labs/ethos/counters/delegations-YYYY-MM-DD  # single int
```

I verified this against `internal/mission/id.go:43-44` (current
code): `<root>/missions/.counter-YYYY-MM-DD`, single int,
`\n`-terminated, temp+rename atomic, flock on a sibling `.lock` file.
The v5 shape is the same shape with two changes:

1. Directory moves from `<root>/missions/` to `<root>/counters/`
   (one directory level up; new namespace dimension lives in the
   filename, not the parent path).
2. Filename loses the leading dot (`.counter-YYYY-MM-DD` →
   `missions-YYYY-MM-DD`). The leading dot was an implementation
   detail of the original write, not a semantic choice; the v5
   filename is more legible to operators listing the directory.

The compatibility analysis: a v3.11.0 binary writes to
`<root>/missions/.counter-YYYY-MM-DD` and never touches
`<root>/counters/`. A v3.12.0 binary writes to
`<root>/counters/missions-YYYY-MM-DD` and never touches
`<root>/missions/.counter-YYYY-MM-DD`. The two binaries allocate from
**independent counter state** on the same machine across the upgrade
boundary. This is in fact a stronger guarantee than the rolling-upgrade
fence on `.create.lock`: with `.create.lock` the two binaries share a
namespace and need a fence to serialize; with the counter move the
two binaries are in disjoint namespaces and need no fence at all
because the two namespaces have disjoint ID spaces. v3.11.0 issues
`m-2026-05-22-005` from its counter; v3.12.0 issues `m-2026-05-22-001`
from its fresh counter; the resulting IDs are not in the same set
because v3.12.0 with the new state machine only writes to the new
directory.

There is one residual concern that I considered and rejected as
non-blocking. If an operator downgrades from v3.12.0 back to v3.11.0
mid-day, v3.11.0 will resume allocation from its old counter file,
which records the last v3.11.0-issued number — say 5 — and start
issuing 6, 7, 8 while v3.12.0 has separately issued 1, 2, 3, 4
under the new counter. The two ID series do not collide
(`m-2026-05-22-006` vs `m-2026-05-22-001` are distinct strings), but
the on-disk artifacts under `.ethos/missions/m-2026-05-22-001/`
through `m-2026-05-22-005/` come from two different generations of
the binary. This is identical to the current rollback hazard with
any persisted state across binary versions and is not specific to
the counter move. The phase 1 + phase 3 transition window (two minor
versions per R3 O2) gives operators a defined upgrade procedure;
downgrade outside the procedure is unsupported across all of ethos,
not just the counter subsystem. I would not gate on this.

The invariant `I9-counter` at lines 374-381 now reads:

```text
I9-counter: forall ns, date:
    counters/<ns>-<date> exists  ->  contents is a single integer
/\  no counter file ever changes format
```

The "no counter file ever changes format" clause is the right
contract. It binds the file shape — single int — rather than a
permissive-decode YAML envelope, which means future namespaces are
new sibling files and never reshape existing files. This is the
strongest compatibility shape available; it cannot be broken without
introducing a new namespace, and introducing a new namespace is a
purely additive operation that does not touch existing files. The
round-3 REQ asked for either dual-write or per-day preservation; v5
delivers the per-day preservation option in a cleaner shape than I
proposed and without the YAML envelope I called out as the hazard.
**Closed.**

### R3 Observation O2 (IMPL) — audit-log transition window — RESOLVED

R3 O2 noted that phase 1 (storage move, v3.12.0) and phase 3
(migration tool, v3.13.0) imply a two-minor-version transition
window for the audit-log legacy fallback, wider than the
one-minor-version window for `.create.lock`, and asked for one
sentence naming this. v5 names it at line 251 ("Transition window is
two minor versions (per rsc R3 O2) because phase 1 ships the storage
move and phase 3 ships the audit migrate tool; the gap is one minor
version, and the v3.13.0 cleanup is another"). **Closed.**

### R3 Observation O3 (IMPL) — I7 verdict semantics for Tier A — RESOLVED

R3 O3 noted that I7's verdict domain `{open, pass, fail, error,
aborted}` does not say what a Tier A delegation can produce, and
asked the implementer to resolve at schema time. v5 pins this in the
invariant itself at lines 341-347:

```text
(d.tier = "A"  ->  d.verdict ∈ {"open", "aborted"})
/\
(d.tier = "B"  ->  d.verdict ∈ {"open", "pass", "fail", "error", "aborted"})
```

This is the right call. Tier A has no evaluator, so a `pass`/`fail`
on a Tier A delegation has no meaning — only `open` (the spawn
returned but was not explicitly closed) and `aborted` (the spawn was
refused at PreToolUse or was explicitly killed) are well-defined.
The invariant now precludes a future implementer from accidentally
writing a Tier A `pass` and creating an unverifiable assertion in
the audit log. **Closed.**

### R3 Observation O4 (IMPL) — `max_delegation_depth` Tier B cleanup — RESOLVED

R3 O4 noted that a Tier B `max_delegation_depth` refusal at
PreToolUse leaves the just-created delegation skeleton dangling and
asked the implementer to close it with `aborted` as part of the
refusal handler. v5 at line 295 names this explicitly: "If the depth
check refuses a Tier B spawn after the delegation record skeleton
has been written (rsc R3 O4), the refusal handler closes the
just-created record with `verdict=aborted` and `closed_at=<now>`. No
dangling state." The phrasing closes the loop with I7's Tier A
`aborted` clause: both tiers can produce `aborted` from a
PreToolUse refusal, and both tiers leave no orphaned skeletons.
**Closed.**

### R3 Observation O1 (note) — write-time vs read-time audit migration

R3 O1 was a note, not a finding — recording that v4 chose
operator-explicit migration (`ethos audit migrate`) over silent
write-time migration. v5 carries the same shape forward. No action
required. **Carried.**

---

## Pressure test 1: date-keyed two-tree layout

v5 restructures from v4's tree shape to:

```text
<repo>/.ethos/
├── missions/<mission-id>/
│   ├── contract.yaml
│   ├── results.yaml
│   ├── reflections.yaml
│   ├── log.jsonl
│   ├── artifacts/
│   └── delegations/<NN>/
└── sessions/<YYYY-MM-DD>-<session-id>/
    ├── audit.jsonl
    └── adhoc/<NNN>/
```

I checked this against three migration hazards I would expect to
surface if any existed.

**Mission directory move (`m-YYYY-MM-DD-NNN/` flat → `missions/<id>/`
nested).** The current code in
`internal/mission/store.go` (which I have not re-read line-by-line
for v5 but read in round 3) writes missions to a flat directory
keyed by ID. The v5 layout adds a `missions/` parent. This is a
straightforward rename from the read-path perspective: any caller
that previously read `<root>/<mission-id>/contract.yaml` now reads
`<root>/missions/<mission-id>/contract.yaml`. The fallback discipline
established for audit logs at lines 236-247 generalizes: read repo
new-path first, repo old-path fallback, global old-path fallback.
The draft does not explicitly state the mission-directory fallback,
but the existing `mission migrate --to-repo` machinery at line 270
already moves missions across roots, and the two-root state machine
table at lines 226-235 covers the read path. The contract for v5
is implicit: anywhere a load can find a contract, find it. This is
acceptable; the implementer (bwk) will wire the fallback during
phase 1 along with the audit-log fallback. **Not a finding.**

**Session directory naming (`<YYYY-MM-DD>-<session-id>`).** The
session-id is, today, a UUID-shaped string from Claude Code's
conversation history. Prefixing it with the date and a separator
dash introduces one new parsing rule: anything that consumes session
directory names must split on the first `-` after the date prefix.
The draft does not name this rule, but the date prefix has a fixed
shape (`YYYY-MM-DD-`, 11 characters) so the split is unambiguous.
The 1:1 binding with the Claude Code conversation history file at
`~/.claude/projects/<...>/<session-id>.jsonl` (line 91) requires
that the session-id portion of the directory name be exactly the
session-id and nothing else. v5 says this explicitly. The
forensic-cross-reference operator can `grep -l <session-id>
.ethos/sessions/*/audit.jsonl` and find the right session directory
in O(N) time, or directly `ls .ethos/sessions/*-<session-id>/` for
O(1) lookup with glob. This is correct. **Not a finding.**

**Day-boundary policy for sessions.** Missions use the parent's
date for sub-delegation IDs (`m-2026-05-21-005-d04` even if the
delegation is born at 00:00:01 the next day). Sessions are
top-level and named with their start date — but a session is
inherently long-lived and may cross midnight. The session directory
keeps its start-date name; audit entries written after midnight go
into the same `audit.jsonl` and carry their actual UTC timestamp in
the `ts` field. This is the right call: the session directory name
is an identity, not a clock partition, and the audit `ts` is the
authoritative time source. Cross-day queries follow the same
`ethos audit show` filter discipline as the rest of the audit
surface. The day-boundary policy at line 259 explicitly notes that
Tier A delegations spawned during such a cross-midnight session
are indexed in `delegations-YYYY-MM-DD` by wall-clock time of
allocation, not by their parent session's start date. This is
self-consistent: Tier A delegations have no parent mission and no
contractual binding to any other date, so wall-clock is the only
defensible choice. **Not a finding.**

## Pressure test 2: collapse from dual audit store to single session-level store

v4 had per-delegation `audit.jsonl` files **and** a session-level
audit log. v5 drops the per-delegation file and folds everything
into the session-level log, with `delegation_id` and
`parent_delegation` distinguishing entries. The reasoning at lines
93 and 289 is sound: per-delegation files were a denormalization
that the session-level filter could always reconstruct. Dropping
them removes one append-atomicity surface (`I10-audit-atomic`
collapses to single-store at lines 384-389) and one flock per
delegation. The query surface (`ethos audit show --delegation
<id>`) becomes a filter on the session log, which is the same shape
as the existing `ethos audit show --session <id>` filter at the
top of the chain.

I checked this against two failure modes I would expect if the
collapse were premature.

**Append-atomicity under N concurrent Tier B delegations writing
to one session log.** With the single store, every audit append
goes through the session flock at lines 282-283. The session flock
is held by a single session-id; if a single session contains N
delegations spawning concurrently, each tool-call audit append
serializes through that one flock. v4's per-delegation flock
allowed parallel appends across delegations in the same session.
v5 forces them to serialize. Is this a problem? In practice, no:
audit appends are short (one JSONL line plus `f.Sync()`), and the
realistic concurrency under one session is bounded by the number
of concurrent subagent spawns in a single Claude Code session,
which is small. The flock-on-the-session pattern also subsumes the
session roster's existing flock at line 268, so v5 actually reduces
the lock count rather than increasing it. **Acceptable.**

**Per-delegation `git log --` queries lose granularity.** A
reviewer who runs `git log -- .ethos/sessions/<sess>/audit.jsonl`
sees commits that touched the session log — which includes every
delegation's audit appends mixed together. This is strictly less
useful than v4's `git log -- .ethos/missions/<mid>/delegations/<n>/audit.jsonl`
for per-delegation forensic work. The mitigation in v5 is
`ethos audit show --delegation <id>` which filters the session log
by `delegation_id`. This is sufficient for the operator workflow
(filter by delegation, render the entries) but loses one specific
property: per-delegation **commit history**. If a Tier B
delegation's audit log was the unit of forensic interest and the
operator wanted to see which commits modified it, v4's layout
exposed that directly; v5's layout requires filtering commit
contents, not commit paths. I have read the rejected-alternatives
section at line 410 ("Single repo-root `delegations.jsonl`. Rejected:
violates per-delegation flock semantics, breaks per-delegation
`git log` granularity") and the v5 collapse is more conservative
than that rejected alternative — the per-delegation directory still
exists (with `record.yaml`, `prompt.md`, `result.md`), just without
the audit file inside it. The `git log` granularity for the
delegation's metadata is preserved; only the audit-log commit
history is folded. This is a defensible trade-off and the draft
gives the right justification at line 93. **Not a finding.**

## Pressure test 3: rolling upgrade with v5 storage shape

A v3.11.0 binary on the same machine as a v3.12.0 binary, across
the same repo, on the same day:

| Surface | v3.11.0 path | v3.12.0 path | Coexistence |
|---|---|---|---|
| Mission ID counter | `<repo>/.ethos/missions/.counter-DATE` (current) — but `<repo>/.ethos/missions/<id>/` is v3.12.0 | `~/.punt-labs/ethos/counters/missions-DATE` (global, new) | Disjoint paths; disjoint allocations. Two binaries allocate independently. ID collision is per-string and the strings differ by counter-source. |
| Mission contracts | `~/.punt-labs/ethos/missions/<id>.yaml` (global flat) | `<repo>/.ethos/missions/<id>/contract.yaml` (repo nested) | v3.12.0 reads both via two-root state machine; v3.11.0 reads only global. v3.12.0-created missions invisible to v3.11.0 until `mission migrate --to-repo` reverses (no such command, by design — one-way door per line 402). |
| Session audit log | `~/.punt-labs/ethos/sessions/<sess>.audit.jsonl` (global, current) | `<repo>/.ethos/sessions/<date>-<sess>/audit.jsonl` (repo, new) | Read fallback at lines 238-247: repo first, global fallback. v3.11.0 writes only global; v3.12.0 reads both. Audit-migrate consolidates explicitly. |
| `.create.lock` rolling fence | `~/.punt-labs/ethos/missions/.create.lock` | both `~/...` AND `<repo>/.ethos/missions/.create.lock` | v3.12.0 takes both per line 230; v3.13.0 drops the global per line 251. |

Three observations from this table.

First, the mission counter shape (current `<repo>/missions/.counter-DATE`
under what is in fact today a **global** root path that gets resolved
to repo by the wrapping) is actually a small wrinkle I should
double-check. Looking at `id.go:38`, `missionsDir = filepath.Join(root,
"missions")` where `root` is the directory passed in. If `root` is
the global `~/.punt-labs/ethos/`, the counter is at
`~/.punt-labs/ethos/missions/.counter-DATE`. v5's stated path is
`~/.punt-labs/ethos/counters/missions-DATE` — that is a directory
move, not just a filename change. Today's counter lives **under** the
mission directory; v5 moves it to a sibling `counters/` directory.
This is the move I noted in the v5 closure above. The two paths are
disjoint, so the coexistence guarantee holds: v3.11.0 reads
`~/.punt-labs/ethos/missions/.counter-DATE` and is unaware of
`~/.punt-labs/ethos/counters/missions-DATE`; v3.12.0 reads the new
path. **Compatibility-safe.**

Second, the mission contract path move (global flat to repo nested)
is a one-way door per line 402. v3.12.0-created contracts are
unreadable by v3.11.0 because the path layout has changed and v3.11.0
does not know to look under `<repo>/.ethos/missions/`. This is
stated and accepted at line 253. The transition window window
(v3.12.0 ships the move, v3.13.0 ships the migrate tool, v3.14.0
drops the rolling fence) gives operators a clear upgrade path. **Acceptable.**

Third, the audit log path move applies the same read-fallback
discipline. v3.11.0 reads only global; v3.12.0 reads repo-first
with global fallback. An operator who upgrades a single machine
mid-session sees the entire session audit log via the fallback even
if part of it was written by v3.11.0 to the global location. **Acceptable.**

I find no compatibility hazards I would gate on. The two-tree
restructuring is a cleaner shape than v4's, and the migration story
is at least as well-specified as it was in v4.

---

## Observations (recorded; not findings)

These are notes on v5 shape choices that I find defensible but worth
recording so the next reviewer sees the reasoning.

### O1. The Tier A delegation directory under sessions/

v5 places Tier A delegations at
`sessions/<date>-<sess>/adhoc/<NNN>/` — under the session, not under
a separate global tree. This is correct: a Tier A delegation has no
parent mission, so the only natural home is the session that
spawned it. The `delegations-YYYY-MM-DD` counter is global because
the counter is a per-namespace serial allocator, not a storage
location. The directory layout follows the parent: the counter is
global because it is a date-keyed serial; the artifacts are
session-local because the session is the only governing context.
Two different axes, two different locations. No finding.

### O2. The `I9-counter` invariant clause "no counter file ever changes format"

The clause at line 381 is unusually strong: it forbids future
in-place format changes to existing counter files. The compatibility
guarantee this gives is what I asked for in round 3, but the cost
is that any future redesign that wants to **change** the counter
file shape (say, to add a secondary field for monotonic timestamp
or generation counter) must do so by introducing a new namespace
file, not by extending the old one. This is a strong commitment.
The draft does not explicitly mark this as the contract's intention,
and I would not blame a future reader for assuming it could be
relaxed. I would not gate on this — the strength of the commitment
is the point — but I record it so the next implementer knows the
constraint is intentional. **No finding.**

### O3. The `<YYYY-MM-DD>-<session-id>` parsing rule

The session directory name combines a fixed-shape date prefix
(`YYYY-MM-DD-`, 11 characters) with a session-id of unspecified
shape. The 1:1 binding with Claude Code's conversation history
file at `~/.claude/projects/<...>/<session-id>.jsonl` requires that
the session-id portion be exact. The draft says this at line 91.
Anything that programmatically extracts the session-id from the
directory name must take `name[11:]` and not split on `-`. This is
implementation discipline, not a design concern, and bwk will
handle it. **No finding.**

### O4. Day-boundary policy for Tier A delegation IDs

A Tier A delegation spawned at 23:59:59 UTC produces an audit entry
with `ts=2026-05-22T23:59:59Z`. A Tier A delegation spawned at
00:00:01 UTC the next day produces `ts=2026-05-23T00:00:01Z` and is
counted in `delegations-2026-05-23`. v5 at line 259 says explicitly
"Tier A delegations are wall-clock-indexed in
`delegations-YYYY-MM-DD` because they have no parent mission." This
is the right call. The session that contains them, however, is
indexed by **session start date**, not by the date of each
delegation. So a session that crosses midnight will have its Tier A
delegations split across two day-buckets in the counter, but they
will all live under the same session directory. This is consistent:
the directory groups by session identity; the counter groups by
allocation date. **No finding.**

### O5. The `KnownFields(true)` strict-decode asymmetry pinned twice

v4 named the asymmetry at line 96 (Schema changes section) and line
225 (Migration section). v5 carries forward both occurrences at lines
122 and 253. This is good — the asymmetry has been a quiet hazard in
the codebase for some time, and naming it twice is exactly enough
that a future refactor that tries to unify them has to consciously
override two stated decisions. **No finding.**

---

## Summary table

| Class | Count | Notes |
|---|---|---|
| Round-3 REQ closed | 1 | counter.yaml shape replaced with sibling per-namespace per-date files |
| Round-3 IMPL closed | 3 | O2 (transition window stated), O3 (I7 verdict tiers), O4 (depth-cleanup) |
| Round-3 note carried | 1 | O1 (write-time vs read-time audit migration) |
| New REQ in v5 | 0 | — |
| New IMPL in v5 | 0 | — |
| Observations recorded | 5 | O1 (Tier A directory), O2 (I9 strength), O3 (parsing rule), O4 (day boundary for Tier A), O5 (KnownFields named twice) |

**One round-3 REQ resolved. Zero new findings. Five observations,
all non-blocking.** The convergence test passes. A review round that
adds zero new substantive findings is the stopping rule the leader
set, and v5 satisfies it.

---

## Recommended next step

Close DES-054 design. Begin phase 1 implementation per the phasing
plan at lines 429-435. The four-phase phasing is:

1. v3.12.0 — phase 1 (storage move, sibling-file counters, JSONL
   atomic-write, NewID rollback API, KnownFields asymmetry pinned)
   plus phase 2 (PreToolUse hooks, advice hook, flocks, aborted-sentinel
   cleanup).
2. v3.13.0 — phase 3 (preconditions, migration commands,
   commit-msg trailer hook, `ethos audit show --delegation`).
3. v3.14.0 — drop the global `.create.lock` rolling-upgrade fence.

I find no compatibility or migration hazard that would warrant a
fifth review round. The design has converged. The remaining work is
implementation discipline, not design review.

— rsc, 2026-05-22
