# DES-054 v4 review — convergence test (rop, round 3)

**Verdict: APPROVE.** All five round-2 findings landed in v4. One
trivial editorial inconsistency noted [IMPL]; not a design defect.
The design has converged.

## Spot-check of round-2 findings

### [REQ] R1 — Advice hook suppression in non-interactive contexts

**Landed.** v4 §"Hook architecture / 1. PreToolUse-on-Agent /
Tier dispatch / Neither" (lines 156–163) pins both suppression
triggers:

> Emit advisory to stderr UNLESS suppression conditions apply
> (per rop R1 iii):
>
> - `ETHOS_QUIET_ADVICE=1` is set in the environment, OR
> - `PARENT_SESSION_ID` is already populated (nested ad-hoc spawn —
>   the parent already saw the advisory; suppress the recursive
>   repeat).

The CI case (env-suppressible) and the nested-spawn case (auto-
suppressed) are both addressed exactly as requested. The message
form is preserved one-line, descriptive, with the silencing hint
inline. Pinned.

### [IMPL] R2 — Tier B contract liveness invariant

**Landed.** v4 lines 351–352 add:

```text
I8-live: forall d in delegations:
        d.tier = "B"  ->  d.contract != nil /\ d.contract.closed_at = ""
```

The implication (Tier B requires a live contract at spawn) is now
explicit. The "refuse spawn if mission is closed" prose in §1.d
(line 174–175) has a matching invariant. Good.

### [IMPL] R3 — Concurrency table flock reconciliation

**Landed.** v4 chose option (a) — one flock, one row, prose
consistent. Table line 268:

> | Per-session audit-log flock | 5 | `~/.punt-labs/ethos/sessions/<id>.lock` (same flock covers roster + audit writes per rop R3) | flock-held |

Followed by prose at lines 272–273:

> The session flock at `~/.punt-labs/ethos/sessions/<id>.lock`
> covers both the roster YAML and the audit JSONL writes — one
> flock per session, two write disciplines (per rop R3 / rsc E5).

Table and prose now reconcile. Reader does not have to chase
forward references.

### [IMPL] R4 — auditEntry annotation cleanup

**Landed.** v4 lines 78–92 — the `auditEntry` struct in §Schema
changes is clean of `// NEW` comments. The fields speak for
themselves; the surrounding paragraph (line 94) explains which
fields populate in which tier. The bwk worker has no inline
commentary to transcribe verbatim.

### [IMPL] R5 — max_delegation_depth resolved

**Landed in design body.** v4 lines 280–281 (in §Concurrency model):

> `max_delegation_depth` (per rop R5): a global ethos setting in
> `.punt-labs/ethos.yaml`, default `16`. … exceeding it refuses
> the spawn with a clear error.

Phase 2 work list (line 408) includes `max_delegation_depth`
config read. Documented as a runtime guardrail, not a contract
field — correct, since Tier A has no contract layer.

---

## New findings

### [IMPL] N1 — Open question §2 is stale

v4 §Open questions, item 2 (lines 397–398) still asks:

> **`max_delegation_depth`**: ad-hoc nested spawns (Tier A under
> Tier A) have no budget cap. Should we impose
> `max_delegation_depth: 16` as a global ethos setting …?

This question is already answered in the design body (lines 280–281)
and in the phase 2 plan (line 408). Leaving the question open
contradicts the resolution. Strike item 2 from open questions, or
restate as "resolved — see §Concurrency model."

Trivial edit. No re-review required. Not blocking.

---

## Convergence assessment

Round 1: 21 findings, 18 applied in v2, all 21 applied in v2+.
Round 2: 18 findings (5 REQ — well, 1 REQ + 4 IMPL from this
reviewer; aggregate across three reviewers: 14 REQ + 13 IMPL),
all 27 applied in v4.
Round 3: zero new substantive findings from this reviewer.
One editorial cleanup (N1, IMPL, trivial).

The design has converged on the minimalism axis. The two-tier
split is forced by the data. The advice hook is justified and
bounded. The invariants are tight. The migration path is
specified across the boundary cases the previous round flagged.
The concurrency model is reconciled. No surface is unjustified;
nothing is missing that the operator depends on.

Approve. Cleanup of open-question §2 can land with the next
revision or in implementation; it does not affect any code.

— rop
