# Post-Release Smoke Test

Manual verification script for ethos after release, install, or MCP
server changes.

**What a smoke test must catch.** The things that catch fire in ethos are
its *enforcement guarantees* (write-set admission, the close/abandon result
gate, the distinct-evaluator rule, the pinned-verifier hash) and its *hooks*
(the git seal/trailer hooks, SessionStart/SubagentStop). A read-only pass
that only calls `show`/`list` stays green while every one of those is
broken — that is a false green, worse than no test. So the core of this
procedure (Phase 4) **drives** those behaviors and asserts the **negatives**:
a close with no result must be *refused*, a worker that is also the evaluator
must be *rejected*. The read/liveness checks (Phases 1-2) confirm each surface
responds; they are the appetizer, not the meal.

**Never run the driven tests against this source tree.** Phase 4 creates
identities, roles, teams, missions, and commits — all of which would pollute
the repo. It runs in a disposable local-only git repo under `$HOME`
(`ethos enable` refuses a repo outside `$HOME`), set up in Phase 0 and deleted
in Phase 6. Phases 1-2 (read-only) may run against the real repo; everything
that writes runs in the scratch repo.

Run from a Claude Code session in the ethos repo. The MCP phase needs the
`self` MCP server connected (restart Claude Code after an install); the CLI
and scratch-repo phases need `ethos` on `PATH`. Some hook behaviors
(the commit-msg trailer with a live delegation, SubagentStop distillation)
are session-scoped and can only be fully exercised from inside a Claude Code
session with real subagent spawns — these are marked SESSION-ONLY, mirroring
quarry's Phase 5.

## Prerequisites

Binary installed, plugin installed, doctor clean:

```bash
ethos version       # matches the released version
ethos doctor        # every check PASS (a single Storage WARN is acceptable)
```

Expected: identity directory, human identity, default agent, no duplicate
fields, no orphaned agent files, agent-file currency, team members resolve,
audit seal + trailer hooks present and current, hook binary resolves,
repo-only completeness, extension-file hygiene, mission-file cleanliness,
and storage-placement checks all PASS. A `Storage WARN` naming residual
layout conditions is acceptable (logged, non-fatal); any other WARN or FAIL
is a finding.

The MCP tool names below are the `self` server's: `identity`, `role`,
`team`, `personality`, `talent`, `writing_style`, `ext`, `session`,
`mission`, `adr`, `vendor`, `doctor`. In a dev checkout they resolve under
the `ethos-dev` plugin; in a consumer repo under `ethos`.

## Phase 1: MCP Tools

### 1.1 Doctor

**Tool:** `doctor`

**Verify:** Every check reports PASS (a single `Storage WARN` is
acceptable). The output names the identity directory, the resolved human
identity, and the active team's resolution mode.

### 1.2 whoami / identity (resolution path)

**Tool:** `identity` with `method: whoami`

**Verify:** Returns the caller's resolved identity (name + handle), e.g.
`Claude Agento (claude)`. This is the SessionStart resolution chain
(session → git → OS) answering live.

### 1.3 identity list

**Tool:** `identity` with `method: list`

**Verify:** Lists the vendored identities (15 in the ethos repo). The
active-session identity is marked. Count matches `ethos doctor`'s
"repo-only completeness" line.

### 1.4 identity show

**Tool:** `identity` with `method: get`, `handle: adb`

**Verify:** Shows adb's full record — name (Ada B), kind `agent`,
writing_style, personality, and talent slugs. Every referenced slug is one
that resolves (no dangling reference).

### 1.5 role show

**Tool:** `role` with `method: show`, name `infra-engineer`

**Verify:** Shows the role's responsibilities, permissions, and `model`
(sonnet for infra-engineer). A role that carries a scoped MCP tool set
lists those tools.

### 1.6 team show (active team membership)

**Tool:** `team` with `method: show`, name `engineering`

**Verify:** Lists the active team's members (identity + role per row) and
its collaborations. Every member identity resolves (cross-reference 1.3).
This is the set that should drive agent generation.

### 1.7 team list

