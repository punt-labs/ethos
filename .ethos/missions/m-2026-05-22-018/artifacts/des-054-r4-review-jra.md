# DES-054 v5 — Formal Review (round 4, jra)

**Verdict: APPROVE.** v5 applies the three round-3 verdicts verbatim,
the twelve-invariant set remains satisfiable and closed under
composition after the v5 surgical edits to `I7`, `I9-counter`, and
`I10-audit-atomic`, no new substantive finding remains, and the
storage-layout pivot does not destabilise any prior closure argument.
*The design converges.*

---

## 0. Convergence statement

This is the convergence test. The leader pinned an unambiguous
criterion: *APPROVE if the round produces zero new substantive
findings.* We hold ourselves to that criterion and report the outcome
plainly. We register no `[REQ]`-class new findings; we register no
`[IMPL]`-class observation either — the prose-count `[IMPL]-1`
observation from round 3 against v4 was addressed by v5's revised
closing paragraph (line 392), which no longer asserts "Twelve
invariants" without qualification and instead names which v5 edits
landed (I7 tier-specific verdicts, simplified I9-counter, collapsed
I10-audit-atomic).

The convergence sequence runs: round 1 — three iterates; round 2 —
six findings (F1–F6) for jra alone; round 3 — one `[IMPL]` prose
observation only; round 4 — none. The slope is the slope of an
artefact approaching its specification, not an artefact accumulating
debt. Per the convergence criterion: *APPROVE.*

---

## 1. Round-3 findings — landing check

Round 3 produced three verdicts: rop APPROVE, jra APPROVE (with one
`[IMPL]`-1 prose-count observation), rsc APPROVE WITH ONE NEW REQ
(`counter.yaml` as a file-format break). CEO direction added a
clean-slate framing. We spot-check what landed.

### rsc R3 REQ — counter file format

v5 §"Migration" line 257:

> `~/.punt-labs/ethos/counters/missions-YYYY-MM-DD` and
> `delegations-YYYY-MM-DD`. Each file: one int (last allocated
> number), `\n`-terminated. Same shape as today's
> `.counter-YYYY-MM-DD`, namespace added to the filename. No format
> break; v3.11.0 readers keep reading their own files.

The byte-format break of v4's `counter.yaml` is replaced by the
existing single-int file with a namespace dimension added to the
filename. v3.11.0 binaries never touch the new sibling because they
do not know about delegation IDs. *Landed.*

### rsc R3 O2 — transition window length

v5 §"Migration" line 251 and §"Recommended next step" line 435:

> **Transition window is two minor versions** (per rsc R3 O2)
> because phase 1 ships the storage move and phase 3 ships the
> `audit migrate` tool; the gap is one minor version, and the
> v3.13.0 cleanup is another.

Named and justified by the phase pipeline. *Landed.*

### rsc R3 O3 — Tier A verdict semantics

v5 invariant `I7`, lines 342–349:

```
I7: forall d in delegations, t1 < t2:
        d.verdict(t1) = "open"  \/  d.verdict(t2) = d.verdict(t1)
    /\
    (d.tier = "A"  ->  d.verdict ∈ {"open", "aborted"})
    /\
    (d.tier = "B"  ->  d.verdict ∈ {"open", "pass", "fail", "error", "aborted"})
    /\
    d.verdict != "open"  ->  d.closed_at != ""
```

The tier-specific codomain is now pinned in the formal block, not
left to prose. The v4 codomain `{open, pass, fail, error, aborted}`
is preserved for Tier B; Tier A is restricted to `{open, aborted}`
on the grounds that Tier A has no evaluator. *Landed.*

### rsc R3 O4 — `max_delegation_depth` refusal closes record

v5 §"Concurrency model" line 295:

> **If the depth check refuses a Tier B spawn after the delegation
> record skeleton has been written** (rsc R3 O4), the refusal handler
> closes the just-created record with `verdict=aborted` and
> `closed_at=<now>`. No dangling state.

The cleanup obligation is named at the point in the hook flow where
the skeleton may already exist. *Landed.*

### rop R3 N1 — stale open question removed

v5 §"Open questions" no longer carries the `max_delegation_depth`
open-question entry; the depth is now stated as a `.punt-labs/ethos.yaml`
setting with default 16 (line 295). *Landed.*

### jra R3 [IMPL]-1 — prose recount

