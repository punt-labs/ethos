Review the FULL branch `fix/spawn-binding` in <repo>/.claude/worktrees/session-pid-identity: `git diff 34f0e33..HEAD`. Ignore `.punt-labs/ethos/{sessions,missions}/**` JSONL — generated audit state.

You reviewed an earlier commit on this branch. Your HIGH finding — that `dispatchedWorker` discarded contract-load errors and the caller then asserted a false cause — was accepted; that function has since been **deleted** in a redesign. Review the end state, not the history, and do not assume prior fixes were done correctly.

**What changed.** Pending dispatch bindings moved out of the single `active-mission` slot into a per-mission `dispatch-pending/` store (`internal/mission/active.go` ~400-520), matched by `matchDispatchPending` (`internal/hook/pretooluse_dispatch.go` ~190). Delegations now record `BoundVia` provenance, and `ethos mission abandon --disclaim` can retire a mission whose only delegation was wrongly captured.

**Hard constraint to weigh, same as before:** this code runs in a PreToolUse hook, so dispatch helpers must be non-blocking — a failure must not prevent the user's `Agent()` spawn from running. Several paths deliberately log to stderr and fall through. Do not flag every fallthrough; judge whether each is justified, observable, and failing in the safe direction.

Priorities:

1. **`ReadDispatchPending`'s warning channel.** It returns `(entries, warnings, err)`. Where do those warnings come from, are they all surfaced, and can a malformed or partially-written pending file be silently skipped in a way that loses a binding without telling anyone?

2. **`ConsumeDispatchPending` failure.** The prior mechanism's consume failure left a binding that captured every subsequent matching spawn. Verify the new one's failure is detected, reported, and that the message names the real consequence. There is a long comment there claiming three remedies all share the same failure mode — check that claim is true rather than reassuring.

3. **Partial writes and crash windows.** `WriteDispatchPending` creates one file per pending dispatch. What does a truncated or empty file read back as? Is that distinguishable from a valid entry, and does it fail toward capturing or not capturing?

4. **`DisclaimDelegation` and the `aborted` exclusion in `Abandon`.** These relax a deliberate safety gate. Any swallowed error here means a mission is retired on incomplete information. Check the rollback path too — a prior round added one after the disclaim marker could be written without its audit event.

5. **Anything that swallows an error while touching the audit trail.** In this project the audit trail is the product; a lost or wrong delegation record is data corruption.

Report findings with file:line and a concrete failure scenario. List the fallthroughs you examined and judged acceptable with the reason — a clean report is only credible if it says what was checked.