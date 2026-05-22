# DES-054 Formal Review — jra (REDO)

**Verdict: ITERATE.** The prior formal analysis stands without revision; on
re-examination the nine named edits are individually necessary and jointly
sufficient, with two refinements added below that the first pass under-stated.

## 0. Relationship to the prior review

This is the second pass on the unchanged draft of DES-054. The first
verdict — at `.ethos/missions/m-2026-05-22-003/artifacts/des-054-review-jra.md` —
was ITERATE with nine named edits and a list of six concrete admitted states
the invariants must forbid. The DES has not been revised since. On second
reading I *endorse* §§1–8 of the prior review in full and add two refinements
below (§§R1–R2). The verdict is unchanged.

I record explicitly what the redo gives that the original did not: a
self-assessment of completeness. Two items I now believe were *under-stated*
in the original and one item I now believe was *correct as stated* but for a
reason I did not name. None of the nine edits is retracted; none is weakened;
two are tightened.

## 1. Re-checking the seven pseudo-Z invariants for satisfiability

The contract pins this question explicitly: *name one invariant that is
unsatisfiable under realistic mission shapes, or confirm closure under
composition.* I re-checked the seven statements one by one. None is
*individually* unsatisfiable. The composition closure check, however,
identifies one pair the original did not flag.

### 1.1 I3 ∧ I4 under verifier isolation

I3 requires that audit entries naming a delegation refer to one in
`delegations`. I4 requires that every delegation's `parent` field refer to a
live session. When a verifier spawn fails the DES-035 hash gate, the
parent-side PreToolUse-on-Agent hook has already written the delegation
skeleton (per the DES-054 sequence in §6 of the prior review). The subagent
session never starts. So:

- the delegation exists (skeleton written)
- the parent session exists (the spawning Claude process)
- I4 holds: `parent` resolves
- I3 holds vacuously: no audit entries reference this delegation
- *but* the delegation's `verdict` is `open` and `closed_at = ⊥`

I7 (the monotonicity invariant I added in §2.7) then *forbids* this state
unless the sentinel verdict from Edit 9 is written. So I3 ∧ I4 ∧ I7 *jointly*
forbid the phantom-delegation state — which is the right behaviour. The
prior review caught this but treated Edit 9 as a clean-up; in fact Edit 9 is
*load-bearing for invariant closure*. Without it, the seven (eight, with I7)
invariants are mutually inconsistent under realistic spawn failures.

This sharpens Edit 9: the sentinel verdict is not housekeeping. It is what
makes the invariant set satisfiable in the presence of hash-gate refusal.

### 1.2 The remaining six are individually satisfiable

I1, I2 (as strengthened by Edit 2), I5, I5b (introduced by Edit 3), I6, and I7
are each individually satisfiable. The original review's per-invariant
satisfiability arguments hold without revision.

### 1.3 No invariant is unconditionally unsatisfiable

I have not found an invariant in the DES that admits no realistic mission
shape. The completeness gap is the live problem, not satisfiability.

## 2. Audited-delegation chain at depth ≥ 3 — re-verification

The contract pins: *verify the audited-delegation chain for transitive
closure under depth ≥ 3 — name the case where this breaks, or confirm.*

The original review handled depth = 3 in §3 with the figure
`S₁ → D₁ → D₂ → D₃`. On re-examination at depth = 4 the same failure mode
*compounds*:

```text
S₁ ─Agent─▶ D₁ ─Agent─▶ D₂ ─Agent─▶ D₃ ─Agent─▶ D₄
            ↓           ↓           ↓           ↓
            S₂          S₃          S₄          S₅
```

Without `parent_delegation` (Edit 6), the operator reconstructing the chain
from a commit produced by D₄ must:

1. open `D₄/record.yaml`, read `parent = S₄`
2. consult the *live* session roster to map S₄ → which delegation spawned S₄
3. that delegation is D₃; open `D₃/record.yaml`, read `parent = S₃`
4. step 2 again to map S₃ → D₂
5. step 2 again to map S₂ → D₁
6. step 2 again to map S₁ → originating user/Claude process

Steps 2, 4, 5, 6 *all fail after session purge*. The chain is not just
severed at one link — it is severed at *every* level above the immediate
parent. The longer the chain, the more brittle the reconstruction. Worse:
even *during* the live session, the roster is keyed by session_id, and a
single Claude process can host multiple delegations sequentially; the
roster does not record "session S₂ spawned D₃" as a directed edge — it
records only that S₂ exists and is associated with some identity.