**Tool:** `team` with `method: list`

**Verify:** Lists the repo's teams (engineering, formal-methods, website in
the ethos repo). The active team (`.punt-labs/ethos.yaml` `team:`) is among
them.

### 1.8 personality / talent / writing-style show

**Tools:** `personality` (show `principal-engineer`), `talent` (show one
slug an identity references), `writing_style` (show one slug)

**Verify:** Each returns the attribute's markdown body. The slugs are ones
referenced by a resolved identity in 1.4, proving the attribute store backs
the identity references.

### 1.9 session roster

**Tool:** `session`

**Verify:** Returns the current session roster — this session's id, repo,
host, and the participant identity. Stale sessions are not listed (they are
cleared automatically).

### 1.10 ext list (extension read path)

**Tool:** `ext` with `method: list`, a handle that carries an extension
(e.g. `rmh` carries a `quarry` namespace in the ethos repo)

**Verify:** Lists the identity's extension namespaces. A handle with no
extensions returns an empty set without error. No credential-named keys
appear (cross-reference doctor's "extension key names" check).

### 1.11 adr list

**Tool:** `adr` with `method: list`

**Verify:** Returns the repo's ADRs (id + title). An empty repo returns an
empty list without error.

### 1.12 Mission tool liveness (read)

**Tool:** `mission` with `method: list`

**Verify:** Returns the repo's missions (or an empty list) without error.
This is a liveness check only — the mission **lifecycle** (create → result →
reflect → close) and its enforcement negatives are driven in Phase 4's
scratch repo, NOT here. Do not create a mission against this source tree:
`mission create` writes a row to the tracked `missions.jsonl` and a mission
directory, polluting the repo.

The real contract schema (for Phase 4) is the one `ethos mission scaffold
contract` prints — `evaluator` is an object (`evaluator: {handle: ...}`),
`budget` is `{rounds, reflection_after_each}`, and a ticket goes under
`inputs: {ticket: ...}`. The flat shapes some older docs show are rejected by
the strict decoder.

### 1.13 vendor (dry run)

**Tool:** `vendor` with no apply flag (plan only)

**Verify:** Prints the plan of what a vendor snapshot would pull in,
writing nothing. Exit without error. (The real `--apply --prune` is a
mutating operation — do not run it in the smoke test.)

## Phase 2: CLI Commands

1:1 mirror of Phase 1 using CLI equivalents, plus CLI-only checks. Run from
a terminal on the host where ethos is installed.

### 2.1 Doctor

```bash
ethos doctor
```

**Verify all checks PASS** (a single `Storage WARN` acceptable): identity
directory, human identity, default agent, duplicate fields, orphaned agent
files, agent-file currency, team members, audit seal + trailer hooks, hook
currency, hook binary, repo-only completeness, extension files, extension
key names, mission-file comments, archetype guard, unclassified files,
physical storage placement.

### 2.2 Version

```bash
ethos version
```

**Verify:** Matches the released version (e.g. `ethos 5.1.0`).

### 2.3 whoami

```bash
ethos whoami
```

**Verify:** Prints the resolved caller identity, e.g.
`Claude Agento (claude)`.

### 2.4 identity list

```bash
ethos identity list
```

**Verify:** Lists the vendored identities with the active one marked. Count
matches doctor's repo-only completeness line.

### 2.5 identity show

```bash
ethos identity show adb
```

**Verify:** Shows adb's name, kind, and attribute slugs.

### 2.6 role show

```bash
ethos role show infra-engineer
```

**Verify:** Responsibilities, permissions, and model reported.

### 2.7 team show / list

```bash
ethos team show engineering
ethos team list
```

**Verify:** `show` lists members and collaborations; `list` includes the
active team.

### 2.8 Attribute show

```bash
ethos personality show principal-engineer
ethos talent list
ethos writing-style list
```

**Verify:** `personality show` returns the body; the `list` commands return
the available slugs.

### 2.9 session

```bash
ethos session
```

**Verify:** Prints the session roster (id, repo, host, participant).

### 2.10 ext list

```bash
ethos ext list rmh
```

**Verify:** Lists rmh's extension namespaces (or an empty set without
error).

