# DES-054 Formal Review — jra

**Verdict: ITERATE.** The unifying concept is correct and the seven pseudo-Z
statements are individually well-formed, but four of them admit states the DES
authorises elsewhere; two further failure modes (the ad-hoc synthesis case and
the Stat–Write window) are unguarded by any invariant. The model is sound after
six named edits enumerated in §7; without them the implementation will encode
a contradiction at the boundary between admission control (DES-052), verifier
isolation (DES-035), and the new delegation primitive.

## 1. Schema and signatures

We adopt the following carrier sets and treat the DES-054 entities as the
state of a single abstract machine `AuditedDelegation`.

```text
[SESSION_ID, AGENT_ID, MISSION_ID, DELEGATION_ID, TOOL, PATH, HASH, TS]

Verdict ::= pass | fail | error | open
Source  ::= write_set | extract_into
Status  ::= open | closed | failed | escalated
```

```text
Contract ==
  [ id          : MISSION_ID
  ; status      : Status
  ; write_set   : seq PATH
  ; extract_into: seq PATH
  ; precond     : seq Precondition
  ; synthetic   : bool                   -- new in DES-054
  ; rounds      : ℕ
  ]

Delegation ==
  [ id          : DELEGATION_ID
  ; parent      : SESSION_ID
  ; parent_del  : DELEGATION_ID ∪ {⊥}    -- new; the spawning delegation
  ; agent_type  : AGENT_ID ∪ {⊥}
  ; contract    : MISSION_ID
  ; started_at  : TS
  ; closed_at   : TS ∪ {⊥}
  ; verdict     : Verdict
  ]

AuditEntry ==
  [ ts          : TS
  ; session     : SESSION_ID
  ; parent_sess : SESSION_ID ∪ {⊥}
  ; delegation  : DELEGATION_ID ∪ {⊥}
  ; contract    : MISSION_ID ∪ {⊥}
  ; tool        : TOOL
  ; tool_input  : map(STRING, JSON)
  ; input_hash  : HASH
  ]

State ==
  [ missions    : ℙ Contract
  ; delegations : ℙ Delegation
  ; audit       : seq AuditEntry         -- per-delegation log; total order per delegation
  ; sessions    : ℙ SESSION_ID
  ]
```

The DES draft does not name `parent_del` — every delegation record carries
`parent` (the spawning *session*) but no `parent_delegation`. This omission is
the root of the transitive-closure failure in §3; we add it back here as an
auxiliary field the formal model needs even if the on-disk YAML continues to
encode it implicitly via the session roster.

## 2. The seven pseudo-Z invariants — checked one by one

We restate each invariant in the abstract machine's variables, then ask the
four questions the contract pins: *satisfiability*, *closure under composition*,
*independence*, and *completeness*.

### 2.1 I1 — global mission-ID uniqueness

```text
∀ m1, m2 : missions • m1.id = m2.id ⇒ m1 = m2
```

*Satisfiable.* The global counter under one flock guarantees injective
allocation. *Closed.* `Create(m)` extends `missions` with a fresh id from the
counter; uniqueness is preserved by construction. *Independent.* Stands alone.

*Complete?* Not quite. The counter is described as `~/.punt-labs/ethos/missions/counter.yaml`
held under one global flock. The invariant is *only* preserved across repos if
every Create-on-disk path passes through the global flock. The DES says
"directory-create flock becomes per-repo" — this is the right move for admission
overlap (which is repo-scoped) but the id-allocation flock must remain global
and the DES does not explicitly say the two flocks compose without deadlock.