So depth ≥ 3 *strictly* requires Edit 6. At depth = 2 the chain can be
reconstructed in-flight by inspecting the parent's open delegations; at
depth = 3 and beyond, *only* a `parent_delegation` field on the record makes
the chain durable.

The original review identified this in §3.1; the redo elevates it from a
forensic improvement to a *correctness requirement* for the audited-
delegation primitive at depth. The DES claims `git log --grep="Delegation:"`
surfaces every commit; without Edit 6 this claim is satisfied for
*individual* commits but the *chain* between commits is permanently
unrecoverable for any historical session that has been purged.

## 3. Synthesised ad-hoc contract — formal entity check

The contract pins: *does the synthesised contract have the same invariants
as a leader-authored contract.*

On re-examination, the answer is *no*, and the original review under-named
the asymmetry. I unpack it here as the redo's first refinement.

### 3.1 What the original review found

The original §4 confirmed that the synthetic contract satisfies I1–I3, I5, I6
vacuously, and identified that `validate.go` rule 11 (non-empty write_set)
*rejects* the synthesised contract unless an `AllowEmptyWriteSet` archetype
flag is set. Edit 7 names the archetype the synthesiser must use.

### 3.2 What the redo adds: the precondition asymmetry

A leader-authored contract may carry preconditions. The DES requires
`precond = ∅` for synthetic contracts (Edit 7, second clause). But the
*interaction* with inheritance is not captured: if a leader's mission has
`delegations[].inherits_contract: true` and the parent contract carries
preconditions, the spawned delegation *inherits the preconditions*. If the
spawning Agent call would otherwise have synthesised an ad-hoc contract
(because no `MISSION_ID` is in env), but `inherits_contract: true` is set in
the parent's `delegations[]` block, the spawned child inherits a contract
*with* preconditions — even though it has no `MISSION_ID` of its own at the
point the synthesiser runs.

This is a *type confusion*: the synthesised contract should be
preconditionless by invariant, but inheritance can attach preconditions to a
child that the synthesiser created. The two rules collide.

**Refinement R1.** Tighten Edit 7: *synthetic contracts are
preconditionless and the synthesis path runs only when no parent contract
applies via inheritance.* Inheritance must take precedence over synthesis.
The synthesiser fires only when there is no parent mission *and* no
inherited contract. State this ordering explicitly in the DES.

### 3.3 The evaluator asymmetry

