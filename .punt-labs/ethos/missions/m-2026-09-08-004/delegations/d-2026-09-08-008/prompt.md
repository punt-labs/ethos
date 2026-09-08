Review the FULL branch `fix/spawn-binding` in <repo>/.claude/worktrees/session-pid-identity: `git diff 34f0e33..HEAD`. Ignore `.punt-labs/ethos/{sessions,missions}/**` JSONL — generated audit state.

You reviewed an earlier commit on this branch and found that DES-076's central claim ("One dispatch, one binding, one consuming spawn — the sidecar cannot outlive the worker it was written for") was false, with four unenumerated escapes. That finding forced a **redesign**: pending dispatches now live in a per-mission `dispatch-pending/` store rather than the single `active-mission` slot. DES-076 has been rewritten for round 3 and now carries an explicit residual-risk section.

**Your job is to check the rewritten claims, not the old ones.** Read DES-076 in DESIGN.md as it now stands and verify each assertion against the code. Treat the residual-risk section with particular care: the failure mode I most want caught is an ADR that under-states what it leaves open, because the previous version did exactly that and three defects survived a review round because of it.

Specific claims to verify:

1. **That the C2 class is "closed structurally" because dispatch no longer touches the `active-mission`/origin pair at all.** Verify nothing in the dispatch path still writes or clears that pair, and that the reader's claim-defaulting behavior is now genuinely always correct. If any path can still produce a dispatch-shaped binding in the old slot, the structural claim is false.

2. **That two pending dispatches to the same Worker now coexist and resolve correctly.** They resolve FIFO by file mtime. Enumerate what happens when the spawn order does not match the dispatch order. The ADR reportedly names this as a residual — check whether its description matches what the code actually does, and whether any *other* ordering hazard exists that it does not name (e.g. mtime granularity or ties, clock skew, a file rewritten in place).

3. **That `aborted`-verdict delegations are excluded from `Abandon`'s gate "unconditionally, mechanical not heuristic."** Verify the exclusion cannot be reached by a delegation that did real work, and that it composes correctly with `DisclaimDelegation` — the two now both relax the same gate.

4. **That `HandleSessionEnd` "clears both binding stores."** Verify both, and that it is reached on the paths that matter.

5. **`BoundVia` provenance.** The empty/unknown value must never be treated as evidence a delegation may be disclaimed. Verify by construction, not by comment. Also check whether any code path can write a provenance value that misdescribes how the binding was actually made.

6. **The residual-risk section's completeness.** It reportedly names four residuals. Are there others? This is the highest-value part of your review.

Also assess test quality specifically: does each new test guard the general property it is named for, or only the fixture it constructs? You previously found a test that existed to prove one binding type never converts to another, and which checked only the field that stays the same under that conversion. Check for that shape again.

Report each claim as CONFIRMED or NOT CONFIRMED with file:line evidence, and state the condition under which any conditionally-true claim fails.