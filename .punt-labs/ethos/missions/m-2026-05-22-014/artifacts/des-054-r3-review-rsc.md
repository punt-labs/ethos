# DES-054 v4 Round-3 Review — rsc (compatibility / migration)

**Verdict: APPROVE WITH ONE NAMED EDIT.** The convergence test holds in
substance — the 16 round-2 findings (E1 through E16) all landed in v4
in their stated form, the design is internally consistent, and the new
invariants (`I8-type`, `I8-stable`, `I8-live`, `I9-counter`,
`I10-audit-atomic`) make the tier discipline and append-atomicity
properties first-class. I would call it pure APPROVE except for one
substantive finding that the round-2 review did not surface because it
required comparing the v4 draft against the current code: the
`counter.yaml` schema reshape is a stronger compatibility break than
the draft's invariant captures, and one of the two reasonable fixes
needs to be picked before phase 1 begins. Everything else in this
review is observation or paint.

The classification grid uses the same convention as round 2 — `[REQ]`
changes operator-visible behaviour or the surface contract; `[IMPL]`
is documentation refinement or implementation detail that follows from
already-stated semantics.

---

## Convergence check — round-2 findings (E1 through E16)

Each round-2 finding spot-checked against v4 verbatim. I read the v4
draft top-to-bottom and located the line that addresses each finding.

| # | Round-2 finding | v4 location | Verdict |
|---|---|---|---|
| E1 | Audit-log move state machine (read-fallback, tombstone, first-write trigger) | line 208-219 | Landed in thinner form than I proposed; see Observation 1 |
| E2 | Precondition evaluator reads both legacy and new-location audit logs during transition | line 221 | Landed |
| E3 | Session rosters never move; audit logs do | lines 193-196 | Landed — explicit two-bullet section |
| E4 | `KnownFields` asymmetry: strict for contracts, permissive for audit entries | line 96, line 225 | Landed — named in two places |
| E5 | Concurrency table adds Tier A per-session audit-log flock row | line 268, line 273 | Landed — same flock covers roster + audit per rop R3 |
| E6 | New invariant for audit append atomicity (no interleave) | line 363-366 (`I10-audit-atomic`) | Landed |
| E7 | `ethos audit show --delegation <id>` joins Tier A + Tier B audit views | line 279, line 409 (phase 3) | Landed |
| E8 | `counter.yaml` permissive YAML decoding; future keys append-only | line 243, line 357-358 (`I9-counter`) | Landed — but see Finding 1 below |
| E9 | Day-boundary policy: mission's day, not wall clock | line 245 | Landed with worked example |
| E10 | Specify `counter.yaml` shape (mission_counter, tier_a_delegation_counter, mission_delegation_counters map) | line 231-241 | Landed in different shape than I proposed; see Finding 1 |
| E11 | `ethos audit migrate` exits 0 on no legacy state | line 249 | Landed verbatim |
| E12 | `ethos audit migrate` idempotent across repeated runs | line 250 | Landed verbatim |
| E13 | `ethos audit migrate` recovery after partial failure | line 251 | Landed verbatim |
| E14 | Read-only legacy filesystem fallback policy | line 252 | Landed verbatim |
| E15 | `ethos audit migrate` creates `<repo>/.ethos/sessions/` with `0o700` if absent | line 253 | Landed verbatim |
| E16 | Cross-repo migration: only migrate sessions whose roster names this repo | line 254 | Landed verbatim |

Sixteen for sixteen. The E1 state machine landed in a simpler shape
than my round-2 proposal — the v4 reader does **fallback** without
**concatenation** (line 214-218: `if exists(p_repo) return read(p_repo)`).
That is a defensible simplification and Observation 1 records it as a
note, not a finding. The other fifteen landed verbatim or in
substantively equivalent form.

---

## New finding from comparing v4 against the current code

### [REQ] Finding 1. `counter.yaml` is a file-shape break, not a permissive-decode-compatible append

The round-2 finding E8 asked for an invariant pinning `counter.yaml`
to permissive YAML decoding so future schema additions could
**append** top-level keys safely. The v4 draft codifies the invariant
at line 357-358 (`I9-counter`) and specifies the new file shape at
line 231-241. **The v4 file shape is not an append to today's
on-disk format. It is a full reshape.** This is the only substantive
finding the round-2 review did not surface; it requires reading
`internal/mission/id.go` (which round 2 cited but did not re-read
against the new shape) to see.

**Symptom.** Today, `internal/mission/id.go:43-44` writes one counter
file **per day**, named `~/.punt-labs/ethos/missions/.counter-YYYY-MM-DD`
(dot-prefixed). The contents are a single integer — the next mission
number for that day. A v3.11.0 binary that allocates an ID on
2026-05-22 reads `.counter-2026-05-22` (or treats absent as zero),
increments, and writes `.counter-2026-05-22` back.

