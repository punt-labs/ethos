# Ethos Development Lifecycle — Live Example

A complete walkthrough of the ethos development lifecycle using a real
bead executed on 2026-04-10: `ethos-db7` — consolidate verifier contract
load to eliminate a TOCTOU window.

Every output in this example is real. The mission `m-2026-04-10-003` was
created, executed, reviewed, merged, and closed in a single session.
Total wall time: **12 minutes 55 seconds**.

## The Bead

```text
ethos-db7 (P3, task)
Title: mission: consolidate verifier contract load to single ReadFile + Unmarshal
```

**Problem**: `checkVerifierHash` in `internal/hook/subagent_start.go` does
`Store.Load(id)` (which calls `os.ReadFile` + `yaml.Unmarshal` internally)
then a separate `os.ReadFile` for `RawYAML`. Two reads = TOCTOU window.
If the contract file changes between reads, the parsed contract and the
raw bytes diverge.

**Fix**: One `os.ReadFile`, unmarshal locally, use the same bytes for both
hash verification and rendering.

**Result**: 1 file changed, +50/-23 lines. PR #212 merged.

## Timeline

| Phase | Duration | % | What |
|-------|----------|---|------|
| 1. Claim + Branch | 18s | 2% | `bd update`, `git checkout -b` |
| 2. Mission Create | 24s | 3% | Contract with write-set, criteria, evaluator |
| 3. Worker Execution | 2m10s | 17% | bwk implements the fix |
| 4. Verify + Result | 31s | 4% | `make check`, submit structured result |
| 5. Reflection | 31s | 4% | Leader assesses convergence |
| 6. Code Review | 1m25s | 11% | Local code-reviewer, address finding |
| 7. Commit + PR | 1m0s | 8% | Commit, push, create PR |
| 8. CI + Merge + Close | 6m36s | 51% | CI, Copilot, Bugbot, resolve, merge, close |
| **Total** | **12m55s** | | |

Coding was 17% of wall time. The review pipeline (local + remote) was
62%. This is the correct distribution — review is the bottleneck, not
implementation.

## Mission Event Log

```text
2026-04-10 07:19 PDT  create   by claude  worker=bwk evaluator=djb bead=ethos-db7
2026-04-10 07:22 PDT  result   by bwk     round=1 verdict=pass
2026-04-10 07:23 PDT  reflect  by claude  round=1 rec=continue
2026-04-10 07:31 PDT  close    by claude  status=closed verdict=pass round=1
```

4 events. 1 round. No rework.

## Files

Each numbered file documents one phase with real commands and outputs:

| Phase | File | Description |
|-------|------|-------------|
| 1. Claim | [`01-claim.sh`](01-claim.sh) | Bead claim + branch creation |
| 2. Mission | [`02-mission.yaml`](02-mission.yaml) | Mission contract (actual contract used) |
| 3. Delegation | [`03-delegation-spec.md`](03-delegation-spec.md) | Task spec sent to bwk |
| 4. Result | [`04-result.yaml`](04-result.yaml) | Worker's structured result |
| 5. Reflection | [`05-reflection.yaml`](05-reflection.yaml) | Leader's convergence assessment |
| 6. Code Review | [`06-code-review.md`](06-code-review.md) | Local review findings + fix |
| 7. PR & Merge | [`07-pr-and-merge.md`](07-pr-and-merge.md) | PR creation, Copilot/Bugbot, merge |
| 8. Close | [`08-close.sh`](08-close.sh) | Mission close, bead close, recap |

## Key Observations

**Mission granularity matters.** This mission was 73 lines across 1 file
with 4 success criteria — small enough for 1 round, large enough to
justify the contract overhead. Fixed overhead per mission is ~4 minutes
(claim, contract, result, reflection, commit, PR). Missions under 20
lines should be batched. Missions over 200 lines should be split.

**The contract is the spec.** The write-set (`internal/hook/subagent_start.go`)
bounded the worker's scope. The success criteria ("one ReadFile, same
bytes, make check passes") were individually verifiable. The evaluator
(`djb`) was pinned at creation, preventing goalpost drift.

**Layered review catches different things.** The local code-reviewer
found duplicated symlink logic (maintenance hazard). Bugbot found
duplicated `decodeAndValidate` logic (same class). Neither was a bug —
both were architectural observations the leader resolved as accepted
tradeoffs with comments.

**The event log is the audit trail.** 4 lines tell the full story:
who created it, who delivered, whether the leader agreed, and when it
closed. `ethos mission log m-2026-04-10-003` reconstructs this at any
time.
