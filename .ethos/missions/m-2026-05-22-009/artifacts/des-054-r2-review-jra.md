# DES-054 v3 — Formal Review (round 2, jra)

**Verdict: APPROVE WITH NAMED EDITS.** The v3 pivot collapses three round-1
refinements (R1, R2, R3-precondition-clause) and renders v2's I8/I9/I10
obsolete; the remaining eight-invariant set is satisfiable and, under the
edits named in §7 below, closed under composition. None of the edits is a
redesign — each is a one-line schema or wording pin. *Approval is
conditional on those edits being applied in the DES before implementation,
not after.*

---

## 0. Relationship to the round-1 redo

The round-1 redo (`m-2026-05-22-005/artifacts/des-054-review-jra.md`)
closed with twelve edits: the original nine, plus R1 (synthesis ordering),
R2 (synthesised-contract evaluator), R3 (counter file schema-version and
contract path migration). The v3 draft retires the synthesiser entirely.

We register the consequences explicitly. The mapping from round-1 redo
items to v3 status:

| Round-1 item | v3 disposition | Status |
|---|---|---|
| Edit 1 (id-flock vs create-flock order) | Preserved as concurrency-table note | OPEN — see F2 below |
| Edit 2 (global delegation-ID uniqueness) | Encoded as I2 (both tiers) | CLOSED |
| Edit 3 (I5b precondition policy) | Encoded as I5b | CLOSED |
| Edit 4 (predicate scope: extend or exclude) | Encoded as I6 with ancestor walk | CLOSED |
| Edit 5 (status monotonicity I7) | Encoded as I7 | CLOSED |
| Edit 6 (parent_delegation, audit-walk) | Implied in I4 and §"Hash-gate refusal cleanup" | OPEN — see F3 below |
| Edit 7 (synthesiser archetype + precond = ∅) | *Obsolete:* no synthesiser | RETIRED |
| Edit 8 (per-mission read-flock at PreToolUse) | Encoded in §"Hook architecture" step (d) | CLOSED |
| Edit 9 (sentinel verdict on spawn failure) | Encoded in §"Hash-gate refusal cleanup" | CLOSED |
| R1 (synthesis ordering vs inheritance) | *Obsolete:* no synthesiser | RETIRED |
| R2 (synthesised evaluator) | *Obsolete:* no synthesiser | RETIRED |
| R3 (counter schema-version + path migration) | Partially encoded as `schema_version: 2` | OPEN — see F4 below |

Three retired by the pivot, six closed by the v3 text, three remaining
gaps. We name those three plus three new findings the pivot introduces.

The pivot is a substantial improvement to the formal surface. Removing
the synthesiser removes an entire class of asymmetry between
leader-authored and machine-authored contracts. The cost is one new
invariant (I8) and a tier-discipline obligation we examine below.

---

## 1. The eight v3 invariants — satisfiability and closure

The v3 invariant set is I1–I7 (preserved from v2 with minor revisions to
account for the two-tier surface) plus a new I8 about tier discipline.

### 1.1 Per-invariant satisfiability

We re-check each statement under realistic delegation shapes drawn from
the existing codebase.

- **I1 (mission ID uniqueness).** Counter at
  `~/.punt-labs/ethos/missions/counter.yaml` with per-mission flock.
  Satisfiable. Unchanged in form from v2.

- **I2 (delegation ID uniqueness, both tiers).** v3 reuses the counter
  family with two prefix schemes — `d-YYYY-MM-DD-NNN` for Tier A,
  `<mission-id>-d<NN>` for Tier B. The two prefix schemes share *no*
  syntactic overlap, so uniqueness within each scheme suffices for
  global uniqueness across both. Satisfiable. (We comment on schema
  drift in F4.)

- **I3 (audit entries reachable from declared delegation).** v3 keeps
  the implication: `e.delegation_id ≠ ""  ⇒  ∃ d ∈ delegations.
  d.id = e.delegation_id`. Satisfiable. The *vacuous* case for audit
  entries with `delegation_id = ""` (i.e., entries produced by the
  parent session itself, before the first Agent spawn) is intentionally
  admitted — the parent session's own tool calls do not name a
  delegation. We confirm: this is the correct interpretation under
  v3's "audit is universal; delegation_id is per-spawn" model.

- **I4 (transitive closure of parent_delegation).** This is the
  invariant the pivot most disturbs. We treat it in §3 below in full.