The v4 draft at line 229-241 proposes a **single file**,
`~/.punt-labs/ethos/missions/counter.yaml` (no dot prefix, no per-day
suffix), with **nested YAML** keying missions and delegations by date
or mission ID:

```yaml
schema_version: 2
missions:
  2026-05-22: 9
delegations:
  adhoc:
    2026-05-22: 14
  mission:
    m-2026-05-22-007: 3
    m-2026-05-22-008: 2
```

Three independent changes in one move:

1. **Filename**: `.counter-YYYY-MM-DD` → `counter.yaml`. Different
   path; the old file is invisible to the new binary unless explicitly
   read.
2. **Visibility**: dotfile → ordinary file. Tooling that lists the
   directory (operators inspecting state, future audit tools) sees the
   counter where it did not see one before.
3. **Shape**: scalar int per file → nested map per file. A v3.11.0
   binary reading `counter.yaml` does not recognize the format at all
   — its decoder targets `<repoRoot>/.ethos/missions/.counter-YYYY-MM-DD`
   as a path that does not exist, falls through to "missing file is
   zero" (line 64 comment in `id.go`), and **allocates ID 1**.

**Analysis.** The third change is the dangerous one. `I9-counter`'s
"meaning(k) is monotonic across versions" promise binds future
**additions** to `counter.yaml`, but v3.11.0 → v3.12.0 is not a
within-`counter.yaml` schema bump — it is a **file-format
replacement**. v3.11.0 does not read `counter.yaml` at all. It reads
`.counter-YYYY-MM-DD`. If v3.12.0 writes `counter.yaml` and leaves
the old per-day files in place, v3.11.0 reads the now-stale per-day
files and re-allocates already-issued IDs. If v3.12.0 writes
`counter.yaml` and unlinks per-day files (or never writes them),
v3.11.0 on the same machine on the same day starts from zero. Either
way, v3.11.0 and v3.12.0 are not free to coexist on one machine
across the upgrade window.

This is qualitatively the same hazard the rolling-upgrade fence at
line 223 was added to prevent for `.create.lock` — two binaries
allocating from different state. The fence covers `.create.lock`; it
does not cover `counter.yaml`.

**The fix.** Two acceptable shapes, leader picks one:

1. **Dual-write transition** (mirrors the `.create.lock` rolling
   upgrade pattern at line 223). v3.12.0 writes **both** the new
   `counter.yaml` and the legacy `.counter-YYYY-MM-DD` per-day file
   in lockstep, keyed by the same lock (`counter.yaml.lock`). Read
   path: `counter.yaml` is canonical; legacy is mirror-only. v3.13.0
   drops the legacy write. Transition window is one minor version,
   same as the create-lock fence.

2. **Preserve the per-day file scheme inside the new envelope**.
   Keep `.counter-YYYY-MM-DD` as the on-disk shape (one file per day,
   one int per file), and add a sibling per-day file for delegation
   counters (`.counter-delegations-YYYY-MM-DD`, `.counter-mission-<id>`
   for mission-bound delegations). No unified `counter.yaml`.
   `schema_version` becomes a per-day-file header comment, and
   `I9-counter` retains its permissive-decode semantics on the
   per-day files. This is the smaller change to the migration story
   at the cost of more files on disk.

My recommendation is option 1 — it parallels the `.create.lock`
pattern already chosen for the rolling-upgrade case, and the unified
`counter.yaml` shape has real legibility wins for operators inspecting
state. Option 2 is the smaller-blast-radius option if the leader
wants to minimize the migration surface on the v3.12.0 release.

Either way, the DES needs one new paragraph in the migration section
naming the chosen path before phase 1 starts. The mechanism is small;
leaving the choice implicit is the problem.

This is the only substantive new finding. If addressed, v5 is the
final draft and the design converges.

---

## Observations (not findings)

These are notes on shape choices in v4 that I find defensible but
worth recording so the reasoning survives.

### Observation 1. The v4 read-fallback at line 214-218 is read-only and does not concatenate

My round-2 edit-1 table proposed a **write-time** migration: the
first write to a session whose legacy audit log exists triggers a
copy-forward of the legacy entries into the repo-local file, fsync,
then tombstone the legacy file. The v4 draft at line 208-219 keeps
only the **read-time** fallback: if the repo path exists, read it;
else if the legacy path exists, read it; else empty.

The difference matters only for the case where a session writes
entries on both sides of the upgrade boundary **before** the operator
runs `ethos audit migrate`. In v4's design, the operator sees the
new entries in the repo path and the legacy entries are invisible
until `audit migrate` runs. After migrate, the legacy file becomes
the implicit tombstone (line 252) and the union is at the repo path.

This is acceptable because:

1. The migration command is explicit (the draft commits to this at
   line 377). Operators are expected to run it as part of the
   upgrade procedure.
2. The window where the entries are split is bounded by operator
   action, not by clock time.