### 2.11 find (state query)

```bash
ethos find missions --status closed --format json
```

**Verify:** Returns closed missions as JSON (or an empty array). Exit 0.

### 2.12 Mission tool liveness (CLI, read)

```bash
ethos mission list
```

**Verify:** Lists missions (or empty) without error. The CLI mission
lifecycle and its enforcement negatives are driven in Phase 4's scratch repo,
NOT here — `ethos mission dispatch`/`create` writes a tracked mission row and
directory into whatever repo it runs in, so it must never run against this
source tree.

### 2.13 Mission lint

```bash
ethos mission scaffold > .tmp/smoke-contract.yaml
ethos mission lint .tmp/smoke-contract.yaml
```

**Verify:** `scaffold` prints a fillable skeleton; `lint` runs advisory
heuristics and reports them. Cleanup: `rm -f .tmp/smoke-contract.yaml`.

### 2.14 adr list

```bash
ethos adr list
```

**Verify:** Lists ADRs (id + title) or an empty list. Exit 0.

### 2.15 provenance (message for a ref)

```bash
ethos provenance message HEAD
```

**Verify:** Prints the generated provenance body for HEAD (or reports no
blame records for the ref). Exit 0 — this is the squash-merge body
generator; it must not error on a clean ref.

### 2.16 vendor (dry run)

```bash
ethos vendor --team engineering
```

**Verify:** Prints the snapshot plan without `--apply`; writes nothing.
Exit 0. (Do NOT run `--apply --prune` in the smoke test.)

## Phase 3: Scratch-repo setup, enable/disable, write paths

Everything that writes runs here, in a disposable local-only git repo under
`$HOME` — never against this source tree. `ethos enable` refuses a repo
outside `$HOME`, which is why the scratch repo lives there. Set
`D="$HOME/.ethos-smoketest"` for the rest of Phases 3 and 5.

### 3.1 Create the scratch repo

```bash
D="$HOME/.ethos-smoketest"; rm -rf "$D"
mkdir -p "$D" && git -C "$D" init -q
git -C "$D" config user.email smoke@example.com
git -C "$D" config user.name "Smoke Test"
git -C "$D" commit --allow-empty -q -m init
```

**Verify:** A clean local git repo exists at `$D`. It has no remote — nothing
here is ever pushed.

### 3.2 Setup (writes a repo config + activates a bundle)

```bash
printf 'name: Smoke Operator\nhandle: smoke\nemail: smoke@example.com\nbundle: foundation\n' > /tmp/ethos-setup.yaml
cd "$D" && ethos setup --file /tmp/ethos-setup.yaml
```

**Verify:** Creates the `smoke` human identity, writes
`.punt-labs/ethos.yaml`, activates the `foundation` bundle, assigns
`smoke=ceo`/`claude=coo`, and generates `.claude/agents/foundation-*.md`.
This gives the scratch repo a resolvable identity set so missions can
reference real handles (`foundation-implementer`, `foundation-reviewer`, …).
Note: `setup` does NOT install git hooks — `enable` (3.3) does.

### 3.3 Enable

```bash
cd "$D" && ethos enable
```

**Verify:**

- Exit 0
- Deposits `.punt-labs/ethos/CLAUDE.md` and `ETHOS-SETUP.md`
- Writes the `.punt-labs/ethos/enabled` marker
- Adds the `@.punt-labs/ethos/CLAUDE.md` import line to the repo `CLAUDE.md`
- Installs the `pre-commit` and `commit-msg` git hooks (confirm:
  `ls "$D/.git/hooks/" | grep -E 'pre-commit|commit-msg'`)

### 3.4 Doctor in the enabled repo

```bash
cd "$D" && ethos doctor
```

**Verify:** The audit seal + trailer hook checks report "active" and current
(they report "not enabled here" before 3.3). Any FAIL is a finding.

### 3.5 Enable idempotent

```bash
cd "$D" && ethos enable
```

