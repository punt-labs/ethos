You are the worker on ethos mission m-2026-09-08-001. Start with:

    ethos mission show m-2026-09-08-001

That contract is authoritative. Work in a fresh worktree off current main (`ec47a92`) — `git worktree add` under `.worktrees/`, branch `fix/cluster2-mission-layers`. Repo root is <repo>. Do not push; report and I push.

READ THE TRIAGE NOTES BEFORE ANY CODE. `bd show ethos-6adb`, `ethos-ouy9`, `ethos-lj4k`, `ethos-5yej`, `ethos-7tqd`. Each has a note dated 2026-09-07 with a measured mechanism.

**Three of the five beads record a WRONG mechanism in their original text, and the notes correct them.** Build to the note. Specifically:

- `ethos-6adb` blames cwd resolution and proposes a `--repo` flag or `git rev-parse` at invocation. That is wrong and the note marks it **do not implement**. The real cause is `Store.List()` (store.go:1645) enumerating the unscoped global tree alongside the per-repo one.
- `ethos-lj4k` says `Abandon` runs unlocked. It doesn't — it holds a *different lock file*. `Store.withLock` takes `<globalMissionsDir>/<id>.lock` (store.go:424); `AcquireMissionLockExclusive` takes `<repoRoot>/.punt-labs/ethos/missions/<id>/.lock` (delegation.go:450). The note also rejects the filed fix: adding the second lock invites a lock-ordering deadlock.
- `ethos-7tqd` says orphaned contracts sit inert and nobody notices. Backwards — an orphan is *active*, and captures the next unrelated `Agent()` spawn as its delegation. I reproduced this on myself today.

**THE CENTRAL POINT: four of these five are one defect.** `internal/mission` has two storage layers that disagree about scope, and call sites choose inconsistently — enumeration spans both, locking uses one while the data lives in the other, resolution uses only one.

So: **write the layer model down as a DESIGN.md ADR before touching code.** Which layer is authoritative for enumeration, for locking, for reads, for writes, and what the global tree is still for. The fixes should fall out of that decision. If you start with the code you will patch four symptoms and leave the ambiguity.

Measured evidence you can rely on, from today: 841 contracts in the global tree, 19 open, and **zero carry a `repo:` field** — so repo-filtering alone fixes nothing for existing data. Decide whether the global layer gets excluded from enumeration outright or backfilled.

`ethos-5yej` is correctly NOT-A-BUG and must stay that way — resolving the repo layer to the main work tree is what lets a linked worktree see its parent's missions, which I relied on all day. Don't change the behaviour; just **name** in the ADR what a worktree's own `.punt-labs/ethos` means. Today it silently means nothing.

PROCESS — these are not optional, and each one exists because it caught something real today:

- Every behavioural change gets a regression test **verified failing against pre-fix code first**. Say in your result which you confirmed red.
- `make check` green before every commit. No suppressions.
- Detached gate before reporting: `rm -rf .tmp/hc && mkdir -p .tmp/hc && git archive HEAD | tar -x -C .tmp/hc`, then `setsid env -u CLAUDE_PID -u CLAUDECODE -u CLAUDE_CODE_SESSION_ID -u CLAUDE_CODE_AGENT -u CLAUDE_CODE_CHILD_SESSION sh -c 'cd <abs>/.tmp/hc && go test ./... -count=1' < /dev/null | tee out.txt`. **PIPE, never redirect** — `setsid cmd > file` truncates to zero bytes while exiting 0. Report the `ok` count **and** the `FAIL` count; a zero FAIL count cannot distinguish nothing-failed from nothing-ran. Expect 27 ok / 0 FAIL / 1 no-test-files.
- `GOOS=windows GOARCH=amd64 go build ./...` must still exit 0 — `internal/mission` gained Windows locking in PR #507 and you're editing that package.
- Commit incrementally, one logical step each.

Prior art: PR #507 (`ec47a92`) fixed this exact asymmetry shape one package over — `session.WriteCurrentSession` was a plain non-atomic write while `writeRoster` beside it was fully atomic. `ouy9`'s `writeContract` is that same defect. `DES-074` in DESIGN.md is the session-identity decision this cluster follows.

Report tersely: the ADR's decision, what changed per bead, tests confirmed red first, gate counts, the Windows build result, and anything in my triage you think is wrong. Push back if you disagree — twice today a worker was right and I was wrong.