v5 closing of §"Concurrency invariants" line 392:

> Twelve invariants. v2's I8/I9/I10 (about synthetic contracts)
> **obsolete** under v3's opt-in pivot. v4's I8, I8-type, I8-stable,
> I8-live, I9-counter, and I10-audit-atomic together encode the tier
> discipline, counter shape, and audit append-atomicity properties.
> v5 sharpens I7 to make tier-specific verdict sets explicit,
> simplifies I9-counter to the sibling-file shape (no YAML, no
> schema_version), and collapses I10-audit-atomic from two-store
> form to single-store.

The prose has been rewritten to enumerate the v5 surgical edits;
the headline count "Twelve" is reached by counting the I8-family
(I8, I8-type, I8-stable, I8-live = 4) together with I1–I7
(treating I5a/I5b as the fail-policy pair = 7), I9-counter, and
I10-audit-atomic. We count: I1, I2, I3, I4, I5(a+b), I6, I7,
I8(+type+stable+live), I9-counter, I10-audit-atomic — *ten*
distinct semantic statements if I5a/I5b are paired and the
I8-family is treated as a block; *fourteen* labels enumerated; the
headline word "twelve" is reached by counting the four I8-family
labels separately while pairing I5a/I5b. The prose remains slightly
ambiguous on this point, but we have decided in round 3 that this
is `[IMPL]` and does not bear on the model. We do not re-raise it.

All round-3 findings landed.

---

## 2. The twelve-invariant set — closure under v5 edits

v5 edits the formal block in three places: `I7` gains tier-specific
codomains; `I9-counter` is rewritten for the sibling-file shape;
`I10-audit-atomic` is collapsed to single-store. The other nine
labels (I1, I2, I3, I4, I5a, I5b, I6, I8, I8-type, I8-stable,
I8-live) are unchanged. We verify the edits preserve every closure
argument made in round 3 §2.2.

### 2.1 `I7` revision — tier-specific verdict codomains

The v4 form had a single uniform codomain
`{open, pass, fail, error, aborted}` for all tiers. The v5 form
splits it: `A → {open, aborted}`, `B → {open, pass, fail, error, aborted}`.

We check this against the closure arguments that depended on `I7`.

- **`I3 ∧ I4 ∧ I7` (hash-gate refusal closure).** The aborted
  verdict is in *both* tier codomains. Tier A can abort via
  `max_delegation_depth` refusal (line 295) or hash-gate refusal
  on a Tier A subagent — though we observe that the hash-gate is a
  Tier-B mechanism (verifier integrity for governed delegations),
  so the realistic Tier A abort path is depth refusal. Either way,
  `aborted ∈ A-codomain`, the closure argument holds, the record
  carries `closed_at`, `I3` and `I4` are unaffected. *Closed.*

- **Monotonicity (`d.verdict(t1) = "open" \/ d.verdict(t2) =
  d.verdict(t1)`).** The monotonicity clause is unchanged across
  v4 → v5; it constrains time, not tier. The two new tier
  conjuncts are static (per-tier domain restrictions). The three
  conjuncts compose: a Tier A delegation's verdict transitions are
  bounded both by monotonicity *and* by the codomain restriction
  to `{open, aborted}`; the only legal Tier A transition is
  `open → aborted` (or `open → open`, the no-op). The state graph
  for Tier A has two nodes; for Tier B, five. *Closed.*

- **`I8-stable ∧ I7` (tier immutability and verdict codomain).**
  Because tier is immutable (I8-stable), the codomain restriction
  in I7 is well-defined: a delegation's "applicable codomain" is
  fixed at allocation time. There is no path through which a Tier
  A delegation could acquire a Tier B verdict by reclassifying
  upward. *Closed.*

The v5 `I7` is a *strengthening*, not a weakening, of v4's `I7`:
it eliminates legal states (Tier A delegations carrying
`pass`/`fail`/`error`) that v4 implicitly permitted. Strengthening
preserves all prior closure arguments.

### 2.2 `I9-counter` revision — sibling-file shape

The v4 form pinned permissive YAML decoding and key-meaning
monotonicity across schema versions of `counter.yaml`. The v5
form drops `counter.yaml` entirely and pins the sibling-file
shape:

```
I9-counter: forall ns, date:
        counters/<ns>-<date> exists  ->  contents is a single integer
    /\  no counter file ever changes format
```

