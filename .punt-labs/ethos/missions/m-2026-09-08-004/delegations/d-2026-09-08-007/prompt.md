Review the FULL branch `fix/spawn-binding` in <repo>/.claude/worktrees/session-pid-identity: `git diff 34f0e33..HEAD`. Ignore `.punt-labs/ethos/sessions/**` and `.punt-labs/ethos/missions/**` JSONL files — generated audit state, not authored code.

This branch has been through two prior local-review rounds and a mid-branch REDESIGN, so review the end state, not the history. Do not assume earlier findings were fixed correctly; several were fixed by replacing the mechanism entirely.

**What the branch does.** `ethos mission dispatch` writes a contract; a separate `Agent()` call spawns the worker. Bridging that gap, dispatch records a pending binding so the spawned worker's delegation files under the right mission. The original bug: that binding captured the NEXT spawn regardless of who it was, mis-attributing unrelated agents. Round 1 scoped the binding to the contract's declared `Worker`. Round 3 then moved pending dispatches out of the single `active-mission` slot into a new per-mission store (`dispatch-pending/`, one file per pending dispatch) because a second dispatch destroyed the first's binding. Separately, delegations now record `BoundVia` provenance, and `ethos mission abandon --disclaim` can retire a mission whose only delegation was wrongly captured.

Decision record is **DES-076 in DESIGN.md**. Read it and check the code does what it claims — on the PR that merged just before this one, the most expensive defect class was prose asserting behavior the code did not have, and this branch has already produced four instances of it.

Priorities:

1. **The new `dispatch-pending` store** (`internal/mission/active.go` ~line 400-520) and its consumer `matchDispatchPending` (`internal/hook/pretooluse_dispatch.go` ~line 190). This is new code that replaced a reviewed mechanism. Concurrency, partial-write, and cleanup behavior all matter — the files live under a per-session directory and are removed on consume.
2. **FIFO matching.** `matchDispatchPending` returns the first entry whose `Worker` equals the spawn's agent type, oldest first. When two pending dispatches share a worker handle, this is a guess. Is the guess safe? What happens when it is wrong? Is there any signal that an ambiguous choice was made?
3. **`Abandon`'s gate changes** — `countBlockingDelegations`, the `aborted`-verdict exclusion, and `DisclaimDelegation`. This relaxes a deliberate safety gate; verify it cannot retire a mission that genuinely had work done.
4. **Backward compatibility.** `BoundVia` is a new field on records that already exist on disk. The empty value must never be treated as evidence a delegation is disclaimable.
5. **CLAUDE.md compliance** for this repo (see repo CLAUDE.md and docs/development.md).

Report concrete findings with file:line. State explicitly which categories you checked and found clean — a clean report is only credible if it says what was examined.