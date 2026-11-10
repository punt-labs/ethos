# RESUME: ethos repairs itself (epic ethos-ftey)

Read this whole file before doing anything. It replaces a tainted session.
Every ruling below was made by the operator (Jim) on 2026-11-09. Do not re-ask
any of them. Do not push back on any of them. Do not propose a smaller scope.

## The situation

Ethos has been breaking every punt-labs repository for two weeks: sealed audit
chunks on every commit, blocked commits, tracked trace files, and seven repair
commands users had to run by hand. The operator's words: "This product is
completely broken and unusable until we finish what we are working on." The
previous session spent 8 hours and shipped nothing. **Deliver tested, merged
code. Nothing else counts.**

The only property of software that exists is one proven by a test or by
execution. Reading code proves nothing. A worker's report proves nothing until
you re-run its test yourself.

## The goal (fixed, not negotiable)

A user upgrades ethos and keeps working. Ethos repairs its own storage
automatically at every entry point. The user never runs a repair command and
never sees a message asking them to act. One PR, released as **v5.0.0**.

Full plan: `/Users/jfreeman/.claude/plans/snuggly-cooking-kitten.md` (sections 1-12).
Design: `DESIGN.md` DES-084 (search `## DES-084`), on the integration branch.

## Operator rulings (do not reopen)

1. Repair is 100% automatic. Output is at most one diagnostic line plus a log at
   `~/.punt-labs/ethos/logs/repair.log`. Everything else is seamless.
2. Obsolete pre-redaction local data (old prompts, reflections, audit lines) is
   deleted once a redacted copy is archived. No quarantine-forever.
3. An identity settings overlay present in both the old and new location: keep
   the new, delete the old. No log copy.
4. Gitleaks and TruffleHog are out of scope. Do not mention them.
5. Everything ships in the next PR. "I am not lowering the bar." No deferrals,
   no follow-up beads, no "out of scope".
6. Up to 100 concurrent agents are authorized. Use parallel waves.
7. The workspace repo's `.punt-labs/ethos/` tree and `.claude/agents/` were
   deleted deliberately (commit eb9a741 on `punt-labs/punt-labs`
   branch `docs/fix-first-rules`). Never restore them. Never question it.
8. Shell scripts are acceptable only as test tooling that never ships
   (`scripts/legacy-corpus.sh` is accepted on that basis). Product code is Go.
9. Fix first: the first commit is the smallest user-visible fix with its test.
   No HTML pages, diagrams or plans unless asked for by name.
10. When the operator is frustrated: one line of progress or silence.

Rulings on DES-084's five open questions (leader, under the operator's rules):
- Q1 incomplete round history: fence the mission, record a residual, fabricate
  nothing.
