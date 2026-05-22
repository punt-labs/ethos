# DES-054 v3 review — minimalism (rop, round 2)

**Verdict: APPROVE with one REQUIREMENT and four IMPLEMENTATION
findings.** The CEO pivot collapses the surface in the right direction:
governance is opt-in, the synthesizer is gone, audit enrichment carries
the load. The two-tier split is the minimal expression of the opt-in
direction. The advice hook earns its keep — barely — under one
specific constraint. The Tier A / Tier B audit serialization model
is correct; the two write paths are necessary, not gratuitous. The
eight invariants are well-formed.

The classification convention: **[REQ]** means the finding changes
WHAT the system does (surface contract, behaviour visible to the
operator, schema on disk). **[IMPL]** means the finding changes HOW
the system is built (mechanism, structure, file layout, code shape)
without changing the operator-visible contract.

## Pressure test 1 — is the Tier A / Tier B split the simplest expression of opt-in?

Yes. The pivot from v2+ to v3 deletes ~300 LOC of synthesizer,
meta-evaluator, rule-6 relaxation, three invariants, and a `synthetic`
flag. What remains is a single branch in PreToolUse-on-Agent:
**inheritance match or MISSION_ID env → Tier B; else Tier A**. There
is no third tier and there cannot be one — every Agent call either
has a contract reachable through inheritance/env or it does not.
The dichotomy is forced by the data, not invented by the design.

The contract layer is additive. Tier A is Tier B minus the contract.
The audit schema is universal; `contract_id` is `omitempty`. One can
collapse all Tier A reasoning to "Tier B with contract_id unset" and
the design still reads. That is the right shape.

I prefer it to v2+.

## Pressure test 2 — does the advice hook earn its keep?

Marginally yes, **conditional on it staying out of the way**. The hook
exists for one reason: to teach the operator about `mission dispatch`
without forcing them through it. A docs-only nudge (`AGENTS.md`,
`README`, command help) would not appear at the moment of the
ungoverned spawn — it would appear in advance and be forgotten.
A stderr line at spawn time appears in the operator's log next to the
spawn, which is the readable moment.

The hook is justified IF:

- it is non-blocking. The draft says so.
- it does not appear in non-interactive contexts that pipe stderr
  to a log file (CI, agent-internal spawns). The draft does not
  address suppression. See finding R1 below.
- the message is one line, descriptive not prescriptive. The draft's
  open-question §4 lands on "descriptive — operators choose," which is
  correct.

If the hook ever grows past one line of stderr, or if it ever fires in
a context where the operator is not present to read it, it has stopped
earning its keep.

## Pressure test 3 — Tier A and Tier B audit serialization, two write paths

Necessary. The two paths reflect a real distinction in the data:

- Tier A audit entries belong to **a session**. There is no
  containing mission directory; the session is the longest-lived
  enclosing scope.
- Tier B audit entries belong to **a delegation under a mission**.
  Co-locating them with the contract is what makes
  `mission show <id>` work without joins.

A single write path would force one of two compromises: either Tier A
entries leak into a synthesized mission directory (the synthesizer the
CEO just deleted), or Tier B entries fragment across session logs and
must be reassembled by `delegation_id`. Neither is simpler than two
appenders sharing a flock discipline.

The `delegation_id` field on every entry keeps the two views
queryable together, which is what the operator actually wants for
`ethos audit show <delegation>`.

The flock layout is acceptable: per-session flock under
`~/.punt-labs/ethos/sessions/<id>.lock` for Tier A; per-delegation
flock under `~/.punt-labs/ethos/delegations/<id>.lock` for Tier B.
Two locks, two scopes, no shared mutable state. See finding R3 on
the documentation of this split.

## Pressure test 4 — eight invariants vs v2+'s ten

Correct consolidation. v2's I8/I9/I10 governed the `synthetic` flag —
that a synthesized contract was distinguishable from a hand-authored
one, that audit log carried it, that meta-evaluator presence aligned
with it. With the synthesizer gone, all three become vacuous. v3's
new I8 (`d.tier = "A" -> d.contract = nil` and the symmetric clause)
is the single load-bearing assertion replacing them.

I1–I7 are unchanged in meaning. The audit-reachability invariant I3
correctly admits Tier A entries via the `delegation_id` field rather
than via a per-delegation directory; the predicate-scope invariant I5
correctly restricts evaluation to "when a Tier B contract is in
scope." Both are tighter, not weaker.

One quibble — see finding R2 — on I8's exhaustiveness.

---

## Findings

### [REQ] R1 — Advice hook suppression in non-interactive contexts

The draft says the advice hook emits a stderr line plus optional JSON
via `hooks.json`. It does not say what happens when no human is
present to read the stderr line. Two cases to address explicitly:

a. **CI agent runs.** A `gh-actions` workflow that spawns `Agent`
calls will write the advice to the workflow log. Acceptable noise
but should be `EHOSTS_QUIET=1`-suppressible by an env var the
operator sets in the workflow.

b. **Nested Tier A spawns.** A Tier A leader spawns a Tier A child.
The child's PreToolUse-on-Agent fires another advice line into the
parent session's stderr. Inside-the-tree spawns should not advise;
only the outermost ungoverned spawn should. The simplest
implementation: suppress the advice when `PARENT_SESSION_ID` is set.