3. The read-time fallback never returns a wrong answer — it returns a
   strict prefix of the union, never a stale post-upgrade view.

The trade-off is that an operator who upgrades and does not run
`audit migrate` for a week sees an incomplete history during that
week. The round-2 proposal would have made the migration transparent
at the cost of more code on the write path. v4 chose
operator-explicit over silently-correct. Both shapes are sound.

I would not edit v4 to add the write-time migration. The note is
recorded so the next reviewer who asks "why isn't the migration
transparent" has the answer.

### Observation 2. Phase 1 ships the storage move; phase 3 ships the migration tool

Line 407 puts the two-root storage state machine in **phase 1**.
Line 409 puts `ethos audit migrate` in **phase 3**. If phase 1 ships
in v3.12.0 and phase 3 ships in v3.13.0, the migration tool arrives
one minor version after the storage move. Operators on v3.12.0 live
with read-time fallback only, which is correct but means they cannot
**consolidate** their audit history into the new location until
v3.13.0.

The rolling-upgrade fence at line 223 says "transition window is one
minor version" for `.create.lock`. The audit-log legacy-fallback
window is **at least** two minor versions under the phasing in this
draft (v3.12.0 ships the move, v3.13.0 ships the consolidation
tool). That is fine if intentional; it is worth one sentence in the
migration section noting that the audit-log transition window is
wider than the create-lock window. **[IMPL]** — bwk can add the
sentence when implementing phase 1 if the leader confirms the
phasing.

### Observation 3. I7 verdict semantics for Tier A

The verdict domain at line 327-329 (`I7`) is `{open, pass, fail,
error, aborted}`. Tier A delegations have no contract and no
evaluator (line 336-338, `I8`). What does `verdict` mean for a Tier
A delegation? An audited-only spawn has no acceptance criteria; its
"verdict" is whatever value the leader assigns when the spawn
returns. The draft does not say. Either Tier A delegations carry
verdicts (and the field is leader-assigned) or they do not (and the
field is absent / `omitempty` / `nil`).

This is a minor IMPL clarification, not a REQ — the implementing
specialist can pick when writing the delegation record schema. But
the invariant at I7 should either restrict the domain to Tier B or
add a clause for Tier A. **[IMPL]** — bwk to resolve at
implementation time; document in the delegation record schema.

### Observation 4. `max_delegation_depth` refusal leaves Tier B mission record dangling

Line 281 says exceeding `max_delegation_depth` refuses the spawn at
PreToolUse-on-Agent with a clear error. For Tier B spawns, the
contract was already created by `mission create` or `mission
dispatch` **before** the spawn fires (line 169-176). A depth-exceed
refusal at PreToolUse leaves a Tier B mission record on disk with
`open` verdict and no worker bound — semantically dangling.

The recovery path exists (`ethos mission close --status failed`,
which the contract for mission `m-2026-05-22-014` uses in its
success criteria) but it is manual. **[IMPL]** — the PreToolUse
refusal handler should call `mission close --status failed` on the
just-created mission record as part of the refusal, so the depth
guard does not leave half-state. The draft does not say this; the
implementing specialist should add it. Not a REQ because the
operator-visible behaviour (refused spawn, depth-exceeded error) is
correct either way; this is internal cleanup.

---

## Summary table

| # | Finding / observation | Class | Owner |
|---|---|---|---|
| 1 | `counter.yaml` shape reshape (not append-compatible) needs dual-write transition or per-day preservation | REQ | claude (DES edit) + bwk (impl choice) |
| O1 | v4 read-fallback is read-only; write-time migration was dropped (acceptable) | note | — |
| O2 | Phase 1 ships storage move; phase 3 ships migrate tool — transition window widens to two minor versions | IMPL | bwk (sentence in DES at impl time) |
| O3 | I7 verdict semantics undefined for Tier A | IMPL | bwk (delegation record schema) |
| O4 | `max_delegation_depth` refusal should close just-created Tier B mission | IMPL | bwk (PreToolUse refusal handler) |

**One REQ. Four observations.** The round-2 review surfaced eight
REQs and eight IMPLs against v3; the round-3 review surfaces one REQ
against v4. The signal is convergence: each iteration on the
migration / compatibility axis reduces the open question count by an
order of magnitude. v4 → v5 (with finding 1 applied) is the final
shape.

---

## Recommended next step

Apply finding 1 to v5 — one paragraph in the migration section
naming the `counter.yaml` transition strategy (option 1 dual-write or
option 2 per-day preservation). The four observations are not gating;
they can be picked up during phase 1 / phase 3 implementation by the
specialist (bwk) without a further round of design review.

If the leader prefers, the four observations can be folded into v5
as well — they are documentation refinements, not changes of
substance — but they do not block round-3 approval.

The convergence test passes for the migration / compatibility axis.
Design is stable modulo finding 1.

— rsc, 2026-05-22