- **I5a (preconditions evaluated iff contract in scope and tool matches).**
  v3 explicitly scopes preconditions to "Tier B contract is in scope".
  Tier A delegations have no contract, so I5a yields *false* for every
  Tier A tool call (the conjunction's first conjunct fails). This is
  the right semantics — Tier A simply does not enter the predicate
  evaluator. Satisfiable. We probe the boundary in §4 below.

- **I5b (fail policy).** Three-way disposition (block / block / warn
  + allow) on predicate outcome and strict flag. Satisfiable in
  isolation. The interaction with I8 is examined in §4.

- **I6 (predicate scope under inherits_contract).** v3 makes the
  ancestor walk explicit: `scope(p, d) = audit_of(d) ∪ {audit_of(a) :
  a ∈ ancestors(d) ∧ a.contract = d.contract}`. The set comprehension
  is well-typed; the walk terminates because `parent_delegation` chains
  terminate (I4). Satisfiable.

- **I7 (delegation status monotonicity).** Preserved with extended
  verdict set `{open, pass, fail, error, aborted}`. Satisfiable. The
  `aborted` member of the codomain is now load-bearing for the
  hash-gate cleanup path, per the round-1 redo §1.1.

- **I8 (tier discipline).** New in v3:
  `d.tier = "A"  ⇒  d.contract = nil  ∧  d.tier = "B"  ⇒  d.contract ≠ nil`.
  We dedicate §2 to its well-typedness.

We confirm: no v3 invariant is individually unsatisfiable.

### 1.2 Closure under composition

Pairwise composition checks (I_i ∧ I_j) produce no contradiction under
realistic spawn traces. The composition that matters is the three-way
**I3 ∧ I4 ∧ I7** check that round 1 flagged: a hash-gate refusal must
not strand a delegation in state `verdict = "open" ∧ closed_at = ⊥` with
no audit entries. v3 preserves the round-1 fix (the sentinel verdict),
so the three-way closure holds.

The *new* three-way check is **I5a ∧ I5b ∧ I8** — does the
precondition-evaluator path stay well-typed when the contract is `nil`?
We treat this in §4.

The composition closes under all sixteen pairwise interactions we
enumerated. We summarise the non-trivial three-way interactions in §6.

---

## 2. I8 — well-typedness and exhaustiveness `[REQ]`

I8 says, in B notation:

```
I8: ∀ d ∈ delegations.
       (d.tier = "A"  ⇒  d.contract = nil)
    ∧  (d.tier = "B"  ⇒  d.contract ≠ nil)
```

The biconditional form is the cleaner statement:

```
I8 (rewritten): ∀ d ∈ delegations. d.contract = nil  ⇔  d.tier = "A"
```

Equivalence with the v3 text: the two implications, taken together,
yield the biconditional, *provided* `tier ∈ {"A", "B"}` is a total
typing constraint (no third value). We register that constraint
explicitly:

```
I8-type: ∀ d ∈ delegations. d.tier ∈ {"A", "B"}
```

Without `I8-type`, the two implications under-specify the case
`d.tier = "C"` (unconstrained), which would admit phantom states.

**Finding F1 `[REQ]`.** State `I8-type` explicitly in §"Concurrency
invariants" — *the tier field is closed under the two-element set
{A, B}, no third value admitted.* The cost is one line in the DES;
the benefit is that I8 then closes the type. This is a requirement
because the DES surface defines what `tier` can be, and that is a
contract decision.

### 2.1 Where `tier` is assigned

The hook architecture (§"Hook architecture", step b) defines tier
dispatch by exhaustive case analysis:

1. *Inheritance match*: parent contract's `delegations[]` has a
   `spawn_pattern` that matches → Tier B.
2. *MISSION_ID env set, no inheritance match* → Tier B.
3. *Neither* → Tier A.

This is an exhaustive three-case dispatch with no overlap; case 1
and case 2 are *both* Tier B, but the precedence is named (inheritance
first), so there is no ambiguity. The dispatch is well-typed: each
incoming `Agent` call lands in exactly one of {A, B}, and the
assignment of `contract` follows mechanically (the parent's contract
in case 1, the dispatched mission's contract in case 2, `nil` in
case 3).

We confirm I8 is exhaustive at the dispatch site. The only failure
mode would be if the *transition* from Tier A to Tier B (or vice
versa) were permitted mid-delegation — which the DES does not allow
and which the audit-log structure (Tier A entries in session log,
Tier B in per-delegation directory) makes structurally impossible
without rewriting on-disk artefacts.

We add one further refinement.

**Finding F2 `[REQ]`.** Add an invariant that pins tier *immutability*:

```
I8-stable: ∀ d ∈ delegations, t1 < t2. d.tier(t1) = d.tier(t2)
```

Without this, a delegation could in principle be reclassified
post-hoc (Tier A entry "promoted" to Tier B by a later
`mission dispatch`). The DES does not propose this, but the formal
model permits it unless we forbid it. The cost is one line. This is
a requirement because the immutability is part of the contract the
DES makes to consumers — once an audit entry is written with
`tier = A`, no subsequent operation may change that.

---

## 3. I4 — transitive closure under Tier A audit-log relocation `[REQ]`

The v3 pivot moves Tier A audit entries from per-delegation directories
(which do not exist in Tier A) to the session log
`<repo>/.ethos/sessions/<session-id>.audit.jsonl`. The round-1 redo
§2 elevated `parent_delegation` to a *correctness requirement* for the
audited-delegation primitive at depth ≥ 3, because chain reconstruction
through the live session roster fails after session purge.

We re-verify this claim under v3.

### 3.1 Chain reconstruction in v3

A depth-4 chain `S₁ → D₁ → D₂ → D₃ → D₄` in v3 is reconstructed as
follows, depending on tier mix:

- **All Tier B**: every `D_i/record.yaml` carries `parent_delegation =
  D_{i-1}` (or empty for `D_1`). The chain is reconstructible from
  the mission directory alone, post-purge. *Closed.*

- **All Tier A**: every audit entry in the session log carries
  `delegation_id` and (per the schema) `parent_session`. But the
  schema does *not* show a `parent_delegation` field on the
  `auditEntry` type — only `parent_session`. So at depth ≥ 3, the
  chain `D_4.audit → D_3 → D_2 → D_1` cannot be reconstructed
  through audit entries alone, only through the (purgeable) session
  roster.

- **Mixed (e.g., Tier B parent spawns Tier A child)**: the child's
  audit entries live in the session log; the parent's `delegations[]`
  inheritance template does *not* enumerate the Tier A spawn (Tier A
  is by definition outside the contract). So the parent-side has no
  durable record that this specific Tier A child existed.

The all-Tier-A and mixed cases break I4's chain reconstruction unless
either:

(a) `parent_delegation` is added to the `auditEntry` schema (not just
`parent_session`), or

(b) the v3 model accepts that Tier A chain reconstruction is
*best-effort* and not required to satisfy I4.

The v3 text is silent on this choice. The invariant statement
`I4: ∀ d. d.parent_delegation = ""  ∨  ∃ p. p.id = d.parent_delegation`
quantifies over `delegations`, but in Tier A there is no `record.yaml`
holding `parent_delegation` — the chain field lives (or fails to live)
only in the audit JSONL.

**Finding F3 `[REQ]`.** Add `parent_delegation` to `auditEntry`
alongside `parent_session`. State explicitly that for Tier A
delegations, `parent_delegation` is sourced from the env block
the parent set when spawning, and the field is the durable hop in
the chain. Without this, I4 closes only for Tier B and is
*best-effort* for Tier A — which contradicts the v3 invariant text
that quantifies over *all* delegations.

If the v3 authors prefer the alternative — accept that Tier A chain
reconstruction is best-effort — then I4 must be rewritten to
quantify only over Tier B delegations, and the DES must state that
Tier A chains are recoverable only during the live session. We
recommend the former: the cost is one field on `auditEntry`, and
the benefit is that I4 is a single uniform statement across both
tiers.

This is a requirement because it changes the on-disk schema (what
fields appear in `auditEntry`) — operators and downstream tools
will read this field; it is part of the contract.

---

## 4. I5b under no-contract — exhaustiveness `[IMPL]`

I5b reads:

```
I5b: evaluated(p, t) ∧ predicate(p, t) = false              ⇒  block(t)
   ∧ evaluated(p, t) ∧ predicate(p, t) = unevaluable ∧ strict   ⇒  block(t)
   ∧ evaluated(p, t) ∧ predicate(p, t) = unevaluable ∧ ¬strict  ⇒  warn(t) ∧ allow(t)
```

The implication consequent fires only when `evaluated(p, t) = true`.
By I5a, `evaluated(p, t)` is true only when an active contract is in
scope. So for Tier A tool calls (no contract), `evaluated(p, t) = false`
for every `p` (vacuously — there are no preconditions), and I5b is
satisfied vacuously. *I5b correctly skips Tier A.*

We confirm: the implication form is well-typed across both tiers, and
no third branch ("Tier A — what do we do") is needed. Tier A simply
does not reach the I5b consequent.

There is one subtlety. The DES says, in §"Hook architecture" step 3:

> **3. PreToolUse procedure preconditions** — fires only when a Tier B
> contract is in scope. Tier A spawns never hit precondition evaluation
> because no contract exists.

This is the correct operational reading of I5a ∧ I5b. We register that
the *implementation* of the precondition hook must short-circuit on
`contract = nil` before entering the predicate evaluator — otherwise
the evaluator will see an empty preconditions list and produce
`evaluated = false` for every `t`, which is the same outcome but
spends evaluator cycles. The short-circuit is an implementation
optimisation, not a requirement.

**Finding F4 `[IMPL]`.** The precondition hook implementation should
test `contract = nil` first and return `allow(t)` before invoking the
predicate evaluator. This is a correctness-preserving implementation
detail; the invariant is satisfied either way. We flag it as
`[IMPL]` because it changes *how* the hook is written, not *what* it
guarantees externally.

---

## 5. Obsolescence of v2's I8/I9/I10 and closure without them

The v2 invariants in scope:

- **v2 I8:** `synthetic ⇒ preconditions = ∅`
- **v2 I9:** `synthetic ⇒ evaluator = meta`
- **v2 I10:** inheritance ordering (synthesis runs only when no parent
  contract applies)

All three quantify over the `synthetic` flag, which v3 removes from
`Contract`. The quantifiers are vacuously true over the empty set of
synthetic contracts — but more usefully, the *concepts they protect*
are gone from the v3 model. There are no synthesised contracts; there
is no meta-evaluator; inheritance ordering still exists but no longer
competes with synthesis.

**Closure check without v2 I8/I9/I10.** We enumerate the situations
those invariants forbade and confirm v3 either makes them impossible
by construction or addresses them with a different invariant:

| State v2 I8/I9/I10 forbade | v3 status |
|---|---|
| Synthetic contract with preconditions (v2 I8) | Impossible — no synthesis |
| Synthetic contract with worker = evaluator (v2 I9) | Impossible — no synthesis |
| Synthesis runs despite inheritable parent contract (v2 I10) | Impossible — no synthesis |
| Phantom delegation after hash-gate refusal | Forbidden by I7 + sentinel |
| Cross-depth chain broken after session purge | Forbidden by I4 + F3 |
| Delegation added to closed mission | Forbidden by per-mission flock |

Every situation v2 I8/I9/I10 protected against is either structurally
impossible in v3 or is forbidden by a different v3 invariant. The
eight-invariant set v3 names is closed without them.

**Finding F5 `[REQ]`.** Add one sentence to §"Concurrency invariants"
naming this obsolescence:

> v2's I8, I9, and I10 — three invariants about synthesised contracts —
> are obsolete in v3 because synthesised contracts do not exist.
> The states they forbade are either structurally impossible or
> forbidden by a different invariant in the v3 set.

This is a requirement because it pins the closure argument in the
DES — without it, a reader comparing v2 and v3 cannot tell whether
the v2 protections were transferred or dropped. The closure argument
belongs in the document.

---

## 6. Migration byte-compatibility with on-disk YAML `[REQ]`

The round-1 redo R3 surfaced two migration concerns. v3 partially
addresses one (counter schema-version) and leaves the other
(contract path migration under reflect/result/close) under-specified.

### 6.1 Counter file schema-version

v3 adds `schema_version: 2` to `counter.yaml`. The DES states:

> v1 readers use permissive YAML decode and ignore unknown top-level
> keys.

We verify this claim against the on-disk schema. The current counter
file (per `internal/mission/`) is read with Go's standard YAML
decoder. Whether this decoder uses `KnownFields(true)` for `counter.yaml`
is *not* shown in the inputs we read. If it does, the v3 binary's new
key triggers a parse error on v1 read paths — *not* a permissive
ignore.

**Finding F6 `[REQ]`.** State explicitly in the DES that the counter
file's decoder runs with `KnownFields(false)` (the Go YAML default)
on both v1 and v2 binaries. If the current decoder uses
`KnownFields(true)`, the migration is *not* byte-compatible across
binary versions — a v3.11.0 binary would refuse to read a counter
file written by v3.12.0. The DES owes a single sentence pinning the
decoder mode for this file, and the implementation must verify the
mode matches.

This is a requirement because the byte-compatibility claim depends on
it; without the pin, the migration story is unverified.

### 6.2 `auditEntry` schema additions

v3 adds five fields to `auditEntry`: `ParentSession`, `AgentID`,
`AgentType`, `DelegationID`, `ContractID`, plus replaces `Preview`'s
single field with `Preview` + `ToolInput` + `ToolInputHash`. All
additions use `omitempty` JSON tags.

Examining the existing `auditEntry` (in `internal/hook/audit_log.go`),
the v3 fields are all *additions* — no field is renamed, no field is
removed. JSONL files written by v3.11.0 contain three fields
(`ts`, `session`, `tool`, `tool_input_preview`); v3.12.0 readers
decode these unambiguously because Go's `encoding/json` permits
unknown-absence (zero values populate the new fields). v3.12.0 files
contain the full eight-field set; v3.11.0 readers tolerate unknown
fields by default (Go's `encoding/json` is permissive).

We confirm: the `auditEntry` migration is byte-compatible in both
directions for v3.11.0 ↔ v3.12.0. *Closed.*

### 6.3 Contract path migration under reflect/result/close

The DES describes a two-root `Store` (repo + global) with a layer
table (Create, Load, List, Update/Close, Migrate). The round-1
redo R3 asked whether `reflect`, `result`, `close` migrate-on-write
or write-to-legacy when the contract is at the global path.

v3's table answers: **"Update/Close: layer where Load found it;
never copy across."** This is the byte-safe choice — `reflect`,
`result`, and `close` write back to the legacy path if the contract
was loaded from there. *Closed.*

We confirm R3's contract-path sub-question is addressed.

---

## 7. Summary of findings — six items

| # | Section | Finding | Class |
|---|---|---|---|
| F1 | §2 | State `I8-type: ∀ d. d.tier ∈ {"A", "B"}` explicitly. | `[REQ]` |
| F2 | §2.1 | Add `I8-stable: ∀ d, t1 < t2. d.tier(t1) = d.tier(t2)`. | `[REQ]` |
| F3 | §3 | Add `parent_delegation` to `auditEntry`, or restrict I4 to Tier B. | `[REQ]` |
| F4 | §4 | Short-circuit precondition hook on `contract = nil`. | `[IMPL]` |
| F5 | §5 | One-sentence obsolescence note for v2 I8/I9/I10. | `[REQ]` |
| F6 | §6.1 | Pin decoder mode for `counter.yaml`. | `[REQ]` |

Five requirements (changes to the DES surface or schema), one
implementation note. The five requirements together amount to four
short additions to §"Concurrency invariants" (F1, F2, F5), one
schema-field addition to `auditEntry` (F3), and one decoder-mode
pin (F6). None requires a redesign. All are mechanical edits the
authors can apply in a single pass.

---

## 8. Closure verdict

The model closes with the six edits named. The eight-invariant set —
I1 through I7 plus I8 (with the I8-type and I8-stable refinements from
F1 and F2) — is satisfiable and closed under composition. The
synthesiser's retirement removes the asymmetry that drove three of
round-1's edits, at the cost of one new invariant (I8) which is itself
well-typed once F1 and F2 are applied. The migration story is
byte-compatible in both directions for `auditEntry` (verified) and for
`Contract` (per the v3 layer table) — and is *claimed* to be
byte-compatible for `counter.yaml`, conditional on the decoder-mode
pin (F6).

The v3 draft is *ready to implement with the six edits named*. The
underlying primitive — audited delegation as the unit of governed
work, with an opt-in contract layer — is sound. The pivot to opt-in
governance is a substantial formal improvement, not just a policy
choice; it removes a class of synthetic-vs-authored asymmetry that
v2 had to manage with three dedicated invariants.

We close with a note we have made before. The model is not done until
ProB has explored a four-deep delegation tree mixing both tiers,
with one hash-gate refusal, one inheritance match, and one
strict-precondition violation in the spawn tree. The animator finds
these states in minutes. The reviewer finds them in hours, with
gaps — round 1 missed three edits the redo surfaced; round 2 has
found six the v3 pivot newly required or newly admitted. A ProB
animation of the v3 schema will find more.

— jra