**Edit 1.** State explicitly: id-allocation flock is acquired and released
*before* the per-repo directory-create flock is taken. The two are never held
simultaneously. Ordering preserves I1 across machines and prevents the
diamond deadlock (repo A holds id-flock, waits on repo B's create-flock; repo
B holds B's create-flock, waits on the id-flock).

### 2.2 I2 — delegation-ID uniqueness within parent mission

```text
∀ d1, d2 : delegations •
    (d1.contract = d2.contract ∧ d1.id = d2.id) ⇒ d1 = d2
```

*Satisfiable, closed.* Same flock argument. *Independent.*

*Complete?* The DES says delegation IDs are globally unique by construction
(`<mission-id>-d<NN>` or `d-YYYY-MM-DD-NNN`). The pseudo-Z statement is
*weaker* than the DES claim — it only requires uniqueness *within* a parent
mission. The two forms diverge on the ad-hoc case: an ad-hoc delegation has no
parent mission, so the within-mission quantifier is vacuously true and the
real uniqueness requirement collapses onto the global counter.

**Edit 2.** Strengthen I2 to match the DES claim:

```text
∀ d1, d2 : delegations • d1.id = d2.id ⇒ d1 = d2
```

Drop the contract-equality antecedent. The DES has already done the work of
making delegation IDs globally unique; the invariant should say so.

### 2.3 I3 — audit-to-delegation referential integrity

```text
∀ e : audit • e.delegation ≠ ⊥ ⇒ ∃ d : delegations • d.id = e.delegation
```

*Satisfiable, closed, independent.* The hook writes the delegation record's
skeleton *before* the spawned subagent can produce audit entries (PreToolUse
on `Agent` precedes any PostToolUse on the child).

*Complete?* Yes, but the invariant is one-directional. The DES does not
forbid a *delegation with no audit entries* (a delegation that returns
without making any tool call), which is legitimate. So the converse

```text
∀ d : delegations • ∃ e : audit • e.delegation = d.id     (REJECTED)
```

is correctly not asserted. The asymmetry is the right one.

### 2.4 I4 — delegation parent reachability

```text
∀ d : delegations • ∃ s : sessions • s.id = d.parent
```

*Satisfiable.* Every Agent-tool call originates in some session.

*Closed under spawn?* This is where the trouble begins. Consider:

```text
session S₁ ─ spawn ─▶ delegation D₁ ─ Agent ─▶ delegation D₂
```

D₂'s `parent` field — per the DES schema — is "session_id of the spawning
context". The spawning context for D₂ is the *subagent* that D₁ produced, not
session S₁. Claude Code assigns the subagent a distinct session_id, call it
S₂. So D₂.parent = S₂, and S₂ must be in `sessions` at the moment D₂ is
recorded.

Claude Code adds the subagent to the session roster via SubagentStart. The
hook runs before any of the subagent's PreToolUse calls. So S₂ ∈ sessions
before D₂ is written. *Closed.*

*Independent, complete?* Yes — but I4 as written is too weak for the
transitive-closure question in §3.

### 2.5 I5 — precondition evaluation scope

```text
∀ t : tool_calls, p : active_contract.precond •
    evaluated(p, t) ⇔ (t.tool = p.tool ∧ matches(t.path, p.path_glob))
```

*Satisfiable, independent.*

*Closed?* The bi-implication is well-formed only when `active_contract` is a
function of `t` (the call site has exactly one contract in scope). The DES
guarantees this in the `MISSION_ID` env var. However, the synthesised ad-hoc
contract case has `precond = ∅`, so the right side is vacuous and the left
side correctly evaluates nothing — consistent.

*Complete?* The bi-implication says nothing about the *outcome* of
`evaluated(p,t)`. The rejected alternative ("always block when a precondition
cannot be evaluated") states the policy ("fail open on infrastructure errors,
fail closed on predicate evaluation that returns false") but the policy is not
captured in any invariant. A reviewer who reads only the seven statements
cannot tell what happens when `audit_contains_path` cannot find the audit log
file. *This is a completeness gap.*

**Edit 3.** Add I5b:

```text
∀ t : tool_calls, p : precond • evaluated(p, t) ∧ ¬satisfied(p, t)
  ⇒ blocked(t, reason = p.message)
∀ t : tool_calls, p : precond • evaluated(p, t) ∧ unreachable(audit_of(t))
  ⇒ ¬blocked(t)                                  -- fail-open on infra error
```

The policy is now formally pinned, and the `strict_preconditions` archetype
flag's job is to flip the second clause to `⇒ blocked(t)`.

### 2.6 I6 — predicate-scope confinement

```text
∀ p : preconditions • scope(p) ⊆ delegation_of(p)
```

*Satisfiable.* By construction the predicate lookup uses `DELEGATION_ID` from
the env to find the calling delegation's audit log.

*Closed under spawn?* Consider a contract whose precondition reads
`audit_contains_tool(Read)`. The worker mission's delegation D_w runs, makes
Read calls; the verifier mission's delegation D_v runs *separately* under
DES-035 verifier isolation, with a different `DELEGATION_ID`. If the contract
has the precondition attached to a verifier Write of the verdict YAML, whose
audit log is consulted?

The DES is ambiguous here. `audit_contains_path(file_path, ${tool_input.file_path})`
in the verifier's contract: the only sensible reading is *the verifier's own
audit log*, because I6 says scope ⊆ delegation_of(p). But the operational
intent the DES describes ("a verdict YAML may only be recorded after the
judge has Read the PNG it is judging") is the *judge's* (verifier's) Read,
which is in the verifier's own log. Good — this case happens to coincide.

However a different example breaks I6 cleanly. Suppose the worker's contract
says "a Write to `results/final.yaml` requires that the *verifier's*
delegation recorded a `Bash` invoking `go test`". The intent is sensible:
the worker may only commit after the verifier proved tests passed. The
precondition language as currently scoped (one delegation's audit log) cannot
express this. I6 is *correct* about what the implementation does, but it
*forbids a class of preconditions* the DES does not warn the reader about.

**Edit 4.** Either (a) extend the predicate language to take a delegation
selector (`audit_contains_tool(Bash, delegation = verifier_of(${MISSION_ID}))`)
and weaken I6 to permit it, or (b) state explicitly in the DES that
cross-delegation preconditions are out of scope and call out the canonical
"worker waits on verifier" pattern as a *deliberate* exclusion to be
addressed in a future DES.

We prefer (b) for the same minimality reason rop will likely cite — the
predicate language earns its keep when it stays closed. The exclusion must
nonetheless be *named*.

### 2.7 I7 — implicit invariant: delegation-status monotonicity

Not in the DES. We add it because the implementation needs it.

```text
∀ d : delegations •
    d.verdict ∈ {pass, fail, error} ⇒ d.closed_at ≠ ⊥
∀ d : delegations •
    d.verdict = open ⇔ d.closed_at = ⊥
```

This is the analogue of validate.go rule 5 (status ↔ closed_at) lifted to
delegations. Without it, a delegation can be open with a closed_at, or
closed without a closed_at, and `git log --grep="Delegation: ..."` queries
return half-truths.

**Edit 5.** Add the delegation-status invariant explicitly.

## 3. Transitive closure: D → D' → D''

Consider:

```text
S₁ ─Agent─▶ D₁ ─Agent─▶ D₂ ─Agent─▶ D₃
            ↓           ↓           ↓
            S₂          S₃          S₄
```

The DES claims `git log --grep="Delegation: m-..."` surfaces every commit.
But a commit-msg trailer carries *one* delegation ID — the closest enclosing
one (the value of `DELEGATION_ID` in the env at the time `git commit` ran).

Two failure modes follow:

**3.1 The chain is reconstructible only via the audit log, not via the
commit message.** Given a commit with `Delegation: m-2026-X-d07`, an operator
who wants to know "what was the parent of d07?" must read
`<repo>/.ethos/missions/m-2026-X/delegations/d07/record.yaml` and chase the
`parent` field — which per the DES schema is a *session ID*, not a parent
delegation ID. To climb the chain S₂ → D₁, the operator must consult the
session roster (global, ephemeral, *not committed*).

After the session ends, the roster is purged. The chain S → D backlink is
*permanently severed.*

**Edit 6.** Add `parent_delegation` to the delegation record. The chain is
then audit-log-only and survives session purge. The session field still
captures *which Claude process* spawned it for live-debugging; the
parent_delegation field captures *which logical delegation it inherited
context from* for forensic reconstruction.

**3.2 Precondition scope at depth.** Suppose D₂'s contract has a
precondition `audit_contains_path(file_path, X)`. The audit log consulted is
"the calling delegation's" — i.e., D₂'s own. But D₂ inherited its contract
from D₁ (the `inherits_contract: true` default). The Read that satisfies the
precondition was made by D₁, *not* D₂. The precondition fails. The worker
sees a block message they cannot diagnose because the Read *did* happen — just
in the parent.

This is the precondition-scope rule colliding with the contract-inheritance
rule. The DES sets both defaults; together they make `audit_contains_tool(Read)`
unsatisfiable for any nested delegation inheriting its contract.

The fix is either to make the audit-log lookup walk the parent-delegation
chain (changing I6 to `scope(p) ⊆ ancestor_delegations_of(p)`), or to
disallow contract inheritance for preconditioned contracts. The first is more
useful and is the natural reading of "the calling delegation's audit log" —
once contracts inherit, the audit context should inherit symmetrically.

We recommend the first. With Edit 6 (`parent_delegation`) the walk is a
simple while-loop over `record.yaml` files.

## 4. The synthesised ad-hoc contract

The synthesised contract carries:

```text
type = task
write_set = [ ]
extract_into = [ ]
success_criteria = [ "delegation completed" ]
budget = { rounds = 1, reflection_after_each = false }
evaluator = claude (today)
precond = [ ]            -- implicit
synthetic = true         -- not in DES; we add it
```

We ask whether this entity satisfies the same invariant set as a
leader-authored contract.

**I1–I2.** Yes; ID allocation is uniform.

**I3.** Yes; audit entries reference the delegation, which references this
contract.

**I5.** Vacuously — `precond = ∅`. Safe.

**I6.** Vacuously — same. Safe.

**Validate() rules (validate.go).** Here the synthetic contract *fails*.
Rule 11 says "write_set must contain at least one entry"; the archetype
flag `AllowEmptyWriteSet` would relax this — so the ad-hoc archetype must
set that flag. Rule 13 says "success_criteria has at least one entry"; the
proposed "delegation completed" satisfies it. Rule 12 (rounds in [1,10]):
satisfied at 1. Rule 14 (current_round in [1,rounds]): satisfied at 1.

So the synthetic contract is well-formed *only* if the synthesis path goes
through `ValidateWithArchetype` with an archetype carrying
`AllowEmptyWriteSet = true`. The DES does not say which archetype is used.

**Edit 7 (renumbered to be the sixth edit since Edit 1 was numbered).**
Name the archetype the synthesiser uses. Either reuse an existing read-only
archetype (`report`, `inbox`) or introduce `ad-hoc` with explicit defaults.
The synthesiser must call `ValidateWithArchetype(c, archetypeAdHoc)`, not
plain `Validate(c)`, or the synthesis will fail rule 11 at the first
spawn.

**Can a synthesised contract's preconditions reference audit entries that
don't exist?** With `precond = ∅` the question is vacuous *today*. Tomorrow,
when a future DES allows operators to set per-repo default preconditions on
the ad-hoc archetype, the answer becomes important. The safe answer is:
*synthetic contracts cannot carry preconditions in the initial release.*
Pin this in the schema:

```text
∀ c : missions • c.synthetic ⇒ c.precond = ∅
```

This forbids the foot-gun before it loads.

## 5. The Stat–Write race, re-examined under DES-054

DES-052 admission control already serialises *contract creation* under a
per-repo directory-create flock, so two missions cannot both claim
overlapping write_set / extract_into at admission time. The race the DES-052
review identified is at *enforcement* time, inside PreToolUse:

```text
T₁ (verifier A): Stat ./foo/new.go  → ENOENT
T₂ (verifier B): Stat ./foo/new.go  → ENOENT
T₁: Write ./foo/new.go              → succeeds (creates file)
T₂: Write ./foo/new.go              → succeeds (overwrites)
```

This race exists only when *two delegations* under *two missions* both have
`./foo/` in their `extract_into`. Under DES-052's six-rule form, two
extract_into directories may overlap (ei-dir × ei-dir = never conflict), so
admission permits the configuration.

**Does DES-054 reshape the race?** Yes, in two ways, neither of which closes
it.

**5.1 Per-delegation flock.** DES-054 adds a per-delegation flock at
`~/.punt-labs/ethos/delegations/<delegation_id>.lock`. The flock is held *for
the lifetime of one delegation*. It guarantees that the verifier's audit log
and record.yaml are not concurrently mutated by two writers. It does *not*
serialise PreToolUse tool calls between two different delegations — that
would defeat parallelism.

So two delegations under two missions, both extract_into-ing `./foo/`, hold
two different flocks. The Stat–Write race is unchanged. *The new flock does
not close the existing window.*

**5.2 Audit-log enrichment.** PostToolUse now writes a per-delegation audit
entry. Suppose T₁'s Write succeeds and T₂'s Write succeeds. Both produce
audit entries naming the same `file_path`. From the audit log alone, an
operator can detect the collision *after the fact* — `tool_input_hash` is
content-addressable, so the two entries are distinguishable by content even
when the path matches. This is forensic improvement, not race elimination.

**5.3 New window?** The DES-054 PreToolUse hook for `Agent(...)` performs a
sequence:

1. resolve mission
2. allocate delegation_id (counter flock)
3. write skeleton record.yaml
4. write prompt.md
5. set env block
6. allow tool

Step 3 is a Write under `<repo>/.ethos/missions/<mission-id>/delegations/<delegation-id>/`.
Two PreToolUse hooks invoked concurrently for the same parent mission produce
two different delegation_ids (counter flock) and two different subdirectories
(no collision). No new race here.

However, the *parent mission* may not yet have its directory created at the
first Agent call. Step 3 is `os.MkdirAll(<repo>/.ethos/missions/<mid>/delegations/<did>/)`.
Two concurrent Agent calls under the same fresh mission both attempt MkdirAll
on `<mid>/`. `MkdirAll` is idempotent — no race. But the per-mission flock
that the rest of the mission store uses is *not* held during the PreToolUse
hook (the hook does not know it should take it). If a third process is
simultaneously closing the parent mission, the close-time invariant ("no
delegations may be added after close") is not enforced.

**Edit 8.** PreToolUse-on-Agent must acquire the per-mission flock (read
mode) before writing the delegation record, and must refuse the Agent call
if the parent mission's status is not `open`. This serialises against close
and prevents post-close delegation skeletons from appearing on disk.

The wider race (two Writes to the same extract_into path) remains a DES-052
issue, not a DES-054 one. DES-054 makes the collision *detectable* but does
not close it. The honest framing in the DES should say so.

## 6. Interaction with DES-035 verifier isolation

DES-035 sets `ETHOS_VERIFIER_ALLOWLIST` and `ETHOS_VERIFIER_EXTRACT_INTO`
when a verifier spawns. DES-054 sets `MISSION_ID` and `DELEGATION_ID`. A
verifier *is* a delegation, so the four env vars stack on the same process.
We check the invariants compose.

**Contract identity.** The verifier's `MISSION_ID` is the mission it
*verifies*. The verifier's `DELEGATION_ID` is fresh — a new delegation under
that mission. The PreToolUse hook today consults
`ETHOS_VERIFIER_ALLOWLIST` first (write-set enforcement). DES-054 adds a
precondition pass *after* the allowlist check. Order:

1. allowlist check (DES-035)
2. extract_into stat-then-allow (DES-052)
3. precondition predicate evaluation (DES-054)

The composition is correct *if and only if* step 3 reads the verifier's own
audit log (path resolved via `DELEGATION_ID`). The current pretooluse.go
reads `ETHOS_VERIFIER_ALLOWLIST` from env — DES-054 must add a parallel read
of `DELEGATION_ID` and resolve the audit path the same way. The DES says it
does; the schema is consistent.

**Hash gate.** SubagentStart's verifier hash gate (subagent_start.go)
refuses the spawn if the evaluator's content has drifted. DES-054 does not
change this; the delegation record is written by PreToolUse on the *parent*
side, *before* the subagent spawns. If the hash gate refuses, the subagent
never starts, but the delegation skeleton has already been written.

**Edit 9.** PreToolUse-on-Agent must roll back the delegation skeleton if
the Agent call ultimately fails (subagent refused by hash gate, or
short-circuited by a stricter precondition). Otherwise the audit trail
contains a `started_at` with no matching `closed_at` and no audit entries —
a phantom delegation. The cleanup is either a sentinel verdict
(`verdict = aborted`) written by PostToolUse-on-Agent, or a sweep at next
SubagentStop. The sentinel is simpler and we recommend it.

## 7. Summary of required edits before approval

| # | Section | Change |
|---|---|---|
| 1 | §2.1 | State id-allocation flock acquire/release order vs per-repo create-flock; never held simultaneously. |
| 2 | §2.2 | Strengthen I2 to global delegation-ID uniqueness; drop the contract-equality antecedent. |
| 3 | §2.5 | Add I5b: precondition outcome semantics — fail-closed on `¬satisfied`, fail-open on `unreachable(audit)`. |
| 4 | §2.6 | Either extend the predicate language with a delegation selector, or explicitly exclude cross-delegation predicates in the DES with the canonical "worker waits on verifier" use case named. |
| 5 | §2.7 | Add the delegation-status monotonicity invariant. |
| 6 | §3 | Add `parent_delegation` to the delegation record so the chain survives session purge. Audit-log lookup walks the parent chain. |
| 7 | §4 | Name the archetype used by the synthesiser; pin `synthetic ⇒ precond = ∅` in schema. |
| 8 | §5.3 | PreToolUse-on-Agent acquires per-mission flock (read) and refuses if parent mission is not open. |
| 9 | §6 | Sentinel-verdict (`aborted`) for delegations whose spawn fails downstream of skeleton write. |

Edits 1, 2, 5, 7 are mechanical schema changes. Edits 3, 4 are policy
clarifications the DES owes the reader. Edits 6, 8, 9 are state-management
fixes that the seven invariants as written do not catch but the
implementation will need.

## 8. Closure verdict

The DES-054 model is *almost* sound. The unification under "audited
delegation" is the right concept and the storage layout is correct. The
seven pseudo-Z statements as written are individually satisfiable, mutually
consistent on their stated quantifiers, and closed under single-step
composition. They are *incomplete*: I have named six concrete states the
DES authorises and the invariants do not forbid (synthetic-contract
validation gap, missing parent_delegation chain, precondition outcome
underspecified, scope rule incompatible with inheritance default,
delegation-status monotonicity missing, Stat–Write race unchanged).

With the nine edits above the model closes. Without them, the
implementation will encode contradictions at the precondition / inheritance
boundary and produce phantom delegations on hash-gate refusal. Neither is
catastrophic; both are well-typed bugs that DES discipline catches cheaper
than ProB will.

*The model is not done until ProB has explored the parent-delegation chain
under realistic bounds (say, three levels of nesting, two concurrent ad-hoc
contracts, one verifier under DES-035 isolation). The asymmetry between
parent_session and parent_delegation is the kind of state the animator
finds in minutes that the reviewer misses in hours.*

— jra
