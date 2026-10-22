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
   --root-id <id> --primary-id <id>` (new, §5 point 1; it creates the
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
   without lineage would blank a fork's `ParentSession` (§2.3.2).

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
  refreshes the roster's `LastSeen` (below). A failed `ensure` stores
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
  then `ethos session ensure --session sid --parent-session source ...`.
  The recursion keeps a visited set and refuses a repeated id (§2.3.1).
  A missed fork event therefore registers the child and every missing
  intermediate ancestor, parent first, before `claim-forked` runs.
  **A failed or inconclusive lineage read aborts registration (round 5).**
  If `ctx.session.get` fails, times out, returns no record, or returns a
  record the plugin cannot classify as root or fork, `ensureRegistered`
  registers nothing for that session, logs the reason, and the caller
  proceeds unattributed. It never defaults to "root": a fork registered as
  a root would get an empty `ParentSession`, and lineage is write-once, so
  the error would be permanent and the fork could never inherit a
  delegation. There is also no upgrade path from an empty `ParentSession`
  to a set one; `ensure` refuses it as a lineage conflict (next item). The
  repair for a session registered with the wrong lineage is operator
  action: delete its roster (`ethos session delete --session <id>`, which
  also clears its sidecars), after which the next `ensureRegistered`
  registers it again from OpenCode's record. **One read is conclusive:**
  when a fork's parent was deleted in OpenCode, `ctx.session.get(parent)`
  returns "not found", and that is a conclusive answer, not an inconclusive
  one. The child registers, with `ParentSession` taken from its own
  persisted `fork.sessionID` or the event's `parentID`. The parent is not
  ensured. Fork-claim propagation then refuses through the existing
  ancestor-Load-failure rule (§2.3.1), and an explicit operator `ethos
  mission claim` on the child still works. Transport errors and timeouts
  stay inconclusive and abort registration.
- `ethos session ensure` runs under the per-session roster lock
  (`Store.withLock`, as `Create` and `Join` do). If no roster exists, it
  creates one. If a roster exists, it changes only the roster's
  `LastSeen` field, which it sets to the current time: `Started`,
  participants, and extensions survive. If a roster exists and the
  supplied `--parent-session` differs from the stored `ParentSession`
  (including one side empty), it exits non-zero with a lineage-conflict
  error and changes nothing, `LastSeen` included. A non-empty
  `--parent-session` whose roster does not exist is also an error.
  Lineage is therefore write-once and parent-first by construction, and a
  plugin restart cannot blank it. A session whose claim is already
  written (§2.4) is unaffected: `ensure` touches only the roster file,
  never the active-mission or delegation-binding sidecars.

**Purge must not reclaim a live OpenCode session (round 5).** The
registered roster has root and primary equal to the OpenCode session id,
so `CreateInCheckout` collapses them to one participant (§2.1 step 1).
`isStale` (`internal/session/store.go:942-957`) treats a one-participant
roster as a solo session and ages it out by `Started` alone
(`store.go:946-948`, `isOlderThan`, `staleTTL = 24h` at line 933). An
OpenCode session that stays live for more than 24 hours would therefore
be deleted, with its claim and delegation sidecars, by `ethos session
purge` (`Store.Purge`, `store.go:385-404`) or by `PurgeTombstoned`
(`store.go:514`), and every later `mission claim` would be refused. The
mechanism that keeps a live session out of `isStale`'s reach:

- `Roster` gains `LastSeen` (RFC 3339), written by `Store.Ensure` on
  create and refreshed on every successful call, under the roster lock.
- `isOlderThan` measures age from the later of `Started` and `LastSeen`.
  An absent `LastSeen` (every Claude Code roster, every existing roster)
  behaves exactly as today, and Claude Code's numeric-primary branch
  (`store.go:950-956`) is untouched.
- The memo's 15-minute refresh interval is what drives refreshes: any
  OpenCode session that makes a tool or shell call at least every 24 hours
  keeps `LastSeen` current. Its observability to `isStale` is that
  `isStale` reads the field from the roster it already loads.
- A session idle for more than 24 hours with no event ages out and is
  purged, as any abandoned session is. That is correct: its next tool call
  re-registers it via `ensureRegistered` (as a root with no claim, or as a
  fork with its lineage re-read). A claim lost this way must be re-made.
  `session.deleted` (§2.6) handles explicit endings; the TTL handles
  missed ones.

`isStale`/`isOlderThan` and `Roster.LastSeen` are therefore implementation
work, listed in §5 point 1. They are a change to shared session code, not
OpenCode-specific logic.

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
  (§5 point 2) until one succeeds (spec §5.6's corrected, round-4 guard:
  the full ancestor set under transitive closure, not a single hop; see
  §2.3.1). `claim-forked` refuses a witness that is not an ancestor or
  holds no claim; on success it allocates a new delegation under the
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
  5), and it memoizes the per-session outcome so it does not spawn a
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
- *Go side* (`claim-forked`, §2.3.2): follows the stored `ParentSession`
  upward from the child with a visited set. It stops on a match (pass), on
  an empty `ParentSession` (the witness is not an ancestor: refuse), on a
  repeated id (refuse, fail closed), or **when `ss.Load` fails on any
  session in the chain, including a missing roster (refuse, fail
  closed)**. The last case matches the spec: when an ancestor's record is
  removed (`EndSession`, a `session.deleted` event, or a purge), "the
  chain is structurally unwalkable past that point" and the relation
  reflects only "as much of the ancestor chain as still has a live,
  readable record" (`docs/spec-opencode-integration.tex:1054-1064`). A
  load error that is not "not found" (a corrupt roster) refuses the same
  way and is reported to the caller. Each step either reaches an empty
  link, fails to load, or visits a new roster, and the roster set is
  finite, so the walk terminates. A cycle cannot be written through `ensure` (the parent must
  already exist and lineage is write-once, §2.1.1), so the visited set
  guards only against a hand-edited or corrupt roster file.

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
(`internal/hook/session_start.go:184`'s `root`/`primary` pair, where
`primary.Parent = root.AgentID`) — not a link between two different
sessions' rosters. Confirmed by direct read: nothing else in
`internal/session/` or `internal/mission/` stores session-to-session
parentage. Without that data, a `claim-forked` call's `--witness` flag is
exactly the "caller-supplied, trusted outright" shape this spec's own
`ForkNeverInheritsClaim` theorem (§7.1, alternative A) and round-4 guard
exist to prevent — a malicious or buggy plugin could name any session as
`--witness` and the guard would have nothing server-side to check it
against.

The fix is a new field on the *session* roster, not the participant:
`Roster.ParentSession string` — the OpenCode `sessionID` this session was
forked from, set once, at roster-creation time, and never mutated
afterward. Two write paths populate it:

- **`ethos session ensure`** (§2.1.1, §5 point 1) takes an optional
  `--parent-session <id>` flag. On `ForkSession` (§2.2), the plugin passes
  the fork source (the live event's `parentID`, or a persisted record's
  `fork.sessionID`, §2.2) as this flag's value when it first registers the
  child session's roster. The parent's roster must already exist, which is
  why `ensureRegistered` registers ancestors first. A later `ensure` with a
  different value is refused, never applied (§2.1.1), so a plugin restart
  cannot blank or rewrite lineage.
- A root session (§2.1, no `parentID` on its `session.created` payload, or
  no `fork` on its persisted record) is registered with `ParentSession`
  left empty — there is nothing to record, and an empty value reads
  unambiguously as "this session has no ethos-known parent," matching the
  spec's own `noParent` case (spec §5.1).

`claim-forked --session <child> --witness <parent>`'s guard then resolves
`witness?`'s ancestor-set membership with the Go walk of §2.3.1, one step
at a time, with each step reading `ParentSession` off the *Go-stored*
roster (`ss.Load(sessionID).ParentSession`) instead of trusting a
caller-supplied chain: starting from `child`, follow `ParentSession`
upward, checking at each hop whether the current session ID equals
`witness`, until a match is found (the guard passes), the chain ends at an
empty `ParentSession` (no match anywhere in the ancestor set: refuse), or
an id repeats (refuse, fail closed). The plugin's
own `--witness` argument is now merely *which* session to check for —
`claim-forked` itself walks the lineage from data it alone wrote via
`session ensure`, so a plugin that lies about `--witness` simply gets a
refusal: the walk from `child` will not reach a session the plugin never
registered as an ancestor, regardless of what the flag claims.

This is additive to `Roster`'s existing fields (`internal/session/session.go`'s
struct, listed in `session.go:25-32`) and requires no change to any
existing `Create`/`CreateInCheckout` caller — an empty string is the
correct default for every current caller that does not yet record a fork
(`ethos session create`, which never sets it, and Claude Code's
`SessionStart` hook until §2.5's wiring lands). Neither the field nor the
flag contains any OpenCode-specific logic; see §2.5.

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
`claim-forked` and `Roster.ParentSession` are harness-neutral Go primitives
with no OpenCode-specific logic in them: they take two session IDs and walk
Go-stored lineage. Only the *trigger* that calls them differs per harness.

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
implementation stage (§5, point 3): Claude Code's `SessionStart` hook, when
it observes `source: "fork"`, registers lineage with `ethos session ensure
--parent-session <parent>` (or `Store.Ensure` in-process) and calls `ethos
mission claim-forked`, the same two steps the OpenCode plugin takes on
`session.forked`. The spec leaves one point open for Claude
Code: whether a forked session's own `CLAUDE_PID` resolves to a fresh roster
entry automatically, or the hook must register one. The implementation stage
must confirm this against a running Claude Code, as the spec states.

### 2.6 EndSession: `session.deleted` (round 5)

Rounds 1-4 registered OpenCode sessions but never ended them, so rosters
and claim/binding sidecars outlived their sessions until a purge, and the
two harnesses differed: Claude Code's `SessionEnd` hook deletes the
session (`internal/hook/session_end.go:57`, `ss.Delete`). The plugin now
handles `session.deleted` (§1.2 item 1; `.tmp/session-event.ts:177-185`)
by running `ethos session delete --session <id>` (`runSessionDelete`,
`cmd/ethos/session.go:697`, wrapping `Store.Delete`, `store.go:200`). That
call holds the per-session lock and clears the active-mission claim, the
delegation binding, and every pending dispatch before removing the roster
(`deleteFiles`, `store.go`), and the plugin drops the id from its memo
(§2.1.1). This instantiates the spec's `EndSession`. A forked child whose
ancestor was deleted becomes unwalkable past that ancestor, and
`claim-forked` refuses (§2.3.1), which is the spec's own behavior. A missed
`session.deleted` event is covered by the TTL: the roster stops being
refreshed and `isStale` reclaims it (§2.1.1). Deleting an unknown id is not
an error the plugin acts on; it logs and continues.

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
| `ETHOS_SESSION` is non-empty but names a session with no roster: purged, never registered, mistyped, or inherited from a different host or checkout | present, names nothing in the roster store | **The existing reader does not check liveness.** `resolve.SessionID` returns any non-empty trimmed value (`resolve.go:316-318`) without consulting the roster, and `WriteCommitTrailers` reads only the sidecars (`commit_trailers.go:45-68`). With no sidecars: silent, no trailer. With a retained claim-origin sidecar (a sidecar with no roster has no GC path, `internal/mission/active.go:605-608`): **the trailer is emitted for the dead session.** The spec's `ResolveCommitAttribution` requires `alive` and a matching `bornEpoch` (`docs/spec-opencode-integration.tex:1378-1389`); the reader does not implement that guard, and this design does not change the reader. Liveness is enforced on the write side instead (`RefuseIfSessionGone` before any claim, `mission.go:2113`; teardown clears sidecars with the roster). What the plugin guarantees, and what it does not: it sets `ETHOS_SESSION` only to an id for which `ethos session ensure` succeeded within the memo's refresh interval (15 minutes, §2.1.1), and the Unknown arm (§3.2.1) never sets a session id. It cannot see a purge by another process, so a roster deleted inside that window can still be named. Two bounds make that harmless: `Store.Delete`/`Purge` clear the sidecars together with the roster (`deleteFiles`), so a purged session has no trailer to emit, and `LastSeen` (§2.1.1) keeps `isStale` from purging a live session. The residual case is an orphan sidecar (`active.go:605-608`), which the plugin cannot rule out and this design does not claim to. |
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
   backed by a new `session.Store.Ensure`, and a new `Roster.ParentSession`
   field** (§2.1.1, §2.3.2). `Store.Ensure(sessionID, root, primary, repo,
   host, parentSession)` takes the per-session roster lock
   (`Store.withLock`) once and, inside it: creates the roster if none
   exists; on an existing roster changes only `LastSeen`; returns a
   lineage-conflict error (changing nothing) if an existing roster's
   `ParentSession` differs from `parentSession`; and returns an error if
   `parentSession` is non-empty and has no roster. `session create` is
   unchanged and keeps its replace semantics for its existing callers; the
   plugin never calls it. The `ParentSession` and `LastSeen` fields are new
   on `internal/session/session.go`'s `Roster` struct. **`isStale` change
   (round 5):** `isOlderThan` (`store.go:960-966`) measures age from the
   later of `Started` and `LastSeen`, so `Purge` and `PurgeTombstoned`
   (`store.go:404,514`) do not reclaim a live OpenCode session after 24
   hours (§2.1.1). Rosters with no `LastSeen`, including every Claude Code
   roster, behave as today. This is a change to shared session code with
   its own tests: a one-participant roster started 25 hours ago with a
   fresh `LastSeen` is not stale; the same roster without `LastSeen` is.
   Every reader that walks ancestor chains (§2.3.1's walk,
   `claim-forked`'s guard below) depends on it being populated correctly,
   parent first, by `ForkSession` (§2.2) through `ensureRegistered`.
2. **A delegation-allocation CLI primitive for `AllocateDelegationForkPropagated`.**
   `ethos mission claim` (§2.1, §2.4) already covers `JoinRootSession` +
   `ClaimMission`. Fork-propagation (§2.3) needs a new, narrow primitive —
   e.g. `ethos mission claim-forked --session <child> --witness <parent>`
   — that performs the spec's own guard (ancestor-set membership via the
   `Roster.ParentSession` chain from point 1, not the caller's own
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
   (`internal/hook/pretooluse_dispatch.go:881-888`'s
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

   1. *Order: skeleton, then binding, then claim.* The claim is the commit
      point. Without a claim-origin binding for the session,
      `WriteCommitTrailers` returns before it reads the delegation binding
      (`commit_trailers.go:53-56`), so a binding written before the claim is
      inert and a reader never sees a delegation trailer without its
      `Mission:` trailer. This is the same ordering principle
      `WriteActiveMissionOrigin` documents for its own pair: "an interrupted
      pair never reads wrong" (`active.go:240-250`). The binding is also
      not trusted alone: `commit_trailers.go:65` emits it only when its
      `MissionID` equals the claimed mission.
   2. *Lock order and scope.* `claim-forked` splits into an unlocked read
      phase and a locked write phase.
      - *Read phase, outside any lock:* read the witness's claim
        (`ReadActiveMissionBinding`, the same reader `WriteCommitTrailers`
        uses) and walk the Go-stored lineage (§2.3.1). A refusal here
        (witness not an ancestor, ancestor roster missing, witness holds no
        claim) takes no lock and writes nothing.
      - *Write phase:* the lock is the **child session's** dispatch-pending
        lock (`mission.WithDispatchPendingLock(globalRoot, child, ...)`,
        the one `runMissionClaim` already takes for the same session,
        `cmd/ethos/mission.go:2112`), acquired **first**: that lock is
        always the outermost one, taken before any mission or delegation
        lock and never nested inside one (`active.go:495-497`). Inside it,
        in this order: `RefuseIfSessionGone(child)`; re-check that the child
        holds no claim; allocate the id with `NewID` (item 3,
        `pretooluse_dispatch.go:775`); acquire the mission lock and then
        the delegation lock (`AcquireMissionLock` and after,
        `pretooluse_dispatch.go:794-806`), the order `dispatchTierB` uses:
        `NewID`, mission lock, delegation lock; re-load the mission and confirm
        it is still open (the TOCTOU re-check at lines 820-836); then the
        three writes of item 1. The witness is not locked, so its claim can
        be released between the read phase and the write phase. That window
        is accepted: the child's claim is written for the mission as read,
        the open-mission re-check bounds it, and the spec's guard is itself
        evaluated against the state at the time of the operation.
   3. *Delegation id allocation.* The id comes from the mission-scoped
      counter, `NewID`, with commit-or-rollback exactly as the dispatch
      path does it: a deferred `releaseID(success)` that returns the
      counter if the call fails before the skeleton is on disk, and
      `success = true` the moment `WriteDelegationSkeleton` returns, after
      which the id is never rolled back because reuse of an id whose
      skeleton exists would collide (`pretooluse_dispatch.go:776-788,
      853-861`).
   4. *Distinct exit statuses.* `claim-forked` returns three outcomes the
      plugin can tell apart, none of them the generic failure status:
      success (0); **witness refused** (not an ancestor, an ancestor
      roster failed to load, or the witness holds no claim), on which the
      plugin tries the *next* ancestor as witness; and **write failed**
      (any lock or write error in the write phase), on which the plugin
      retries the *same* witness. The exact numbers are fixed in the
      implementation PR; the requirement is that all three are distinct
      from each other and from the usage-error status, and are tested.
   5. *No orphan skeleton from a retry: reuse or roll back.* Two cases.
      (i) The write phase fails after the skeleton is on disk and the
      process survives: it closes that skeleton with verdict `aborted`,
      as `enforceDelegationDepth` does for a refused spawn
      (`pretooluse_dispatch.go:863-872`), removes a binding it wrote, and
      exits "write failed". The counter slot stays consumed (item 3), so
      the retry allocates a fresh id. (ii) The process dies mid-write and
      cleanup never runs: the next call, under the same child lock, reads
      the child's delegation binding; if the child has no claim, the
      binding names the witness's mission, and a skeleton exists for the
      binding's delegation id, it **reuses** that id and finishes the
      missing writes; otherwise it ignores the binding and allocates anew.
      A child that already holds a claim fails the spec's
      `sid? not-in dom claims` guard, so nothing fires: the call writes
      nothing and exits success with an "already claimed" message, because
      the work is done. Since the claim is written last, that state implies
      the skeleton and binding were written. The implementation PR tests each interrupt
      point (after skeleton, after binding, after claim) for both cases.
   6. *Failure surfaces; it is never swallowed.* The plugin logs a "write
      failed" exit and retries on the child's next `execute.before`
      (§2.3), which the claim guard makes safe. Until a retry succeeds the
      child's commits carry no trailers: the safe direction.

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
   `claim-forked` and `Roster.ParentSession` (points 1-2) are harness-neutral
   (§2.5), so two triggers call them: the OpenCode plugin on
   `session.forked` (point 4), and Claude Code's `SessionStart` hook when it
   observes `source: "fork"`. The Claude Code trigger is a change to
   `internal/hook/session_start.go` that registers lineage through
   `Store.Ensure` with `parentSession` and calls `claim-forked`. It is not a separate future concern:
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

   **One source of truth.** The supported harnesses are a single ordered
   list in `internal/enable` (lexical order by name, so position privileges
   neither), one entry per harness: its name, its enable step, its disable
   step, and an `enabled?` probe (CLAUDE.md import present; plugin file
   present and registered). The prompt choices, the usage-error text, the
   unknown-name error, the command's help text, and `ethos disable`'s
   prompt all read that list. Adding a harness is one entry. No code path
   names `claude` or `opencode` outside it, and neither is the value used
   when the user gives no answer.

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