A leader-authored contract names an evaluator distinct from the worker
(per the team table in `CLAUDE.md`: "within each row, the worker and
evaluator must be distinct handles"). The synthesised contract sets
`evaluator = claude` (the leader). A spawn made by claude attaches to an
ad-hoc contract whose evaluator *is also claude*. Worker = evaluator. The
distinct-handles invariant — which `validate.go` enforces for explicit
contracts via rule 6 — is *violated by construction* in synthesised
contracts spawned by claude.

Either the synthesiser must use a different default evaluator (a designated
meta-evaluator identity, as the DES hints at "future DES may introduce a
dedicated meta-evaluator identity"), or `validate.go` rule 6 must be
relaxed *only* for synthetic contracts, with the relaxation pinned in
schema by `synthetic = true ⇒ evaluator may equal worker`.

**Refinement R2 (extends Edit 7).** Either introduce the meta-evaluator
identity now (e.g., `meta`) and use it as the synthesised evaluator, or
explicitly relax rule 6 for synthetic contracts with the relaxation written
into `validate.go` as a guarded clause. The DES must pick one; silently
violating the distinct-handles invariant is the worst option and is what the
draft proposes today.

## 4. Stat–Write race under DES-054 — re-examination

The contract pins: *does adding the delegation record reshape the race.*

The original §5 answer was: no, DES-054 does not close the DES-052 race; it
makes the collision *detectable* via `tool_input_hash` but not preventable;
and Edit 8 closes a *new* small window introduced by the PreToolUse-on-Agent
sequence.

On re-examination this remains correct. I add one observation the original
did not state clearly.

### 4.1 The audit hash does not close the race, but it does reshape *attribution*

Pre-DES-054, when two delegations race on `./foo/new.go` and both succeed,
the file's final content is one of the two writes; the audit log has no
record linking either write to a specific delegation. Post-DES-054, both
writes carry `delegation_id` and `tool_input_hash`. The race is unchanged
*at the filesystem layer*, but the *blame* is now fully reconstructible: the
operator can determine which delegation wrote the surviving bytes by
comparing the file's content hash to the two `tool_input_hash` values in the
audit log.

This is not race elimination. It is *causal attribution under race*. The
distinction matters for the verdict: the prior review correctly framed
DES-054 as not closing the race, but did not note that DES-054 closes a
*different* observability gap — *who wrote the file we are looking at* —
which the DES draft does not advertise.

The DES should claim this observability improvement explicitly; today it is
silent on the race entirely. An honest framing in the DES is:

> DES-054 does not close the DES-052 Stat-then-Write race. It does make the
> race's outcome attributable: the audit log contains hashes that uniquely
> identify which delegation produced which surviving bytes. Race prevention
> is a future DES; race attribution is a DES-054 deliverable.

This is *not* a new edit — it is a framing correction. Add it to the DES
under "What DES-054 deliberately does NOT do" or under a new "What DES-054
incidentally improves" section.

### 4.2 Edit 8 remains correct

The PreToolUse-on-Agent sequence's new window (steps 1–6 of §5.3 in the
original) is real and Edit 8's per-mission read-flock acquisition is the
right closure. Re-examined: no change.

## 5. Formal-methods evaluation — invariants explicit, closed, migration byte-compatible

The contract pins jra's per-criterion evaluation. I take them one by one.

### 5.1 Invariants explicit

After the nine edits + R1 + R2: yes. I1–I7 plus the I5b policy clause and the
synthesised-contract guard are each stated as set-theoretic predicates over
the abstract machine's state. The implementation can derive proof
obligations directly from this schema.

Without the edits: no. I7 (status monotonicity) and I5b (precondition
outcome policy) are absent from the DES; the implementation would have to
infer them from prose.

### 5.2 Invariant set closed under composition

Composition closure under spawn requires:

- I1 closes: counter flock is global. With Edit 1, deadlock-free.
- I2 closes (as strengthened by Edit 2): same counter.
- I3 closes: skeleton record written before subagent runs.
- I4 closes: SubagentStart adds session to roster before any tool call.
- I5 closes: MISSION_ID env propagation.
- I5b closes (with Edit 3): the policy is stated.
- I6 *fails* to close under inheritance, per §3.2 of the prior review
  (the precondition-scope-vs-inheritance collision). Edit 6's audit-walk
  fixes this *only if* the implementation walks `parent_delegation` when
  evaluating preconditions. The DES must state this walk explicitly; the
  prior review's recommendation that I6 weakens to
  `scope(p) ⊆ ancestor_delegations_of(p)` is the closure fix.
- I7 closes (with the sentinel from Edit 9): every spawn produces a
  delegation with `closed_at` set, success or failure.

With all nine edits, the set is closed. Without Edit 4 (or its alternative
explicit exclusion), I6 is closed but *narrower* than the DES advertises;
without Edit 6, the cross-depth chain is broken; without Edit 9, I7 is
violated by hash-gate refusal.

### 5.3 Migration byte-compatible with on-disk YAML

The DES claims `omitempty` on all new fields for `Contract` and
`auditEntry`. This is byte-compatible for *reads* of v3.11.0 files by a
DES-054 binary. For *writes*, the new binary produces files with new fields
populated; an *old* binary reading these files will silently drop unknown
fields on round-trip, which is the standard YAML behaviour and acceptable.

Two migration concerns the DES does not address:

**(a) Path migration.** `~/.punt-labs/ethos/missions/<id>.yaml` moves to
`<repo>/.ethos/missions/<id>/contract.yaml`. The DES says "read-fallback
covers existing on-disk state." This is correct for `show` but the DES does
not say what `reflect`, `result`, `close` do when the contract is at the
global path. Do they migrate-on-write, or do they write to the legacy path?
The latter is byte-safe; the former is invisible migration. The DES owes a
sentence on this.

**(b) Counter file shape change.** The DES adds "delegation ID counter
lives in the same global counter file" with a different key. The current
counter file (per validate.go and friends) is a single integer under one
key. Adding a second key changes the YAML shape. An old binary reading the
new file will either accept the new key as unknown (safe) or error on the
schema mismatch. The DES must specify which, and the counter migration
must be either:

- shape-compatible (add a sibling key the old binary tolerates), or
- versioned (the file gains a `schema_version` field and the old binary
  refuses cleanly rather than silently misreading).

**Refinement R3 (new — call it Edit 10).** Specify the counter file's
schema-version field and the migration semantics for contract paths under
`reflect` / `result` / `close`. Without this, the migration is not in fact
byte-compatible across the v3.11 → v3.12 boundary in the way the DES
claims.

This is a *new edit* the original review missed. I name it now.

## 6. Concrete states the invariants admit but must forbid

The original review enumerated six. On re-examination, all six remain. I do
not add or retract any.

| # | State | Forbidden by edit |
|---|---|---|
| 1 | Synthetic contract failing rule 11 (empty write_set) | Edit 7 (archetype with AllowEmptyWriteSet) |
| 2 | Synthetic contract with preconditions via inheritance | Edit 7 + R1 (ordering) |
| 3 | Synthetic contract with worker = evaluator | Edit 7 + R2 (meta-evaluator) |
| 4 | Cross-depth chain broken after session purge | Edit 6 (`parent_delegation`) |
| 5 | Phantom delegation after hash-gate refusal | Edit 9 (sentinel verdict) |
| 6 | Delegation added to closed mission | Edit 8 (per-mission read-flock) |
| 7 | Counter file schema drift across binary versions | R3 / Edit 10 (schema-version) |

Item 7 is new in the redo.

## 7. Summary of edits — updated

The nine original edits remain. Three refinements/additions:

| # | Section | Change | Status |
|---|---|---|---|
| 1 | §2.1 | Acquire/release order for id-flock vs create-flock | UNCHANGED |
| 2 | §2.2 | Strengthen I2 to global delegation-ID uniqueness | UNCHANGED |
| 3 | §2.5 | Add I5b (precondition outcome policy) | UNCHANGED |
| 4 | §2.6 | Predicate-scope: extend or explicitly exclude cross-delegation | UNCHANGED |
| 5 | §2.7 | Add delegation-status monotonicity invariant | UNCHANGED, ELEVATED — load-bearing for closure |
| 6 | §3 | Add `parent_delegation`; audit-walk inherits | UNCHANGED, ELEVATED — required at depth ≥ 3 |
| 7 | §4 | Name archetype; pin `synthetic ⇒ precond = ∅` | TIGHTENED by R1 + R2 |
| 8 | §5.3 | Per-mission read-flock at PreToolUse-on-Agent | UNCHANGED |
| 9 | §6 | Sentinel verdict on spawn failure | UNCHANGED, ELEVATED — load-bearing for I7 |
| R1 | §3.2 redo | Synthesis runs only when no parent contract applies via inheritance | NEW |
| R2 | §3.3 redo | Meta-evaluator identity or guarded relaxation of rule 6 | NEW |
| R3 / 10 | §5.3 redo | Counter file schema-version; contract path migration semantics under reflect/result/close | NEW |

Twelve items total. The original nine are necessary; the three new ones close
gaps the first pass did not surface.

## 8. Closure verdict

The model closes with twelve edits, not nine. The unification under "audited
delegation" remains the right concept. The seven pseudo-Z invariants — eight
with I7 — are satisfiable and, with Edits 1, 4, 6, and 9, closed under
composition. The synthesised-contract path needs the precondition-ordering
clarification (R1) and the evaluator question resolved (R2) before it can
satisfy the distinct-handles invariant. The migration story needs schema-
versioning (R3) to be genuinely byte-compatible across the binary boundary.

The DES is *not* ready to implement as-is. It is ready to implement *with the
twelve edits named*. None of the twelve requires a fundamental redesign;
each is a schema change, a policy pinning, or an ordering clause. The
underlying primitive — audited delegation as the unit of governed work — is
sound.

*The model is still not done until ProB has explored the parent-delegation
chain at depth four under realistic bounds, with one synthesised contract,
one verifier under DES-035 isolation, and one hash-gate refusal in the
spawn tree. The animator finds these states in minutes. The reviewer finds
them in hours, with gaps. The redo found three the first pass missed; a
ProB run will find more.*

— jra
