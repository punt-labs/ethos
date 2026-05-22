# DES-054 review — minimalism (rop, redo)

**Verdict: ITERATE.** I endorse the prior review's findings and add two
sharpenings. The draft is unchanged from round one; the four blocking
edits stand. With those applied I would APPROVE.

## Provenance

This is mission m-2026-05-22-004, a redo of m-2026-05-22-001. The
prior review at
`.ethos/missions/m-2026-05-22-001/artifacts/des-054-review-rop.md` was
written but never submitted via the close ceremony — the leader closed
the mission on the worker's behalf. The CEO flagged this as improper
and asked for the review to be redone with proper ceremony.

I have re-read the prior review and the draft. The prior analysis is
correct. I endorse it in full, supplement it with two findings below,
and submit the close ceremony myself this time.

## Endorsement

The prior review's four blocking edits stand without modification:

1. Close the predicate language. Replace the three predicate forms
   with the two-predicate floor (`must-read-inputs` implicit;
   `require_read: [...]` explicit). Remove `${tool_input.file_path}`
   substitution. State explicitly that the predicate set is two, not
   "four supported forms."
2. Defend ad-hoc contract synthesis with a worked example. Cite
   review-cycle fix rounds and this review mission.
3. Reconcile the delegation-lock directory between the concurrency
   table and the storage-layout diagram.
4. Add two rejected alternatives: "reuse session_id as delegation_id"
   and "single repo-root delegations.jsonl."

Edit 5 (note the storage-move seam) is editorial and remains optional.

## Supplement A — the four/three predicate accounting gap is in print

The prior review made the point in passing. It is sharper than that
and worth pulling out.

The draft says two contradictory things about the predicate count:

- Line 222 (the "deliberately does NOT do" section):
  *"It does not introduce a general predicate language. **The four
  supported `Require` predicates** are the closed initial set."*
- Lines 119–123 (the design section): three forms shown,
  `audit_contains_tool`, `audit_contains_path(<arg>, <pattern>)`,
  `audit_contains_path(file_path, ${tool_input.file_path})`.
- Lines 302–304 (the rop-review section): *"four closed predicates
  cover the demonstrated need."*

Three places. Two counts. The fourth predicate is named nowhere in
the draft. Either (a) the draft is wrong about the count and means
three, or (b) one predicate has been silently dropped between
sections.

Either way the draft contradicts itself in print. Edit 1 fixes this
by collapsing to two; if the author insists on four, they owe the
reader the missing predicate.

## Supplement B — one more cheaper alternative the DES did not consider

The prior review confirmed three of the four primitives earn their
keep and proposed the predicate language be cut to two predicates.
For the per-delegation flock I want to name one alternative the DES
did not enumerate:

**Lock-free append-only delegation records, with the per-mission
flock retained only for the mission's contract and result writes.**

Reasoning. A delegation record's lifecycle is:

1. Skeleton written at PreToolUse (one author: the parent's hook).
2. Audit JSONL appended during the delegation (one author: the
   delegate, via PostToolUse hooks running in the delegate's process).
3. Result written at SubagentStop (one author: the delegate's stop
   hook).

There is only ever one writer per file. The "concurrent delegations
under one parent mission" case the DES uses to justify per-delegation
locks does not produce contention on a single delegation's files —
each delegation is a different directory.

What does need a lock is the delegation-ID counter (line 208 already
serializes this under the shared global counter file). What does not
need a lock is the per-delegation record once allocated.

So the cheaper design is: keep the global counter flock, drop the
per-delegation flock, rely on single-writer-per-file invariants. One
fewer concurrency primitive, same correctness.

I am not recommending this change for the DES. The per-delegation
flock is cheap (one file descriptor per open delegation), it makes
the locking model uniform with the per-mission case, and uniformity
of model is itself a minimalism. But the DES owes the reader an
acknowledgment that the flock is for *uniformity*, not for
*correctness against any contended writer*. The current Verdict line
under primitive 4 ("necessary for correct concurrency") is too
strong — it is necessary for *symmetry with the per-mission
discipline*, which is a different argument.

**Recommendation (advisory, not blocking).** Replace the primitive-4
verdict with: *"The per-delegation flock costs one fd per open
delegation. It is not strictly necessary for correctness — each
delegation has a single writer per file — but it is necessary for
uniformity with the per-mission flock and for forward-compatibility
with future writers (e.g., a result-amender hook). Keep."* That is
the accurate defense.

## Pressure test — is audited delegation one missing concept or five separate problems? (Confirmed.)

The prior review's verdict — one concept, with symptom 5 (in-tree
mission state) a separable storage decision — is correct. I will not
rehearse the argument.

One case where the unification could be wrong, that I want to record
as inspected and rejected:

*A future operator wants prompts kept private to the spawning context
(security review of subagents on regulated work) but the delegation
record itself surfaced for audit (compliance with mission spend
tracking).* Under the DES's design, `prompt.md` is in the same
directory as `record.yaml`. Splitting them across visibility scopes
would mean splitting the delegation record itself across two layers.

This is the case where "delegation is one thing" would have been
wrong. I inspected it. The DES's answer is that the delegation
directory is the visibility unit — operators who need prompt secrecy
either redact the prompt before commit, or move sensitive
delegations out of repo entirely (the global-fallback path the DES
already supports). The unification survives because the visibility
boundary is at the directory, not at any sub-field.

Confirmed: one concept.

## Pressure test — predicate language closure (one fixed vs four vs expression language)

The prior review argued the closure is not closed — the substitution
syntax `${tool_input.file_path}` is the seed of an expression
language the draft pretends it isn't writing.

I want to add the "one fixed predicate" case and the "general
expression language" case as the two endpoints, so the chosen point
is defended against both directions:

- **One fixed predicate** ("audit_contains_read_for_all_input_paths,
  derived from prompt-token scan"). Insufficient for the
  output-shape case where the contract must name what to read. The
  floor must be at least two.
- **Two predicates** (the prior review's proposal:
  `must-read-inputs` implicit + `require_read:` explicit). Closed.
  No parser. No substitution. Covers both demonstrated cases.
- **Four predicates with substitution syntax** (the draft). Not
  closed — `${tool_input.file_path}` is the kernel of a templating
  language, and the count is internally inconsistent (Supplement A).
- **General expression language** (the rejected alternative the DES
  correctly killed for sandbox reasons). Out of scope.

The chosen point is two predicates. The argument against one is
demonstrated by the output-shape case. The argument against four is
demonstrated by the substitution-syntax leak. The argument against a
general language is the sandbox cost the DES already names. Two is
the smallest closed point that covers the worked example.

## Pressure test — rejected alternatives the DES missed

The prior review proposed two additions:

1. *Reuse `session_id` as `delegation_id`.* Confirmed missing from the
   draft's rejected-alternatives section. The draft mentions sessions
   and delegations coexist on line 218 but does not frame it as a
   rejected design.

2. *Single repo-root `delegations.jsonl` (one append-only file).*
   Confirmed missing. This is the alternative someone *will* propose
   the day after the DES ships ("why a directory per delegation?"),
   so the DES should pre-empt the question.

I confirm both. I have not found a third addition I would call
blocking.

## Summary of named edits (final)

The prior review's four blocking edits stand:

1. **Close the predicate language to two predicates.** Remove
   substitution syntax. Reconcile the count contradiction across
   lines 222, 119–123, and 302–304.
2. **Cite review-cycle fix rounds in the ad-hoc-contract defense.**
3. **Reconcile the delegation-lock directory** in the concurrency
   table vs the storage-layout diagram.
4. **Add two rejected alternatives** (reuse session_id, single
   repo-root jsonl).

One supplemental advisory (Supplement B): rewrite the per-delegation
flock's verdict line to defend it on uniformity, not on contention.

## On the operational test (for mcg)

The prior review's operational argument holds. Two predicates plus
synthesized ad-hoc contract plus per-delegation flock cover the four
gaps with the minimum complexity that survives "does this actually
need to ship now?"

The redo did not change the recommendation. It clarified one
contradiction in the draft (Supplement A) and tightened one defense
(Supplement B). Neither changes the verdict.

ITERATE. With the four blocking edits applied, APPROVE.

— rop
