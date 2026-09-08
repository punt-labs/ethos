Work in <repo>/.worktrees/fix-cluster2 on branch fix/cluster2-mission-layers. This is a PR #508 review-cycle fix (round 5) — mechanical, no mission needed.

ONE finding, from Copilot, which I verified before sending:

`Store.conflictScanIDs` (internal/mission/store.go, around line 1886-1915) builds its result by ranging over the `owned` map returned by `repoMissionIDs` and appending to `ids`. Go randomizes map iteration order, so the audit-owned tail of the returned slice is non-deterministic. That slice feeds `checkWriteSetConflicts`, whose `[]Conflict` result feeds `formatConflictError` (internal/mission/conflict.go:539), which joins one line per conflict in slice order. So when a new contract conflicts with two or more existing missions and at least one came from the audit-owned tail, the operator sees the same conflicts in a different order run to run.

The repo-tree half is already deterministic — `listRepoTree` goes through `os.ReadDir`, which sorts. Only the appended tail is unordered.

Fix: sort the owned IDs before appending. Sort only the tail, not the whole slice — keeping "repo tree first, then audit-owned" preserves the grouping the current code intends and keeps the diff to the actual defect. Say in your report if you think sorting the whole result is better and why; I will take the argument.

Requirements:

1. A regression test verified FAILING before the fix. A naive single-call test is flaky-red, not reliably red — construct it so it fails deterministically pre-fix, e.g. build a store with three or more audit-owned IDs, call `conflictScanIDs` repeatedly (50+ iterations), and assert every result is identical to the first. Confirm it goes red pre-fix and green post-fix, and state in your report which form you used and what you observed.
2. Check for the same pattern elsewhere in internal/mission — any other `for x := range someMap` that appends to a slice which reaches operator-facing output or a test assertion. Fix the class, not just this instance. Report what you found, including "nothing else" if that is the answer.
3. `make check` green before the commit. No suppressions.
4. `GOOS=windows GOARCH=amd64 go build ./...` must still exit 0.
5. Detached gate before reporting: `rm -rf .tmp/hc && mkdir -p .tmp/hc && git archive HEAD | tar -x -C .tmp/hc`, then `setsid env -u CLAUDE_PID -u CLAUDECODE -u CLAUDE_CODE_SESSION_ID -u CLAUDE_CODE_AGENT -u CLAUDE_CODE_CHILD_SESSION sh -c 'cd <repo>/.worktrees/fix-cluster2/.tmp/hc && go test ./... -count=1' < /dev/null | tee out.txt`. PIPE, never redirect — redirecting truncates the file to 0 bytes while still exiting 0. Report the `ok` count AND the `FAIL` count; a zero FAIL count alone cannot distinguish nothing-failed from nothing-ran. Expect 27 ok / 0 FAIL / 1 no-test-files.
6. Commit with a conventional message. Do NOT push — report and I push.

No CHANGELOG entry needed unless you judge the ordering change user-visible enough to warrant one; say which you chose and why.