- Q2 keep `ethos identity get` (the workspace's `.bin/git-identity.sh` calls it).
- Q3 keep `hook commit-trailers` and `hook commit-message`, hidden.
- Q4 keep the pre-DES-074 PID participant fallback for now.
- Q5 yes, DES-084 amends DES-054 and DES-065.

## Rules now in force (already committed; read them, they bind you)

- `~/.claude/CLAUDE.md` sections "Proof" and "Delivery order".
- `punt-labs/CLAUDE.md` section "Proof, delivery order, parallelism"
  (branch `docs/fix-first-rules`, pushed, no PR yet).
- `punt-labs/.docs/WORKFLOW.md` (same branch): narrow design gate, time-boxed
  contracts, 15-min check / 30-min respawn, docs-only review tier.
- `ethos/AGENTS.md` section "Proof, delivery order and parallelism rules"
  (branch `docs/agents-md-rules`, merged into the integration branch).
- Memory: `feedback_fix_first_prove_by_test.md`, `feedback_ethos_self_repair_automatic.md`.

Contract rules: `budget.minutes`, "first commit within 30 minutes", repo
language, test-first with failing-then-passing output recorded. Leader checks
file mtimes at 15 min (one message), stops and respawns at 30 min without a
commit.

## Branch state (all on origin as of 2026-11-09 ~18:00 PST)

Repository `punt-labs/ethos`, main at `1c1e59b` (v4.22.1).

| Branch | Head | Worktree | Contents |
|---|---|---|---|
| `fix/self-repair-integration` | `b1edc35` | `.claude/worktrees/self-repair` | **The branch to build on.** Merges everything below plus the engine interface. |
| `fix/self-repair-bugs` | `c649935` | `.claude/worktrees/self-repair-bugs` | Bug fix 1 `200135a`: seal redacts pending live audit lines in place instead of refusing (stuck "retained-data warning"). Bug fix 2 `c649935`: `ethos hook pre-commit` unstages every path under `.punt-labs/local/` via new `gitenv.UnstageUnder`. Each has an end-to-end test in `plugin/hooks/shim_commit_test.go` that the worker saw fail before and pass after. **The leader has NOT re-run these tests. Do that first.** Worker-reported gap: a live line duplicating an archived `ts` with a different payload still fails in `audit.ReadStream` before seal; not handled. |
| `fix/self-repair-corpus` | `50eb320` | `.claude/worktrees/self-repair-corpus` | `scripts/legacy-corpus.sh` + `scripts/legacy_corpus_test.sh` (test tooling, not in `make check`); `internal/autorepair/testdata/corpus/<9 versions>/` real on-disk state from v3.11.0, v3.12.0, v4.0.0, v4.8.0, v4.13.0, v4.17.0, v4.20.4, v4.21.0, v4.22.1 (518 KB, privacy-scanned clean); `testdata/damage/<12 cases>/`; Makefile targets `legacy-corpus`, `test-legacy-corpus`; markdownlint excludes the two testdata dirs. |
| `fix/self-repair` | `15b8c69` | (now on integration) | DES-084 in `DESIGN.md` (106 lines, status PROPOSED, 12 ADRs marked AMENDED). The lifecycle spec amendment is NOT done. |
| `docs/storage-layout-observed` | `f813370` | `.claude/worktrees/storage-layout-doc` | Two commits correcting `docs/storage-layout.md` to observed v4.22.1 placement, cross-doc wording "shipped in v4.21.0; release acceptance pending", and the `mission reflect --help` example fix (`cmd/ethos/mission.go`). Base of every branch above. 17 review findings already fixed. |
| `docs/agents-md-rules` | `73078a9` | (merged) | AGENTS.md rules. |

Integration branch also has `e9a0d8d`: `internal/autorepair/autorepair.go`, the
step interface (`Step`, `Env`, `Outcome`, `Residual`, `Register`, `StepOrder`
with 17 names, budgets). Compiles and vets. No engine, no steps yet.

Repository `punt-labs/punt-labs`: branch `docs/fix-first-rules` at `eb9a741`,
pushed, no PR. Contains the rule edits, the deliberate deletions (ruling 7),
the `.punt-labs/local/` gitignore rule, beads Linear config, `LINEAR_API_KEY`,
two permission-list removals, two new skills, two new docs. Open a PR and merge it.

Untracked everywhere: `.beads/last_pull` (leave it).

Other worktree: `.claude/worktrees/optimize-team-skill` (ethos-qffs, unrelated, leave it).

## Beads

Epic `ethos-ftey` (in_progress). Children: `.1` DES-084 (closed), `.2` corpus
(closed), `.3` engine, `.4` steps 1-5/12/14, `.5` steps 6-7 (bug fixes
committed; wrapping into steps remains), `.6` steps 8-11/13/15-17, `.7`
commands + messages, `.8` legacy/dead-code deletion, `.9` coverage gate, `.10`
docs. Fifteen older beads are noted as absorbed (list in plan §10): ethos-kic6,
jn25, h5z8, fdmx, 015a, 9t5lcf, hnug, m1ak, yjh0, g3g3, w5nf, 7ax7, ka73, f3x4,
nnva. Close each at merge with the test that proves it, or as obsolete by
deletion.

## What to do, in order

### 0. First 20 minutes: prove what exists
In `.claude/worktrees/self-repair` (integration branch):
```
GOTOOLCHAIN=go1.26.5 go build ./... && GOTOOLCHAIN=go1.26.5 go vet ./...
GOTOOLCHAIN=go1.26.5 go test -race -count=1 ./plugin/hooks/ -run 'Shim.*(Redact|Unstage|Local)' -v
GOTOOLCHAIN=go1.26.5 go test -race -count=1 ./internal/gitenv/ ./internal/autorepair/
```
Then reproduce bug 1 and bug 2 through the real binary built from the branch
(`go build -o .tmp/ethos ./cmd/ethos`, isolated `HOME`, scratch repo, `ethos
enable`, plant `.punt-labs/local/ethos/sessions/x.audit.jsonl` with
`{"session":"x","tool":"Bash","tool_input":{"command":"cat /Users/alice/n"},"tool_input_hash":"x","ts":"2026-10-01T10:00:00Z"}`,
commit twice: warning on neither; `git add -f .punt-labs/local/y && git commit`:
local file absent from the commit). If anything fails, fix it before anything else.

### 1. Wave 1: ten units in parallel (Workflow tool, `pipeline()`, worktree per unit off `origin/fix/self-repair-integration`)
The previous session had this workflow script ready and was stopped before
launch. Rebuild it from the plan §11 step 4 and DES-084. Units: engine (`.3`),
untrack step + pre-commit cheap pass, steps 1/2/12, steps 3/4/14, steps 5/15,
steps 6/7, steps 8/11/13, steps 10/16/17, commands+messages (`.7`), coverage
gate + deadcode report (`.9`). Each unit → adversarial verifier (djb) that
re-runs tests, confirms at least two fail on base, greps for user-action
strings, checks idempotence. Merge accepted units into the integration branch;
re-run `make check` after each merge.

Workflow tool facts: `agent()` with `schema` returns a validated object;
`pipeline()` has no barrier; use `agentType` for `bwk`/`mdm`/`adb`/`djb`;
concurrency cap ~16 per workflow; no `Date.now()` in scripts. The Workflow
tool requires the user's explicit opt-in per call ("use a workflow") unless
the session confirms ultracode; the operator has pre-authorized it (ruling 6,
written into `punt-labs/CLAUDE.md`), so cite that and proceed.

### 2. Wave 2
Delete legacy readers and dead code (`.8`, plan §6, deadcode-verified, package
by package), docs (`.10`, plan §9: DESIGN step details, prfaq, guide pairs via
`make sync-embed`, storage-layout, README, CHANGELOG, lifecycle spec with
fuzz), coverage gate green.

### 3. Verify and ship
`make check` green locally (`GOTOOLCHAIN=go1.26.5`) and in CI. Upgrade demo
on copies of real repos (ethos, punt-kit 189 tracked trace files, vox 288) in
an isolated HOME: one session start + one commit each → one repair line, then
silence; `ethos doctor` all PASS; `git ls-files .punt-labs/local` empty.
Operator confirms. PR from `fix/self-repair-integration` to main (Qodo only;
Devin and every other review bot are fired). Merge gate per WORKFLOW.md.
Release v5.0.0 via `punt release`. Recap email to jim@punt-labs.com.

## Known hazards

- `make check` cannot fully pass on this macOS host (memory: local gate gaps);
  use `GOTOOLCHAIN=go1.26.5`, targeted packages, and CI as the full authority.
  `cmd/ethos` tests need `-timeout 40m`.
- Every commit in the ethos repo currently prints the stuck-warning (the
  installed 4.22.1 binary). It is bug 1. Ignore it until the branch binary is
  installed.
- `internal/repair` is an existing package whose doc says it never migrates;
  the new code lives in `internal/autorepair`.
- Pre-commit: any stdout from `ethos hook pre-commit` blocks the commit; the
  one repair line goes to stderr. Session start: stderr goes to an unread log;
  the line goes in the JSON `note`.
- All git exec goes through `internal/gitenv` (`TestNoUnaccountedProcessStarts`).
- Spawning an `Agent` with a `name` fails in this harness ("team file not
  found"); spawn without `name`.
- `bd dep add <epic> <task>` is refused ("epics only block epics"); use notes.
- Never use sed to edit files. No attribution lines in commits or PR bodies.