**Verify:** Exit 0; the import line reports "already present" and is not
duplicated; the marker is not re-added as a second entry.

### 3.6 Disable

```bash
cd "$D" && ethos disable
```

**Verify:**

- Exit 0
- Removes the `@`-import line and the `enabled` marker, removes both git hooks
- The `.punt-labs/ethos/` subtree is left dormant, not deleted

Re-enable before Phase 4 (`cd "$D" && ethos enable`) — Phase 4's hook test
needs the hooks installed.

## Phase 4: Enforcement and hooks (the core)

The heart of the smoke test. Runs entirely in the scratch repo from Phase 3
(`D="$HOME/.ethos-smoketest"`, enabled). Each step asserts a guarantee by
driving it and checking the **negative** — a broken build passes the read
phases but fails here. Mission IDs are `m-YYYY-MM-DD-NNN`; capture the one
`create`/`dispatch` prints (parse its own stdout line, not any ID that
appears in a conflict-check warning).

### 4.1 Distinct-evaluator rule (negative)

```bash
cd "$D"
printf 'leader: claude\nworker: foundation-implementer\nevaluator:\n  handle: foundation-implementer\nwrite_set:\n  - out/\nsuccess_criteria:\n  - x\nbudget:\n  rounds: 1\n  reflection_after_each: true\n' > /tmp/m-same.yaml
ethos mission create --file /tmp/m-same.yaml
```

**Verify:** REJECTED — "worker … cannot also be evaluator; assign a distinct
reviewer". A mission id being returned is a FAIL (the verifier could review
its own work).

### 4.2 Close result-gate (negative)

```bash
cd "$D"
printf 'leader: claude\nworker: foundation-implementer\nevaluator:\n  handle: foundation-reviewer\nwrite_set:\n  - out/\nsuccess_criteria:\n  - "write out/x.txt"\nbudget:\n  rounds: 1\n  reflection_after_each: true\n' > /tmp/m-ok.yaml
MID=$(ethos mission create --file /tmp/m-ok.yaml | grep -oE 'm-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9]+' | tail -1)
echo "mission: $MID"
ethos mission close "$MID" --status closed
```

**Verify:** `create` returns an id; `close` is REFUSED — "cannot close: no
result artifact for round 1". A close that succeeds with no result is a FAIL
(the result gate is broken).

### 4.3 Abandon result-gate (negative)

```bash
cd "$D"
printf 'mission: %s\nround: 1\nauthor: foundation-implementer\nverdict: pass\nconfidence: 1.0\nevidence:\n  - name: "wrote out/x.txt"\n    status: pass\n' "$MID" > /tmp/r-ok.yaml
ethos mission result "$MID" --file /tmp/r-ok.yaml
ethos mission abandon "$MID" --reason "should fail"
```