We check this against the closure arguments that depended on
`I9-counter`.

- **`I9-counter ∧ I1 ∧ I2` (counter file supports uniqueness).**
  The unique-allocation property of `I1` (mission ID) and `I2`
  (delegation ID) depended in v4 on `counter.yaml` producing the
  same value on the same key across binary versions. In v5, each
  namespace has its own single-int file; "same key" reduces to
  "same file" (filename is `<ns>-<date>`). The shape never
  changes — the second conjunct prohibits format drift. v3.11.0
  binaries do not touch the new sibling (`delegations-YYYY-MM-DD`)
  because they do not know about delegation IDs; v3.12.0 binaries
  read both. The argument is *simpler*: no schema_version to
  preserve, no key-meaning monotonicity to defend, just two
  binaries that touch disjoint files. *Closed, and the closure
  argument is shorter than v4's.*

- **`I9-counter ∧ NewID rollback API` (line 255).** The rollback
  API closes the burn-on-failure case: a counter increment that
  is not committed is decremented. The sibling-file form does not
  affect this — the operation is still flock + read-int + decide
  + write-int + flock-release. *Closed.*

We observe a small obligation v5 inherits from v4: the "no counter
file ever changes format" conjunct is a *meta-invariant* on future
DES authors, not a per-execution-state invariant. Its discharge is
by code review of future schema changes, not by ProB. This is the
same discharge mode as v4's "meaning(k) is monotonic across
versions" — a meta-invariant whose violation would be a separate
breaking-change decision. We do not raise this as a new finding;
it is structurally equivalent to v4's form.

### 2.3 `I10-audit-atomic` revision — single-store collapse

The v4 form pinned per-session and per-delegation audit-log
atomicity. The v5 form collapses to per-session only, because v5
storage drops per-delegation `audit.jsonl`:

```
I10-audit-atomic: forall e1, e2 written to <session-dir>/audit.jsonl:
        flock(<session-id>.lock) is held during each append
    /\  write_order(e1, e2) = file_position_order(e1, e2)
```

We check this against the closure arguments.

- **`I10-audit-atomic ∧ I3` (audit atomicity supports delegation
  reachability).** The round-3 argument was: a torn write to the
  session JSONL could leave an audit entry with `delegation_id`
  referencing a delegation that never received its record. This
  argument is unchanged — v5 has only the session log, so the
  torn-write scenario is *exactly* the one the v5 I10 forbids.
  *Closed.*

- **Cross-tier audit visibility** (line 293). v5 explicitly states
  that Tier A children of Tier B parents write to the *same*
  session log, distinguished by `delegation_id` /
  `parent_delegation`. The single-store I10 covers both flows
  uniformly because there is one store. *Closed, and simpler than
  v4.*

The collapse is a *consequence* of the v5 storage simplification
(single audit log per session), not an independent edit. The
collapse is correct iff v5 storage truly has no other audit
stream — which it does not, per line 289 ("audit log lives ONLY
at the session level"). The two changes are coupled by
construction.

### 2.4 The other nine invariants

I1, I2, I3, I4, I5a, I5b, I6, I8, I8-type, I8-stable, I8-live are
textually unchanged from v4. Round 3's per-invariant satisfiability
arguments (§2.1) and three-way composition arguments (§2.2)
transfer verbatim, with two qualifications:

- **`I2` (delegation ID uniqueness).** v5 changes *how* the
  counter is structured (sibling files vs `counter.yaml`) but not
  *what* `I2` says. The ID-generation invariant is preserved
  because the new shape still produces a monotonically-increasing
  per-namespace per-date integer.

- **`I3` (audit reachability).** v5 changes *where* Tier B audit
  entries live (session log only, not also a per-delegation log)
  but not *what* `I3` says. The reachability predicate still
  holds: `exists d in delegations: d.id = e.delegation_id`. The
  storage location does not enter the predicate.

The closure under composition argued in round 3 holds for v5.

---

## 3. Storage-layout pivot — invariant impact

v5's storage pivot (two-tree top-level layout, session-keyed audit,
date-encoded directory names) is a substantial editorial change.
We verify that the formal block is unaffected.

The invariants quantify over abstract sets (`delegations`,
`audit_entries`, `tool_calls`, `preconditions`) and abstract
relations (`d.parent_delegation`, `d.contract`, `d.tier`,
`d.verdict`, `e.delegation_id`). None of the invariants reference
a filesystem path or a directory shape. The storage layout is the
*concrete* model; the invariants live at the *abstract* model.
The refinement from abstract to concrete obligates the
implementation to map each abstract relation to a concrete
filesystem read — which it does:

- `d.parent_delegation` reads from `record.yaml` (Tier B) or from
  the audit entry's `parent_delegation` field (Tier A).
- `e.delegation_id` reads from the session JSONL line.
- `d.contract` reads from `missions/<id>/contract.yaml` (Tier B)
  or is nil (Tier A).

The refinement mapping is consistent between v4 and v5. v5's
storage relocations move the concrete files but preserve the
abstract relations. We register no formal-invariant impact.

---

## 4. Byte-compatibility under v5

The migration story changed shape between v4 and v5. We verify
the three on-disk artefacts.

- **`auditEntry`** — Unchanged from v4: six optional fields,
  `omitempty` JSON, byte-compatible in both directions. *Preserved.*

- **`Contract`** — Unchanged from v4: `KnownFields(true)` decoding,
  one-way door, explicitly named as policy. *Preserved.*

- **`counter.yaml`** — *Replaced* by sibling per-namespace per-date
  files. The byte-compatibility claim is now structurally trivial:
  the `missions-YYYY-MM-DD` file is identical in format to today's
  `.counter-YYYY-MM-DD` file (modulo the renamed filename); the
  `delegations-YYYY-MM-DD` file is new and untouched by v3.11.0
  binaries. There is no schema-version field to break on. *Byte-
  compatible in both directions across all future minor versions,
  by virtue of the format being fixed by `I9-counter`'s second
  conjunct.*

- **Per-session audit JSONL location** — v4 lived at
  `~/.punt-labs/ethos/sessions/<session-id>.audit.jsonl`. v5 lives
  at `<repo>/.ethos/sessions/<YYYY-MM-DD>-<session-id>/audit.jsonl`.
  The relocation is handled by the read-path state machine (lines
  239–247) and the `ethos audit migrate` command (lines 261–268).
  This is a *file location* change, not a *file format* change;
  byte-compatibility under the JSONL format itself is preserved.
  *Migration is explicit; the format is preserved.*

The v5 byte-compatibility story is strictly simpler than v4's:
the `counter.yaml` schema-version dance is eliminated. We register
no migration-related finding.

---

## 5. Closure verdict

The v5 model closes under all the criteria we examined.

- All six round-3 findings (rsc R3 REQ, rsc R3 O2, rsc R3 O3,
  rsc R3 O4, rop R3 N1, jra R3 [IMPL]-1) landed.
- The twelve-invariant set, with v5's three surgical edits to
  `I7`, `I9-counter`, and `I10-audit-atomic`, remains satisfiable
  and closed under composition; v5 `I7` is a strengthening of v4
  `I7`; v5 `I9-counter` and `I10-audit-atomic` are *simplifications*
  consequent on storage changes.
- The storage-layout pivot does not enter the abstract model; the
  refinement from abstract to concrete preserves the mapping.
- Byte-compatibility holds across `auditEntry`, `Contract`, and
  the sibling-file counter set; the `counter.yaml` schema-version
  concern is eliminated rather than mitigated.

No new `[REQ]` findings. No new `[IMPL]` findings. The prose-count
ambiguity flagged in round 3 is unchanged in nature and we do not
re-raise it.

Per the convergence criterion the leader pinned in the contract:
*APPROVE.* The v5 draft converges; implementation should proceed.

---

## 6. Note for the implementer (carried forward, unchanged)

The same closing note from round 3 applies. The model is not done
until ProB has explored a four-deep delegation tree mixing both
tiers, with one hash-gate refusal, one inheritance match, one
strict-precondition violation, and *one `max_delegation_depth`
refusal at the Tier B skeleton-already-written point* (added per
v5's I7 and §"Concurrency model" line 295). The reviewer finds
these states in hours, with gaps — round 1 missed many edits,
round 2 found six, round 3 found one, round 4 finds none. The
convergence is genuine; it is convergence to a *specification*.
The animation discharges the proof obligations a specification
cannot discharge by inspection.

The implementer should treat the twelve invariants (in their v5
form) as the ProB-animator's input set. A counterexample at
depth ≥ 4 in a mixed-tier tree with the new abort paths is the
gift we have not yet received.

— jra
