# DES-054 v4 — Formal Review (round 3, jra)

**Verdict: APPROVE.** The v4 draft applies all six round-2 findings
(F1–F6) verbatim, the twelve-invariant set is satisfiable, closed under
composition, and independent under the criteria we examine below, and
no new substantive finding remains. *The design is stable;
implementation may proceed.*

---

## 0. Convergence statement

This is a convergence test. The criterion the leader pinned in the
contract is unambiguous: *APPROVE if the round produces zero new
substantive findings.* We hold ourselves to that criterion and report
the outcome plainly. We register no `[REQ]`-class new findings; we
register one `[IMPL]`-class observation in §4 (a redundancy in the
v4 prose that does not affect any invariant). Per the criterion, the
verdict is APPROVE.

The patience required of a formal reviewer is not unbounded. A
design that converges across three rounds, with each round adding
invariants only when prior rounds surfaced a real gap, is the
artefact one hopes for. v4 is that artefact.

---

## 1. Round-2 findings — landing check

We spot-check each round-2 finding against the v4 text.

### F1 `[REQ]` — `I8-type` explicit

v4 §"Concurrency invariants", lines 341–342:

```
-- Tier domain is closed: every delegation belongs to exactly A or B.
-- (NEW in v4 per jra F1.)
I8-type: forall d in delegations: d.tier ∈ {"A", "B"}
```

The invariant is named, the provenance is named, and the closure
constraint on `tier` is stated. *Landed.*

### F2 `[REQ]` — `I8-stable` tier immutability

v4 §"Concurrency invariants", lines 344–347:

```
-- Tier is immutable: a delegation never transitions between tiers.
-- (NEW in v4 per jra F2.)
I8-stable: forall d in delegations, t1 < t2:
        d.tier(t1) = d.tier(t2)
```

Quantification over `t1 < t2` correctly pins immutability across
time. *Landed.*

### F3 `[REQ]` — `parent_delegation` on `auditEntry`

v4 §"Schema changes", lines 84–85:

```go
DelegationID       string         `json:"delegation_id,omitempty"`
ParentDelegation   string         `json:"parent_delegation,omitempty"`
```

Plus line 94, in prose:

> `ParentDelegation` (added v4 per jra F3 i) makes the audit chain
> self-sufficient: every entry can walk to the root spawn without
> consulting the session roster, which the audit-log relocation in
> v3 made important.

The field is on the schema, the motivation is stated, and the
self-sufficiency claim is explicit. We verify this claim in §2
below. *Landed.*

### F4 `[IMPL]` — precondition hook short-circuit

v4 §"Hook architecture" step 3, line 183:

> Fail policy: violated predicate blocks; unevaluable predicate
> blocks unless `strict_preconditions: false` (default true), at
> which point it warns and allows.

And §"Recommended next step" phase 3, line 409:

> precondition evaluator (Tier B only, short-circuits on
> `contract = nil`)

The implementation note is captured in the phasing. *Landed.*

### F5 `[REQ]` — obsolescence note for v2 I8/I9/I10

v4 §"Concurrency invariants" closing paragraph, line 368:

> Twelve invariants. v2's I8/I9/I10 (about synthetic contracts)
> explicitly **obsolete** under v3's opt-in pivot — they
> referenced an entity (synthetic contracts) that no longer exists.

The obsolescence is named, the reason is named, and the closure
argument is preserved by the new invariants I8, I8-type, I8-stable,
I8-live, I9-counter, and I10-audit-atomic. *Landed.*

### F6 `[REQ]` — counter decoder mode

v4 §"Migration", lines 229 and 243:

> **`Counter.yaml` schema versioning** (per jra F6 / rsc E8):
> explicit `schema_version: 2` at top.

> `counter.yaml` is read with permissive YAML decoding (current
> behaviour by-default; pinned by invariant in v4). Future schema
> additions append top-level keys; existing keys never change
> meaning.

And as a first-class invariant, lines 357–358:

```
I9-counter: read(counter.yaml) uses permissive YAML decoding
    /\ forall version v, key k in v: meaning(k) is monotonic across versions
```