**Verify:** the result submission is accepted (`verdict=pass`); the `abandon`
is then REFUSED — "cannot be abandoned: result artifact(s) exist … run `ethos
mission close` instead". An abandon that succeeds after a result is a FAIL.
Then close it for real: `ethos mission close "$MID" --status closed` (now
permitted — a result exists).

### 4.4 Git hooks fire on commit

```bash
cd "$D" && echo world > hooktest.txt && git add hooktest.txt
git commit -q -m "test: smoke commit with hooks"; echo "exit=$?"
find .punt-labs/local/ethos/trace -name 'log-*.jsonl' | head
```

**Verify:** the commit succeeds (exit 0) with the ethos `pre-commit` and
`commit-msg` hooks active, and the pre-commit seal hook has written a sealed
log under `.punt-labs/local/ethos/trace/`. A commit that fails, or hooks that
produce no sealed trace, is a finding. (No Mission/Delegation trailer is
expected here — trailers append only with a live delegation bound to the
session; see 4.5.)

### 4.5 SESSION-ONLY — trailer + SubagentStop distillation

These need a Claude Code session with real subagent spawns and cannot be
driven from a shell (the same constraint as quarry's Phase 5). From a session
in a repo with a vendored identity, spawn a registered worker under an active
mission and confirm: the `commit-msg` hook appends the Mission/Delegation
trailer to a commit made during that delegation, and SubagentStop records the
subagent's work. Mark SKIP when running the shell-only pass.

## Phase 5: Scratch-repo teardown

```bash
rm -rf "$HOME/.ethos-smoketest" /tmp/ethos-setup.yaml /tmp/m-same.yaml /tmp/m-ok.yaml /tmp/r-ok.yaml
ls -d "$HOME/.ethos-smoketest" 2>&1   # must report "No such file or directory"
```

**Verify:** the scratch repo and temp files are gone. Confirm no global-store
pollution: `ls ~/.punt-labs/ethos/roles/ ~/.punt-labs/ethos/identities/` show
only the real roster, no `smoke*`/`foundation-smoke*` leftovers (setup's
identities live in the scratch repo's `.punt-labs/`, which the `rm` removed).

## Phase 6: Install Verification

Only run after a fresh install or re-install.

### 6.1 Binary on PATH and version

```bash
command -v ethos
ethos version
```

**Verify:** Resolves to `~/.local/bin/ethos` (or the documented install
path) and prints the released version. The hook binary doctor check should
resolve to the same path.

### 6.2 Plugin installed

```bash
claude plugin list 2>/dev/null | grep -i ethos
```

**Verify:** `ethos@punt-labs` is installed at user scope (or `ethos-dev`
in a dev checkout). After install, Claude Code must be restarted (twice per
the installer) for the MCP server to connect.

### 6.3 MCP server connects

In a restarted Claude Code session, confirm the `self` MCP tools
(`identity`, `mission`, …) are listed and `doctor` (1.1) answers. A server
that fails to connect means the plugin or binary path is wrong.

### 6.4 Git hooks refreshed

```bash
ethos doctor | grep -iE 'seal hook currency|trailer hook currency'
```

**Verify:** Both report the hook section matches this ethos build's sha —
a mismatch means the installed binary and the deposited hooks diverged.

## Quick Pass Criteria

- Phase 1 (read liveness): every MCP tool call succeeds; identity/role/team/
  attribute reads cross-reference consistently (a slug shown in 1.4/1.8
  resolves); no mission is created against this source tree
- Phase 2 (read liveness): every CLI check succeeds; `ethos version` matches
  the release; `find`/`adr`/`provenance`/`vendor` dry-run all exit 0
- Phase 3 (scratch repo): setup writes a config + activates a bundle; enable
  deposits guide + marker + import + hooks; disable removes import + marker +
  hooks and leaves the subtree dormant; enable is idempotent
- **Phase 4 (the core)**: the distinct-evaluator rule, the close result-gate,
  and the abandon result-gate all **refuse** the bad case; a real commit fires
  the git hooks and writes a sealed trace. A green Phase 4 is what makes this a
  smoke test rather than a liveness ping
- Phase 5: scratch repo and temp files removed; no global-store pollution
- Phase 6 (install): binary resolves to the install path, plugin installed,
  MCP server connects after restart, hook currency matches the build

## Quick Fail Indicators

- `doctor` shows "orphaned agent files" FAIL — a generated `.claude/agents/`
  file exists for an identity the active team does not list (see PL-1082)
- `doctor` shows "agent file currency" FAIL — committed `.claude/agents/`
  bytes do not match what the installed binary generates; regenerate and
  commit, or the tree churns every session
- `doctor` shows "repo-only completeness" FAIL — an identity references a
  slug (personality/writing-style/talent) that does not resolve in the
  vendored set; agent generation then skips that member by name
- `identity show` names a slug that `personality`/`talent`/`writing_style`
  show cannot resolve — a dangling attribute reference
- `mission create` with worker==evaluator returns an id instead of being
  rejected (4.1) — the distinct-evaluator rule is off; the verifier could
  review its own work
- `mission close` succeeds with no result submitted (4.2) — the result gate is
  broken (close must refuse until a result for the current round exists)
- `mission abandon` succeeds after a result exists (4.3) — the abandon gate is
  broken (a produced result must force close, not abandon)
- a commit in the enabled scratch repo fails, or leaves no sealed trace under
  `.punt-labs/local/ethos/trace/` (4.4) — the git hooks are not firing
- `enable` leaves no `enabled` marker or no import line — enablement is
  partial; sessions will not load the guide
- `seal hook currency` / `trailer hook currency` mismatch — the installed
  binary and the deposited git hooks diverged; a running MCP server started
  before the install is serving the old binary (reconnect it)
- MCP tools absent after install — Claude Code not restarted, or the plugin
  points at the wrong binary path

## Report Format

After running the smoke test, produce a report in this format. Include it in
the release recap email.

```text
Ethos Smoke Test Report
=======================
Version:  <version>
Host:     <hostname>
Date:     <YYYY-MM-DD HH:MM>
Repo:     <repo and active team>
Mode:     <plugin / --no-plugin>

