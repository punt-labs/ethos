Review commit 3cf6bf4 on branch fix/spawn-binding in <repo>/.claude/worktrees/session-pid-identity. Diff against parent 34f0e33 (`git diff 34f0e33..3cf6bf4`), ignoring `.punt-labs/ethos/sessions/**` and `.punt-labs/ethos/missions/**` JSONL files — generated audit state, not authored code.

Context: this changes how `ethos mission dispatch`'s "active-mission" sidecar binds an `Agent()` spawn to a mission. Previously it bound the next spawn unconditionally, silently mis-attributing unrelated agents as delegations. Now it binds only a spawn whose agent type matches the contract's declared `Worker`, and consumes the binding on success.

This code sits in a PreToolUse hook, so it has a hard constraint the reviewer must weigh: **dispatch helpers must be non-blocking.** A failure here must not prevent the user's `Agent()` spawn from running. That means several error paths deliberately log to stderr and fall through rather than returning an error. Your job is NOT to flag every fallthrough as a silent failure — it is to judge whether each one is (a) genuinely justified by that constraint, (b) actually observable when it happens, and (c) failing in the safe direction.

Specific things to scrutinize:

1. **`readActiveMissionForDispatch`'s error paths** in `internal/hook/pretooluse_dispatch.go`. Several return `("", false)` after writing to stderr. For each: is the stderr message actionable — does it tell the operator what was bound, what happened, and what to do? Is there any path that returns the no-binding answer WITHOUT any diagnostic at all where one is warranted?

2. **The contract-load-failure case.** DES-076 says a contract that fails to load while gating a dispatch binding is treated as a mismatch, not a block, and explicitly acknowledges this diverges from the repo's own "malformed env never silently admits" doctrine. Read that justification in DESIGN.md and judge it. Is the divergence sound, and is the failure actually surfaced when it occurs? A deliberate, documented, observable divergence is fine; an undocumented or invisible one is not.

3. **`consumeDispatchBinding`.** If clearing the sidecar fails, what happens? A failure to consume means the binding survives and can capture a later spawn — the exact bug being fixed. Is that failure detected and reported, or swallowed?

4. **Anything that swallows an error while touching the audit trail.** In this project the audit trail is the product; a lost or wrong delegation record is data corruption, not a cosmetic issue.

Report findings with file:line and a concrete failure scenario for each. State explicitly which fallthroughs you examined and judged acceptable, with the reason — a clean report is only credible if it says what was checked.