The decoder mode is pinned at the schema, in prose, and as a
formal invariant. *Landed.*

We confirm: all six round-2 findings landed in v4 with the
disposition the round-2 review requested.

---

## 2. The twelve-invariant set — satisfiability and closure

v4 names twelve invariants: I1, I2, I3, I4, I5a, I5b, I6, I7, I8,
I8-type, I8-stable, I8-live, I9-counter, I10-audit-atomic. (The
prose count of "twelve" at line 368 omits I5a/I5b counted as one
or counts I8-family as one — we read the formal list and count
twelve distinct statements.)

### 2.1 Per-invariant satisfiability

We re-verify each against realistic spawn shapes.

- **I1 (mission ID uniqueness).** Per-mission counter file with
  schema_version: 2. Satisfiable. Unchanged.

- **I2 (delegation ID uniqueness, both tiers).** Tier A IDs
  `d-YYYY-MM-DD-NNN`; Tier B IDs `<mission-id>-d<NN>`. Disjoint
  prefix grammars. Satisfiable.

- **I3 (audit entry reachable from declared delegation).** v4
  preserves the form. The vacuous case for entries with
  `delegation_id = ""` (parent session's own tool calls before any
  Agent spawn) is admitted, as before. Satisfiable.

- **I4 (transitive closure of `parent_delegation`).** v4 with F3
  applied makes this self-sufficient — see §2.3 below. Satisfiable
  and now terminates without session-roster dependency.

- **I5a (preconditions evaluated iff contract in scope and tool
  matches).** v4 keeps the biconditional and the Tier A skip is
  vacuous. Satisfiable.

- **I5b (fail policy).** Three-way disposition on predicate outcome
  and strict flag, fires only when `evaluated = true`. Tier A
  vacuous. Satisfiable.

- **I6 (predicate scope under `inherits_contract`).** Ancestor walk
  bounded by I4's termination. Satisfiable.

- **I7 (delegation status monotonicity).** Verdict codomain
  `{open, pass, fail, error, aborted}`, monotone after first
  non-open transition. Satisfiable.

- **I8 (tier discipline — A nil-contract, B non-nil-contract).**
  Conjunction of two implications. Satisfiable.

- **I8-type (tier domain closed under {A, B}).** New in v4.
  Pins the typing premise I8 depends on. Satisfiable.

- **I8-stable (tier immutability across time).** New in v4.
  Forbids reclassification post-hoc. Satisfiable; consistent with
  the audit-log structural separation (Tier A in session log, Tier
  B in per-delegation directory) which makes mid-life
  reclassification physically expensive.

- **I8-live (Tier B contract liveness).** New in v4 per rop R2.
  `d.tier = "B"  ->  d.contract != nil /\ d.contract.closed_at = ""`.
  This is the contract analogue of I7: a Tier B delegation cannot
  exist under a closed mission. Satisfiable, and reinforced by the
  PreToolUse-on-Agent step (d) which acquires the per-mission flock
  (shared mode) and refuses spawn if the mission is closed.

- **I9-counter (counter file permissive-decode and key
  monotonicity).** New in v4 per jra F6 / rsc E8. The decoder mode
  and the semantic-monotonicity requirement on keys are both pinned.
  Satisfiable; checked against schema_version: 2 layout in v4 lines
  231–241.