This is a REQ because it changes operator-visible behaviour (whether
the advice fires) under conditions the draft does not currently
specify. The operator-facing contract is "I will see one suggestion
when I make an ungoverned spawn at the top level," not "I will see
N suggestions for an N-deep ad-hoc tree."

Edit: under "Hook architecture / 1. PreToolUse-on-Agent / Tier A
branch", add a suppression rule:

> Advisory is emitted to stderr unless `ETHOS_QUIET_ADVICE=1` is set
> or `PARENT_SESSION_ID` is already populated (nested ad-hoc spawn).
> Only the outermost ungoverned spawn in a tree advises.

### [IMPL] R2 — I8 invariant should cover the env-set-but-no-contract case

I8 reads:

```text
d.tier = "A"  ->  d.contract = nil
d.tier = "B"  ->  d.contract != nil
```

The hook architecture says Tier B can be entered two ways: (a) by
inheritance match against parent's `delegations[]`, (b) by MISSION_ID
env being set. Case (b) leaves a window: leader passes MISSION_ID
through but the corresponding mission has been closed between
dispatch and spawn. The advice-hook logic in §c refers to "refuse
spawn if mission is closed" under the Tier B flock acquire — good —
but the invariant should make the implication visible at the
contract layer.

Suggest adding:

```text
-- A Tier B delegation has a live contract at spawn time.
I8b: forall d in delegations:
        d.tier = "B"  ->  d.contract != nil /\ d.contract.closed_at = ""
```

Or fold the liveness clause into I8 directly. This is IMPL because
the behaviour ("refuse spawn if mission is closed") is already
specified; the finding is that the invariant set should reflect it.

### [IMPL] R3 — Concurrency table should mark the Tier A session-audit flock

The concurrency table (§Concurrency model) lists:

| Session roster flock | 5 | `~/.punt-labs/ethos/sessions/` | flock-held |
| Session audit JSONL | 5 | `<repo>/.ethos/sessions/` (in repo) | persistent |

The audit JSONL row is missing its flock. The prose under "Tier A
audit serialization" correctly states writers serialize through
`~/.punt-labs/ethos/sessions/<session-id>.lock` — but that is the
same path as the roster flock row above. Two distinct write
disciplines (roster mutations, audit appends) share one lock file.

Either:

a. Use one flock for both, and say so in the table (one row covers
   roster mutation and audit append). Or
b. Add a separate `<session-id>.audit.lock` to keep the audit
   appender independent of roster state.

(a) is simpler and matches the draft's prose. State it in the table
so the reader does not have to reconcile two passages.

This is IMPL: the operator never sees the flock file. It is a code
structure question only.

### [IMPL] R4 — Delete redundant "v3 only" annotations where context already conveys tier

The schema-changes section labels every new audit field with
`// NEW`. Some fields are also annotated `// NEW — set for both
tiers` or `// NEW — set only for Tier B`. The reader can infer
from `omitempty` and the section's prose which fields populate in
which tier. The annotations are documentation, not structure.

Two of them carry useful information (`delegation_id` set for both,
`contract_id` set only for Tier B). The rest are noise. Trim to
the load-bearing two and let the field names speak.

This is the kind of cleanup that can happen during implementation;
flagging it now so the bwk worker does not transcribe the comments
into Go source verbatim.

### [IMPL] R5 — Open question §2 (max_delegation_depth) should be resolved before Phase 2

The draft leaves `max_delegation_depth: 16` as an open question.
This matters because Tier A under Tier A under Tier A has no contract
budget and no implicit bound — a runaway spawn loop would write to
the session audit JSONL until the disk fills. The session flock
prevents corruption but not exhaustion.

Recommend resolving in the design rather than deferring:

- Set `max_delegation_depth: 16` as a global ethos setting (read from
  `.punt-labs/ethos.yaml`, default 16).
- PreToolUse-on-Agent counts `PARENT_SESSION_ID` chain length via the
  `delegation_id` linkage and refuses spawns past the depth, with
  exit-status semantics documented.
- Document the setting in `AGENTS.md`.

Worth resolving now because the implementation phase split (§Recommended
next step) puts the advice hook in Phase 2 and a depth cap is a small
addition to the same hook. Catching it in Phase 2 is cheaper than
revisiting in Phase 4.

This is IMPL because the existence of a depth cap is an operational
guardrail, not a behaviour the operator depends on at the surface.
The chosen number (16, 32, …) is similarly an IMPL choice.

---

## Editorial — non-findings

- The draft's revision history (v1 → v2 → v2+ → v3) is the right
  shape and the right length. Keep it.
- The "What DES-054 deliberately does NOT do" list is doing real
  work; it documents the negative space the CEO direction created.
  Keep it.
- "Rejected alternatives" now contains the two items prior reviews
  asked for (`session_id` reuse, single repo-root `delegations.jsonl`).
  Closed.

## Summary

One REQ (R1: advice suppression in non-interactive / nested
contexts). Four IMPL (R2: I8 liveness; R3: session flock table row;
R4: comment cleanup; R5: depth cap). The REQ requires CEO consent
because it specifies operator-visible behaviour the draft does not
currently bound. The four IMPL findings can be applied by the
implementer without re-review.

The v3 surface is minimal in the sense that matters. The two-tier
split is forced by the data. The advice hook is the single piece
of new mechanism that the operator sees, and it is one line of
stderr. The synthesizer is gone and nothing replaced it. The
invariant count went down. The LOC budget went down. That is the
right direction.

Approve, on the condition R1 lands before merge.

— rop
