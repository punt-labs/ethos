Review ONLY the delta `git diff f9bbf00..HEAD` on branch `fix/spawn-binding` in <repo>/.claude/worktrees/session-pid-identity. Ignore `.punt-labs/ethos/{sessions,missions}/**` JSONL — generated audit state.

**Scope discipline matters here.** The rest of this branch has already had two full local-review rounds by three reviewers each, producing 25+ findings that are all fixed. Do NOT re-review the earlier commits. These 12 commits are the fixes for the most recent round and have had zero review. Read earlier code only as context for whether these changes are correct.

The tree is clean, `make check` passes, `GOOS=windows GOARCH=amd64 go build ./...` exits 0. So you are looking for correctness and design problems, not gate failures.

Priorities, roughly by risk:

1. **`052d18b` — the three-way classification in `matchDispatchPending`** (`internal/hook/pretooluse_dispatch.go`). Pending dispatch entries are now classified open / stale / unresolvable. Stale entries are skipped AND cleared; unresolvable ones are skipped but deliberately NOT cleared (a failed contract load proves nothing — the file may return after a branch switch). Verify: can a viable entry ever be misclassified? Can the skip loop pick a *later* entry when an earlier viable one exists? Does a lone unresolvable entry correctly fall through rather than block? Is anything cleared that shouldn't be?

2. **`6c601df` — `internal/session/store.go`'s `deleteFiles` now clears mission sidecars.** This is the widest blast radius in the branch: `deleteFiles` is the shared primitive under `Delete`, `Purge`, and `PurgeTombstoned`, in a package this branch otherwise does not touch. Check for unintended consequences on the purge/tombstone paths, whether the advisory (log-and-continue) discipline is right here, and whether clearing sidecars during a *purge of someone else's stale session* can affect a live session.

3. **`af9b941` — an AST-based drift guard** asserting `DelegationVerdictAborted` has exactly three write sites. Verify the guard actually detects a fourth (the author says they falsified it by inserting one). Check it can't be trivially defeated — e.g. by an alias, a variable, or a call through a wrapper — and that it fails with a message that tells the next person what to do.

4. **`18bb940` — `bindDispatchedMission` now reads the pending queue back** to report actual queue position at dispatch time. New read on a write path; check ordering, error handling, and that it can't itself fail the dispatch.

5. **The doc changes** (`0c824ae` rewriting `docs/mission-abandon.md`, `6c07b73`, `964cb93`, `60cec2f`): verify the new text matches the code's actual behavior. On this branch, prose asserting behavior the code does not have has been the single most recurring defect class — five separate instances so far.

Report concrete findings with file:line. State which of the five areas you examined and found clean; a clean report is only credible if it says what was checked.