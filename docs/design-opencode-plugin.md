# OpenCode V2 plugin / adapter design

Implements the harness-neutral model in `docs/spec-opencode-integration.tex`
(`Registry`/`SESSION`/`DELEGATION`, PR #534) for real OpenCode V2. Design
only — no code in this PR. Bead `ethos-5y4r`.

Every claim about OpenCode's own behavior below cites the file this mission's
reference material actually contains, under `.tmp/` in this repo at mission
time (`effect.mdx`, `effect-plugin.ts`, `plugins-index.mdx`, `plugins-web.mdx`,
`migrate-v1.mdx`, `session-event.ts`, `session-status-event.ts`,
`event-manifest.ts`, `schema-session.ts`, `session-effect.ts`,
`session-promise.ts`, `schema-AGENTS.md`), plus the companion research
artifact `docs/prfaq-opencode-integration.md` the spec itself was built from.
Nothing below is asserted from training-data memory of OpenCode; where the
reference material did not answer a question, this document says so and
states what a future implementation round must confirm, rather than guessing.

**Round 2 corrections (mission `m-2026-10-18-008`, PR #536):** three
independent automated reviewers converged on five defects in round 1; the
leader personally verified the first three against real source before
routing this round. Fixed here: (1) §2.1 now registers an OpenCode
session's roster before any `mission claim` attempt, including
plugin-restart reconciliation (§2.1.1); (2) §1.2, §3.2, §3.3, and DESIGN.md
now correctly attribute `ETHOS_SESSION` propagation to `ctx.shell.hook
("create.before", ...)`, not `ctx.tool.hook("execute.before", ...)`, and
disclose the resulting sessionID-correlation gap rather than asserting it
away; (3) §2.3.2 adds a `Roster.ParentSession` field so `claim-forked`'s
ancestor-set guard verifies a caller-supplied witness against Go-stored
lineage instead of trusting it outright, and §5 specifies that
`claim-forked` also writes the matching `DelegationBinding` with the claim
(finding 5; round 2 called this "atomic", which round 5 corrected: the
writes are ordered and retryable, not atomic, §5 point 2). §6.1 adds a confirmed (not speculative) collision
with `punt-labs/points`'s `make doctor` gate (round 3 narrows its scope —
see below). Nothing else in §§1-4 or §7 was re-litigated; those sections were
independently verified accurate by the leader before this round was
dispatched.

**Round 4 corrections (mission `m-2026-10-21-003`, PR #536):** the operator
read `internal/enable/enable.go` directly and found that `ethos enable`'s
deposit step is Claude-Code-specific, so a `--opencode` flag on it would
make OpenCode an opt-in qualifier on a Claude Code default. Fixed: (1) §5
point 5 now specifies coequal `ethos enable claude` / `ethos enable
opencode` subcommands with no privileged bare form; (2) §2.2-§2.3.2 and §5
state that `claim-forked` and `Roster.ParentSession` are harness-neutral
(new §2.5), and §5 point 3 adds Claude Code's own fork wiring (`SessionStart` with `source:
"fork"`) to the same implementation stage, per `docs/spec-opencode-integration.tex`
§Harness Instantiation, Case 2; (3) wording elsewhere no longer frames
either harness as the default.

**Round 3 corrections (mission `m-2026-10-18-020`, PR #536):** the leader
personally verified two further items against real sources this round —
context7 against `/anomalyco/opencode`'s dev branch, a direct `WebFetch`
of a real, open GitHub PR, and a direct read of `punt-labs/points`'s own
repo. Fixed: (1) §3.2-§3.3's sessionID-correlation gap, framed in round 2
as an open-ended design risk, is now stated as a narrow, time-bound
window: real today against the current OpenCode V2 dev-branch API, and
closing specifically once `github.com/anomalyco/opencode/pull/54842`
(OPEN/unmerged, opened 2026-10-12) merges and adds `source.sessionID` to
`ShellCreateBefore`; (2) §6.1's `punt-labs/points` conflict, framed as a
general incompatibility between `points`-routed repos and
ethos-OpenCode-integrated repos, is now stated as a same-repo-only
collision — confirmed by reading `points`'s own source, `points` never
writes a `"plugins"` key in any repo it configures, so the collision only
occurs when `ethos enable opencode` runs inside `points`'s own
checkout. §§1-2, §4, §5 (apart from its §6.1 cross-reference), and §7
were not re-litigated this round.

**Round 5 corrections (mission `m-2026-10-22-017`, PR #536):** eleven open
review findings were each checked against real source, and all eleven
held. Fixed: (1) §1.1-§1.2 now use the Effect plugin shape throughout
(Effect-returning hook registration, `Stream` event consumption, scoped
cleanup), not Promise idioms; (2) §2.3.1 drops the ten-hop cap, which the
spec's full ancestor-set guard does not have, for cycle detection; (3)
§3.1 and the new §3.2.1 handle `commit-msg.sh:55` skipping the lookup when
`MISSION_ID` or `DELEGATION_ID` is inherited, with a two-armed environment
policy that also clears inherited `ETHOS_SESSION` and Claude signals; (4)
§4's table now separates a non-empty `ETHOS_SESSION` naming a missing
session (which `resolve.SessionID` accepts) from a missing propagation;
(5)-(7) §2.1.1 removes the `ctx.session.list` dependency (absent in
`v2.0.32`) and the snapshot-before-subscribe window by registering
sessions lazily, and replaces the replace-semantics `session create` with
a create-if-absent `session ensure` that keeps lineage write-once; (8),
(11) the inherited-environment findings, covered by (3); (9) §5 point 2
replaces the claim that `WithDispatchPendingLock` makes the three sidecar
writes atomic with a publication order and retry rule; (10) §2.3 has both
fork triggers register missing rosters and fork links before `claim-forked`,
and §2.2 reads the persisted `fork.sessionID`, not `parentID`. Separately,
§5 point 5 applies the operator's 2026-10-22 enablement ruling: bare `ethos
enable` prompts on a terminal with both harnesses preselected, explicit
forms skip the prompt, and no terminal with no harness named is a usage
error naming both. This supersedes round 4's usage error for bare `ethos
enable`. DES-078 also carries the second-pass items: `Roster.LastSeen` and
purge, the `session.deleted` handler (§2.6), the `singleSession` option,
`claim-forked` locking and exit statuses, and the enable marker reorder.

**Round 6 corrections (mission `m-2026-10-22-026`, PR #536):** local review
of round 5 found defects, and the leader ruled on each. Fixed: (1) §2.1.1,
§2.3.2, and §5 point 1 move the recorded parent and the liveness time out
of the `Roster` struct into two per-session sidecar files, so a roster
re-marshal, including one by an older `ethos` binary, cannot erase them;
`ethos session create` refuses an id whose sidecar records a parent, and a
Claude Code fork `SessionStart` calls `Ensure` (§2.5); (2) `isStale` reads
`last-seen` first and decides by it alone when it parses, with the required
tests named, and the 24-hour idle purge is documented as intended; (3) the
parent-exists check applies at first registration only, and `--parent-gone`
records a dangling parent after a conclusive "not found" (§2.1.1, reconciled
with §5 point 1); (4) `claim-forked` requires a claim-origin witness binding,
keeps a `claim-forked-pending` record so no retry leaves an orphan skeleton,
and states that `Ensure` and `claim-forked` never nest locks (§5 point 2);
(5) §5 point 2 item 4 is a complete cause-to-status table with four
statuses and a plugin action for each; (6) the deposited guide is
harness-neutral, and `enable opencode` wires it in through the
`opencode.json` `instructions` array, idempotently (§5 point 5); (7) the harness list is generated, sorted,
and enforced by a named test (§5 point 5); (8) DES-078 decision points 2 and
4 are corrected; (9) the `AcquireMissionLock` citation is
`pretooluse_dispatch.go:791-808`, and the nearby TOCTOU, `NewID`, binding,
and `session create` citations were re-checked against source and adjusted.

**Round 7 corrections (mission `m-2026-10-22-030`, PR #536):** the evaluator
found one blocking defect and six wording defects in round 6. Fixed: (1)
`Ensure` writes `last-seen` only when the caller passes `touch` (CLI
`--touch`); the OpenCode plugin passes it on every `ensure`, Claude Code's
fork `SessionStart` never does, so a Claude Code roster keeps today's PID
staleness logic, with a new `TestIsStale` row for a Claude fork roster
(§2.1.1, §5 point 1); (2) the `claim-forked` reconcile close takes the
record's shared mission lock and the skeleton's delegation lock, and the
order is stated (§5 point 2); (3) "child roster unreadable" is refused by the
read-phase `ss.Load`, not `RefuseIfSessionGone`; (4) the outcome of an
operator claim after an interrupted `claim-forked` that named the same
delegation is stated (superseded in round 9 by the live-record rule, §5 point
2 item 5); (5) the write-once hard-link temp file has a
recognizable name that `deleteFiles` removes, and the NTFS requirement is
stated; (6) every Claude-only part of the shared guide is enumerated, and
`validate-content` rejects `.claude/` and `Claude Code` in it (widened in
rounds 8 and 9); (7) the
`session create` citation is `session.go:79`.

**Round 8 corrections (missions `m-2026-10-22-031` and `m-2026-10-22-042`,
the model; `m-2026-10-22-046`, this reconciliation; PR #536):** the
lifecycle operations of §2.1.1, §2.6, and §5 points 1 and 2 were modelled as
a state machine in `docs/spec-opencode-lifecycle.tex` and model-checked with
ProB, and the evaluator (jra) accepted it. The model found nine
counterexamples (C1 to C9) in the prose of rounds 6 and 7. **With all nine
fix switches `ztrue` the spec's operations are the authority for those
sections, and where this document and the spec disagree the spec wins.**
Fixed: (1) a `claim-forked` read-phase refusal reconciles first (C1,
`fixRefuseReconciles`; §5 point 2); (2) `deleteFiles` removes the roster
before the `parent-session` sidecar (C2, `fixDeleteOrder`), closes the
skeletons its session names first and stops if that fails (C5,
`fixDeleteCloses`; spec N2) (§2.1.1, §2.6) (round 9 narrows this to an open
fork skeleton the session owns and its record names, C10); (3) `ensure` refuses an edge
that closes a cycle (C3, `fixCycleCheck`), and the visited set of the
ancestor walk stays mandatory (spec N4) (§2.1.1, §2.3.1); (4) an
already-claimed child closes its skeleton unless the binding is live (C4,
`fixInertBinding`), and a reconcile that does not resume the record also
closes the skeleton of the child's current binding (C9, `fixSupersede`)
(§5 point 2 item 5) (round 9: that wording is withdrawn; the clause applies
on the fresh and resume paths only); (5) the mission is re-read after the skeleton is on
disk, before the binding (C6, `fixPostCheck`) (§5 point 2 item 1); (6) a
roster with a live numeric participant is never purged, which **reverses
row 2 of the `TestIsStale` table**, and an unreadable roster with a fresh
`last-seen` is not purged (C7, C8; `fixStaleLive`, `fixCorruptPurge`)
(§2.1.1); (7) every close writes verdict `aborted` (spec N5). The limits the
spec records and this design relies on are stated where they bite: N3
(`Store.Close`'s sweep is best-effort, §5 point 2 item 5), N4 (§2.1.1 rule
3), N7 (the child's claim is two files, §5 point 2 item 1), and N8 (a live
session with an unreadable roster and no `last-seen` is still purged,
§2.1.1). Leader-run review findings, not covered by the model: (a) the
`claim-forked` status prose says success plus three non-success statuses;
(b) every spelled-out plugin `ensure` command carries `--touch`; (c) the
`Store.Close` and `DisclaimDelegation` citations are
`internal/mission/store.go:1107,1217-1246` and `:1589`; (d) a Claude Code
fork `SessionStart` whose parent has no roster retries `Ensure` with
`parentGone` (§2.5); (e) a `last-seen` read error is not stale with a
warning, a future-dated one is capped at now, and the older-binary purge
exposure is stated (`cmd/ethos/session.go:881`) (§2.1.1); (f) the read phase
reads the child's claim lock-free first so an already-claimed child returns
success, a corrupt pending record or `record.yaml` is "do not retry", a Go
panic (status 2) and a signal death are in the status table, and
`cmd/ethos/main.go:51-71` must map the new statuses with `errors.As` (§5
point 2 items 2 and 4; round 9 adds that it is consulted before the prefix
test); (g) the compiler enforces the lock non-nesting only
if the write phase lives in `internal/mission` and `RefuseIfSessionGone` is
re-expressed over the reader interface (§5 point 2); (h) a reconcile close
whose mission directory is gone takes no lock and treats the skeleton as
gone (round 9: only from a recorded, resolvable store root). The shared guide's Claude-only parts now include lines 11 and 43-44,
`validate-content` rejects `Claude`, `SessionStart`, `CLAUDE_`, `subagent`,
and `.claude/` (allowing the whole word `CLAUDE.md`; round 9 derives the list
from the harness list), `TestHarnessNamesOnlyInList`
also fails on a harness-specific import or identifier outside
`harnesses.go` and matches allowed literals by exact equality (round 9
allows only `.punt-labs/ethos/CLAUDE.md`), and
`opencode.json` ownership and spelling normalization are specified (§5 point
5).

**Round 9 corrections (missions `m-2026-10-22-048`, the model's second pass,
and `m-2026-10-23-005`, this reconciliation; PR #536):**
`docs/spec-opencode-lifecycle.tex` was extended after a review of the design
against the Claude Code paths the first model omitted. It now has fifteen
counterexamples (C1 to C15), fifteen fix switches, and, on the leader's
ruling, a durable pending record; the evaluator (jra) accepted it after
`m-2026-10-22-048`. **With all fifteen switches `ztrue` the spec's operations
are the authority, and where this document and the spec disagree the spec
wins** (round 8 said nine). Each amendment below cites the spec's ledger.
Fixed: (1) the **origin discriminator**: a skeleton is a fork skeleton
(`BoundVia = claim_forked`, a new value) or a Tier B skeleton, and
`deleteFiles`, the purge, every reconcile close, `mission release`, and the
close-time clearing close only an open fork skeleton the session owns; a
binding that names a Tier B skeleton is cleared and the skeleton is left open
(C10; §2.1.1, §5 point 2 item 5); (2) the **`claim-forked-pending` record is
durable** (C12, the leader's ruling): it carries a stage (complete or not)
and the mission store root, persists for the life of the fork claim, and is
removed only by `mission release`, the close-time clearing of the claim
(`ClearMissionBindings`), or `deleteFiles`, each of which first closes the
fork skeleton it names; a complete record is never resumable, and the
"record does not parse" status now applies for the claim's lifetime (§5
point 2 items 4 and 5); (3) `mission release` and the close-time clearing
**close what they unname** (C11, C12), and must hold the child's
dispatch-pending lock around the whole close, unname, clear sequence (spec
N16); (4) **`SessionStart` for every source** goes through `Ensure` without
`touch` (C13; §2.1.1, §2.5, §5 point 3); (5) **every skeleton close is a
compare-and-set** on verdict `open` under the delegation lock, the
`Store.Close` sweep included (C14; §5 point 2 item 5); (6) the **resume path
takes the step-5 locks** and re-reads the mission before it writes (C15; §5
point 2 write phase); (7) **`fixSupersede` applies only on the fresh and resume
paths**, and the resume clause is defensive and unexercised (C9; §5 point 2
item 5). Review findings not covered by the model: (a) **B2**: the record
stores the mission store root (`resolve.StoreRepoRoot()` at write time) and
`deleteFiles` step 1, `release`, and every reconcile close use it;
"directory gone" is concluded only from a recorded, resolvable root (§2.1.1);
(b) `ensure --touch` does not refresh a roster that fails to load (spec
`LcEnsureRefresh`; §2.1.1); (c) the exit-status table gains rows for I/O
errors on the child's and the witness's binding reads, a `ParentOf` error, a
`ReadDelegationBinding` error in a reconcile, a record-removal failure, and
an abort whose cleanup close fails, a mission-closed witness is "witness
refused", and a column states the Claude Code `SessionStart` action; the
`errors.As` mapping is consulted before the prefix test at
`cmd/ethos/main.go:85-100` (§5 point 2 item 4); (d) the guide deny list is
derived from the harness list and covers OpenCode terms and any `@`, the
persona-injection sentence (guide lines 19-20) is named, and the harness-name
test scans all of `cmd/ethos`, `internal/doctor`, and `cmd/validate-content`
with one allowlisted literal (§5 point 5); (e) the last-seen read-error branch
is "not stale, warn" on every path, `Store.Purge` has no production caller
and `PurgeTombstoned` refuses an unreadable roster without `--force`
(`store.go:469-503`), and the claim that `ethos serve` ages rosters out is
deleted (§2.1.1); (f) "a child whose roster is gone cannot have a record" is
false and is corrected (§5 point 2 item 2); (g) crash-leftover temp files
(`last-seen.*.tmp`, `claim-forked-pending.*.tmp`, `active.go:289`) are
removed by `deleteFiles`; (h) a refusal never resumes the record, and the
purge paragraph of §2.1.1 points to the round 8 and 9 bullets that state the
rules in force. §2.7 states which invariants the model proves exhaustively
and which only to a bound, and the limits N3 to N18 the design relies on.
**Round 9 supersedes these earlier statements**, each also marked where it
appears: round 7's "an operator claim of the same delegation after an
interrupted `claim-forked` leaves the skeleton and binding in place" (now the
live-record rule, §5 point 2 item 5); round 8's "a reconcile that does not
resume the record also closes the skeleton of the child's current binding"
(now exactly the fresh and resume paths); round 8's "closes the skeletons its
session names" (now only an open fork skeleton the session owns and its record
names); round 8's "`SessionStart` for any other source keeps today's
`CreateInCheckout`" (now `Ensure` for every source); round 8's "removed as the
last step of a successful write" and "the record is then overwritten or
removed" (the record is durable); round 8's reconcile close "treats the
skeleton as gone" whenever the mission directory is missing (now only from a
recorded, resolvable root); round 8's allowlist of the base name `CLAUDE.md`
for the harness-name test; and the round 8 deny list of five hand-written terms.

## 1. Plugin shape and lifecycle hooks

### 1.1 Entry point

A `Plugin.define({ id, effect: (ctx) => Effect.Effect<void, never,
Scope.Scope> })` module (`.tmp/effect-plugin.ts:57-64`;
`.tmp/effect.mdx:5-30`). This design uses the Effect-native form
throughout, and the two OpenCode plugin styles are not interchangeable.
In the Effect form (`.tmp/effect.mdx:5-6`) "context operations return
Effects or Streams, callbacks return Effects, and plugin lifetime is
represented by `Scope`." Concretely, in this design:

- `setup` is an `Effect.gen` program; each registration is a `yield*` on an
  Effect that must actually run. A hook registration has the type
  `(name, callback: (input) => Effect<void, Failures>) =>
  Effect<Registration, never, Scope.Scope>` (`.tmp/effect.mdx:1505-1515`),
  so calling `ctx.tool.hook(...)` without yielding it registers nothing.
- `ctx.event.subscribe()` returns a `Stream`, not an `AsyncIterable`
  (`.tmp/effect.mdx:1192-1234`). The plugin consumes it with
  `Stream.filter` / `Stream.runForEach` and forks that consumer with
  `Effect.forkScoped`, so the plugin scope interrupts it on unload
  (`.tmp/effect.mdx:1194-1195`).
- `ctx.session.get` and every other session call return Effects
  (`.tmp/effect.mdx:798`, `yield* session.get({ sessionID })`); the plugin
  yields them inside the hook callbacks.
- Every call to the `ethos` binary is wrapped in an Effect (the
  implementation stage picks the spawn primitive). Each wrapped call
  recovers every failure (`Effect.catchAll`), logs it, and completes
  normally. The `execute.before` failure channel is `Tool.Error`
  (`.tmp/effect.mdx:1492-1495`), and a failed callback blocks the tool
  call (`.tmp/effect.mdx:1461-1463`), so an `ethos` failure must never
  surface there: attribution is best-effort and never gates the agent's
  work.

The Promise-based `Plugin.define({ id, setup(ctx) })` form
(`.tmp/migrate-v1.mdx:62-71`) is not used. Earlier rounds described both
forms as differing only in idiom, with `for await` consumption and a
returned cleanup closure. That was wrong, and the sections below no longer
use those idioms.

Loaded from `.opencode/plugins/ethos.ts` (project-level) for a per-repo
install, mirroring `vox`/`z-spec`'s existing per-repo enablement pattern
(`.punt-labs/vox/CLAUDE.md`, `.punt-labs/z-spec/CLAUDE.md`) — gated on the
same repo-level `.punt-labs/ethos/enabled` marker that either harness's
enable command writes (§5, point 5), not a new marker. The plugin file
itself is deposited only when opencode is selected for enablement
(`ethos enable opencode`, or chosen at the `ethos enable` prompt, §5 point 5).
`.tmp/migrate-v1.mdx:38` confirms V2 discovers local plugins from
`.opencode/plugins/`.

The `effect` function returns no cleanup closure. Each registration and the
forked event consumer live in the plugin `Scope`, which disposes them when
the plugin unloads (`.tmp/effect.mdx:1238-1239` "Registrations remain
active for the plugin scope"; `.tmp/effect-plugin.ts:57-60`'s
`Effect.Effect<void, never, Scope.Scope>`). Cleanup is scope finalization,
not code the plugin writes.

### 1.2 Hooks actually registered

Three registered hooks, no more: the event subscription, `ctx.tool.hook`,
and `ctx.shell.hook`. (Round 2 said "four" and listed a fourth item that
registers nothing; round 5 corrects the count. Item 4 below records a
deliberate non-registration.) Round 1's count conflated two distinct hook
domains (see the correction below):

1. **`ctx.event.subscribe()`**, a `Stream` of `OpenCodeEvent` consumed under
   `Effect.forkScoped` (`.tmp/effect.mdx:1192-1234`, §1.1). This is the
   primary trigger for `JoinRootSession` and `ForkSession` (spec
   §5.1-5.2). The subscription delivers the server's whole public event
   stream; the plugin applies `Stream.filter` for `session.created`,
   `session.forked`, and `session.deleted` by `item.type`
   (`.tmp/effect.mdx:1211-1221`). `session.deleted` is the OpenCode
   session-end event (`.tmp/session-event.ts:177-185`, `type:
   "session.deleted"`, payload `Base`, i.e. the `sessionID`). Its handler is
   the OpenCode `EndSession` (round 5, §2.6): it runs `ethos session
   delete --session <id>` (`cmd/ethos/session.go`'s `sessionDeleteCmd`,
   wrapping `Store.Delete`, `internal/session/store.go:200`) and drops the
   plugin's in-process memo entry for that id. This mirrors Claude Code's
   `SessionEnd`, so neither harness leaks rosters or sidecars.

2. **`ctx.tool.hook("execute.before", callback)`**
   (`.tmp/plugins-index.mdx:1721-1729`, `.tmp/effect.mdx:1453-1467`). This is
   the *second*, trigger-independent path for `ForkSession` /
   `AllocateDelegationForkPropagated` (§2.3). Every tool call carries the
   firing session's own `sessionID` as a required field — confirmed via the
   companion research's direct read of `packages/plugin/src/effect/tool.ts`
   (`docs/prfaq-opencode-integration.md` line 255-256: "the `execute.before`
   event carries `sessionID` as a required field"); this mission's own
   `.tmp/` material does not carry that file, so this one field claim is
   attributed to the companion artifact rather than re-derived here, exactly
   as the spec's own `AllocateDelegationForkPropagated` discussion does for
   the same fact (spec §"Fork event missed during plugin disconnection").
   **Correction (round 2, finding 2):** round 1 additionally claimed this
   hook as "the sole mechanism for §3's `ETHOS_SESSION` propagation into
   spawned subprocesses," citing an `event.env` field on `ToolExecuteBefore`.
   That field does not exist. Direct read of both `.tmp/effect.mdx:1484-1500`
   and `.tmp/plugins-index.mdx:1740-1754`'s `ToolHooks`/`ToolExecuteBefore`
   reference blocks shows `execute.before`'s event carries no `env` field at
   all — only `sessionID`-bearing tool identity and input/output. The `env`
   field belongs to a different hook domain entirely: `ShellHooks`/
   `ShellCreateBefore` (`.tmp/effect.mdx:1436-1451`,
   `.tmp/plugins-index.mdx:1708-1719`), registered separately as
   `ctx.shell.hook("create.before", ...)`. §3.2 below corrects the
   propagation mechanism accordingly.

3. **`ctx.shell.hook("create.before", callback)`**
   (`.tmp/effect.mdx:1419-1451`, `.tmp/plugins-index.mdx:1694-1719`). This is
   the mechanism §3.2 actually relies on for `ETHOS_SESSION` propagation —
   `ShellCreateBefore`'s `env: Record<string, string | undefined>` field
   (confirmed present on both reference copies) is the one place this
   design's reference material confirms a plugin can mutate an outbound
   shell's environment before it runs.

4. No `ctx.session.hook(...)` registration (prompt/context/compaction/etc.,
   `.tmp/plugins-index.mdx:1336-1649`) is needed. Those hooks intercept
   model-call content; nothing in the Registry model reads or writes model
   input/output.

`ctx.storage` (`.tmp/plugins-index.mdx:912-949`) is **not** used for any of
this design's durable state. The Registry's durable state is the existing
`.punt-labs/ethos/` global tree (sessions, active-mission and
delegation-binding sidecars) — the harness-neutral files both Claude Code's
Tier B dispatch and this plugin read and write (`internal/mission/active.go`). Using
`ctx.storage` instead would create a second, OpenCode-private copy of
state the commit-msg hook (a plain git hook, harness-blind, §3) cannot
read, defeating the one cross-harness requirement this whole bead exists
to satisfy. `ctx.storage`'s own documented scope — "durable JSON values
scoped to the plugin" (`.tmp/plugins-index.mdx:914`) — is private per-plugin
storage, not a shared file the commit-msg hook could open directly even if
ethos wanted to; this is a structural mismatch, not a style preference.

### 1.3 Why not `ctx.session.hook`

`.tmp/migrate-v1.mdx:142-163`'s V1->V2 hook table confirms none of
`ctx.session.hook`'s registered names (`"prompt"`, `"context"`,
`"compaction"`, `"generate"`, `"title"`, `"model.request"`, `"http.request"`,
`"http.response"`, the three `"experimental.ws.*"` names, `"retry"`) fire on
session creation or forking — they all intercept an in-flight model request
or a prompt already admitted into an existing session
(`.tmp/plugins-index.mdx:1562-1648`'s `SessionHooks` interface). The spec's
own non-goals section (§"The exact wire shape of an OpenCode plugin
payload") already narrows the event surface to `Plugin.define`,
`ctx.event.subscribe`, and `session.created`; this design adds
`ctx.tool.hook("execute.before", ...)` for fork-miss reconciliation
(§2.3) and `ctx.shell.hook("create.before", ...)` for `ETHOS_SESSION`
propagation (§3.2) on top, the former grounded in the companion FAQ's own
"fix does not need a second code path" argument
(`docs/prfaq-opencode-integration.md` lines 250-289), the latter in
§3.2's own confirmed `ShellCreateBefore.env` field.

## 2. Session ID mapping: OpenCode parent/child -> Ethos Registry

### 2.1 JoinRootSession

On `event.type === "session.created"` with no `parentID` on the payload
(`.tmp/session-event.ts:48-67`'s `Created` schema: `sessionID`, `projectID`,
`location`, `parentID` *optional*), the plugin (the `parentID` test only
selects this path; registration in step 1 resolves lineage itself through
`ensureRegistered`, §2.1.1, so a fork whose `session.created` arrives first
is still registered with its fork source):

1. **Registers the OpenCode session with ethos's own roster first, before
   any claim is attempted.** `ethos session ensure --session <id>
   --root-id <id> --primary-id <id> --touch` (new, §5 point 1; it creates the
   roster only when none exists, and never rewrites an existing one, see
   §2.1.1) writes the session's roster file — built from the same
   `session.Store.Create` / `CreateInCheckout` participant logic
   (`internal/session/store.go:49-94`) that `ethos session create`
   (`cmd/ethos/session.go:684-692`) already uses. It is **not**
   `session create`: `Store.CreateInCheckout` builds a fresh roster with a
   new `Started` time on every call (`store.go:82-93`), so replaying it
   discards any later `iam`, `join`, or extension state. The roster file is
   the same one `RefuseIfSessionGone`
   (`internal/hook/pretooluse_dispatch.go:494`) checks for before letting
   any sidecar write proceed. Without this step, every later `ethos
   mission claim <id>` call against the brand-new OpenCode session fails:
   `RefuseIfSessionGone` calls `ss.Load(sessionID)`
   (`internal/session/store.go`'s `Load`), which errors `fs.ErrNotExist`
   for a session with no roster file, and `runMissionClaim`
   (`cmd/ethos/mission.go:2076-2129`) propagates that error outright — a
   brand-new OpenCode session, with no roster entry of its own, has
   nothing for `Load` to find. The root and primary participant IDs are
   both the OpenCode `sessionID` itself (there is no separate
   human/agent split to preserve the way Claude Code's `userID`/
   `claudePID` split does — `internal/hook/session_start.go`'s
   `createSessionRoster` — because OpenCode's plugin has no equivalent
   two-process boundary to record); `ss.Create`'s own de-duplication
   (`internal/session/store.go:76-82`'s comment on "a solo session where
   the agent is the human") collapses the two rows into one roster entry
   when they match, exactly as it already does for that case.
2. Checks whether any session in this OpenCode instance already holds an
   active mission claim relevant to the new session (the plugin's own
   bookkeeping of "the session I'm meant to attribute this claim to" —
   see §2.4 for how the plugin itself learns which mission to claim).
3. If so, calls `ethos mission claim <id>` — the existing Tier B
   primitive (`cmd/ethos/mission.go:2076-2129`'s `runMissionClaim`) — as a
   subprocess, with `ETHOS_SESSION=<opencode-session-id>` set in that
   subprocess's own environment so `resolve.SessionID` resolves to the
   OpenCode session, not whatever ambient session (if any) the plugin's
   own host process happens to carry. By this point the session's roster
   entry from step 1 already exists, so `RefuseIfSessionGone` passes.

This instantiates `JoinRootSession` (spec §5.1): `rec?.parent = noParent`,
`rec?.root = sid?`. The claim act itself is no new ethos-side mechanism —
`ClaimMission` (spec §5.3) models `ethos mission claim` for every harness;
Claude Code and OpenCode call the identical primitive, the harness-neutral seam the
spec's own `ETHOS_SESSION` finding establishes (spec §"Scope":
"`ETHOS_SESSION` is ALREADY a harness-neutral integration seam, with zero
new ethos-side mechanism needed for OpenCode to use it"). What *is* new
is step 1: the roster-registration call the claim depends on, which
round 1 omitted. Round 2 reused `session create` for it. Round 5 found
that `session create` replaces an existing roster, so step 1 is now the
create-if-absent `session ensure`, which is new Go surface listed in §5
point 1.

#### 2.1.1 Registration is lazy and idempotent: restart, missed events, and the event-loss window

**Corrections (round 5).** Earlier rounds reconciled a restart by
enumerating live sessions with `ctx.session.list(...)` and replaying
`ethos session create` for each. Both halves were wrong.

1. *`ctx.session.list` is not in the targeted release.* The companion
   research records that `SessionDomain` at the released `v2.0.32` tag has
   no `"list"` member: it was added by a `dev`-branch commit merged after
   that release (`docs/prfaq-opencode-integration.md:408-418`). The copy of
   `effect.mdx` under `.tmp/` documents it (`.tmp/effect.mdx:833-837`)
   because it describes `dev`, not the release. This design therefore does
   not call `ctx.session.list`, and needs no minimum-version check for it.
2. *Snapshot-then-subscribe loses events.* Reading a session page before
   subscribing leaves a window: a root created after the page is read and
   before the subscription starts appears in neither source, is never
   registered, and a later root-session claim fails `RefuseIfSessionGone`.
   This design takes no snapshot, so there is no window to close.
3. *`session create` is not idempotent.* `CreateInCheckout` always builds a
   new roster with a new `Started` and only the supplied participants
   (`internal/session/store.go:58-93`, see §2.1 step 1). Replaying it after `iam`,
   a `join`, or an extension update loses that state, and replaying it
   without lineage would reset a fork's recorded parent (§2.3.2).

The replacement is one function in the plugin, `ensureRegistered(sid)`,
and one new Go command, `ethos session ensure` (§5 point 1):

- `ensureRegistered(sid)` runs at every point where the plugin learns of a
  session: the `session.created` and `session.forked` events, every
  `execute.before` firing (the session's own `sessionID` is always present,
  §1.2), and every `create.before` firing that carries
  `source.sessionID` (§3.2). It is safe to call twice for the same
  session, so an event delivered both by the live stream and by a hook is
  harmless. A plugin restart needs no special path: the first tool or
  shell call of any session that predates the restart registers it.
  **Memo (round 5).** The plugin keeps an in-process map from session id
  to the time of its last successful `ethos session ensure`. A hit younger
  than the *refresh interval* (15 minutes, a constant far below the
  24-hour stale TTL) skips the subprocess. A hit at or past the interval
  is treated as a miss: the plugin calls `ensure` again, which re-verifies
  that the roster exists (re-creating it if a purge removed it) and
  refreshes the session's `last-seen` sidecar (below). The plugin passes
  `--touch` on every `ensure` call; `last-seen` is written only when the
  caller asks for it (round 7, below). A failed `ensure` stores
  nothing. The memo is never trusted for a write: `mission claim` and
  `claim-forked` re-check the roster themselves under the session lock
  (`RefuseIfSessionGone`, `cmd/ethos/mission.go:2113`), so a memo that is
  stale because of an outside purge inside the interval can only cause a
  refused claim, never a wrong one. A `session.deleted` event for the id
  drops its memo entry (§2.6). The plugin cannot observe a purge by
  another process, which is why the memo expires instead of being
  invalidated.
- To register `sid`, the plugin first resolves its lineage from OpenCode
  (`ctx.session.get({ sessionID: sid })`, §2.3): a fork has a fork source,
  a root has none. For a fork it calls `ensureRegistered(source)` first,
  then `ethos session ensure --session sid --parent-session source --touch ...`.
  The recursion keeps a visited set and refuses a repeated id (§2.3.1).
  A missed fork event therefore registers the child and every missing
  intermediate ancestor, parent first, before `claim-forked` runs.
  **A failed or inconclusive lineage read aborts registration (round 5).**
  If `ctx.session.get` fails, times out, returns no record, or returns a
  record the plugin cannot classify as root or fork, `ensureRegistered`
  registers nothing for that session, logs the reason, and the caller
  proceeds unattributed. It never defaults to "root": a fork registered as
  a root would have no recorded parent, and lineage is write-once, so
  the error would be permanent and the fork could never inherit a
  delegation. There is also no upgrade path from no recorded parent to a
  set one; `ensure` refuses it as a lineage conflict (next item). The
  repair for a session registered with the wrong lineage is operator
  action: delete its roster (`ethos session delete --session <id>`, which
  also clears its sidecars), after which the next `ensureRegistered`
  registers it again from OpenCode's record. **One read is conclusive
  (round 6):** when a fork's parent was deleted in OpenCode,
  `ctx.session.get(parent)` returns "not found", and that is a conclusive
  answer, not an inconclusive one. The plugin then calls `ethos session
  ensure --session <child> --parent-session <parent> --parent-gone
  --touch`. The
  recorded parent is the child's own persisted `fork.sessionID` or the
  event's `parentID`. The parent is not ensured, and the child's recorded
  parent dangles by design. `--parent-gone` is the only way to record a
  parent that has no roster, and the plugin passes it only for that
  conclusive "not found". Fork-claim propagation then refuses through the
  existing ancestor-Load-failure rule (§2.3.1), which stays fail-closed,
  and an explicit operator `ethos mission claim` on the child still
  works. Transport errors and timeouts stay inconclusive and abort
  registration, and they never produce `--parent-gone`.
- `ethos session ensure` runs under the per-session roster lock
  (`Store.withLock`, as `Create` and `Join` do). Its rules, in order:
  1. *Parent check, first registration only.* The requirement that a
     non-empty `--parent-session` name a session with a roster applies
     only when `ensure` registers the session for the first time (no
     roster exists and no lineage sidecar records a parent). If it fails,
     `ensure` exits non-zero and writes nothing. `--parent-gone` (round 6)
     waives this check for a first registration: it records the dangling
     parent and is accepted only together with a non-empty
     `--parent-session`. On an existing roster, `--parent-gone` has no
     effect.
  2. *Lineage is write-once.* If a lineage sidecar records a parent, or a
     roster exists, and the supplied `--parent-session` differs from the
     recorded one (including one side empty), `ensure` exits non-zero with
     a lineage-conflict error and changes nothing, `last-seen` included,
     whether or not `--touch` was passed.
     A matching parent is accepted whether or not the parent's roster
     still exists: a deleted parent never blocks a refresh.
  3. *Cycle refusal (round 8; lifecycle spec ledger C3,
     `fixCycleCheck`).* When `ensure` is about to write a new edge (no
     recorded parent for the session, a non-empty `--parent-session`), it
     refuses if the session is the supplied parent or appears in the
     supplied parent's recorded-parent chain. The chain is read from the
     `parent-session` sidecars, whether or not the ancestors still have
     rosters, and the read keeps a visited set and stops on a repeated id.
     The refusal exits non-zero with a lineage-cycle error and writes
     nothing. It covers the self-parent (`--parent-gone` with the session as
     its own parent) and the two-session loop (`a -> b` recorded with `b`
     unregistered under `--parent-gone`, then `b -> a`; or `a` deleted and
     re-registered under `b`). This replaces the round 6 statement that a
     cycle can only be written through a `--parent-gone` parent, which the
     model refutes (`docs/spec-opencode-lifecycle.tex`, ledger C3; verified:
     `Acyclic` holds with `fixCycleCheck` and fails without it, runs
     `L_acyc_cycle` and `L_acyc_w`).
     **Limit (spec N4).** `Ensure` holds only the lock of the session it
     writes. Two sessions inserting the two edges of a cycle concurrently
     each read a chain without the other's edge, and both succeed; the
     model's operations are atomic and cannot show this. This design does
     not serialise edge insertion across sessions. It takes the other
     alternative the spec allows: the visited set of the ancestor walk
     (§2.3.1) is mandatory, so a cycle that is written anyway is refused at
     read time and never followed. The cycle refusal is therefore a
     write-time guard against the sequential case, and `Acyclic` as an
     invariant of the stored sidecars holds of a serialised store only.
  4. *Create or refresh.* If no roster exists, `ensure` writes the lineage
     sidecar first (when a parent is supplied), then the roster, then, only
     when `--touch` was passed, `last-seen`. A crash between the steps
     leaves a sidecar with no roster, and a retry with the same parent
     completes the registration; the reverse order would leave a fork
     registered as a root. If a roster exists, `ensure` with `--touch`
     changes only `last-seen` (set to the current time), and `ensure`
     without `--touch` changes nothing: `Started`, participants, and
     extensions survive either way.
     **A roster that fails to load is not refreshed (round 9; lifecycle
     spec `LcEnsureRefresh`, whose guard excludes the `corrupt` set).**
     `ensure --touch`
     loads the existing roster before it writes `last-seen`. If the load
     fails (the file exists and does not parse), `ensure` writes nothing,
     leaves any existing `last-seen` as it was, and exits non-zero with an
     error naming the roster file. The reason is C8: an unreadable roster
     with a fresh `last-seen` is protected from purge, so a refresh would
     let a corrupt roster outlive the purge for as long as the plugin kept
     touching it. A roster that cannot be read is reclaimed by the purge
     once its `last-seen` ages out, or by `ethos session delete`. `ensure`
     without `--touch` does not read the roster and changes nothing, so a
     roster that exists but does not parse is a no-op for it (spec N11).
     Required test: `ensure --touch` against an unparseable roster that
     already has a `last-seen` exits non-zero and leaves that file
     byte-identical; against one with no `last-seen`, it creates none.
  A plugin restart therefore cannot reset lineage, and a session whose
  claim is already written (§2.4) is unaffected: `ensure` never touches the
  active-mission or delegation-binding sidecars.
- **Lineage and liveness live in sidecar files, not in the roster
  (round 6).** Round 5 stored `ParentSession` and `LastSeen` as `Roster`
  fields. `Join`, `JoinSelf`, `Leave`, and `CreateInCheckout` each load or
  build a `Roster` and re-marshal it (`store.go:82-93`, `110-196`), so any
  such call, including one from an older `ethos` binary still serving MCP
  that does not know the new fields, would drop them. Two files under the
  session's existing sidecar directory, `sessions/<id>/` (the directory
  `ActiveMissionPath` and `DelegationBindingPath` already use,
  `active.go:34-38,342-346`), hold them instead:
  - `parent-session`: the recorded parent id. It is created once, by
    writing a temporary file and hard-linking it into place (`os.Link`),
    so a reader never sees a partial file and a second creation fails. It
    is never rewritten. **The temporary file has a recognizable name
    (round 7):** `parent-session.tmp-<random>`, in the same directory as
    the final file (a hard link must stay on one volume). `Ensure` removes
    it after the link succeeds or fails, and `deleteFiles` removes every
    file matching `parent-session.tmp-*` in the session's sidecar
    directory, so a crash between the write and the link leaves a file
    that session deletion cleans up and that no reader mistakes for the
    sidecar. **Platform requirement:** the hard link needs a filesystem
    that supports links. Unix filesystems do; on Windows, where the
    session lock is `LockFileEx` (`store_windows.go`), the state directory
    must be on NTFS, since FAT and exFAT volumes refuse `os.Link`. A
    failed link for any reason other than "file exists" makes `ensure`
    exit non-zero and write nothing further.
  - `last-seen`: an RFC 3339 timestamp, replaced atomically on every
    successful `ensure` that passes `--touch` (`Store.Ensure`'s `touch`
    argument). The OpenCode plugin passes it on every call. Claude Code's
    `SessionStart` never does, so a Claude Code roster never gets a
    `last-seen` sidecar (round 7, below). The replacement goes through
    `writeSidecarFile` (`active.go:279-315`), whose temp file is named
    `last-seen.*.tmp`; a crash between its write and the rename leaves that
    file, and `deleteFiles` removes it (step 5).
  Both are written under the roster lock (inside `Ensure`'s `withLock`).
  Both are removed by `deleteFiles` (`store.go:278-301`), in the order of
  the next item. The directory listing in
  `Store.List` skips directories (`store.go:360-362`), so the sidecars
  never show up as sessions. `Store.ParentOf(id)` and `Store.LastSeen(id)`
  read them without a lock. `Roster` gains no field.
- **`deleteFiles` order (round 8, amended in round 9; lifecycle spec ledger
  C2, C5, C10, C12, `fixDeleteOrder`, `fixDeleteCloses`, `fixOrigin`,
  `fixDurableName`; spec N2).** The one primitive that `Delete`, `Purge`, and
  `PurgeTombstoned` funnel through performs seven steps in this order and
  stops at the first failure (`LcDeleteSession`, `LcPurgeSession`, steps 1 to
  7):
  1. Close, with verdict `aborted`, every open skeleton that the session's
     `claim-forked-pending` record names **and that is a fork skeleton the
     session owns** (the origin discriminator, below). The close is a
     compare-and-set on verdict `open` under the skeleton's delegation lock,
     and it uses the **mission store root recorded in the record**, never the
     working directory (B2, below). §5 point 2 item 5 specifies the close and
     its locks. Without this step the record, which since round 9 names the
     skeleton for the life of the claim, is cleared at step 4 and the
     skeleton stays open and unnamed (C5), which blocks `Abandon` of its
     mission (`countBlockingDelegations`, `internal/mission/store.go:1973`).
     A delegation binding names a skeleton here only to be cleared at step 3:
     if the binding names a Tier B skeleton, the binding is cleared and the
     skeleton is **left open** (C10), because it is a subagent's delegation
     and a session ending does not end it. The model also lets this step
     close a fork skeleton that only the binding names. With the durable
     record no such state is reachable (`Fa_orph_norel_dur` holds without
     the binding-named close), and a binding carries no store root, so the
     implementation closes on the record alone: a binding that no record
     accompanies is treated as a Tier B binding and only cleared.
  2. The active-mission claim and its origin file.
  3. The delegation binding.
  4. The pending record and `claim-forked-pending.*.tmp`, the crash-leftover
     temp files of its write (`writeSidecarFile` names its temp file
     `<name>.*.tmp`, `internal/mission/active.go:289`), with the
     dispatch-pending entries that `clearMissionSidecarsLocked` already
     clears in this position.
  5. `last-seen` and `last-seen.*.tmp`, the crash-leftover temp files of its
     replacement.
  6. The roster file.
  7. The `parent-session` sidecar and every `parent-session.tmp-*` file,
     last.
  The sidecar goes after the roster because the roster is what the design
  treats as authoritative for lineage. Round 7's order removed the sidecar
  before a roster removal that could then fail, leaving a roster whose
  recorded parent had vanished, so that a later `ensure` with a different
  parent, or with none, succeeded: the roster's lineage had changed (C2;
  `LineageStable` holds with `fixDeleteOrder`, run `L_lin_order`, and fails
  without it, `L_lin_w`). A failure at step 7 leaves a sidecar with no
  roster, which is the case **Repair** below already covers. A failure at
  steps 1 to 6 leaves the roster as the retry token for a later purge, as
  today (`store.go:230-245`).
  **Step 1 short-circuits (spec N2).** Every later step assumes step 1
  succeeded, because step 4 clears the only name an open fork skeleton has. A
  failed close therefore ends the call at step 1 with nothing cleared and
  the roster kept. This is unlike `clearMissionSidecarsLocked`
  (`internal/session/store.go:336-348`), which joins its errors and
  continues; an implementation that copied that shape would clear the
  names and reinstate C5. A pending record or a skeleton `record.yaml` that
  does not parse is a failure of step 1 too: the call fails with an error
  that names the file and the manual remedy (remove it), and does not clear
  past it. Nothing in the model represents a corrupt record (spec N3); the
  choice here is the loud one.
  **The origin discriminator (round 9; ledger C10, `fixOrigin`).** The model
  gives a skeleton a kind: `dkFork` for the one `claim-forked` writes and
  `dkTierB` for the one a Tier B dispatch writes
  (`pretooluse_dispatch.go:775-884`). A Tier B dispatch writes the leader's
  own delegation binding, with `ParentSession` equal to the session itself,
  and nothing in the dispatch closes the skeleton (`subagent_stop.go` does
  not, and `CloseDelegationSkeleton` has no caller outside the tests), so it
  stays open until `Store.Close` sweeps it. A step 1 that closed everything
  the binding names would abort a running subagent's delegation when its
  leader's session ended or was purged (run `F1D_off`, four steps). In the
  code the kind is the skeleton's `BoundVia`: `claim-forked` writes a new
  value, `claim_forked` (a new constant beside `BoundViaSidecarClaim`,
  `internal/mission/delegation.go:59-62`), and every existing value is Tier
  B. The skeleton's `ParentSession` is the owning child for a fork skeleton
  (the model's `dOwn`). The binding sidecar gains no field: it carries the
  origin of the skeleton it names. The close routine loads the skeleton's
  `record.yaml` and checks `BoundVia == claim_forked` and `ParentSession ==
  <the session>` before it closes. **Every** close that this design adds
  applies the same test and touches only an open `dkFork` skeleton owned by
  the session: `deleteFiles` step 1, the purge (which reaches it through
  `deleteFiles`), the reconcile closes of §5 point 2 item 5 (refusal,
  claimed case, resume, supersede, abort), `mission release`, and the
  close-time clearing of the claim. Tests: `F1D`, `F1P`, `F1R` as cases
  (the Tier B skeleton survives `deleteFiles`, a purge, and a reconcile, with
  its binding cleared where the operation clears bindings).
  **The mission store root is recorded (round 9; review finding B2).** The
  record stores `resolve.StoreRepoRoot()` as it was when the record was
  written (`internal/resolve/resolve.go:729`; the root under which
  `RepoStatePath(root, "missions", ...)` holds the mission and
  `AcquireMissionLock(root, ...)` takes its lock). `deleteFiles` step 1,
  `mission release`, the close-time clearing, and every reconcile close use
  that recorded root. They never call `StoreRepoRoot()` for a skeleton that a
  record names, because `Delete`, `Purge`, `release`, and `close` run from any
  working directory, and a different working directory resolves a different
  root, or none (`StoreRepoRoot` returns `""` outside a repository). "The
  mission directory is gone" is concluded **only** from a recorded root that
  is non-empty and resolvable, and whose mission directory then does not
  exist; the close then takes no lock and treats the skeleton as gone. A
  record whose root is empty, absent, or not resolvable (the root path
  itself does not exist or cannot be read) is a **step 1 failure**: nothing
  is cleared, the roster is kept as the retry token, and the error names the
  record and the remedy (restore the checkout, or remove the record by hand).
  It is never read as "gone": an unmounted volume or a moved checkout would
  otherwise strand an open skeleton with its only name deleted. A write phase
  that cannot resolve a non-empty root writes no record (§5 point 2 item 4,
  root resolution fails: "do not retry"). Required test: `Delete`, `release`,
  and a reconcile run from a working directory other than the repository's,
  and from one outside any repository, still close the skeleton; a record
  with an empty root, and one whose root does not exist, fail step 1 with the
  roster, claim, binding, and record all still present.
- **Existing creators respect the sidecar.** `ethos session create`
  (`Store.CreateInCheckout` called on behalf of the CLI) against an id
  whose `parent-session` sidecar records a parent refuses and changes
  nothing, rather than building a fresh roster over a recorded lineage.
  **Claude Code `SessionStart` for every source goes through `Store.Ensure`
  (round 9; lifecycle spec ledger C13, `fixSessionStart`,
  `LcSessionStart`).** The hook reports a `source`, and
  `createSessionRoster` (`session_start.go:69,194`) reads none: for every
  source it calls `CreateInCheckout`, which writes a new roster over any
  existing one (`store.go:58-94`). Round 8 replaced that call for the
  `fork` source only and kept it for the others. The model refutes that:
  a fork that is later resumed or compacted gets a `SessionStart` with
  source `resume` or `compact`, `CreateInCheckout` rewrites its roster with
  no lineage over a sidecar that records one, and the roster says root
  while the sidecar says child (run `S_lin_off`, four steps; `S_lin_on`
  holds, 1,025 states, all visited). The hook therefore calls
  `Store.Ensure` in process for **every** source, without `touch`: for a
  session that has a roster it changes nothing, and for one that has not it
  registers it as `ensure` does. The `fork` source supplies the parent from
  the payload; every other source supplies the parent `Store.ParentOf`
  already records (none, if none), which is what the write-once rule of
  `Ensure` requires of a caller that is not creating lineage. The
  replacing create is reached only by the CLI `session create`, which
  refuses an id whose sidecar records a parent (`LcCreateCli`, unchanged).
  A fork never replaces a roster that already has lineage and never gets a
  `last-seen` sidecar. The `CLAUDE_PID` pointer file is written after
  `Ensure` for every source, as `createSessionRoster` does today (§2.5).
  Required tests: a `SessionStart` with source `resume`, and one with source
  `compact`, against a fork that has a roster and a `parent-session`
  sidecar, change neither.
  **Limit (spec N11; not modelled further).** A `SessionStart` for a roster
  that exists but does not parse is a no-op under `Ensure`, where
  `CreateInCheckout` rewrote it. The purge reclaims such a roster, and
  whether the hook should heal it is not decided here.
  **Limit (not modelled).** A resumed session gets a new `claudePID`, and
  `CreateInCheckout` recorded it as the roster's numeric primary
  participant. Under `Ensure` an existing roster is left as it is, so a
  session whose `SessionEnd` never ran (a crash) and is then resumed keeps
  the old, dead primary id, and `isStale` treats the roster as stale by its
  PID branch (`store.go:950-956`) while the new process is alive. The model
  holds the participant kind fixed across `SessionStart` (spec
  `LcSessionStart`, `rkind' = rkind`) and does not examine this. The
  implementation stage must decide whether the hook refreshes the primary
  participant of an existing Claude Code roster, and it is escalated to the
  leader with this design.
- **Repair.** A sidecar left behind with no roster (an older binary's
  `Delete` does not know to remove it, or a failure at step 7 of
  `deleteFiles`) is authoritative: `ensure` and
  `create` treat it as the recorded lineage and refuse a different parent.
  The operator repair is `ethos session delete --session <id>`, whose
  `deleteFiles` clears the sidecars whether or not the roster exists.
  `Repair` is unchanged by round 8 and now also covers the step 7 failure
  (lifecycle spec, consequences list).

**Purge must not reclaim a live OpenCode session (round 5, corrected in
round 6, amended in rounds 8 and 9).** The rules in force are the bullets
that follow this opening (round 8's, with round 9's additions); the opening
records how they were reached, and where it states an earlier round's rule
(the sidecar alone decides, an unreadable roster is purged on any
`last-seen`) the bullets replace it. The registered roster has root and primary equal to the
OpenCode session id, so `CreateInCheckout` collapses them to one
participant (§2.1 step 1). `isStale` (`internal/session/store.go:942-957`)
treats a one-participant roster as a solo session and ages it out by
`Started` alone (`store.go:946-948`, `isOlderThan`, `staleTTL = 24h` at
line 933). An OpenCode session that stays live for more than 24 hours would
therefore be deleted, with its claim and delegation sidecars, by `ethos
session purge` (`Store.Purge`, `store.go:385-404`) or by `PurgeTombstoned`
(`store.go:514`), and every later `mission claim` would be refused. Round 5
also let an `isOlderThan` change decide only the age base. That left the
two-participant branch (`store.go:949-956`) in charge: a roster whose
`Participants[1]` is a dead numeric id would still be stale however fresh
its `last-seen`. Round 6 puts the sidecar first, and round 8 amends the
result where the lifecycle model found two counterexamples (C7, C8,
`docs/spec-opencode-lifecycle.tex`); the bullets below state the result:

- `isStale` becomes a `Store` method that takes the session id, because it
  must read the sidecar. It first reads `last-seen`. **When the file is
  present and parses as RFC 3339, staleness is decided by the age of the
  later of `Started` and `last-seen` against `staleTTL`, with one
  exception: a roster with a live numeric participant is never stale
  (round 8; ledger C7, `fixStaleLive`).** "Live numeric participant" is
  `Participants[1].AgentID` parsing as an integer for a process that is
  alive (`isProcessAlive`), the test today's PID branch applies
  (`store.go:950-956`). The empty-roster and participant-count branches do
  not run. An unparseable `Started` counts as the zero time for that
  maximum, so a parseable `last-seen` still governs.
  **This reverses round 6's table row 2** ("idle past the TTL decides, even
  with a live PID"). The model shows that row and the invariant that no
  live session is purged cannot both stand: a roster with a live numeric
  participant, registered with `--touch` and with a `last-seen` more than
  the TTL old, was purged (run `P_w`; `fixStaleLive` alone leaves the next
  counterexample, `P_stalelive`, and `fixCorruptPurge` alone leaves this
  one). The reversal is a decision of the leader, recorded in the spec's
  ledger, not a theorem of the model. `last-seen` still decides for a
  roster with no live numeric participant, which is every OpenCode roster
  (root and primary are the session id, a non-numeric string), and a dead
  numeric participant with a fresh `last-seen` is still not stale.
- **A `last-seen` that cannot be read or is not trustworthy (round 8).**
  Three cases that the model abstracts into "absent" or "parses" are
  specified here. (1) A read error other than not-found and not a parse
  failure (permission denied, I/O error) is treated as **not stale**, with
  one warning on stderr naming the file: failing to read evidence of
  liveness is not evidence of death, and purging on it would delete a live
  session's claim. **The same rule holds on every branch (round 9):** when
  the roster itself fails to load and its `last-seen` cannot be read, the
  roster is likewise **not purged**, with one warning naming the file; only
  an absent or unparseable `last-seen`, or one older than the TTL, lets the
  purge proceed (next bullet). (2) A `last-seen` later than the current time (a clock
  that stepped backwards, or a hand-written file) is capped at now, so its
  age is zero and it counts as fresh; the roster is kept until the clock
  passes the stored time plus the TTL. A far-future stamp therefore
  protects a roster for as long as it says; only a corrupt or hand-edited
  file can produce one, and the repair is `ethos session delete`. (3) An
  unparseable `last-seen` is the "absent" case below, as before.
- **An unreadable roster is not purged on a fresh `last-seen` (round 8;
  ledger C8, `fixCorruptPurge`).** `Purge` and `PurgeTombstoned` delete a
  roster whose `Load` fails before `isStale` is consulted
  (`store.go:391-397` and the unreadable-roster branch of
  `PurgeTombstoned`). Both now read `last-seen` first; a valid `last-seen`
  within the TTL leaves the unreadable roster alone, and any other case
  (absent, unparseable, older than the TTL) purges it as today, and a
  `last-seen` that cannot be read leaves it alone with a warning (previous
  bullet). **Reachability (round 9).** `Store.Purge` has no production
  caller: only tests call it, and the one production purge is `ethos session
  purge`, which calls `PurgeTombstoned` (`cmd/ethos/session.go:881`).
  `PurgeTombstoned` refuses an unreadable roster unless `--force` is given
  (`store.go:469-503`), so the C8 amendment changes behavior in production
  only under `--force`. The amendment is made in both functions so that the
  two never disagree. **Limit
  (spec N8).** `NoLiveSessionPurged` judges liveness only from what ethos
  can read. A live Claude Code session whose roster fails to parse and has
  no valid `last-seen` is still purged, as it is today; Claude Code rosters
  carry no `last-seen` (callers pass no `touch`), so `fixCorruptPurge`
  protects OpenCode rosters only.
- **The older-binary exposure (round 8).** Both sidecars survive an older
  `ethos` binary's roster re-marshal, but an older binary's `isStale`
  ignores `last-seen`. `ethos session purge` from such a binary
  (`cmd/ethos/session.go:881` calls `PurgeTombstoned`) ages a
  one-participant OpenCode roster out by `Started` alone at 24 hours, with
  its claim. Nothing else ages a roster out: `ethos serve` runs no purge, and
  `PurgeCurrent` (`session_start.go:159`, `session.go:868`) removes PID
  pointer files only. An older binary's `deleteFiles` also does not know the
  `claim-forked-pending` record: it removes the roster and leaves the record
  and its open fork skeleton, which a later `ethos session delete` by a
  current binary closes (the record names it). The
  plugin re-registers the session on its next `ensure` (as a root with no
  claim, or as a fork with its lineage re-read), so the damage is a lost
  claim, not a stale trailer. The design cannot prevent a purge by another
  binary; the operator upgrades every `ethos` binary on the host before
  enabling the OpenCode plugin.
- **When `last-seen` is absent or unparseable, the existing logic runs
  unchanged**, including its handling of an unparseable `Started`
  (`store.go:962-964`, stale). **Round 7 (replacing round 6's claim that
  every Claude Code roster has no `last-seen`):** `Ensure` writes
  `last-seen` only when the caller passes `touch` (CLI `--touch`). The
  OpenCode plugin passes it on every `ensure`. Claude Code's fork
  `SessionStart` never passes it. Round 6 had `Ensure` write `last-seen`
  on every call, and that would have given a Claude Code fork roster, which
  has a live numeric `Participants[1]`, a sidecar that overrides the PID
  branch: after 24 hours with no refresh, the roster would be purged
  while its process was alive. With `touch` off, a Claude Code roster, fork
  or not, has no `last-seen` and behaves exactly as today; its
  numeric-primary liveness branch (`store.go:950-956`) is untouched. Every
  existing roster likewise has no `last-seen`.
- Both call sites pass the id: `Purge` (`store.go:404`, no production
  caller) and `PurgeTombstoned` (`store.go:514`).
- The memo's 15-minute refresh interval is what drives refreshes: any
  OpenCode session that makes a tool or shell call at least every 24 hours
  keeps `last-seen` current.
- **The 24-hour idle purge is intended.** A session idle for more than 24
  hours with no event ages out and is purged, as any abandoned session is,
  whatever its roster looks like. That is correct: its next tool call
  re-registers it via `ensureRegistered` (as a root with no claim, or as a
  fork with its lineage re-read). A claim lost this way must be re-made.
  `session.deleted` (§2.6) handles explicit endings; the TTL handles
  missed ones.

Required tests (implementation stage, `internal/session`), each a case in
one table-driven `TestIsStale` unless noted:

| Roster | `last-seen` | Expected |
|---|---|---|
| two participants, `Participants[1].AgentID` a numeric id with no live process | fresh | **not stale** (the sidecar overrides the PID branch) |
| two participants, `Participants[1]` a numeric id with a live process, `Started` 25 hours ago | 25 hours ago | **not stale** (round 8, ledger C7: a live numeric participant is never purged; this row was "stale" in rounds 6 and 7) |
| two participants, dead numeric `Participants[1]`, `Started` 25 hours ago | 25 hours ago | stale (the later time is past the TTL and no live numeric participant protects it) |
| one participant, `Started` 25 hours ago | fresh | not stale |
| one participant, `Started` 25 hours ago | 25 hours ago | stale |
| one participant, `Started` fresh | future-dated | not stale (capped at now) |
| one participant, `Started` 25 hours ago | present but unreadable (permission denied) | not stale, with a stderr warning |
| one participant, `Started` 25 hours ago | absent | stale |
| one participant, `Started` fresh | 25 hours ago | not stale (the later time governs) |
| zero participants | fresh | not stale (sidecar first; `Ensure` never writes such a roster) |
| zero participants | unparseable | stale (existing empty-roster branch) |
| two participants, dead numeric `Participants[1]` | unparseable | stale (existing PID branch) |
| unparseable `Started`, one participant | fresh | not stale |
| Claude fork roster with live numeric `Participants[1]`, `Started` 25 hours ago | absent | not stale (today's PID branch; `Ensure` without `touch` wrote no sidecar) |

Purge tests (round 8, ledger C8; round 9), against `Purge` and
`PurgeTombstoned` (the latter with `--force`, since without it an unreadable
roster is refused whatever its `last-seen` says): a roster that fails to
parse with a fresh `last-seen` is not purged; the same roster with no
`last-seen`, an unparseable one, or one older than the TTL is purged; the
same roster with a `last-seen` that cannot be read (permission denied) is
not purged and a warning names the file. Without `--force`, `PurgeTombstoned`
refuses the unreadable roster in every case.

Separate tests assert: `Ensure` without `touch` writes no `last-seen` on
create and none on refresh, and with `touch` writes it on both; `Join`, `JoinSelf`, `Leave`, and `CreateInCheckout`
leave both sidecars byte-identical; a hand-written roster re-marshalled
without any new field (the older-binary case) leaves them intact;
`deleteFiles` removes both sidecars and the crash-leftover temp files
(`parent-session.tmp-*`, `last-seen.*.tmp`, `claim-forked-pending.*.tmp`)
and, when it fails, keeps the roster;
`deleteFiles` performs its steps in the order of the `deleteFiles` order
item, and a fault injected at each of the seven steps leaves the state the
model gives for that prefix (round 8, ledger C2: a failure at step 6 leaves
the roster and its `parent-session` sidecar; a failure at step 7 leaves a
sidecar with no roster, and `ensure` for that id with a different parent is
refused); a `deleteFiles` on a session whose pending record names an open
fork skeleton the session owns closes it `aborted` before clearing anything,
and when that close fails the claim, binding, and record are all still
present (ledger C5, N2); a `deleteFiles` on a session whose binding names a
Tier B skeleton clears the binding and leaves the skeleton open (ledger C10);
a `deleteFiles` run from a working directory other than the repository's
still closes the skeleton, and one whose record has an empty or unresolvable
store root fails step 1 and clears nothing (B2);
`ensure` obeys each numbered rule above, including the cycle refusal for a
self-parent, a two-session loop, and the delete-then-re-register loop
(ledger C3), the matching-parent
refresh with the parent's roster deleted and `--parent-gone` on first
registration; `session create` refuses an id whose sidecar records a
parent; and a `SessionStart` with `source: "fork"` against a roster that
already has lineage changes nothing.

`isStale`, the two sidecars, `Store.Ensure`, `Store.ParentOf`, and
`Store.LastSeen` are therefore implementation work, listed in §5 point 1.
They are a change to shared session code, not OpenCode-specific logic.

A session that predates the plugin and never fires a tool or shell call
stays unregistered. Nothing it does can reach a commit through the
plugin, and an operator `ethos mission claim` against its id is refused
as an unknown session. That refusal is fail-closed, and the next tool or
shell call from that session registers it. This limit is disclosed, not
hidden. The formal model's `JoinRootSession` fires on creation; this
design registers lazily. A missed `session.created` therefore yields a
*registered but unclaimed* root: the roster exists once the first tool or
shell call arrives, but the claim that `JoinRootSession` followed by
`ClaimMission` would have made is not made by registration. A claim still
comes from §2.4 (the inherited `ETHOS_SESSION` or an operator's `ethos
mission claim`), so the registry state is not the same as the model's
until one of those happens. Registration alone attributes nothing.

### 2.2 ForkSession

On `event.type === "session.forked"` — `SessionEvent.Forked`, a dedicated
event distinct from `session.created`, confirmed by direct read of
`.tmp/session-event.ts:187-201` (`Forked = Event.durable({ type:
"session.forked", ..., schema: { ...Base, parentID: SessionID,
boundary: SessionFork.Boundary, instructions: ..., instructionEntries:
... } })`) — the payload carries `parentID` directly, a required
(non-optional) `SessionID`. `.tmp/api.ts:559-571` confirms the exact wire
shape the plugin receives over `ctx.event.subscribe()`:

```ts
{
  type: "session.forked",
  data: {
    sessionID: Session.ID
    parentID: Session.ID
    boundary: Session.ForkBoundary
    instructions?: {...}
    instructionEntries?: InstructionEntry.Snapshot
  }
}
```

**Two different "parent" fields (round 5).** The live `session.forked`
event names the fork source in its `parentID`. The *persisted*
`Session.Info` record that `ctx.session.get` returns carries two separate
links (`.tmp/schema-session.ts:33-37`): `parentID` (optional, hierarchy)
and `fork: { sessionID, boundary }` (optional, fork provenance). A review
of OpenCode `v2.0.32`'s `SessionProjector.projectFork` reports that a fork
is stored with `parent_id: null` and the source in `fork_session_id`; that
projector source is not among this mission's `.tmp/` material, so the
implementation stage must confirm it against the running release. Whatever
the projector does, this design reads **only `fork.sessionID`** from a
persisted record and the event's `parentID` from a live event, and treats
both as the same relation, "fork source". It never follows the persisted
`parentID` as lineage: a record read that way would look like a root, and
a missed fork would never recover its delegation. A persisted record with
no `fork` is a root.

`.tmp/api.ts:237-239` additionally confirms the request/response shape of
the `session.fork` RPC operation itself:
`SessionForkInput = { sessionID, before?: SessionMessage.ID }`,
`SessionForkOutput = Session.Info` — matching the spec's own
`ForkSession` precondition that the fork's own contract ties parent and
child only through `parentID` plus transcript/instruction inheritance, not
through anything else (spec §5.2's note, corrected per the spec's own
review history, that the companion research's earlier over-general
"nothing ties the two identities together" draft was wrong only about
the ABSENCE of a direct `parentID` link, not about the absence of anything
beyond it).

This instantiates `ForkSession` (spec §5.2) directly: `rec?.parent =
forkedFrom(parentSid?)`, `rec?.root` inherited from the parent (so the
spec's `root`-consistency invariant, §"Registry", holds the instant the
child session enters the registry). `.tmp/schema-session.ts:30-59`'s
`Session.Info` schema confirms `root`/`family` are not fields OpenCode's
own wire schema carries on a session record at all — `Session.Info` has
`id`, `parentID` (optional), and a `fork: { sessionID, boundary }` struct,
but no `root` or `family` field — consistent with the spec's own
characterization of `root`/`family` as *derived* projections the spec
models (§"Registry"'s own note: "A session's FAMILY... is deliberately NOT
a second stored field"), not wire payload fields. The plugin does not need
to read a `root`/`family` field off any OpenCode payload; it derives the
Registry's own `root` by the spec's rule (inherit from `parentID`'s own
record), exactly as `ForkSession`'s own guard computes it.

### 2.3 AllocateDelegationForkPropagated: two triggers, one operation

Per spec §5.6 and the companion FAQ's "What happens if the fork event
itself is missed?" (`docs/prfaq-opencode-integration.md` lines 233-289),
this operation fires from either of two independent triggers against the
identical guard:

- **Primary: the `session.forked` event itself.** On receipt, the plugin
  calls `ensureRegistered` for the child (§2.1.1), which registers the
  fork source and any missing ancestors first. It then offers each
  registered ancestor, nearest first, as `witness?` to `claim-forked`
  (§5 point 2) until one succeeds, moving to the next ancestor only on
  "witness refused", which includes a witness whose claimed mission is
  closed (§5 point 2 item 4). The guard is spec §5.6's corrected, round-4
  guard: the full ancestor set under transitive closure, not a single hop
  (§2.3.1). `claim-forked` refuses a witness that is not an ancestor, holds
  no claim, or holds only a non-`claim`-origin binding (§5 point 2); on
  success it allocates a new delegation under the
  witness's mission for the forked child and writes the child's own claim
  (spec §5.6's `claims' = claims (+) {sid? |-> claims(witness?)}`).

- **Secondary, trigger-independent: `ctx.tool.hook("execute.before", ...)`.**
  `.tmp/event-manifest.ts` confirms `ctx.event.subscribe()` draws from the
  same volatile, no-replay server event stream every other event
  (`session.created`, `session.status`, etc.) uses — there is no
  OpenCode-side distinction between "fork events are replayed" and "other
  events are not." The companion research independently confirms this
  volatility directly against three OpenCode spec sources (restart FAQ,
  `docs/prfaq-opencode-integration.md` lines 235-241): a subscription
  disconnected at the moment of a fork permanently misses that fork's
  event. The fix is not a new operation: every tool call a session makes
  carries that session's own `sessionID`
  (`docs/prfaq-opencode-integration.md` line 255-256, `packages/plugin/src
  /effect/tool.ts`), so on *every* `execute.before` firing, the plugin
  checks whether the firing session is already claimed. The plugin has no
  file access, so it does not read the claim itself: it relies on
  `claim-forked`'s own "already claimed, exit 0" outcome (§5 point 2 item
  4, with the plugin action for each exit status), and it memoizes the per-session outcome so it does not spawn a
  subprocess on every tool call. If the session is not yet claimed, it reads the session's own record via
  `ctx.session.get({ sessionID })` — part of the hook-plugin's own
  `SessionDomain` (`.tmp/session-effect.ts:153-173`'s `Pick<SessionApi<unknown>,
  ... "get" ...>`) — for its `fork.sessionID` (not `parentID`, see §2.2),
  and applies the identical `AllocateDelegationForkPropagated` guard.
  **Correction (round 5).** Round 4 invoked the guard here without
  registering anything, so for a missed fork the Go side held no roster for
  the child or for any intermediate ancestor, and its walk from
  `ss.Load(child)` could not recover the delegation. Both triggers now run
  the same two steps in the same order: `ensureRegistered(sid)` (which
  writes the missing rosters and fork links, parent first, preserving any
  that exist, §2.1.1), then `claim-forked`.

Both triggers invoke the *same* operation with the *same* guard
(`sid? not-in dom claims`, `id? not-in dom delegations`), so calling it twice
for one session is safe by construction, exactly as the spec's own Failure
Modes section states (§"Fork event missed during plugin disconnection"): a
session already claimed via the live event fails the first guard on the
second attempt and nothing fires; a session whose fork event was missed
has no claim yet, so the operation fires exactly once, from whichever
trigger notices first. This is a property of the spec's guard, not
something the OpenCode-side implementation has to re-derive or re-prove —
it inherits the spec's own already-stated idempotence.

#### 2.3.1 Ancestor-set resolution in practice

The spec's round-4 guard (§5.6) requires `witness?` to be a member of
`sid?`'s full ancestor set under the transitive closure of the one-step
parent link, not merely the immediate parent — because the spec's own
`Registry` is bounded only for model-checking purposes (`maxSessions = 2`;
spec §"Global Constants"), while the real Registry is unbounded and a
grandparent's claim must still be reachable.

**Correction (round 5): no depth cap.** Round 4 bounded the walk at ten
hops, borrowing `internal/process/tree.go:16`'s `maxWalkDepth`. That is
not equivalent to the spec's guard. The constant bounds a *process-tree*
walk and says nothing about OpenCode fork depth. With a valid lineage
whose only claimed ancestor is eleven links away, a ten-hop walk finds no
claim although the formal operation permits attribution, and the fail-closed
result is a spurious refusal. The spec bounds its comprehension by
`dom sessions`, the finite set of live sessions, not by a constant (spec
§5.6), so a faithful implementation walks the whole ancestor set.

Both walks therefore run to the end of the lineage and terminate by cycle
detection, not by a depth constant:

- *Plugin side* (`ensureRegistered`, §2.1.1): follows `fork.sessionID`
  upward through `ctx.session.get`, keeping a set of visited session ids.
  A repeated id means a malformed lineage; the plugin logs it and registers
  nothing further for that chain.
- *Go side* (`claim-forked`, §2.3.2): follows the recorded parent
  (`Store.ParentOf`, the `parent-session` sidecar of §2.1.1) upward from
  the child with a visited set. It stops on a match (pass, once the
  witness's own roster loads, below), on
  an empty recorded parent (the witness is not an ancestor: refuse), on a
  repeated id (refuse, fail closed), or **when `ss.Load` fails on any
  session in the chain, including a missing roster (refuse, fail
  closed)**. A dangling parent recorded with `--parent-gone` (§2.1.1)
  falls in the last case: the walk cannot load it, so propagation refuses.
  The last case matches the spec: when an ancestor's record is
  removed (`EndSession`, a `session.deleted` event, or a purge), "the
  chain is structurally unwalkable past that point" and the relation
  reflects only "as much of the ancestor chain as still has a live,
  readable record" (`docs/spec-opencode-integration.tex:1054-1064`). A
  load error that is not "not found" (a corrupt roster) refuses the same
  way and is reported to the caller. Each step either reaches an empty
  link, fails to load, or visits a new roster, and the roster set is
  finite, so the walk terminates. **Round 8 (replacing round 5's
  "a cycle cannot be written through `ensure` except through a
  `--parent-gone` parent").** That sentence is false (lifecycle spec,
  ledger C3). A cycle is writable without `--parent-gone`, after a session
  is deleted and registered again under its own descendant, and a session
  can be its own parent under `--parent-gone`. `ensure` therefore refuses
  an edge that would close a cycle (§2.1.1 rule 3, `fixCycleCheck`). The
  visited set stays **mandatory**, not a defence only: the refusal reads a
  chain under the lock of one session, so two concurrent insertions can
  still close a cycle (spec N4), and the walk must refuse on a repeated id
  for that case and for a hand-edited or corrupt sidecar.
- *The witness's own roster is loaded.* A match is not accepted until
  `ss.Load(witness)` succeeds, which is the spec's liveness conjunct on
  `witness?` (`docs/spec-opencode-integration.tex:1007-1009`). The same
  walk loads the child first (its absence is a different outcome, the
  "child roster gone" row of §5 point 2).

The walk is upward only, from child toward ancestors, as in §7.1
alternative B. There is no supported-depth restriction to document and no
truncation outcome to reconcile with the spec.

#### 2.3.2 Verifying the witness server-side, not trusting the plugin's claim

**Correction (round 2, finding 3).** Round 1 proposed `ethos mission
claim-forked --session <child> --witness <parent>` (§5, point 1) performing
the ancestor-set guard "server side in Go." It did not specify what
Go-owned data that guard reads. `internal/session/session.go`'s
`Participant.Parent` (`session.go:10`) is a *within-roster* link — which
participant, inside one session's own roster, joined on whose behalf
(`internal/hook/session_start.go:187-188`'s `root`/`primary` pair, where
`primary.Parent = root.AgentID`) — not a link between two different
sessions' rosters. Confirmed by direct read: nothing else in
`internal/session/` or `internal/mission/` stores session-to-session
parentage. Without that data, a `claim-forked` call's `--witness` flag is
exactly the "caller-supplied, trusted outright" shape this spec's own
`ForkNeverInheritsClaim` theorem (§7.1, alternative A) and round-4 guard
exist to prevent — a malicious or buggy plugin could name any session as
`--witness` and the guard would have nothing server-side to check it
against.

The fix is a recorded parent *per session*, not per participant: the
OpenCode `sessionID` this session was forked from, set once, at
registration time, and never mutated afterward. **Round 6:** it is stored
in the `parent-session` sidecar of §2.1.1, not in a `Roster` field, so a
roster re-marshal cannot erase it. Round 2 introduced it as
`Roster.ParentSession`; the name survives only in the earlier-round
notes. Two write paths populate it:

- **`ethos session ensure`** (§2.1.1, §5 point 1) takes an optional
  `--parent-session <id>` flag. On `ForkSession` (§2.2), the plugin passes
  the fork source (the live event's `parentID`, or a persisted record's
  `fork.sessionID`, §2.2) as this flag's value when it first registers the
  child session's roster. At first registration the parent's roster must
  already exist, unless the plugin passes `--parent-gone` for a parent
  OpenCode conclusively reports deleted (§2.1.1). That is why
  `ensureRegistered` registers ancestors first. A later `ensure` with a
  different value is refused, never applied (§2.1.1), so a plugin restart
  cannot blank or rewrite lineage.
- A root session (§2.1, no `parentID` on its `session.created` payload, or
  no `fork` on its persisted record) is registered with no `parent-session`
  sidecar — there is nothing to record, and an absent value reads
  unambiguously as "this session has no ethos-known parent," matching the
  spec's own `noParent` case (spec §5.1).

`claim-forked --session <child> --witness <parent>`'s guard then resolves
`witness?`'s ancestor-set membership with the Go walk of §2.3.1, one step
at a time, with each step reading the recorded parent off the *Go-stored*
sidecar (`Store.ParentOf(sessionID)`) instead of trusting a
caller-supplied chain: starting from `child`, follow the recorded parent
upward, checking at each hop whether the current session ID equals
`witness`, until a match is found (the guard passes), the chain ends at an
absent parent (no match anywhere in the ancestor set: refuse), or
an id repeats (refuse, fail closed). The plugin's
own `--witness` argument is now merely *which* session to check for —
`claim-forked` itself walks the lineage from data it alone wrote via
`session ensure`, so a plugin that lies about `--witness` simply gets a
refusal: the walk from `child` will not reach a session the plugin never
registered as an ancestor, regardless of what the flag claims.

This leaves `Roster` (`internal/session/session.go:25-32`) unchanged and
requires no change to any `Join`, `JoinSelf`, or `Leave` caller. Two
creators change: `ethos session create` refuses an id whose sidecar
records a parent, and Claude Code's fork `SessionStart` calls `Ensure`
(§2.1.1, §2.5). Neither the sidecar nor the flag contains any
OpenCode-specific logic; see §2.5.

### 2.4 How the plugin itself learns "which mission"

Neither `JoinRootSession`/`ClaimMission` (§2.1) nor the fork-propagation
path (§2.3) specifies *how the plugin discovers* which mission a brand-new
root session should claim — that information has no OpenCode-native
channel (there is no `MISSION_ID` environment variable OpenCode threads
into its own session the way Claude Code's PreToolUse dispatch threads
`additional_env`, per `internal/mission/active.go`'s own doc comment on
why the active-mission sidecar exists at all: "additional_env from
PreToolUse does NOT persist into hook script processes"). Two sources, in
the order the plugin checks them, mirroring `dispatchTierB`'s own
`BoundVia*` precedence (`pretooluse_dispatch.go:711-912`):

1. **`ETHOS_SESSION` already set in the OpenCode server process's own
   environment**, naming a *parent* session that already holds a claim —
   this is the ordinary "a human or an orchestrating agent started
   `opencode serve` from inside an already-mission-claimed shell" case,
   and resolves through the exact same `resolve.SessionID` chain Claude
   Code uses, with no OpenCode-specific code at all.
2. **An explicit operator action** — `ethos mission claim <id>` run
   directly against the OpenCode session's own ID once it is known (e.g.
   from `ethos session list` after `JoinRootSession` registers it) — the
   same manual claim an operator already runs for a Claude Code session.

This design does **not** invent an OpenCode-specific "declare the mission
for this session" API. `JoinRootSession`'s own plugin-side code is
therefore mostly a *propagation* mechanism (§3) plus the fork-handling
machinery (§2.2-2.3); the actual claim act is `ethos mission claim`,
unchanged, run by whatever process already knows the mission ID. A future
round that wants OpenCode-native mission declaration (e.g. a custom
`/ethos-claim` OpenCode command via `ctx.command.transform`,
`.tmp/plugins-index.mdx`'s command domain) is an additive UX improvement
on top of this mapping, not a precondition for it, and is explicitly out
of scope for this design.

### 2.5 Fork propagation is harness-neutral: Claude Code needs it too

The machinery in §2.2, §2.3, and §2.3.2 is not OpenCode-specific.
`claim-forked` and the `parent-session` sidecar (§2.1.1) are harness-neutral
Go primitives with no OpenCode-specific logic in them: they take two session
IDs and walk Go-stored lineage. Only the *trigger* that calls them differs per harness.

The spec's Harness Instantiation section, Case 2
(`docs/spec-opencode-integration.tex`, §"Harness Instantiation", Claude Code
subsection), records the verified fact. Claude Code's `--fork-session`,
`/fork`, and `/branch` each mint a genuine new top-level session, exactly as
OpenCode's `session.fork` does, and a `SessionStart` hook fired for such a
session reports `source: "fork"` (Claude Code v2.1.214,
`code.claude.com/docs/en/hooks.md`). The spec concludes that
`ForkNeverInheritsClaim` is **not** vacuous for Claude Code in this case and
needs the same session-level claim-isolation treatment OpenCode gets. The
other Claude Code mechanism, Task-tool subagents and hook subprocesses,
shares its leader's session and mints no new one, so it is unaffected.

Nothing in `internal/mission/` or `internal/hook/` implements this for
Claude Code today: no `AllocateDelegationForkPropagated`-shaped primitive
exists anywhere in the codebase. Building `claim-forked` for OpenCode alone
would therefore itself create a parity difference between the harnesses.
This design makes the Claude Code wiring in scope for the same
implementation stage (§5, point 3): Claude Code's `SessionStart` hook
registers the session with `Store.Ensure` in process, **for every source and
without `touch`** (round 9; ledger C13, §2.1.1), instead of
`createSessionRoster`'s `CreateInCheckout` (`session_start.go:69,194`). When
the source is `fork`, `Ensure` is given the fork's parent from the payload;
for every other source it is given the parent `Store.ParentOf` records. For
the `fork` source the hook then calls `claim-forked` (§5 point 2), the same
two steps the OpenCode plugin takes on `session.forked`. The two steps run
one after the other and are never nested (§5 point 2, lock constraint). The
`SessionStart` path still writes the `CLAUDE_PID` pointer file
(`session_start.go:196`) as it does today.
**A fork whose parent has no roster (round 8).** The pointer file is written
only after the roster was created (`session_start.go:194-197`), and `Ensure`
refuses a first registration whose parent has no roster (§2.1.1 rule 1). A
fork of a parent that ended, or was purged, would therefore get no roster
and no `CLAUDE_PID` pointer, and every later session-resolving call in that
session would fail. The hook retries `Ensure` once with `parentGone` when
the first call returns the typed `ErrParentNotFound`. The retry is
conclusive and safe in this process because ethos's own store is the
authority here: the parent's roster file is absent (not unreadable, which
is a different error and is not retried). The fork then gets its roster, a
dangling `parent-session` sidecar, and the pointer file, and `claim-forked`
refuses through the walk's `Load` failure (§2.3.1), the same fail-closed
result as the OpenCode `--parent-gone` path (§2.1.1). Any other `Ensure`
error is reported on stderr and leaves no pointer file, as a failed
`createSessionRoster` does today. Required test: a `SessionStart` with
`source: "fork"` whose parent session has no roster produces a roster for
the fork, a `parent-session` sidecar naming the missing parent, and the
`CLAUDE_PID` pointer, and `claim-forked` for it exits "witness refused";
a second test with a parent roster that exists but fails to parse does
not retry with `parentGone` and writes no pointer.
`Ensure` never overwrites a recorded lineage, so a repeated, resumed, or
compacted `SessionStart` for a session that already has one cannot erase it.
This holds of every source because every source calls `Ensure` (round 9;
round 8 had kept `CreateInCheckout` for the other sources, which erased the
lineage of a resumed fork, C13). The spec
leaves one point open for Claude Code: whether a forked session's own
`CLAUDE_PID` resolves to a fresh roster entry automatically, or the hook
must register one. The implementation stage must confirm this against a
running Claude Code, as the spec states. It must also confirm which field of
the `SessionStart` payload names the fork's parent session; this design has
not verified one.

### 2.6 EndSession: `session.deleted` (round 5)

Rounds 1-4 registered OpenCode sessions but never ended them, so rosters
and claim/binding sidecars outlived their sessions until a purge, and the
two harnesses differed: Claude Code's `SessionEnd` hook deletes the
session (`internal/hook/session_end.go:57`, `ss.Delete`). The plugin now
handles `session.deleted` (§1.2 item 1; `.tmp/session-event.ts:177-185`)
by running `ethos session delete --session <id>` (`runSessionDelete`,
`cmd/ethos/session.go:697`, wrapping `Store.Delete`, `store.go:200`). That
call holds the per-session lock and runs `deleteFiles` (`store.go:278-301`):
it first closes, `aborted`, the open fork skeleton the session owns and its
pending record names (round 9: a Tier B skeleton that the session's binding
names is not closed, only unnamed; the close uses the store root recorded in
the record), then clears the active-mission claim, the delegation binding,
the pending record, and every pending dispatch, then removes `last-seen`, the
roster, and last the `parent-session` sidecar (order, origin discriminator,
and short-circuit in §2.1.1). A failed close keeps the roster and everything
else as the retry token for a later `session delete` or purge; the plugin
logs the failure.
The plugin drops the id from its memo
(§2.1.1). This instantiates the spec's `EndSession`. A forked child whose
ancestor was deleted becomes unwalkable past that ancestor, and
`claim-forked` refuses (§2.3.1), which is the spec's own behavior. A missed
`session.deleted` event is covered by the TTL: the roster stops being
refreshed and `isStale` reclaims it (§2.1.1). Deleting an unknown id is not
an error the plugin acts on; it logs and continues.

### 2.7 What the lifecycle model proves, and how far (round 9)

`docs/spec-opencode-lifecycle.tex` checks each invariant as the negation of a
goal given to ProB, on a slice of the machine (the operations that can affect
that invariant), at the instance named. "Exhaustive" below means ProB visited
every reachable state of that slice and found no counterexample; "bounded"
means it stopped at a state limit with levels still open and found none in the
states it saw. The instance is two missions and two delegations, and two
sessions for the `claim-forked` slices and three for the lineage and purge
slices, unless stated. Time takes three values with a threshold of one, and
the participant kinds are three. A claim that an invariant holds is a claim
about the slice and instance in the table, and nothing wider.

| Invariant | Exhaustive | Bounded |
|---|---|---|
| `LineageStable` (with `fixDeleteOrder`, `fixSessionStart`) | lineage slice, three sessions (21,953 states); start slice (1,025) | none |
| `Acyclic` (strong form, every sidecar; `fixCycleCheck`) | lineage slice, three sessions (15,029); start slice (1,025) | none; but see N4 below |
| `NoLiveSessionPurged` (`fixStaleLive`, `fixCorruptPurge`) | one session (817) | two and three sessions (40,000 states, not exhausted) |
| `NoOrphanOpenSkeleton`, fork skeletons (`fixDurableName`) | claim slice (16,601; 106,881 at three delegations); claim+ slice, which includes a Tier B dispatch over a completed fork claim, the operator claim, the mission close and the close-time clearing (257,269); delete slice (4,877) | claim+delete+close, two sessions (200,000 states, depth 8) and three (40,000, depth 5); claim, three sessions (200,000, depth 12) |
| `ReconcileOnExit` | claim slice (16,601); claim+ (257,269) | the same full-machine runs |
| `SweepComplete` (`fixDeleteCloses`, `fixPostCheck`) | sweep slice (37,025) | the same full-machine runs |
| `NoForeignAbort` (`fixOrigin`) | the three Tier B slices (11,073; 33,217; 2,013) | the same full-machine runs |
| `NoVerdictOverwrite` (`fixCas`) | sweep slice (19,697) | verdict slice (300,000 states); full-machine runs |
| `LiveSpared` | claim slice (16,601); claim+ (257,269) | full-machine runs |
| `LockedWrites` (a condition of the model's lock assumption, not an invariant of the design; `fixReuseLocks`) | claim slice (16,601) | full-machine runs |
| `WellFormed` (the model's own well-formedness) | every run in which it is checked | the three-session full-machine run |

No invariant has an open counterexample. The limit is coverage: the full
`claim-forked` machine, with the deletes, purges and closes interleaved, is
exhausted only slice by slice, and the second propagations of C9 and C11 are
exercised by the exhaustive claim and claim+ slices, not by the bounded
full-machine rows. The three-session rows exercise the multi-hop ancestor walk
and are bounded at 40,000 and 200,000 states. A switch is shown necessary
where a run isolates it (the ledger's rows); the amendments that the durable
record makes redundant for `NoOrphanOpenSkeleton` (the binding-named close at
release, `fixSupersede`) are labelled defensive in §5 point 2 item 5, and
the resume path's supersede clause and the guard that a complete record is
never resumed are unexercised and unverified (`MutSupReuse`, `MutReuseDone`
survive).

**Limits the design relies on.** The model's operations are atomic except
where a process can die or a removal can fail, so it cannot show any race. The
limits the spec states and where this design meets them: N2, `deleteFiles`
step 1 short-circuits (§2.1.1); N3, `Store.Close`'s sweep is best-effort and a
skeleton with no `record.yaml` refuses `Abandon` (§5 point 2 item 5); N4,
`Acyclic` holds of a serialised store only, so the visited set of the walk is
mandatory (§2.1.1 rule 3, §2.3.1); N5, every close but the sweep writes
`aborted` (§5 point 2 item 5); N7, the child's claim is two files (§5 point 2
item 1); N8, a live Claude Code session whose roster does not parse and has no
`last-seen` is still purged (§2.1.1); N9 and N17, the Tier B dispatch is one
atomic step and three of its paths are not represented (§5 point 2 item 5);
N10, the compare-and-set is taken to be atomic as the delegation lock is; N11,
a `SessionStart` for a roster that does not parse is a no-op (§2.1.1); N12,
the owner test of the origin filter is vacuous in the model, whose only
writers of a binding are the session itself; N13, the dispatch-pending
entries `ClearMissionBindings` clears and the MCP path that calls it are not
represented; N15, the record's stage is one bit and a record that no longer
parses while the claim lasts is outside the model; N16, the lock around
`release` and the close-time clearing is an implementation constraint (§5
point 2 item 5); N18, that a release removes the record is hygiene and the
`pendDone` guard of the resume path is defensive (§5 point 2 item 5). The
read phase and the start of the write phase are one step in the model, so the
window in which the witness releases its claim between them is accepted and
not examined.

## 3. `ethos hook commit-trailers` without Claude process-tree discovery

### 3.1 The existing resolution chain, unchanged

`resolve.SessionID` (`internal/resolve/resolve.go:311-359`) already
resolves via exactly two steps, in order:

1. `ETHOS_SESSION` env var, trimmed, if non-empty — `SessionSourceEnv`.
2. A corroborated `CLAUDE_PID` -> `ss.ReadCurrentSession(pid)` pointer-file
   lookup — `SessionSourcePID`. This step requires `process.UnderClaudeCode()`
   to be true first (`resolve.go:320-322`); absent that signal, `SessionID`
   returns `ErrNotUnderClaudeCode` without ever touching the pointer file.

There is **no process-tree walk fallback** in this chain at all — confirmed
directly by reading `resolve.go:311-359` for this mission (matching the
spec's own corrected finding, spec §"Scope": an earlier spec draft wrongly
described a walk fallback that the real function does not have).
`runHookCommitTrailers` (`cmd/ethos/hook.go:234-251`) calls `resolve.SessionID`
unconditionally and takes one of three branches:

- `err == nil`: writes trailers for that session (`hook.WriteCommitTrailers`,
  `internal/hook/commit_trailers.go:45-72`).
- `errors.Is(err, resolve.ErrNotUnderClaudeCode)`: silent, no stderr, no
  trailer — "no session was ever expected" (`hook.go:237`).
- any other error (wraps `resolve.ErrNoSession`): prints `ethos:
  commit-trailers: %v; no trailer added` to stderr, adds no trailer
  (`hook.go:238`).

**This design changes none of that code.** `ethos hook commit-trailers`
resolves the committing OpenCode session purely by `ETHOS_SESSION` already
being correctly set in the committing shell's environment when it runs —
step 1 of the existing chain, already harness-neutral by construction
(spec §"Scope": "`ETHOS_SESSION` is ALREADY a harness-neutral integration
seam"). The entire OpenCode-side engineering problem is making
`ETHOS_SESSION` land, correctly, in the environment of whatever subprocess
eventually runs `git commit` — not teaching `resolve.SessionID` a new
discovery mechanism. **Setting `ETHOS_SESSION` is necessary but not
sufficient (round 5):** `plugin/hooks/commit-msg.sh:55` skips the
`commit-trailers` lookup whenever `MISSION_ID` or `DELEGATION_ID` is
already non-empty in the environment, so inherited attribution variables
must be cleared in the same write. §3.2.1 specifies that. `process.UnderClaudeCode()`'s three signals
(`CLAUDE_PID`, `CLAUDECODE`, a `claude`-named process ancestor —
`tree.go:238-254`) are all Claude-Code-specific by name and by design; this
design does not touch, extend, or generalize that function. An OpenCode
commit with `ETHOS_SESSION` unset and no Claude Code ancestor correctly
returns `ErrNotUnderClaudeCode` — the ordinary "no session was ever
expected" silence, exactly right for a plain OpenCode-driven commit this
plugin has not wired up (§3.3).

### 3.2 Propagating `ETHOS_SESSION` into the committing shell

**Correction (round 2, finding 2).** Round 1 attributed this propagation to
`ctx.tool.hook("execute.before", ...)`, claiming its event carries a
mutable `env` field. It does not. Direct read of both copies of the
reference material confirms `ToolExecuteBefore`
(`.tmp/effect.mdx:1484-1500`, `.tmp/plugins-index.mdx:1740-1754`) has no
`env` field at all. The hook that *does* expose a mutable environment is a
different one, on a different domain: `ctx.shell.hook("create.before",
...)`'s `ShellCreateBefore` event
(`.tmp/effect.mdx:1436-1442`, `.tmp/plugins-index.mdx:1712-1718`) —
`{ command, cwd, timeout, shell, env: Record<string, string |
undefined> }`, confirmed identical on both copies. That is the mechanism
this design now specifies: on every `create.before` firing, the plugin
rewrites the attribution variables in `event.env` (setting
`ETHOS_SESSION = <sessionID>` when the session is known, and clearing the
inherited variables the existing hook would otherwise honor, per §3.2.1)
before the shell command runs, exactly the shape `.tmp/migrate-v1.mdx:104-137`'s `"shell.env"` ->
`ctx.shell.hook("create.before", ...)` migration example sets
`event.env.COMPANY_ENV = "development"` for a shell command about to run.

**The correlation gap this correction surfaces, and the confirmed window in
which it closes (round 3 correction).** `ShellCreateBefore`'s own field
list — `command, cwd, timeout, shell, env` — confirmed identical on both
copies of the reference material, carries **no `sessionID` field** in the
current OpenCode V2 dev-branch API. This is a real gap as that API stands
today, not a speculative one, and this design does not claim the gap is
closed for the dev-branch API as currently shipped.

It is, however, a narrow, time-bound window with a known closing
condition — not an open design question this document defers to a future
round's judgment call, and not something that needs a plugin-local
correlation workaround at all. A direct `WebFetch` of
`github.com/anomalyco/opencode/pull/54842` ("feat(plugin): shell hook
command source and TUI link open hook", OPEN/unmerged as of this check,
opened 2026-10-12) confirms it adds `source?: ShellCreateSource` to
`ShellCreateBefore`, where `ShellCreateSource = { type: "tool" | "user",
sessionID: Session.ID }`. Once PR #54842 merges, `ShellCreateBefore`
carries its own `sessionID` directly on every firing whose `source.type` is
`"tool"` — the plugin reads `event.source.sessionID` and sets
`event.env.ETHOS_SESSION` from it directly, with no plugin-local state, no
"most recently seen" proxy, and no ambiguity about which of several
concurrently running sessions issued the shell spawn. (`ToolExecuteBefore`
carries `sessionID` as a required field today, confirmed directly against
`/anomalyco/opencode`'s dev branch, `packages/plugin/src/effect/tool.ts` —
but that is a different hook, for a different event type, and is not the
mechanism #54842 adds to `ShellCreateBefore` itself.) The one condition the
implementation stage must check before relying on `ShellCreateBefore
.source.sessionID` is whether #54842 has merged to the branch OpenCode V2
ships from — the field does not exist before that merge, and this design
targets OpenCode V2 exclusively (per the spec's own "Scope" and "Harness
Instantiation" sections), so no earlier-API fallback is in scope here.

Until #54842 merges, this design does not adopt a guessing mechanism to
close the gap early. It still does not leave the environment alone: see
§3.2.1, which replaces the earlier "leave `event.env` untouched" rule.

#### 3.2.1 Environment isolation for every shell the plugin sees (round 5)

**Correction.** Rounds 2-4 said that when the firing session is ambiguous
the plugin leaves `event.env` untouched, "degrading to the existing
silent no trailer case." That is not fail-closed. Three unchanged pieces
of existing code make an untouched environment able to emit a *wrong*
trailer when OpenCode runs inside a claimed shell, the §2.4 launch case
where `opencode serve` starts from an already-claimed shell, and the
server's environment is inherited by every shell it spawns:

- `resolve.SessionID` accepts any non-empty, trimmed `ETHOS_SESSION`
  immediately (`internal/resolve/resolve.go:316-318`). An inherited
  parent value therefore names the parent's claim, and a commit in an
  unrelated or forked child session is stamped with the parent's mission.
- `plugin/hooks/commit-msg.sh:55` runs the `commit-trailers` lookup only
  when both `MISSION_ID` and `DELEGATION_ID` are empty. If either was
  inherited from an attributed worker shell, the hook skips the lookup and
  stamps the *launching worker's* values (`commit-msg.sh:160-161`), even
  when the plugin set a correct child `ETHOS_SESSION`. Setting
  `ETHOS_SESSION` alone is not enough.
- With `ETHOS_SESSION` empty, an inherited `CLAUDE_PID` that still
  corroborates as a live ancestor resolves the *enclosing Claude Code
  session* from its pointer file (`resolve.go:320-359`;
  `internal/process/tree.go:68-86`), so even clearing `ETHOS_SESSION`
  can attribute an OpenCode commit to a Claude session.

The policy is therefore two-armed, and both arms rewrite the environment
on every `create.before` firing. `event.env` is `Record<string, string |
undefined>` (`.tmp/effect.mdx:1441`); the plugin assigns the empty string
rather than `undefined`, because every reader above treats an empty value
as absent (`commit-msg.sh:55` uses `-z`; `resolve.go:316` and
`tree.go:125-127` trim then test for empty; `tree.go:249` tests `!= ""`),
whereas the deletion semantics of `undefined` are not documented.

| Arm | Condition | Plugin writes into `event.env` | Resulting commit |
|---|---|---|---|
| Known | The plugin establishes, with no ambiguity, which single session issued the shell: `source.sessionID` once #54842 has merged (§3.2), or the `singleSession` plugin option (defined below, off by default). The session has been through `ensureRegistered` within the memo's refresh interval (§2.1.1). | `ETHOS_SESSION=<sid>`; `MISSION_ID=""`; `DELEGATION_ID=""` | The hook runs the lookup (`commit-msg.sh:55` is true), `resolve.SessionID` returns `<sid>` from `ETHOS_SESSION` before any Claude signal is consulted, and the session's own claim and delegation binding decide the trailers. A session with no claim gets none. |
| Unknown | Any other shell: ambiguous session before #54842, an event with no `source`, a `source.type` that is not `"tool"`, or a failed `ensureRegistered`. | `ETHOS_SESSION=""`; `MISSION_ID=""`; `DELEGATION_ID=""`; `CLAUDE_PID=""`; `CLAUDECODE=""` | No attribution path resolves a session. `resolve.SessionID` finds no `ETHOS_SESSION` and no corroboratable `CLAUDE_PID`, so it returns `ErrNotUnderClaudeCode` (silent) or, if a `claude`-named process is still an ancestor, `ErrNoSession` (a stderr diagnostic, `hook.go:238`). Either way no trailer is added and no Claude session is substituted. |

**The `singleSession` option (round 5).** Earlier rounds said "a deployment
confirmed to run one session at a time" without saying how the plugin
confirms it. It is now a concrete plugin option, `singleSession`, a boolean
read from the plugin's `options` (`.tmp/effect-plugin.ts:29`; configured
in the plugin entry of `opencode.json`, `.tmp/effect.mdx:43-46`), **default
`false`**. The operator sets it to assert that this server runs one
session at a time. When it is `true`, the Known arm applies to a shell
whose `create.before` event carries no `source.sessionID`, and the session
is the only id the plugin currently holds in its memo that has not been
deleted (§2.6). "Held" means an unexpired memo entry (§2.1.1). If the plugin holds more than one such id, or none, the
shell takes the Unknown arm: the option never makes the plugin pick among
sessions. With the option off, no deployment, however small, gets the Known
arm before #54842.

Residual risk: the first shell from a session the plugin has not yet seen,
for example a shell typed in the TUI, can be attributed to the sole
memoized session. The operator's assertion when enabling the option covers
that risk.

The Unknown arm suppresses attribution; it does not refuse the spawn.
Refusing would turn a best-effort traceability feature into a gate on the
agent's work. The plugin logs one diagnostic naming the arm taken. The
plugin reads the server's inherited `ETHOS_SESSION` once, at setup, for
§2.4 source 1; it does not mutate the server process's own environment.
Setting the server's global environment per session would mix concurrent
sessions, which is the failure this section exists to prevent.

The policy covers shells the plugin sees. A shell that never fires
`create.before` is outside it, which §3.3 states as an open item.

### 3.3 What this does NOT cover — stated plainly

- **The sessionID-correlation gap named in §3.2 is NOT an open design risk
  requiring a plugin-local correlation workaround (round 3 correction).**
  It is a narrow, time-bound window, real today against the current
  OpenCode V2 dev-branch API, that closes once
  `github.com/anomalyco/opencode/pull/54842` merges and
  `ShellCreateBefore` starts carrying `source.sessionID` directly (§3.2).
  The implementation stage's one job here is to check whether #54842 has
  merged before relying on that field — not to design around an
  unresolved ambiguity. Round 1 separately asked whether a human typing
  `git commit` directly into an OpenCode TUI terminal goes through the
  same `create.before` hook as an agent-issued shell spawn
  (`docs/prfaq-opencode-integration.md` lines 143-151) — that question is
  distinct from the sessionID-correlation gap and remains unconfirmed by
  any source read for this design. A future implementation round must
  confirm it directly against a running `opencode serve` instance (the
  same verification gap the companion research names for the
  restart-liveness question, lines 456-463) before this propagation path
  can be trusted for the human-typed commit path specifically; the
  agent-issued tool-call path's sessionID source is now confirmed per
  §3.2, pending only #54842's merge.
- **Until OpenCode PR #54842 merges, a server that runs more than one
  session gets no `Mission:` or `Delegation:` trailers on plugin-spawned
  shells (round 5).** Without `source.sessionID` on `ShellCreateBefore`,
  the plugin cannot say which session issued a shell, so every such shell
  takes the Unknown arm and its commits carry no trailers (§3.2.1). Only a
  server configured with `singleSession: true` (one live session) gets
  attribution in that window. This is the cost of refusing to guess a
  session id, stated plainly rather than implied.
- **A shell that never fires `create.before` keeps whatever environment the
  OpenCode server inherited (round 5).** If the human-typed TUI terminal
  question above resolves "no, it does not go through the hook," a commit
  typed there carries the server's inherited `ETHOS_SESSION`,
  `MISSION_ID`, and `DELEGATION_ID`. When the server was started from a
  claimed or attributed shell (§2.4 source 1), that can stamp the
  launching session's trailers on a commit that session did not make. The
  plugin cannot close this: mutating the server process's own environment
  would mix concurrent sessions (§3.2.1). The implementation stage must
  confirm the TUI path against a running `opencode serve`. Until then,
  the supported launch guidance is to start the server with the
  attribution variables cleared (`env -u MISSION_ID -u DELEGATION_ID`,
  and `-u ETHOS_SESSION` when §2.4 source 1 is not in use), and the design
  records the exposure rather than claiming the hole is closed.

## 4. Fail-closed behavior for ambiguous attribution

This design adds **no new case** to `ResolveCommitAttribution`'s four-way
split (spec §"ResolveCommitAttribution", §4.2 below) and changes no line of
`runHookCommitTrailers`, `WriteCommitTrailers`, or `commit-msg.sh`. The
existing resolver is blind to which harness set `ETHOS_SESSION`, which is
what lets one hook serve both harnesses. It is also *not* fail-closed
against inherited state, so round 5 splits the table below into the cases
that really occur, and §3.2.1 adds the plugin-side environment policy that
makes the unsafe rows unreachable for every shell the plugin sees:

| Case | `ETHOS_SESSION` | Outcome |
|---|---|---|
| OpenCode plugin correctly propagated it into the committing shell, and that session holds a usable claim | present, names a live session with a claim | Trailers emitted — spec's 4th case. |
| OpenCode plugin correctly propagated it, but the named session has no claim (unclaimed leader work, a released claim) | present, names a registered session, `candidate? not-in dom claims` | Silent, no trailer, **no diagnostic** — spec's 3rd case, the ordinary "mission-free commit" state. |
| `ETHOS_SESSION` is non-empty but names a session with no roster: purged, never registered, mistyped, or inherited from a different host or checkout | present, names nothing in the roster store | **The existing reader does not check liveness.** `resolve.SessionID` returns any non-empty trimmed value (`resolve.go:316-318`) without consulting the roster, and `WriteCommitTrailers` reads only the sidecars (`commit_trailers.go:45-68`). With no sidecars: silent, no trailer. With a retained claim-origin sidecar (a sidecar with no roster has no GC path, `internal/mission/active.go:605-608`): **the trailer is emitted for the dead session.** The spec's `ResolveCommitAttribution` requires `alive` and a matching `bornEpoch` (`docs/spec-opencode-integration.tex:1378-1389`); the reader does not implement that guard, and this design does not change the reader. Liveness is enforced on the write side instead (`RefuseIfSessionGone` before any claim, `mission.go:2113`; teardown clears sidecars with the roster). What the plugin guarantees, and what it does not: it sets `ETHOS_SESSION` only to an id for which `ethos session ensure` succeeded within the memo's refresh interval (15 minutes, §2.1.1), and the Unknown arm (§3.2.1) never sets a session id. It cannot see a purge by another process, so a roster deleted inside that window can still be named. Two bounds make that harmless: `Store.Delete`/`Purge` clear the sidecars together with the roster (`deleteFiles`), so a purged session has no trailer to emit, and the `last-seen` sidecar (§2.1.1) keeps `isStale` from purging a live session. The residual case is an orphan sidecar (`active.go:605-608`), which the plugin cannot rule out and this design does not claim to. |
| Plugin ran the Unknown arm (§3.2.1), or its propagation did not reach this commit path (§3.3's open item) and the shell has no inherited attribution | `ETHOS_SESSION` empty; `CLAUDE_PID`, `CLAUDECODE` cleared by the plugin | `ErrNotUnderClaudeCode` -> **silent**, or, if a `claude`-named process is still an ancestor, **loud `ErrNoSession` diagnostic** on stderr. No trailer either way. |
| Propagation did not reach this commit path (§3.3's open item) and the shell *inherited* attribution from a launching claimed shell | `ETHOS_SESSION`, `MISSION_ID`, or `DELEGATION_ID` inherited | **A wrong trailer is possible**: the inherited session or the launching worker's mission/delegation is stamped (`resolve.go:316-318`, `commit-msg.sh:55,160-161`). This is the exposure §3.3 discloses. §3.2.1 closes it for every shell that fires `create.before`; it is not closed for shells that do not. |
| OpenCode shell, unset `ETHOS_SESSION`, and a valid Claude Code ancestor that is *not* cleared (a shell the plugin did not see, running under Claude Code) | unset, `CLAUDE_PID` corroborates | `resolve.SessionID` resolves the *enclosing Claude session* from its pointer file (`resolve.go:320-359`). That is correct for a Claude Code-spawned shell and wrong for an OpenCode one; §3.2.1 clears `CLAUDE_PID` and `CLAUDECODE` for every shell the plugin sees so this row cannot occur there. |
| No propagation attempted at all — a plain OpenCode-driven commit with the plugin not installed/enabled, no Claude ancestor | unset, no Claude signal | Silent, `ErrNotUnderClaudeCode` — correctly "no session was ever expected," since from `resolve.SessionID`'s perspective nothing claimed one. |

The load-bearing invariant: **this design never adds a mechanism that
guesses a session ID.** `ETHOS_SESSION` is either correctly propagated by
the plugin (§3.2) or it is not; there is no fallback inference step (no
"pick the newest OpenCode session," no reconstruction from a project path
or a cached event). This matches the spec's own `FailClosedOnAmbiguity`
theorem's precondition exactly — the theorem is about
`ResolveCommitAttribution`'s guard structure, which this design does not
touch — and matches the real incident the spec's own Precedent section
(§"Precedent") names as the reason this discipline exists: commit
`d3ba3c4` (bead `ethos-jawp`, PR #412), where a stale sidecar binding
produced a *wrong* trailer rather than a *missing* one. Nothing in this
OpenCode design introduces a second sidecar, a cache, or any state that
could go stale independent of the Registry's own `claims`/`delegationOf`
maps — the OpenCode plugin's event handlers write to exactly the same
`.punt-labs/ethos/sessions/<id>/{active-mission,delegation-binding}`
sidecars Claude Code's dispatch path writes too (`internal/mission/active.go`),
through the identical CLI primitives (`ethos mission claim`, and a new
delegation-allocation call — §5 — that is itself a thin Go addition, not a
new storage format).

### 4.1 Never guess another session's ID

Concretely, the two places this design could have been tempted to guess,
and why it does not:

- **Fork-miss reconciliation (§2.3)** resolves `witness?` only by walking
  the fork-source links the OpenCode event payload (`parentID`) or
  `ctx.session.get` (`fork.sessionID`, §2.2) itself names — never by picking "the most recently active session" or
  any heuristic. A session with no resolvable ancestor chain, or whose
  ancestor set contains no claim, allocates nothing; the tool call
  proceeds unattributed (Tier A-shaped, though this design does not
  change Tier A allocation itself, which is out of scope here).
- **Commit-trailer resolution (§3.1)** resolves the committing session only
  by `ETHOS_SESSION`'s literal value. If that value is unset, or set but
  unreadable, the existing `resolve.SessionID` error paths fire exactly
  as they do for Claude Code today — no OpenCode-specific guess is ever
  substituted.

### 4.2 Visible diagnostic, verified unchanged

`runHookCommitTrailers`'s existing stderr line — `ethos: commit-trailers:
%v; no trailer added` (`hook.go:238`) — already fires whenever
`UnderClaudeCode()`-style expectation exists but resolution fails. This
design does not add an OpenCode-equivalent of `UnderClaudeCode()` (there is
no `process.UnderOpenCode()`), which means an OpenCode-driven commit with a
broken `ETHOS_SESSION` propagation and no Claude Code signal present falls
into the *silent* branch, not the *loud* one — correctly matching the
spec's own first case ("no session was ever expected") from
`resolve.SessionID`'s point of view, since nothing told it a session was
expected. This is a deliberate scope boundary, not an oversight: adding a
loud-diagnostic branch for "a session was expected under OpenCode
specifically" would require `resolve.SessionID` (or a parallel resolver)
to know it is running under OpenCode, which — per §3.1 — this design
explicitly avoids introducing, to keep the commit-msg hook harness-blind.
A future round could add an `ETHOS_HARNESS=opencode` marker the plugin sets
alongside `ETHOS_SESSION`, giving `resolve.SessionID` a basis for a louder
diagnostic when that marker is present but the session still fails to
resolve; this design does not propose it, because the spec's own
`HarnessNeutrality` theorem (spec §"Theorems") is about the Registry model
treating `harness` as preserved-not-inspected data, and a resolver-side
harness check would be a new precedent this design leaves to a future
ADR rather than introducing unreviewed here.

## 5. What is genuinely new ethos-side code (scoped for the next, implementation, pipeline stage)

This design intentionally minimizes new Go surface. Enumerated so the next
pipeline stage's write-set is unambiguous:

1. **A create-if-absent session primitive, `ethos session ensure`
   (a hidden command beside `session create`, `cmd/ethos/session.go:79`),
   backed by a new `session.Store.Ensure`, and two per-session sidecar
   files, `parent-session` and `last-seen`** (§2.1.1, §2.3.2).
   `Store.Ensure(sessionID, root, primary, repo, checkout, host,
   parentSession, parentGone, touch)` takes the per-session roster lock
   (`Store.withLock`) once and applies the four rules of §2.1.1 inside it:
   the parent-exists check on first registration only (waived by
   `parentGone`, which requires a non-empty `parentSession`); a
   lineage-conflict error, changing nothing, when the recorded parent (the
   sidecar, else none) differs from `parentSession`; a lineage-cycle error,
   changing nothing, when a new edge would close a cycle (round 8, ledger
   C3); and otherwise create
   (sidecar, roster, then `last-seen` only if `touch`) or, if `touch`,
   refresh only `last-seen` (round 9: and only if the existing roster loads;
   one that fails to load writes nothing and exits non-zero, §2.1.1 rule 4). A parent that is absent from the store makes
   `Ensure` return a typed `ErrParentNotFound`, so a caller that has a
   conclusive "not found" can retry with `parentGone` (§2.5). `touch` is the CLI flag `--touch`: the plugin
   passes it on every `ensure`, and Claude Code's fork `SessionStart` never
   does. `Roster` gains no field. `Store.ParentOf` and
   `Store.LastSeen` read the sidecars; `deleteFiles` removes them in the
   seven-step order of §2.1.1 (`last-seen` at step 5, `parent-session` last
   at step 7, ledger C2), after closing the open fork skeleton its record
   names and its session owns (step 1, ledger C5, C10, C12). `session create` keeps its replace semantics for an id with no
   recorded parent and refuses an id whose sidecar records one. The plugin
   never calls it.
   **`isStale` change (round 6, replacing round 5's age-base change):**
   `isStale` becomes a `Store` method taking the session id and consults
   `last-seen` first; when present and parseable, the age of the later of
   `Started` and `last-seen` decides, before the empty-roster and
   participant-count branches (`store.go:942-957`), except that a live
   numeric participant is never stale (round 8, ledger C7, which reverses
   the round 6 rule that the sidecar alone decides). A `last-seen` read
   error is not stale with a warning, and a future-dated one is capped at
   now (§2.1.1). `Purge` and `PurgeTombstoned` read `last-seen` before
   deleting a roster that fails to load (round 8, ledger C8). Both call
   sites pass the id (`store.go:404,514`). Absent or unparseable `last-seen`
   leaves today's logic untouched, and a Claude Code roster never has one
   because its callers do not pass `touch` (round 7). The required tests
   include a Claude fork roster with a live numeric `Participants[1]`,
   `Started` 25 hours ago, and no `last-seen`, which is not stale. This is a change to shared session code
   with the required tests listed in §2.1.1, including a two-participant
   roster with a dead numeric `Participants[1]` and a fresh `last-seen`,
   which is not stale, and the reversed row 2 (live numeric participant,
   `last-seen` 25 hours ago, not stale). The 24-hour idle purge is intended
   (§2.1.1).
   Every reader that walks ancestor chains (§2.3.1's walk,
   `claim-forked`'s guard below) depends on the sidecar being populated
   correctly, parent first, by `ForkSession` (§2.2) through
   `ensureRegistered`.
2. **A delegation-allocation CLI primitive for `AllocateDelegationForkPropagated`.**
   `ethos mission claim` (§2.1, §2.4) already covers `JoinRootSession` +
   `ClaimMission`. Fork-propagation (§2.3) needs a new, narrow primitive —
   e.g. `ethos mission claim-forked --session <child> --witness <parent>`
   — that performs the spec's own guard (ancestor-set membership via the
   recorded-parent chain from point 1, not the caller's own
   `--witness` claim; liveness; `sid? not-in dom claims`) server-side in Go,
   not in the TypeScript plugin, so the guard logic lives once, in the same
   package (`internal/mission`) that already owns `WriteActiveMission` and
   `WriteDelegationBinding`. The plugin's job is only to *call* this
   primitive with the right two session IDs, resolved per §2.2-2.3; it
   does not reimplement the guard. **Correction (round 2, finding 5):**
   round 1 specified that this primitive "allocates a delegation... and
   writes the child's own claim" but did not specify writing the matching
   `DelegationBinding` sidecar. `WriteCommitTrailers`
   (`internal/hook/commit_trailers.go:45-72`) emits a `DELEGATION_ID`
   trailer only when `ReadDelegationBinding` (`internal/mission/active.go`)
   finds a binding whose `MissionID` matches the active-mission claim
   (`commit_trailers.go:65`'s `b.MissionID != missionID` gate) — a claim
   with no matching binding produces a `Mission:` trailer but silently no
   `Delegation:` trailer, the same shape Claude Code's own dispatch path
   avoids by writing both (round 2 said "atomically"; superseded below)
   (`internal/hook/pretooluse_dispatch.go:880-889`'s
   `WriteDelegationBinding` call, alongside the claim and skeleton writes
   in the same critical section). `claim-forked` must also write the
   binding: `mission.WriteDelegationBinding(globalRoot, childSession,
   mission.DelegationBinding{DelegationID: <newly allocated>, MissionID:
   <witness's claimed mission>, ParentSession: witness})`.

   **Correction (round 5): the lock serializes writers; it does not make the
   writes atomic.** Round 2 said the three writes happen "atomically"
   inside one `mission.WithDispatchPendingLock` critical section. That
   lock is a per-session `flock` (`internal/mission/active.go:523-550,
   626-633`) that excludes other lock holders. The skeleton
   (`WriteDelegationSkeleton`), the binding (`WriteDelegationBinding`), and
   the claim (`WriteActiveMission`) remain three separate file writes, and
   a failure or process exit between them leaves a partial state on disk.
   `WriteCommitTrailers` does not take the lock at all
   (`internal/hook/commit_trailers.go:45-72`), so a commit can read between
   the writes. The design specifies publication order and recovery instead
   of claiming atomicity:

   1. *Order: record, skeleton, post-check, binding, claim, then the record
      is marked complete.* The claim is the commit point. Without a claim-origin binding for the session,
      `WriteCommitTrailers` returns before it reads the delegation binding
      (`commit_trailers.go:53-56`), so a binding written before the claim is
      inert and a reader never sees a delegation trailer without its
      `Mission:` trailer. This is the same ordering principle
      `WriteActiveMissionOrigin` documents for its own pair: "an interrupted
      pair never reads wrong" (`active.go:240-250`). The binding is also
      not trusted alone: `commit_trailers.go:65` emits it only when its
      `MissionID` equals the claimed mission.
      **The mission is re-read between the skeleton and the binding (round
      8; lifecycle spec ledger C6, `fixPostCheck`).** Order: record,
      skeleton, **post-check**, binding, claim. After
      `WriteDelegationSkeleton` returns, the write phase loads the mission
      again and, if it is no longer open, aborts (item 5, in-process
      cleanup) and exits "witness refused" (item 4: a witness whose
      mission is closed is refused, so the plugin offers the next ancestor).
      The reason is
      `Store.Close`'s fallback: when the exclusive mission lock cannot be
      taken it sweeps open skeletons with no exclusion
      (`internal/mission/store.go:1107`, "a narrow TOCTOU window remains"),
      so the sweep can run beside the write phase, close the mission, and
      finish before the skeleton exists. Nothing then closes the skeleton
      (run `Fc2_w`). If the mission closed before the skeleton existed, the
      post-check sees it; if after, the sweep sees the skeleton. The
      post-check is not sufficient alone: the aborted write can itself be
      interrupted, leaving a record, which is why `deleteFiles` also closes
      named skeletons (§2.1.1; both are needed, runs `Fc2_post`, `Fc2_del`,
      `Fc2_both`). **The child's claim is written with origin `claim`**
      (`WriteActiveMission`, the call `runMissionClaim` makes, origin file
      absent or removed), which is what lets the child be a witness for its
      own descendants; the spec models it so (`LcCfWriteClaim`) and the
      earlier design did not say. **Limit (spec N7).** The model writes the
      claim and its origin in one step. In the code they are two files
      (`active-mission`, then removal of the origin file,
      `active.go:240-250`), and a reader falls back to `claim` origin when
      the origin file is absent. An interruption between them is not
      represented in the model, and the origin of a binding that does not
      resolve (`BindOriginUnknown`) is not represented either.
   2. *Lock order and scope (round 6).* `claim-forked` splits into an
      unlocked read phase and a locked write phase.
      - *Read phase, outside any lock:*
        1. `ss.Load(child)`. A missing or unreadable child roster is the
           "child roster gone" outcome (item 4), not a witness refusal.
           This `Load` is the only place an unreadable roster is refused.
        2. *Read the child's own claim, lock-free (round 8).* Read the
           child's active-mission binding with `ReadActiveMissionBinding`
           before any witness check. If the child already holds a claim,
           the witness checks of steps 3 and 4 are skipped and the call goes
           straight to the write phase, which reconciles and exits success.
           A claimed child needs no witness, and the witness checks must not
           be able to mask that: without this step, a plugin that offers each
           ancestor in turn would receive "witness refused" from every one
           of them for a child that is in fact claimed, and would memoize
           "unattributed" for it.
        3. Read the witness's claim with `ReadActiveMissionBinding`, the
           same reader `WriteCommitTrailers` uses. **The binding must have
           `Origin == mission.BindOriginClaim`** (`active.go:101-119`;
           `commit_trailers.go:54` and `pretooluse_dispatch.go:222` gate on
           the same value). A dispatch-origin binding, an unknown-origin
           binding (`active.go:103-112`), or no binding is "witness
           refused". Without this test, a leader that merely dispatched a
           mission, and never claimed it, would pass its mission to every
           fork: the false-trailer class `commit_trailers.go:32-40`
           describes.
        4. Walk the Go-stored lineage (§2.3.1), which `Load`s the witness's
           own roster (the spec's liveness conjuncts,
           `docs/spec-opencode-integration.tex:1007-1010`).
        A refusal here (witness not an ancestor, an ancestor roster
        missing, the witness's roster missing, witness holds no claim,
        wrong origin) writes nothing for the child's claim, but **it does
        not return before it reconciles (round 8; lifecycle spec ledger C1,
        `fixRefuseReconciles`).** Every exit other than "write failed"
        takes the child's dispatch-pending lock first, runs the reconcile of
        item 5 in its refusal form, releases the lock, and then returns its
        status. **A refusal never resumes the record (round 9):** resuming
        is a write-phase outcome (item 5, the resume path), because it
        writes the missing binding and claim, and a refusal writes neither.
        A refusal keeps a live record, marking it complete, and closes and
        removes any other. The round 7
        statement that every call reconciles first was true of the write
        phase only: a call that crashed after its skeleton and was then
        refused for good, for example because its witness's roster was
        later purged, never reached the write phase, and its skeleton stayed
        open with its record in place (run `Fa_w`; `Fa_refuse` shows the
        refusal fix alone is not enough, `Fa_inert` the claimed-case fix
        alone is not, and `Fa_all` holds with both). If the reconcile
        itself fails, the call exits "write failed" instead of the refusal
        status, so the plugin retries. When the child's sidecar directory
        does not exist there is no record and nothing to reconcile, and the
        refusal returns without taking a lock. **Correction (round 9, review
        finding F12).** Round 8 added "`deleteFiles` clears the record
        before it removes the roster (§2.1.1), so a child whose roster is
        gone cannot have one." That is false. An older `ethos` binary's
        `deleteFiles` does not know the record: it removes the roster and
        leaves the record and its open fork skeleton. A roster removed by
        hand does the same. The model allows the state too: `LcDeleteSession`
        ranges over a session that has a record and no roster. A child with
        no roster can therefore have a record, so the "child roster gone"
        exit takes the same path as every other exit: if the sidecar
        directory exists it takes the lock and reconciles (the reconcile
        needs the record, not the roster), and `ethos session delete` for
        that id closes the skeleton and clears the record
        (§2.1.1, `deleteFiles` works whether or not the roster exists).
      - *Write phase:* the lock is the **child session's** dispatch-pending
        lock (`mission.WithDispatchPendingLock(globalRoot, child, ...)`,
        the one `runMissionClaim` already takes for the same session,
        `cmd/ethos/mission.go:2112`), acquired **first**: that lock is
        always the outermost one, taken before any mission or delegation
        lock and never nested inside one (`active.go:495-497`). Inside it,
        in this order:
        1. `RefuseIfSessionGone(child)`. It refuses only when the roster
           is gone (`fs.ErrNotExist`); it proceeds on any other `Load`
           error (`pretooluse_dispatch.go:494-508`).
        2. *Reconcile the pending record* (item 5): one of three outcomes
           (the child already holds a claim; the record is resumed; or it is
           closed and removed), chosen by the rules of item 5. A close
           closes only an open fork skeleton the child owns, as a
           compare-and-set on verdict `open` (item 5). **The close takes its
           own locks (round 7), after the child's dispatch-pending lock and
           before step 5's.** For the skeleton the record names, it acquires
           the shared mission lock of the *record's* mission under the store
           root *recorded in the record* (`AcquireMissionLock(recordedRoot,
           recordMission)`; that mission can differ from this call's, and
           the working directory is never consulted: B2, §2.1.1), then that
           skeleton's delegation lock (`AcquireDelegationLock`), the order
           `dispatchTierB` uses, and releases both as soon as
           `CloseDelegationSkeleton` returns, before step 4. `Store.Close`
           sweeps open skeletons holding only the exclusive mission lock
           (`internal/mission/store.go:1107,1217-1246`), and the shared
           mission lock is what excludes that sweep when the sweep holds the
           lock: the sweep either finishes first (the skeleton is already
           closed, so the reconcile leaves it alone) or waits for the
           reconcile. The delegation lock excludes every other writer of the
           same `record.yaml` (the refusal-close paths and
           `DisclaimDelegation`, `internal/mission/store.go:1589`), and,
           since round 9, the sweep's own close of that skeleton (item 5,
           compare-and-set), so the reconcile never rewrites a stale copy of
           the record. The order is the existing global, repo, mission
           (shared), delegation order (`delegation.go:462-466`); the
           reconcile's locks and step 5's are taken one after the other,
           never nested. **If the mission directory under the recorded root
           is gone (round 8, amended in round 9), the close takes no mission
           lock and treats the skeleton as gone:** there is no `record.yaml`
           to rewrite and no sweep to exclude, and a lock on a directory
           that does not exist would create it. "Gone" is concluded only
           from a recorded root that is non-empty and resolvable. A record
           whose recorded root is empty, absent, or unresolvable fails the
           reconcile with "do not retry" (item 4): the error names the record
           and the remedy, and the call never reads it as "gone". The same
           close routine serves `deleteFiles` step 1 (§2.1.1), `mission
           release` and the close-time clearing (item 5), and the in-process
           cleanup, and every use writes verdict `aborted` (spec N5, item
           5).
        3. If the child holds a claim, the spec's `sid? not-in dom claims`
           guard fails: exit success ("already claimed") without writing,
           after the reconcile of step 2. If this call entered through the
           read phase's claimed-child shortcut (read phase step 2) and the
           claim is gone by now, the call exits "write failed" without
           writing, so the plugin's next call runs the full read phase. The
           call never allocates a delegation on witness checks it skipped.
        4. Allocate the id with `NewID` (item 3,
           `pretooluse_dispatch.go:775`), unless item 5 resumes a record, in
           which case there is no allocation and the delegation is the
           record's.
        5. Acquire the mission lock and then the delegation lock
           (`AcquireMissionLock` through the delegation lock's defer,
           `pretooluse_dispatch.go:791-808`), the order `dispatchTierB`
           uses: `NewID`, mission lock, delegation lock. **The resume path
           takes this step too (round 9; ledger C15, `fixReuseLocks`).**
           Round 8 let a resumed call go straight from the reconcile to the
           missing binding and claim writes, with the mission and delegation
           locks of this step not held. The reconcile releases the locks of
           its own close before it returns, so between the reconcile and the
           writes a locked `Store.Close` could run and sweep the mission; the
           model treats the shared mission lock as held for a binding or
           claim write (`LockedWrites`, run `Fa_lockw_off`). The resume path
           therefore takes the locks of this step and re-reads the mission
           (step 6) before it writes anything.
        6. Re-load the mission and confirm it is still open (the TOCTOU
           re-check, `pretooluse_dispatch.go:810-838`). On the resume path
           this is the re-read that the locks of step 5 make meaningful: a
           mission found closed ends the call with "witness refused" (item
           4) and writes nothing, and the next reconcile closes the
           skeleton the record names if the sweep missed it.
        7. Write the `claim-forked-pending` record (item 5), incomplete and
           carrying the store root, then the skeleton, then **re-read the
           mission (the post-check of item 1, ledger C6)**, then the binding
           and the claim of item 1, then **mark the record complete**. The
           record is not removed here (round 9; ledger C12, item 5). On the
           resume path the record and the skeleton already exist, and this
           step writes only the missing binding and claim, then marks the
           record complete.
        The witness is not locked, so its claim can be released between the
        read phase and the write phase. That window is accepted: the child's
        claim is written for the mission as read, the open-mission re-check
        bounds it, and the spec's guard is itself evaluated against the
        state at the time of the operation. The model takes the read phase
        and the start of the write phase as one step and does not examine
        this window (spec, "What this model does not establish"). The same
        holds for the child's own claim read in the read phase.
      - *Lock constraint (round 6).* `Ensure` takes the roster lock
        (`Store.withLock`) and `claim-forked` takes the child's
        dispatch-pending lock outermost. They run **one after the other,
        never nested**, in either direction. The reason is the order
        `deleteFiles` already imposes: it runs inside the roster lock and
        then takes the dispatch-pending lock (`store.go:201-203,279`), and
        no code path takes the dispatch-pending lock and then the roster
        lock (`active.go:619-625`). A `claim-forked` write phase that called
        `Ensure`, `Join`, `Leave`, `Delete`, or anything else that takes
        the roster lock while holding the dispatch-pending lock would
        invert that order and could deadlock against a concurrent
        `Delete` or `Purge`. The write phase therefore uses only lock-free
        roster reads (`Load`, `ParentOf`): it receives a reader interface
        with those two methods and no others, so the compiler enforces the
        constraint. Callers run `ensure` to completion, then
        `claim-forked`.
        **The compiler enforces this only under two conditions (round 8),
        and both are implementation requirements.** (1) The write phase
        lives in `internal/mission`, which `internal/session` imports
        (`store.go:13`) and which therefore cannot import `session` or hold a
        `*session.Store`: its only handle on the roster is the interface it
        declares. Were the write phase in `internal/hook` or `cmd/ethos`,
        both of which import `session`, nothing would stop it from calling
        `Ensure` on a store it can reach. Because `mission` cannot name
        `session.Roster`, the interface declares methods over plain types
        (a roster-state method returning nil, `fs.ErrNotExist`, or another
        load error; and `ParentOf`), and a small adapter in the caller
        implements it over `*session.Store`. (2) `RefuseIfSessionGone` is
        re-expressed over that interface. Today it takes `*session.Store`
        (`pretooluse_dispatch.go:494`); a write phase that called it would
        have to be handed the whole store, which carries every locking
        method. `runMissionClaim` and the dispatch path call the
        re-expressed function through the same adapter.
   3. *Delegation id allocation.* The id comes from the mission-scoped
      counter, `NewID`, with commit-or-rollback exactly as the dispatch
      path does it: a deferred `releaseID(success)` that returns the
      counter if the call fails before the skeleton is on disk, and
      `success = true` the moment `WriteDelegationSkeleton` returns, after
      which the id is never rolled back because reuse of an id whose
      skeleton exists would collide (`pretooluse_dispatch.go:775-787,
      853-861`).
   4. *Exit statuses (round 6, amended in round 9): one status per cause.*
      Round 5 split the outcomes three ways. That left several real causes
      unclassified. The table assigns every cause exactly one of four
      statuses: success (0) and three non-success statuses, **witness
      refused**, **write failed**, and **do not retry** (round 8 corrects
      round 6's "four non-success statuses", which the table never had). The
      numbers are fixed in the implementation PR; the requirement is that the
      three non-success statuses are distinct from each other, from 0, and
      from the existing statuses 1 (generic error) and 2 (usage error and
      `failClosed`, `cmd/ethos/main.go:64-69`), and that a test covers every
      row. Each row names two callers: the OpenCode plugin, which can retry on
      its next `execute.before`, and Claude Code's `SessionStart` hook, which
      runs the same two steps once per fork start (§2.5) and has no second
      trigger.

      | Status | Causes | Plugin action | Claude Code `SessionStart` action |
      |---|---|---|---|
      | **success** (0) | The child is now claimed and bound. Or the child already held a claim ("already claimed"; the pending record is reconciled first, and a live record is kept and marked complete). | Memoize "claimed" for the refresh interval (15 minutes), so a claim lost to a purge is retried after the next re-registration. | Nothing to do. |
      | **witness refused** | The child's lineage does not reach the witness; an ancestor roster or the witness's own roster is missing or fails to load; `Store.ParentOf` fails with an error other than not-found (an unreadable sidecar: the chain cannot be established, so it fails closed and the error is reported, as for a roster that fails to load); a repeated id; the witness holds no claim; the witness binding's origin is not `claim`; **the witness's claimed mission is not open** (found closed at the re-check of write phase step 6, on the resume path as on the fresh one, or at the post-check after the skeleton, item 1: the call has reconciled and cleaned up first, and a claim on a closed mission is a stale claim, which a more distant ancestor may not share). | Try the next ancestor as witness, nearest first. When none is left, log once and memoize "unattributed" for the refresh interval (15 minutes, §2.1.1). | Offer each ancestor in the recorded chain in turn, nearest first. When none is left, log one line to stderr and leave the fork unclaimed; the hook does not retry. |
      | **write failed** | The child's dispatch-pending lock, the mission lock, or the delegation lock cannot be acquired; the mission or delegation lock for an orphan skeleton's close cannot be acquired; the pending record, skeleton, binding, or claim write fails, or the final mark-complete write fails; an earlier orphan skeleton could not be closed for a reason a retry can cure, such as a lock or an I/O error (item 5), including the close that a refusal or an already-claimed exit performs first; the record could not be removed after its skeleton was closed (item 5; the next call finds the skeleton closed and removes the record); the post-check abort or in-process cleanup of item 5 failed to close the skeleton, whatever caused the abort; the child's claim vanished between the read phase's claimed-child check and the lock (write phase step 3). **Reads that fail with an I/O error rather than "absent":** `ReadActiveMissionBinding` on the child's own claim (read phase step 2) or on the witness's binding (step 3), and `ReadDelegationBinding` on the child's binding during a reconcile. An unreadable file is not an absent one: the call has written nothing, and classifying a witness binding read error as "witness refused" would try a more distant ancestor and could attribute the child to that ancestor's mission instead of the nearest's. **Process death by a signal** (OOM kill, a spawn timeout enforced by the plugin) has no exit status at all and is classified here: it is the model's crash (`LcCfCrash`), which leaves whatever was written. | Retry the **same** witness on the child's next `execute.before`, with no memo. Each retry reconciles first, so leftover state is cleaned up before a new write. Log each failure, and for a signal death log the signal. | Log one line to stderr naming the cause. The hook does not retry (it fires once per start, and only the `fork` source calls `claim-forked`), so the fork is unclaimed until an operator runs `ethos mission claim-forked` for it or `ethos mission claim`. The next `claim-forked` call reconciles whatever this one left. |
      | **do not retry** | The child's roster is gone (the read-phase `ss.Load` or the write phase's `RefuseIfSessionGone` reports `fs.ErrNotExist`) or unreadable (only the read-phase `ss.Load` refuses on this: `RefuseIfSessionGone` warns and proceeds on any error other than `fs.ErrNotExist`, `pretooluse_dispatch.go:494-508`, so a roster that exists but fails to parse is caught in the read phase, not the write phase); the mission or its contract fails to load (a mission that loads and is not open is "witness refused", above); `NewID` fails or its counter is exhausted; `tierBGlobalRoot` or another root resolution fails, including `resolve.StoreRepoRoot()` returning no root when the record is to be written, and **a record whose recorded store root is empty or does not resolve** at a reconcile (B2, §2.1.1); **the `claim-forked-pending` record does not parse, or a skeleton's `record.yaml` does not parse** (round 8: retrying cannot repair a corrupt file, and "write failed" would retry it on every tool call for ever; the error names the file, and the remedy is to remove it by hand; the record can now fail to parse for as long as the fork claim lasts, item 5). **Exit status 1 (generic error) and exit status 2** (usage error, `failClosed`, **and a Go runtime panic, which exits 2** and cannot be told from a usage error by its status). | Do not try another ancestor and do not retry in this call. Log; for status 2 log the captured stderr, which carries the panic trace if there was one. Memoize the outcome for the refresh interval, so a broken record costs one subprocess per interval per session; the memo expiring is the retry, and the next call reconciles whatever a panic left. For "child roster gone", also drop the session's `ensureRegistered` memo, so the next call re-registers it first. | Log one line to stderr; do not retry and do not offer another ancestor. The hook runs the write phase behind a `recover`, so a Go panic in process is logged and treated as this status, and it runs under the shell wrapper's fail-open guard (`session_start.go:60-62`), so no status can fail the session start. |

      Usage errors keep status 2 (bad flags). An error that carries no
      status maps to the generic status 1, which the plugin treats as
      "do not retry". The command wraps every failure in a typed error that
      carries its status, and a test asserts that no return path in
      `claim-forked` yields an untyped error.
      **The wrapper needs `errors.As`, and it must be consulted first (round
      8; round 9 adds the order).** `main()` at `cmd/ethos/main.go:51-71`
      classifies the returned error with direct type assertions
      (`err.(usageError)`, `err.(failClosed)`), which do not see a typed error
      wrapped by `fmt.Errorf("...: %w", err)`, and then `isUsageError`
      (`main.go:85-100`) classifies by **message prefix**: `unknown command`,
      `unknown flag`, `required flag`, `accepts`, `invalid argument`, and the
      rest of that list, with status 2. The implementation maps the three new
      statuses with `errors.As` **before** both the direct assertions and the
      prefix test. Were it consulted after them, a typed error whose message
      happened to begin with one of those prefixes (an error text built from
      a flag name, for example) would exit 2, which the table assigns to "do
      not retry", and the plugin would not retry a "write failed". A test
      wraps each typed error once, and once more under a message that begins
      with each prefix of `isUsageError`, and asserts its status survives.
      The exit statuses stay a plugin-facing contract: a panic (status 2)
      and a signal death are the only outcomes the command cannot classify
      itself, which is why the table names them.
   5. *No orphan skeleton from a retry; the pending record is durable (round
      6, amended in rounds 8 and 9).* Round 5 closed an orphan only when the
      same process survived, or when the retry named the same mission. A
      retry with a different witness or a different mission would leave the
      old open skeleton in place. The rule now is the amended operations of
      the lifecycle model (`docs/spec-opencode-lifecycle.tex`: `LcCfRefuse`,
      `LcCfReconcileClaimed`, `LcCfReconcileReuse`, `LcCfReconcileFresh`,
      `LcCfLockReuse`, `LcCfFinish`, `LcCfAbort`, `LcReleaseClaim`,
      `LcClearClosed`).
      - *The pending record is durable (round 9; ledger C12,
        `fixDurableName`, the leader's ruling).* Before
        `WriteDelegationSkeleton`, the write phase writes
        `sessions/<child>/claim-forked-pending` (YAML: delegation id,
        mission id, witness, **stage**, and the **mission store root**) under
        the child's dispatch-pending lock. The stage is one bit, *complete*
        or not (the model's `pendDone`). The record is written not complete
        and is marked complete as the last step of a successful write, after
        the claim. **It is not removed at a successful finish.** It persists
        for the life of the fork claim and is the durable name of the fork
        skeleton. Round 8 removed it "as the last step of a successful
        write", which left the delegation binding as the only name of a
        completed propagation. The child's own Tier B dispatch writes its
        binding over that one (`pretooluse_dispatch.go:880-884`, which this
        design does not change), and the fork skeleton is then open and named
        by nothing (C12, run `C12_off`, eleven steps; `C12_on` holds, 257,269
        states, all visited). Every close of the design is by name, so the
        name must outlive the binding. The record is removed **only** by
        `mission release`, by the close-time clearing of the claim, and by
        `deleteFiles` (step 4), and each of the three first closes the fork
        skeleton the record names (an open skeleton of kind `dkFork`
        that the session owns, closed `aborted` as a compare-and-set on
        verdict `open`, below) and then unnames it. The record also names the
        store root it was written under (B2, §2.1.1), which every one of those
        closes uses. The record file is written through `writeSidecarFile`,
        so a crash leaves `claim-forked-pending.*.tmp`, which `deleteFiles`
        removes (§2.1.1 step 4). A record that does not parse is "do not
        retry" (item 4) for as long as the claim lasts, not only during a
        write; the error names the file and the remedy is to remove it by
        hand, or `ethos session delete`, which fails at step 1 for the same
        reason until it is removed. This durable record is the single
        mechanism the design relies on. The release-close and supersede
        logic below are retained as defensive measures only, and are labelled
        unexercised where the model shows them so.
      - *Live, and the three outcomes of a reconcile (ledger C4, C9, C12).*
        A record is **live** when the child's claim is for the mission the
        skeleton belongs to **and** either the record is complete or the
        child's delegation binding names the skeleton. A complete record is
        live whatever the binding names, because a Tier B dispatch by the
        child may have moved the binding; an incomplete record is live only if
        the binding names it too. Every exit that reconciles (item 2) does one
        of three things:
        - *The call is a refusal (read phase), or the child already holds a
          claim (the claimed case; `fixInertBinding`).* If the record is
          live, it is **kept and marked complete**: the claim and the
          skeleton are in use, and an incomplete live record means the crash
          fell between the claim write and the mark. Otherwise the call
          closes the skeleton the record names, `aborted`, and removes the
          record. The binding of a record that is not live is inert (it
          names a mission that is not the child's claim), so the skeleton is
          closed. A refusal never resumes the record (item 2). The old
          statement that a skeleton "stays open only until its own mission
          closes" is withdrawn: the skeleton blocks `Abandon` and is named by
          nothing that will be read again (run `Fa_inert`;
          `MutLiveNoDone` shows that judging a complete record by the
          binding alone would close a fork skeleton that a child is working
          under, as soon as a Tier B dispatch had moved its binding).
        - *The child holds no claim and the record is resumable
          (`LcCfReconcileReuse`).* The record is **resumable** only when all
          of these hold: it is not complete, its skeleton is open, the
          record's mission and witness equal this call's, the mission is open,
          and the child has no claim. A **complete record is never
          resumable.** That state (a complete record, a child with no claim,
          an open skeleton, an open mission) is unreachable with every
          amendment on, because release, close-time clearing and
          `deleteFiles` each close the skeleton and remove the record in one
          step; the guard is defensive and unexercised (spec N18;
          `MutReuseDone` survives). If it were ever met, the record would be
          closed and removed like any other that is not resumable. The call
          reuses the delegation: it takes the locks of step 5 and re-reads
          the mission (step 6; C15), then writes only the missing binding (if
          the child's binding does not already name the delegation) and the
          claim, and marks the record complete.
        - *The child holds no claim and the record is not resumable
          (`LcCfReconcileFresh`).* The call closes the skeleton the record
          names (an already-closed skeleton is left as it is; a skeleton
          that was never written, `fs.ErrNotExist`, because the crash fell
          between the record and the skeleton, needs no close), removes the
          record, and goes on to allocate an id and write afresh.
      - *Supersede, with its scope stated exactly (ledger C9,
        `fixSupersede`).* On the two paths that go on to write, the fresh
        path and the resume path, with or without a record, the reconcile also
        closes the open fork skeleton that the child's delegation binding
        names when it is not the skeleton the call will use: on the fresh
        path every such skeleton, on the resume path the one that differs from
        the record's. It is **never** applied by a refusal or in the claimed
        case, whose child either does not write or holds a claim and may be
        working under the binding (`MutSupClaimed`). Round 8 worded this as
        "a reconcile that does not resume the record"; that contradicted the
        resume path's own clause and is withdrawn. Round 8 also traced the
        need to a completed propagation, a release of the child's claim, and
        a second propagation. That trace is withdrawn: `mission release`
        clears the binding as well as the claim (`cmd/ethos/mission.go:2145-
        2153`), so after a release there is no binding to overwrite. The state
        is reached by the close-time clearing of another mission instead
        (run `C9_nosup`): the child's operator claim of a second mission is
        closed by the child's own session, `ClearMissionBindings` clears the
        claim, which names the closed mission, and leaves the binding, which
        names the first fork skeleton of the first mission (the two tests are
        independent, `active.go:907-935`), and a second `claim-forked` then
        overwrites that binding. **With the durable record the clause is
        redundant for `NoOrphanOpenSkeleton`:** the record names the first
        skeleton, so `C9_nosup_dur` holds and `Reach_nosup_dur` shows the state
        is unreachable without the clause. The resume path's clause is
        **unexercised**: with every amendment on, no state has a record, a
        child with no claim, and a binding that names an open fork skeleton
        other than the record's (`Reach_all`, 257,269 states, all visited,
        none), and removing the clause changes no invariant (`MutSupReuse`
        survives). It is kept as defensive code at the leader's ruling and is
        **not verified by the model**.
      - *Release and close-time clearing close what they unname (ledger C11,
        C12; spec N16).* `mission release` (`cmd/ethos/mission.go:2135-2179`)
        and the close-time clearing of the claim (`ClearMissionBindings`,
        `internal/mission/active.go:907-957`, called by `clearClosedSessionBindings`,
        `mission.go:2289-2312`, and by the MCP close path,
        `internal/mcp/mission_tools.go:555`) are changes to
        existing Claude Code paths and belong to the implementation PR. Each,
        for a session that has a record, **first closes** the open fork
        skeleton the record names (kind `dkFork`, owned by the session,
        `aborted`, compare-and-set on `open`, using the recorded store root
        and the locks of step 2), **then clears the binding, then removes the
        record**. The close-time clearing does this only when the claim it
        clears names the mission being closed (`LcClearClosed`); a close of
        some other mission leaves the claim and the record alone, and clears
        the binding independently only if the binding names the closed mission
        (the existing test). Before this round `release` left the skeleton
        open after it cleared the claim and the binding (C11, run
        `Fa_orph_norel`, eleven steps). A binding that names a Tier B skeleton
        is cleared and the skeleton is left open: it is a subagent's
        delegation. `release` stays idempotent: with no claim and no record it
        closes nothing and succeeds. The model also closes what the
        *binding* names at release (`fixReleaseCloses`); with the durable
        record both name the same skeleton (`Fa_orph_norel_dur` holds without
        the binding-named close), so that close is defensive and unexercised.
        That a release removes the record is hygiene, not an invariant (spec
        N18, `MyRelKeepRec` survives). **Implementation constraint (spec
        N16):** both commands must hold the **child session's
        dispatch-pending lock around the whole close, unname, clear
        sequence.** Today both clear unlocked: `runMissionRelease` calls
        `ClearActiveMission` and `ClearDelegationBinding` with no lock and
        takes the lock only for `ClearDispatchPending`
        (`mission.go:2145-2171`), and `ClearMissionBindings` takes it only
        for `ConsumeDispatchPending` (`active.go:951`). Without the lock the
        closes of C11 and C12 race a write phase of `claim-forked`, and the
        model, whose operations are atomic, cannot show it.
        `WithDispatchPendingLock` is not reentrant (a second acquire from the
        holding process blocks on itself; its doc comment and
        `clearMissionSidecarsLocked` say so), so both are restructured to take
        the lock once at the top and call the unlocked
        `ClearDispatchPending` or `ConsumeDispatchPending` inside, as
        `deleteFiles` does (`store.go:278-301`). The lock order is the
        write phase's: dispatch-pending outermost, then the shared mission
        lock, then the delegation lock; never the roster lock.
      - *Every skeleton close is a compare-and-set on verdict `open`, under
        the delegation lock (round 9; ledger C14, `fixCas`).*
        `CloseDelegationSkeleton` (`internal/mission/delegation.go:649-692`)
        loads the record and rewrites it with the verdict it is given,
        whatever the record now says. `Store.Close` sweeps open skeletons
        holding the exclusive mission lock, but when it cannot take it
        (`store.go:1107-1114`) it sweeps with no exclusion, and
        `closeDelegationSkeletons` (`store.go:1217-1246`) reads a record,
        finds it open, and calls the close later: a reconcile can close the
        same skeleton `aborted` in between, and the sweep then writes its own
        verdict over it (run `Fv_off`, ten steps). The close is therefore
        amended to load the record **under the skeleton's delegation lock**
        and write only if the verdict is still `open`; a record that is not
        open is left untouched and the caller treats it as closed. The sweep,
        in the locked and the unlocked branch alike, takes the delegation lock
        of each skeleton it closes (after the mission lock when it holds one,
        the order of this item); it needs the global root, which `Store`
        holds as `s.root` (`DisclaimDelegation` passes it,
        `store.go:1589`). A verdict, once not open, never changes. Every
        caller gets the compare-and-set, the depth-gate close of
        `dispatchTierB` (`pretooluse_dispatch.go:870`) included. Test: a
        reconcile close and the unlocked sweep forced to interleave (a seam
        between the sweep's read and its write) leave verdict `aborted`.
      - *Every close writes verdict `aborted` (spec N5), except the sweep's.*
        The model has one bit, open or not, and since C14 one more, aborted
        or not. `countBlockingDelegations`
        (`internal/mission/store.go:1973`) counts every delegation that is
        neither `aborted` nor disclaimed, so each of the closes above (the
        reconcile, the refusal, the claimed case, the supersede, the
        post-check abort, `release`, the close-time clearing, and
        `deleteFiles` step 1) must write verdict `aborted`. A close with any
        other verdict removes the skeleton from the model's open set and
        leaves it blocking `Abandon` in the code. The sweep of
        `Store.Close` writes the mission's own verdict (`pass`, `fail`, or
        `aborted` for an escalation). A test per close site asserts the
        verdict.
      - *A close failure is never silent.* The call uses
        `mission.CloseDelegationSkeleton` directly and propagates its
        error. It does not use `closeDelegationAborted`
        (`pretooluse_dispatch.go:991-1014`), which only writes the error to
        stderr. On a close error the call leaves the record in place and
        exits "write failed" (or "do not retry" for a record that does not
        parse, item 4), so the next retry attempts the close again. A failure
        to remove the record after a successful close is likewise "write
        failed" (item 4); the next call finds the skeleton closed and removes
        the record.
      - *In-process cleanup.* If the write phase fails after the skeleton
        is on disk and the process survives (including the post-check abort
        of item 1), it closes the skeleton `aborted` (same function, same
        error rule), removes a binding it wrote, removes the record, and
        exits "write failed", or "witness refused" when the cause is a
        mission that is no longer open. If the cleanup close itself fails, the
        call exits "write failed" whatever the cause (item 4): the record
        stays in place, and the retry reconciles it first. A failure to mark
        the record complete, after the claim is written, writes nothing else
        to undo: the call exits "write failed", the next call finds the child
        claimed and the record live, and marks it complete. The counter slot
        stays consumed (item 3), so the retry allocates a fresh id.
      - *What the model leaves out (spec N3, N16, N17, N18).*
        `Store.Close`'s sweep skips a delegation record it cannot load and
        only logs a failure to close one
        (`internal/mission/store.go:1236,1242-1244`), so a skeleton the sweep
        misses is not an error anywhere. A skeleton directory with no
        `record.yaml` makes `countBlockingDelegations` return an error
        (`internal/mission/store.go:1994-2000`), which refuses `Abandon` for
        a reason the model has no state for. The design's closes do not rely
        on the sweep; this item records that the sweep itself is
        best-effort. The Tier B dispatch is modelled as one atomic step
        (N9, N17): a dispatch without a roster, the depth-gate close of its
        own skeleton with verdict `aborted` (`pretooluse_dispatch.go:870`),
        and a binding write that fails without being fatal (`:880-889`) are
        not represented.
      - The implementation PR tests each interrupt point (after the
        record, after the skeleton, after the binding, after the claim,
        after the claim but before the mark), plus a retry with a different
        witness and a retry with a different mission, a close that fails, a
        refusal after a crash (C1), an operator claim of a different
        mission after a crash at the binding (C4), an operator claim of the
        same mission after a crash at the binding (the record is live and is
        marked complete, the skeleton stays open and in use), a Tier B
        dispatch by a propagated child over its completed fork claim, then
        `release`, `deleteFiles`, and the close-time clearing each closing the
        fork skeleton the record still names (C12), a complete record that a
        later call finds with a live claim and one whose claim was replaced
        (live and not live), a second propagation after the close-time
        clearing of another mission (C9), a mission closed by the unlocked
        sweep between the record and the skeleton (C6), the sweep and a
        reconcile close interleaved (C14), the resume path taking the step-5
        locks before it writes (C15), a mission directory that is gone
        (item 2 step 2), a record with an empty or unresolvable store root and
        one run from a different working directory (B2), `release` and the
        close-time clearing racing a write phase (N16), and a record or
        `record.yaml` that does not parse.
   6. *Failure surfaces; it is never swallowed.* The plugin logs every
      non-success status. Until a retry succeeds the child's commits carry
      no trailers: the safe direction.

   **Deviation from the spec, stated (round 5).** The spec's
   `AllocateDelegationForkPropagated` leaves `delegationOf' = delegationOf`
   (`docs/spec-opencode-integration.tex:1017-1022`): it records the
   delegation and the child's claim but never binds the child to the new
   delegation. This design's `DelegationBinding` write (round 2, finding 5)
   is needed because `WriteCommitTrailers` emits a `Delegation:` trailer
   only from that binding, and it is not modeled. It is therefore a
   deviation from the formal model, not a refinement of it. The
   implementation PR must amend `docs/spec-opencode-integration.tex` so
   that the operation sets `delegationOf' = delegationOf \oplus \{ sid?
   \mapsto id? \}` (and re-run fuzz and ProB on the amended spec), in the
   same PR, under this repo's rule that a change to lifecycle logic amends
   its spec (`docs/development.md`, "internal/mission lifecycle logic").
   No code for this primitive ships before that amendment is in the PR.
3. **Fork wiring for both harnesses, in the same implementation stage.**
   `claim-forked` and the `parent-session` sidecar (points 1-2) are harness-neutral
   (§2.5), so two triggers call them: the OpenCode plugin on
   `session.forked` (point 4), and Claude Code's `SessionStart` hook when it
   observes `source: "fork"`. The Claude Code trigger is a change to
   `internal/hook/session_start.go`. For **every** source (round 9; ledger
   C13) it registers the session through `Store.Ensure` in process, without
   `touch`, in place of `createSessionRoster`'s `CreateInCheckout`
   (`session_start.go:69,194`): the `fork` source passes the payload's
   parent as `parentSession` (with the `parentGone` retry of §2.5), every
   other source passes the parent `Store.ParentOf` records. For the `fork`
   source it then calls `claim-forked`. It is not a separate future concern:
   shipping only the OpenCode trigger would leave Claude Code's identical gap
   open and reintroduce a parity difference between the harnesses.
4. **`.opencode/plugins/ethos.ts` (or an npm-published equivalent) —** the
   TypeScript plugin itself, implementing §1-§3's three hooks (the OpenCode instantiation of the same Registry model), calling the
   existing `ethos` binary as a subprocess (the same pattern
   `commit-msg.sh` already uses to call `ethos hook commit-trailers`) for
   every state-mutating action. No direct file access to
   `.punt-labs/ethos/` from TypeScript — every write goes through the Go
   binary, so there is exactly one implementation of the Registry's
   locking and validation discipline (`AcquireDispatchPendingLock`,
   per-session flocks), not two. The plugin's scope also includes: the
   `session.deleted` handler that runs `ethos session delete` (§2.6, the
   OpenCode `EndSession`); the in-process `ensureRegistered` memo with its
   15-minute refresh interval (§2.1.1); abort-on-inconclusive lineage
   (§2.1.1); the two-armed environment policy and the `singleSession`
   option, default off (§3.2.1); and the Effect shape (§1.1).
5. **Coequal per-harness enablement: `ethos enable [claude] [opencode]`,
   with harnesses named explicitly or chosen at a prompt, and neither
   privileged.** Read directly,
   `internal/enable/enable.go`'s `EnableTo` is not harness-neutral today: its
   deposit step is Claude-Code-specific (`CanonicalImport =
   "@.punt-labs/ethos/CLAUDE.md"`, written by `claudemd.Register` into the
   repo `CLAUDE.md`, `enable.go:46,127`). Bolting `--opencode` onto it would
   make Claude Code the unqualified default and OpenCode an opt-in
   qualifier. The design instead splits `EnableTo` into two layers:
   - **Harness-neutral repo steps**, which neither harness owns: the gitlink
     guard, the vendored-guide deposit, `.gitignore` protection,
     commit-msg/pre-commit hook chaining, and the
     `.punt-labs/ethos/enabled` marker. Every selection runs them
     idempotently, **in this order: gitlink guard, deposit, gitignore,
     hook chaining, then the marker last** (round 5 corrects round 4's
     claim that this is "exactly as `EnableTo` orders them now"). **This is
     a reorder of today's code.** `EnableTo` writes the marker at
     `enable.go:119-125`, then the `CLAUDE.md` import (`127-135`), and only
     then calls `chainHooks` (`137`), so today a marker can be present
     before hook chaining has run, and a `chainHooks` failure leaves an
     enabled repo with no commit-msg hook. Moving `chainHooks` ahead of the
     marker makes "marker present implies the vendored zone, the
     gitignore protection, and the hooks are all in place" true. The
     harness-native step runs after the marker, as the import does today;
     the marker's meaning is "the neutral steps are complete", not "a
     harness is wired".
   - **One harness-native step per subcommand**: `ethos enable claude`
     writes the `CLAUDE.md` import line; `ethos enable opencode` deposits
     `.opencode/plugins/ethos.ts` and registers it in `opencode.json`'s
     `"plugins"` array (`.tmp/plugins-web.mdx:33-38`'s config shape).
     A harness step writes only its own artifact and never the other's. A
     repo using both harnesses enables both, by naming both or by accepting
     the default prompt selection below.

   **Selecting the harnesses (operator ruling, 2026-10-22; replaces round
   4's usage error for bare `ethos enable`).** Round 4 made bare `ethos
   enable` a usage error. The operator ruled that a bare invocation on a
   terminal should ask instead. `ethos enable` takes zero or more harness
   names:
   - *Named:* `ethos enable claude`, `ethos enable opencode`, and `ethos
     enable claude opencode` (any order, duplicates ignored) enable exactly
     the named harnesses and never prompt.
   - *None named, interactive terminal:* `ethos enable` prompts the user to
     choose which harnesses to enable. The prompt lists every supported
     harness, `claude` and `opencode`, with **both preselected** as the
     suggested default; the user confirms or changes the selection. A
     selection that is empty after the prompt makes no change and exits
     non-zero with `no harness selected`; it never falls through to a
     default.
   - *None named, no interactive terminal* (CI, a script, piped stdin):
     exit non-zero with a usage error that names both harnesses, for
     example `ethos enable: no harness named and no terminal to ask; name
     one or more of: claude, opencode`. It never silently picks one or
     both.
   - *Unknown name:* a usage error that lists the supported harnesses.

   "Interactive terminal" is the existing `isTTY` test on standard input
   (`cmd/ethos/setup.go:808`, `term.IsTerminal`), the check the repo's
   other prompting command uses.

   **The guide is harness-neutral (round 6, extended in round 8).**
   `deposit` writes the guide
   to `.punt-labs/ethos/CLAUDE.md` (`deposit.go:15,87`). That path is the
   org standard's and does not change. Its content does: it carries no
   instruction that only one harness can follow. Today's guide has seven such
   parts, all in `internal/enable/guide/CLAUDE.md`, and each is neutralized
   or moved to the Claude Code native artifact (rounds 7 and 8 enumerate
   them):
   - line 11, "deliberately not `@`-imported here": `@`-import is Claude
     Code's include syntax. Reworded without it, for example "is
     deliberately not loaded into agent context here";
   - lines 19-20, the persona-injection sentence, "Session hooks inject your
     persona into context at start and after compaction." The injection is
     done by Claude Code's `SessionStart` and `PreCompact` hooks. The
     OpenCode plugin registers no session hook (§1.2 item 4) and injects
     nothing, so in an OpenCode session the sentence is false. It moves to
     the Claude Code native artifact, and the shared guide keeps only what is
     true of both harnesses: `ethos whoami` and `ethos iam` resolve and
     declare an identity;
   - lines 20-21, the `.claude/agents/<handle>.md` generation and "restart
     Claude Code" note;
   - lines 43-44, "the write-set is enforced at runtime, so an edit outside
     it fails the mission". Write-set enforcement is carried by Claude Code
     hooks (`write_set` is enforced mechanically only for verifier spawns
     and only for `Write` and `Edit`, `docs/development.md`, DES-069); a
     harness without those hooks enforces nothing, so the shared guide must
     not promise it. The shared sentence keeps the instruction ("commit one
     logical step at a time and stay inside the mission's write-set") and
     drops the enforcement claim; the claim that an out-of-set edit fails
     the mission moves to the Claude Code native artifact;
   - lines 58-67, the Review agents section, which says `ethos seed`
     deploys the agents to `.claude/agents/`;
   - lines 69-74, the local review sequence, which invokes those agents;
   - line 84, the Gotchas entry "Agent types are discovered at
     SessionStart; restart after adding one", a Claude Code behavior.
   The shared guide keeps the harness-neutral content: what the review
   agents are for and the order to run them, reworded without naming a
   directory or a harness. The Claude Code specifics move to that harness's
   native artifact. **`validate-content` enforces this (round 7, widened in
   round 8, derived from the harness list in round 9):** a check rejects the
   embedded guide (`internal/enable/guide/CLAUDE.md`) and this repo's
   deposited copy if either contains any term of the **deny list**. Round
   8's list was written by hand (`.claude/`, `Claude`, `SessionStart`,
   `CLAUDE_`, `subagent`). It named no OpenCode term, so `opencode.json`, a
   `.opencode/plugins/` path, or the word `OpenCode` could enter the shared
   guide unnoticed, and a hand-written list drifts the day a harness is
   added. The deny list is therefore **computed from the harness list**
   (`internal/enable/harnesses.go`, below) by an exported accessor that
   `validate-content` calls: for every entry, its name (`claude`,
   `opencode`), every harness-specific literal the entry carries (Claude
   Code's import line and `.claude/` directory; OpenCode's `opencode.json`,
   `.opencode/plugins`, and the plugin file name), and the entry's
   **vocabulary**, a list of terms of that harness that are neither a name
   nor a path (Claude Code: `SessionStart`, `PreCompact`, `CLAUDE_`,
   `subagent`; OpenCode: `Plugin.define`, `ctx.`). A harness entry that
   adds a term adds it to the deny list with no other edit, and the
   companion test below injects a third entry and asserts that its terms
   are denied. The list also carries one **pattern**: any `@`. `@`-import
   is Claude Code's include syntax, the guide has no other use of the
   character (an email address or a handle does not appear in it), and
   line 11's "`@`-imported" is the case it exists for. Matching is
   case-insensitive. The single **allowlist** entry is the file name
   `CLAUDE.md`, matched by exact equality of the whole word: the guide's own
   path is the org standard's (`.punt-labs/ethos/CLAUDE.md`) and the
   code-reviewer description says "CLAUDE.md-compliance". The check strips
   each occurrence of `CLAUDE.md` that is a whole word (the character before
   it and the character after it are not a letter, a digit, or an
   underscore) before it looks for the deny list. So `CLAUDE_PID`, `Claude
   Code`, `OLDCLAUDE.md`, `CLAUDE.mdx`, `opencode.json`,
   `.opencode/plugins/ethos.ts`, `OpenCode`, and `@.punt-labs/ethos/CLAUDE.md`
   fail, while `.punt-labs/ethos/CLAUDE.md` and `CLAUDE.md-compliance` pass.
   A test feeds each of these strings to the check. The check runs in `make
   check` beside "CLAUDE.md guide embed sync", so a harness name cannot
   return to the shared guide unnoticed.
   Each
   harness's native step wires the one shared file in:
   - `ethos enable claude` writes the `@.punt-labs/ethos/CLAUDE.md` import
     into the repo `CLAUDE.md`.
   - `ethos enable opencode` adds `.punt-labs/ethos/CLAUDE.md` to the
     `instructions` array of `opencode.json`. **Verified by the leader
     against context7 (`/anomalyco/opencode`, dev branch):**
     `services/www/src/docs/content/instructions.mdx` and `config.mdx`
     document `instructions` in `opencode.json(c)`. Entries are relative
     paths or globs (searched from the workspace up to the project root),
     `~/` paths, absolute paths, or http(s) URLs. They load after
     `AGENTS.md` files, in listed order, and entries from all config
     documents are combined. Missing files are skipped. The addition is
     idempotent: append the entry only if absent, and preserve the existing
     entries and their order. (Round 6 first recorded this as blocked because
     `points/docs/opencode/06-instructions-context.md:63-65` calls the
     array inactive; that note is out of date.)
     **Ownership and normalization (round 8).** *Absent* is decided after
     normalizing both sides: a leading `./`, repeated separators, and `.` and
     `..` segments are resolved lexically (the `path.Clean` rule), so
     `./.punt-labs/ethos/CLAUDE.md`, `.punt-labs//ethos/CLAUDE.md`, and
     `.punt-labs/ethos/../ethos/CLAUDE.md` all count as the entry. Globs,
     `~/` paths, absolute paths, and URLs are compared as written and not
     equated with the relative spelling. `enable` appends the entry in the
     canonical spelling `.punt-labs/ethos/CLAUDE.md` only when it is absent,
     and **records that it added it**, in ethos's own state under
     `.punt-labs/ethos/` and not in `opencode.json`, which the operator
     edits. When the entry was already present, in any spelling, `enable`
     records nothing. `ethos disable opencode` removes the entry only if the
     record says `enable` added it, matching by the same normalization, and
     then deletes the record; an entry the operator added, or an entry
     `enable` added that the operator has since removed, is left alone, and
     `disable` reports which case applied. Tests: each equivalent spelling is
     found; an operator-added entry survives `enable` and `disable`; an
     entry `enable` added is removed by `disable` and an unrelated entry
     with the same prefix is not.
   - Harness-specific operating notes live in each harness's native
     artifact, not the shared guide: the Claude Code plugin's commands and
     hook-injected context for Claude Code, and the header of the deposited
     `.opencode/plugins/ethos.ts` for OpenCode.
   The embedded source stays `internal/enable/guide/CLAUDE.md`
   (`enable.go:32`); `validate-content` keeps it in sync with this repo's
   own deposited copy, so rewriting the guide updates both in one PR.

   **One source of truth, enforced (round 6).** The supported harnesses are
   a single list in one file, `internal/enable/harnesses.go`, sorted by
   name (so position privileges neither), one entry per harness: its name,
   its enable step, its disable step, an `enabled?` probe (CLAUDE.md import
   present; plugin file present and registered), and every harness-specific
   literal (the import line, the plugin path, the config key). The prompt
   choices, the usage-error text, the unknown-name error, the command's
   help text, and `ethos disable`'s prompt are **generated from that list**
   by one function; none is a hand-written string that names a harness.
   Adding a harness is one entry. Neither harness is the value used when
   the user gives no answer. The implementation stage adds
   `TestHarnessNamesOnlyInList`, which:
   - parses every non-test `.go` file in `internal/enable`, **all of
     `cmd/ethos`**, **`internal/doctor`**, and **`cmd/validate-content`**
     (round 9; round 8 scanned `cmd/ethos/enable.go` and
     `cmd/ethos/disable.go` only, which left the other places that name a
     harness unscanned) with `go/parser`;
   - fails if any string literal contains `claude` or `opencode`
     (case-insensitive) in any file except `harnesses.go`. **Round 8:** an
     import path is no longer exempt (next item), and a literal is allowed
     elsewhere only by **exact equality** with an entry of a short explicit
     allowlist in the test, never by substring or prefix. **The allowlist is
     the one literal `.punt-labs/ethos/CLAUDE.md`** (round 9; round 8 also
     allowed the base name `CLAUDE.md`, which any remedy text can contain),
     the org-standard guide path, which names a file and not a harness; a
     `//go:embed` directive is a comment, not a literal. A literal such as
     `"@.punt-labs/ethos/CLAUDE.md"`, `".claude/agents"`, or
     `"...internal/enable/guide/CLAUDE.md..."` is not equal to the entry and
     fails;
   - **fails on any import of a harness-specific package** outside
     `harnesses.go`: `internal/claudemd` today (`enable.go` imports it for
     `claudemd.Register`), and any future package whose import path
     contains `claude` or `opencode` (case-insensitive), such as an
     `internal/opencode`. The harness-native steps are reached only through
     the `harnesses.go` entries;
   - **fails on any identifier** (declared or referenced: functions, types,
     variables, constants, fields, parameters, and package qualifiers such
     as `claudemd.Register`) whose name contains `claude` or `opencode`
     (case-insensitive) in any file except `harnesses.go`. Comments are not
     scanned. A helper named `registerClaudeImport` outside the list fails
     even if no literal in it does;
   - fails if the declared list is not sorted by name (`sort.SliceIsSorted`
     over the exported accessor) or is empty;
   - and a companion test injects a third entry into the list and asserts
     that the help text, the usage error, the unknown-name error, and the
     prompt text all contain its name with no other change.
   **Migration work this implies.** Today these name Claude outside any
   list: `CanonicalImport` (`enable.go:46`), `guideRel` (`deposit.go:15`,
   the allowed path above), the import registration and removal in
   `EnableTo` and `Disable` (`enable.go:127,173`), and the command help
   text (`cmd/ethos/enable.go:18-19`). The wider scan of round 9 adds
   places that no earlier round listed: `ethos uninstall`, which looks up
   the `claude` CLI and prints "claude CLI not found" and "Removed Claude
   Code plugin." (`cmd/ethos/uninstall.go:37-49`); the doctor's glob over
   `.claude/agents` (`internal/doctor/doctor.go:198`, whose literal is the
   path element `".claude"`) and any remedy string of the checks that builds
   on it and names a harness path; and the `validate-content` guide-sync check, whose label
   "enable: CLAUDE.md guide embed sync" and remedy text ("...
   internal/enable/guide/CLAUDE.md have drifted ...",
   `cmd/validate-content/main.go:81,88`) embed the base name, which the
   narrowed allowlist no longer passes. Each moves into a `harnesses.go`
   entry or is generated from the list (for the uninstall step, the
   entry's own uninstall hook), or is reworded to name the guide by its full
   path. **The scan also finds literals that name no harness**: the COO's
   identity handle `"claude"` (`cmd/ethos/setup.go`, `mission.go`), the
   `claude-md` export format (`export.go`), the `--pid` flag help "Claude
   PID" (`session.go`), and Claude Code's `.claude/skills` and
   `.claude/agents` roots (`seed.go`, `setup.go`, `create.go`). The
   allowlist does not grow to cover them. The implementation stage runs the
   scan first, lists every hit, and brings the non-harness ones to the
   leader as a design question (rename, or move into the list) before any
   code changes; the design does not decide it here. The test above is what
   proves that none was missed.

   **`ethos disable` mirrors this.** `ethos disable claude`, `ethos disable
   opencode`, and `ethos disable claude opencode` remove only the named
   harness artifacts and never prompt. With none named on an interactive
   terminal, `ethos disable` prompts with the harnesses *currently
   enabled* (by each entry's `enabled?` probe), all preselected. Two no-op
   cases exit 0, never an error: `ethos disable` with nothing enabled
   prints a message (for example `no harness is enabled; nothing to
   disable`), changes nothing, and does not prompt; and `ethos disable
   <harness>` for a harness that is already disabled reports it as a no-op
   (`opencode is already disabled`) and changes nothing. In a mixed
   invocation (`disable claude opencode`, one already disabled) the
   enabled one is disabled and the other is reported as a no-op, exit 0.
   With none named and no
   terminal it exits non-zero with the same both-harness usage error. Each
   removal touches only its own artifact, and the neutral marker and hook
   chaining are removed only when no harness artifact remains. Existing
   callers of bare `ethos enable` (docs, scripts, `ETHOS-SETUP.md`, `ethos
   doctor` hints) are migration work for the implementation stage: on a
   terminal they now meet the prompt, and in CI or scripts they now meet
   the usage error and must name their harnesses. **See §6.1 for a same-repo-only collision the OpenCode
   subcommand has with one consuming repo's own dev-config gate** — not a
   general incompatibility with projects that use that repo's tooling
   elsewhere.

None of 1-5 is implemented in this mission. They are named here so the
mission contract for the implementation stage can cite this section
directly as its write-set boundary.

## 6. Non-goals, restated

Per the mission contract: this design does not touch Circuit's PR-watch
workflow state. The mutable PR head SHA tracked by the GitHub/Circuit
integration is untouched by anything above — every mechanism here resolves
and writes only session/mission/delegation correlation metadata
(`ETHOS_SESSION`, the active-mission and delegation-binding sidecars), the
identical STABLE-metadata/MUTABLE-PR-SHA boundary the spec itself draws
(spec §"Non-goals": "Circuit's PR-watch workflow state... is explicitly a
separate, GitHub-owned concern"). No file this design writes or reads is
shared with, or interpreted by, Circuit's own tracking.

### 6.1 Same-repo-only collision: `ethos enable opencode` inside `punt-labs/points`'s own source tree

**Finding 6 (Qodo), checked directly rather than accepted or rejected
blind, and re-scoped in round 3 after reading `punt-labs/points`'s own
repo directly rather than describing the finding secondhand.**
`punt-labs/points`'s `.bin/check-dev-config.sh` — the script
`make check-dev` runs, which `make doctor` depends on
(`points/Makefile:29-30`, `:104-105`: `doctor: build check-dev`) —
contains this exact gate, confirmed by direct read:

```bash
if jq -e 'has("plugins") or (.experimental? != null)' opencode.json >/dev/null; then
  fail 'project OpenCode plugins/experimental hooks require an explicit area-8 review'
fi
```

(`points/.bin/check-dev-config.sh:16-18`.) This is a real, confirmed
collision, not a speculative one — but it is narrower than "projects that
use `points` for model routing conflict with projects that use the ethos
OpenCode integration." It is **specifically and only** about running
`ethos enable opencode` inside the `points` repo's own checkout: that
command would add a `"plugins"` key to *that repo's own*
`opencode.json`, and `check-dev-config.sh` fails unconditionally the
moment the key exists there, regardless of its contents or purpose.

This is a same-repo-only collision, confirmed by reading `points`'s own
source: `check-dev-config.sh`'s gate reads and fails on `points`'s own
`opencode.json` and runs only under `points`'s own `make check-dev`/`make
doctor` — it is internal tooling `points` applies to itself, not a check
`points` deploys, enforces, or propagates into any other repository that
`points` happens to be used to configure. `points` itself, per its own
README (`points/README.md`), exposes exactly four commands — `route`,
`status`, `verify`, `unroute` — all of which write or read only
model/source/provider routing selection; none of them touches an
`opencode.json` `"plugins"` key in any repo, `points`'s own or otherwise
(confirmed by direct read of `points/.bin/`: no script there writes a
`"plugins"` key anywhere). A repo that uses `points` to route its own
harness-to-model config, and separately runs `ethos enable opencode` for
the ethos OpenCode integration, does not hit this gate at all — the two
mechanisms write disjoint parts of `opencode.json` in that repo, and
`check-dev-config.sh` does not exist outside `points`'s own tree to
object. The one real, narrow case is: don't run `ethos enable opencode`
inside `punt-labs/points`'s own source tree without first resolving the
"area-8 review" its own gate names.

This is **not** resolved by this design, deliberately: whether to (a) make
`ethos enable opencode` idempotent against a pre-existing
`check-dev-config.sh`-style gate (there is no general mechanism for ethos
to know such a gate exists in an arbitrary consuming repo), (b) document
`ethos enable opencode` as requiring the "area-8 review" `points`'s own
gate names before use inside `points`'s own repo, or (c) some other
reconciliation, is an operator decision this design flags rather than
makes. Named here as a narrow, same-repo-only open question for the
implementation stage or the operator to rule on — not silently ignored,
and not accepted as blocking this design's own scope (§6's `Non-goals`),
since it is a property of one repo's own gate script applied to its own
config file, not a general conflict between `points`-routed repos and
ethos-OpenCode-integrated repos, and not anything `ethos enable opencode`
writes incorrectly.

## 7. Rejected alternatives

### 7.1 Session-fork attribution strategy

Three alternatives to the "two independent triggers, one Go-side guarded
operation" design in §2.3 were considered and rejected.

**A — Automatic inheritance: a forked session silently carries its
parent's mission claim forward with no check at all.** Rejected outright,
by both the spec and this design. The spec's own `ForkNeverInheritsClaim`
theorem (spec §"Theorems") exists precisely because automatic inheritance
reopens the exact unattributed/misattributed-commit risk the whole bead is
about: "any unrelated fork of a mission-bound session would silently
inherit mission authorship for whatever the forked conversation does
next" (`docs/prfaq-opencode-integration.md` lines 182-186). A fork used for
an unrelated exploratory branch would otherwise tag its commits with a
mission it has nothing to do with. Every version of this design requires
an explicit, checkable witness (a traceable `parentID`/ancestor-set
membership) before any claim propagates — never a bare assertion.

**B — Root-equality ("same `family`") as the propagation witness, instead
of ancestor-set membership.** This was the spec's own round-2 design, and
it is rejected here for the identical reason the spec itself rejects it in
round 3/4 (spec §"AllocateDelegationForkPropagated", the two documented
exploit traces): root equality is symmetric, so it lets a session's own
*descendant's* claim flow backward and attribute an ancestor that never
itself claimed anything — `JoinRootSession(A) -> ForkSession(B,A) ->
EndSession(A) -> ForkSession(C,B) -> ClaimMission(C,M) ->
AllocateDelegationForkPropagated(B, witness?=C)` lets B acquire a
delegation keyed off C's claim, backward, one level removed from the
direct-parent/child case round 1 already closed. The fix — directional
ancestor-set membership under transitive closure of the one-step
*child->parent* relation — can never reach a descendant from an ancestor's
own position, closing both historical exploits by construction (spec
§"AllocateDelegationForkPropagated", the round-4 re-verification against
both traces). This design's §2.3.1 real-code walk inherits that
directionality exactly: it walks fork-source links upward only, never downward,
so it structurally cannot replay either exploit.

**C — A single live-event trigger only (`ctx.event.subscribe()`), with no
tool-hook fallback.** Rejected because `ctx.event.subscribe()` is
confirmed, across three independent OpenCode sources quoted directly by
the companion research (`specs/v2/event-stream-architecture.md`,
`.../build/client/index.mdx`, `.../build/plugins/rpc.mdx`, all cited
verbatim at `docs/prfaq-opencode-integration.md` lines 235-241), to be a
volatile stream with no replay or catch-up primitive. A plugin
disconnection window (process restart, network blip, reconnection) at the
exact moment a fork fires permanently loses that fork's event under
option C, leaving the forked session unattributable for its entire
lifetime with no recovery path. The chosen design's second trigger
(`ctx.tool.hook("execute.before", ...)`) costs nothing extra to
register — it is already registered for §3's environment propagation —
and is structurally immune to the same disconnection risk, because a tool
call cannot fire without the session itself being alive and making a
request to the very process that would otherwise need to have stayed
subscribed.

**D — Resolve lineage via the CLI-plugin `context.data.session.root`/
`.family` API instead of the event payload's own `parentID` / `ctx.session.get`.**
Rejected on two independent grounds, both confirmed directly rather than
assumed. First, it is unnecessary: `SessionEvent.Forked`'s payload already
carries `parentID` directly (`.tmp/session-event.ts:187-201`,
`.tmp/api.ts:559-571`), so no separate lookup is needed for the live-event
trigger at all. Second, and more importantly for the tool-hook trigger,
it is **unreachable** in this integration's actual deployment target: the
`context.data.session.*` surface belongs to the separate
`@opencode/plugin/tui` plugin type, not the main hook-plugin this design
bundles — confirmed by direct read of the hook-plugin's own `Context`
interface (`.tmp/effect-plugin.ts:26-55`), which enumerates `app, location,
options, agent, aisdk, command, credential, event, experimental,
integration, mcp, model, generate, permission, plugin, provider,
reference, rpc, session, shell, skill, storage, tool, vcs, websearch,
worktree` with no `data` field anywhere — matching the companion
research's "Finding 6" (`docs/prfaq-opencode-integration.md` lines
370-397) exactly. A headless `opencode serve` deployment — the one this
whole integration targets, since ethos sessions run no TUI — cannot reach
`context.data.session` regardless of what else is running, so building the
fork-propagation design around it would have been building against an API
the shipped plugin cannot call.

### 7.2 Commit-trailer resolution strategy

Three alternatives to "no new resolution code; make `ETHOS_SESSION`
propagation correct" (§3) were considered and rejected.

**A — Teach `resolve.SessionID` a third, OpenCode-specific discovery step**
(e.g. an `OPENCODE_SESSION_ID` env var OpenCode itself might set, read as a
fallback alongside `CLAUDE_PID`). Rejected: this re-opens exactly the risk
DES-074 closed for Claude Code. `resolve.SessionID`'s own doc comment states
the design principle directly (`resolve.go:278-310`): a session-pointer
lookup must never fall back to a mechanism that could resolve a *plausible
but wrong* session — the documented root cause of `ethos-vqwn`, where the
old process-tree walk's shared "claude daemon run" PID let one session's
commit get attributed to a different, concurrent one. Introducing a
harness-specific fallback inside the *shared* resolver would make
`resolve.SessionID` harness-aware in exactly the place the spec's own
`HarnessNeutrality` theorem argues it should not be — the spec's
modelling-choice section is explicit that no operation should branch on
which harness a session belongs to (spec §"Modelling choice"). The
one-mechanism-per-harness-via-its-own-native-extension-point precedent the
spec itself cites approvingly — the `model-provider`/Bifrost tool "writing
into each harness's own native extension point rather than inventing a
cross-harness daemon" (spec §"Precedent") — is exactly what §3.2's
`ctx.shell.hook` env-propagation design follows: OpenCode's own plugin
surface sets `ETHOS_SESSION`, the one variable the *existing*, unmodified
resolver already checks first. No new resolver code, no new env var name,
no new harness-conditional branch.

**B — A per-harness commit-msg hook variant: ship an OpenCode-specific
commit-msg script that calls a *different* ethos subcommand, aware it is
running under OpenCode.** Rejected because it duplicates
`plugin/hooks/commit-msg.sh`'s entire idempotency, trailer-formatting, and
host-hook-chaining logic (`commit-msg.sh:1-26`'s preserved
`_host_status` handling alone is nontrivial) for zero behavioral gain: the
single existing script, unmodified, already does the right thing the
moment `ETHOS_SESSION` is correctly set in its environment, regardless of
which harness's shell spawned it. git itself has no concept of "which
coding-agent harness is committing" — a commit-msg hook is a plain
subprocess of `git commit`, inherently harness-blind — so a
harness-specific hook variant would be solving a problem git-level tooling
does not actually have. The spec's own non-goals section makes the same
call for a closely related question (spec §"Non-goals": "Changing Claude
Code's existing behavior" is out of scope specifically because the
existing mechanism already generalizes).

**C — Have the OpenCode plugin write the Mission/Delegation trailers
directly into the commit message itself** (e.g. via a `ctx.tool.hook`
intercepting the `git commit` tool call's arguments and appending
`-m "...Mission: ..."`), bypassing the commit-msg git hook entirely.
Rejected: this would create a second trailer-writing code path alongside
the existing git-hook one, with its own idempotency and
same-mission-cross-check logic to get right and keep in sync
(`WriteCommitTrailers`'s existing `b.MissionID != missionID` gate,
`commit_trailers.go:65`) — a correctness-duplication risk with no
offsetting benefit, since the one existing path already works for any
harness correctly setting `ETHOS_SESSION`. It would also silently stop
working for any commit path that does not go through the specific tool
call the plugin intercepts (a human typing `git commit` directly in an
OpenCode terminal, for instance — the same gap §3.3 already discloses for
env-propagation, duplicated into a second mechanism rather than closed).
The git-hook path is harness-universal by construction: it fires on every
`git commit` regardless of what spawned the shell, which a
tool-call-interception approach cannot match without enumerating every
possible commit-issuing code path OpenCode exposes.