Phase 1: MCP Tools (read liveness)
  1.1  doctor                    PASS / FAIL  <notes if fail>
  1.2  identity whoami           PASS / FAIL
  1.3  identity list             PASS / FAIL
  1.4  identity show             PASS / FAIL
  1.5  role show                 PASS / FAIL
  1.6  team show (active)        PASS / FAIL
  1.7  team list                 PASS / FAIL
  1.8  attribute show            PASS / FAIL
  1.9  session roster            PASS / FAIL
  1.10 ext list                  PASS / FAIL
  1.11 adr list                  PASS / FAIL
  1.12 mission list (liveness)   PASS / FAIL
  1.15 vendor (dry run)          PASS / FAIL

Phase 2: CLI (read liveness)
  2.1  doctor                    PASS / FAIL  <failed checks>
  2.2  version                   PASS / FAIL  <version string>
  2.3  whoami                    PASS / FAIL
  2.4  identity list             PASS / FAIL
  2.5  identity show             PASS / FAIL
  2.6  role show                 PASS / FAIL
  2.7  team show/list            PASS / FAIL
  2.8  attribute show            PASS / FAIL
  2.9  session                   PASS / FAIL
  2.10 ext list                  PASS / FAIL
  2.11 find (state query)        PASS / FAIL
  2.12 mission list (liveness)   PASS / FAIL
  2.13 mission lint              PASS / FAIL
  2.14 adr list                  PASS / FAIL
  2.15 provenance message        PASS / FAIL
  2.16 vendor (dry run)          PASS / FAIL

Phase 3: Scratch repo — setup/enable/disable
  3.1  create scratch repo       PASS / FAIL
  3.2  setup (config + bundle)   PASS / FAIL
  3.3  enable                    PASS / FAIL
  3.4  doctor (enabled repo)     PASS / FAIL
  3.5  enable idempotent         PASS / FAIL
  3.6  disable                   PASS / FAIL

Phase 4: Enforcement and hooks (the core)
  4.1  distinct-evaluator reject PASS / FAIL  ← negative
  4.2  close result-gate reject  PASS / FAIL  ← negative
  4.3  abandon result-gate reject PASS / FAIL ← negative
  4.4  git hooks fire on commit  PASS / FAIL
  4.5  trailer + SubagentStop    PASS / FAIL / SKIP  ← SESSION-ONLY

Phase 5: Teardown
  5.1  scratch + temp removed    PASS / FAIL
  5.2  no global-store pollution PASS / FAIL

Phase 6: Install
  6.1  binary + version          PASS / FAIL  <path>
  6.2  plugin installed          PASS / FAIL
  6.3  MCP server connects       PASS / FAIL / SKIP
  6.4  hook currency             PASS / FAIL

Result: PASS / FAIL
  Passed: <count>
  Failed: <count>
  Skipped: <count>
  Notes: <observations, warnings, or follow-up issues created>
```

SKIP is valid for: 4.5 (SESSION-ONLY — needs a Claude Code session with real
subagent spawns), 6.3 (no Claude Code session to confirm MCP from a
terminal-only run), and any MCP-phase step when run without the plugin
(`--no-plugin` install — use the CLI phase instead). Everything else must be
PASS or FAIL with explanation.