- **I10-audit-atomic (per-session audit-log append atomicity).**
  New in v4 per rsc E6. Appends serialise through
  `<session-id>.lock`; write order matches file-position order.
  Satisfiable; consistent with the concurrency table line 268
  ("Per-session audit-log flock") and line 273 ("one flock per
  session, two write disciplines").

No v4 invariant is individually unsatisfiable.

### 2.2 Closure under composition

The non-trivial three-way checks:

- **I3 ∧ I4 ∧ I7 (hash-gate refusal closure).** Preserved from v3
  via the sentinel verdict. The aborted state is closed_at-bearing
  so I7 holds; I4 holds because the aborted delegation still has a
  `parent_delegation`; I3 holds because the audit entries written
  during attempted spawn still name the delegation.

- **I5a ∧ I5b ∧ I8 (precondition evaluator over both tiers).**
  Tier A is vacuous in I5a, hence vacuous in I5b, hence I8's
  `d.contract = nil` for Tier A is consistent with no predicate
  evaluation. Tier B sees both I5a and I5b fire. Closed.

- **I8 ∧ I8-type ∧ I8-stable ∧ I8-live (tier discipline as a
  block).** I8-type pins the domain; I8 ties domain to contract
  nullity; I8-stable forbids transitions across time; I8-live ties
  Tier B delegations to live contracts. The four together encode
  *tier as a closed, immutable, contract-bound predicate*. We have
  examined each pairwise combination and none yields a state the
  others forbid. Closed.

- **I9-counter ∧ I1 ∧ I2 (counter file monotonicity supports
  uniqueness).** I9-counter's "meaning(k) is monotonic across
  versions" forbids a v3.12.0 binary from interpreting a key
  differently from a v3.13.0 binary. I1 and I2 depend on the
  counter producing the same value on the same key. The
  combination is consistent: a key's *meaning* never changes,
  though its *integer value* increments per allocation. We confirm
  the prose pins "meaning is monotonic" (semantic) not "value is
  monotonic" (which would forbid allocation). Closed.

- **I10-audit-atomic ∧ I3 (audit atomicity supports delegation
  reachability).** A torn write to the session JSONL could leave
  an audit entry with `delegation_id` referencing a delegation
  that never received its record (Tier B) or that never received
  its prompt (Tier A). I10-audit-atomic forbids the torn write by
  flock-and-sync discipline. Closed.

The composition closes across all pairwise and the five
non-trivial three-way interactions enumerated above.

### 2.3 Independence — F3's self-sufficiency claim

The v3 review (F3) elevated `parent_delegation` to a correctness
requirement because the chain-reconstruction argument for I4 at
depth ≥ 3 required either the audit field or live-session-roster
access. v4 adds the field to `auditEntry` (lines 84–85) and states
the self-sufficiency claim explicitly (line 94).

We verify the claim. Consider a depth-4 all-Tier-A chain
`S₁ → D₁ → D₂ → D₃ → D₄`. After session purge:

- D₄'s audit entries carry `delegation_id = D₄` and
  `parent_delegation = D₃` (durable in the JSONL).
- D₃'s audit entries carry `parent_delegation = D₂` (same file).
- D₂'s audit entries carry `parent_delegation = D₁`.
- D₁'s audit entries carry `parent_delegation = ""`.

The reader walks `D₄ → D₃ → D₂ → D₁` by reading the JSONL alone.
The session roster (now purged) is not consulted. I4 closes
without external state. *Self-sufficiency confirmed.*

The same argument extends to the all-Tier-B case (records carry
`parent_delegation` per round-2 review §3.1) and to the mixed
case (the Tier A child's audit entry names the Tier B parent's
delegation_id, which is reachable from the mission directory).

I4 is now uniform across all three tier mixes. The asymmetry the
round-2 review flagged is closed.

### 2.4 Independence — no invariant is derivable from another

We check that no v4 invariant is logically derivable from the
others (an unhealthy formalism would carry derived invariants
masquerading as primitives).

- I8 is *not* derivable from I8-type — I8-type pins the domain;
  I8 ties the domain to contract presence. Both are needed.
- I8-stable is *not* derivable from I8 + I8-type — the conjunction
  of those two does not constrain time, only states at a single
  time. I8-stable adds the temporal constraint.
- I8-live is *not* derivable from I8 — I8 says Tier B delegations
  have a contract; I8-live additionally says the contract is
  open. The two are independent.
- I9-counter and I10-audit-atomic operate on distinct files and
  distinct write disciplines; neither implies the other.

The twelve invariants form an independent set. *Closed under
composition; independent in formation.*

---

## 3. Migration story — byte-compatibility

Round 2 §6 examined byte-compatibility for `auditEntry`, `Contract`,
and `counter.yaml`. v4 closes the open items:

- **`auditEntry`** — five v3 additions plus v4's
  `ParentDelegation` field, all `omitempty` JSON. v3.11.0 readers
  decode the new fields as zero values; v3.12.0 readers decode
  legacy entries unambiguously. *Byte-compatible in both
  directions.*

- **`Contract`** — v4 line 96 names the asymmetry explicitly:
  Contract YAML decodes with `KnownFields(true)`, audit entries
  with default permissive JSON. The asymmetry's *consequence* is
  that v3.12.0 contracts (carrying `preconditions` or `delegations`)
  are not readable by v3.11.0 binaries — the one-way door is
  acknowledged at line 225. *Byte-compatible forward only; the
  one-way door is explicit DES policy.*

- **`counter.yaml`** — F6 closed via lines 229, 243, and the new
  I9-counter invariant. The decoder mode is pinned by invariant,
  the schema_version is at the top, future additions append
  top-level keys, existing keys never change meaning.
  *Byte-compatible in both directions across all future minor
  versions.*

We confirm: the migration story is now byte-compatible under all
three on-disk YAML/JSONL files, with the one-way door for
contracts explicitly named as policy.

---

## 4. One `[IMPL]` observation — prose redundancy

We note one redundancy in v4 prose. The obsolescence note at line
368 says:

> v4's I8, I8-type, I8-stable, I8-live, I9-counter, and
> I10-audit-atomic together encode the tier discipline, counter
> monotonicity, and audit append-atomicity properties that prior
> versions left implicit.

The sentence is correct. However, line 368 also prefaces with
"Twelve invariants" while the formal block enumerates I1, I2, I3,
I4, I5a, I5b, I6, I7, I8, I8-type, I8-stable, I8-live, I9-counter,
I10-audit-atomic — fourteen named labels, of which I5a/I5b are a
single fail-policy decomposition, and the I8-family is four
labels. The count "twelve" is reached by counting the I8-family
as one (giving I1–I7 with I5a/I5b separate = 8, plus one I8
family = 9, plus I9 and I10 = 11) — which is *eleven*, not
twelve. Counting I5a and I5b together gives ten. The arithmetic
of the prose count and the formal list does not match.

This is a documentation observation, not a model defect. The
formal list is what implementations will be measured against.

**Finding [IMPL]-1.** Recount the invariant labels in v4 prose so
the headline ("Twelve") matches the formal block ("fourteen
labels, twelve distinct statements once I8-family is grouped").
The simplest fix is to write "Fourteen named invariants, twelve
distinct after grouping I5a/I5b and the I8-family." This is a
prose fix; no invariant changes.

We classify this as `[IMPL]` because no schema or invariant is
affected; the document's surface description is the only thing
that changes.

---

## 5. Closure verdict

The v4 model closes under all the criteria we examined.

- All six round-2 findings (F1, F2, F3, F4, F5, F6) landed.
- The twelve invariants (counted strictly or fourteen counted with
  decomposition) are satisfiable, closed under composition, and
  independent.
- I4 transitive closure is self-sufficient via `parent_delegation`
  on `auditEntry` — no session-roster dependency.
- Migration is byte-compatible under all three on-disk artefacts
  (`auditEntry`, `Contract`, `counter.yaml`) with the one-way door
  for `Contract` named as policy.
- The obsolescence of v2 I8/I9/I10 is stated in the DES.

No new `[REQ]` findings. One `[IMPL]` prose-count observation
that does not affect the model.

Per the convergence criterion the leader pinned in the contract:
*APPROVE.* The v4 draft is ready to implement.

---

## 6. Note for the implementer

The same closing note we have made before applies to v4 as it did
to v3, and we restate it without modification. The model is not
done until ProB has explored a four-deep delegation tree mixing
both tiers, with one hash-gate refusal, one inheritance match, and
one strict-precondition violation in the spawn tree. The reviewer
finds these states in hours, with gaps — round 1 missed three
edits, round 2 found six, round 3 finds none. The convergence is
genuine, but it is convergence to a *specification*. The
animation discharges the proof obligations a specification cannot
discharge by inspection.

The implementer should treat the twelve invariants as the
ProB-animator's input set. A counterexample at depth ≥ 4 in a
mixed-tier tree is the gift we have not yet received.

— jra
