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
`claim-forked` also writes the matching `DelegationBinding` atomically with
the claim (finding 5). §6.1 adds a confirmed (not speculative) cross-repo
compatibility question about `punt-labs/points`'s `make doctor` gate.
Nothing else in §§1-4 or §7 was re-litigated; those sections were
independently verified accurate by the leader before this round was
dispatched.

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
occurs when `ethos enable --opencode` runs inside `points`'s own
checkout. §§1-2, §4, §5 (apart from its §6.1 cross-reference), and §7
were not re-litigated this round.

## 1. Plugin shape and lifecycle hooks

### 1.1 Entry point

A `Plugin.define({ id, setup(ctx) {...} })` module
(`.tmp/migrate-v1.mdx:62-71`; the Promise-based `Context` shape at
`.tmp/plugins-index.mdx`'s equivalent interface). Ethos ships the
Effect-native variant instead — `Plugin.define({ id, effect: (ctx) =>
Effect.Effect<void, never, Scope.Scope> })`
(`.tmp/effect-plugin.ts:57-64`) — because every other handler this design
registers (`ctx.tool.hook`, `ctx.event.subscribe`, `ctx.session.get`) is a
plain async call either style can make; the Effect variant is chosen only
because ethos's own install is a single static binary invoked as a
subprocess from the hook body (§3), so there is no advantage to adopting
Promise idioms the rest of ethos's Go codebase does not use, and the Effect
form is what `.tmp/effect.mdx` documents as current.

Loaded from `.opencode/plugins/ethos.ts` (project-level) for a per-repo
install, mirroring `vox`/`z-spec`'s existing per-repo enablement pattern
(`.punt-labs/vox/CLAUDE.md`, `.punt-labs/z-spec/CLAUDE.md`) — gated on the
same `.punt-labs/ethos/enabled` marker `ethos enable` already writes, not a
new marker. `.tmp/migrate-v1.mdx:38` confirms V2 discovers local plugins
from `.opencode/plugins/`.

The `setup`/`effect` function registers, and returns a cleanup closure that
disposes all of them (`.tmp/migrate-v1.mdx:290-315`; `.tmp/effect-plugin.ts`'s
`Effect.Effect<void, never, R>` return carries the same scoped-disposal
contract via `Scope.Scope`). Registrations are plugin-scoped and clean up
automatically when the plugin unloads (`.tmp/plugins-index.mdx:1333`
"Hook and transform registrations are scoped to the plugin and are cleaned
up automatically").

### 1.2 Hooks actually registered

Four, no more — corrected in this round from the round-1 count of three,
which conflated two distinct hook domains (see the correction below):

1. **`ctx.event.subscribe({ signal })`**, consumed with `for await`
   (`.tmp/migrate-v1.mdx:296-311`; `.tmp/plugins-index.mdx:1303-1311`). This
   is the primary trigger for `JoinRootSession` and `ForkSession`
   (spec §5.1-5.2). The subscription delivers the server's whole public
   event stream (`.tmp/plugins-index.mdx:1318-1320`,
   `interface EventContext { subscribe(...): AsyncIterable<OpenCodeEvent> }`);
   the plugin filters for `session.created` and `session.forked` by
   `event.type`.

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
delegation-binding sidecars) — the same files Claude Code's Tier B dispatch
already reads and writes (`internal/mission/active.go`). Using
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
`location`, `parentID` *optional*), the plugin:

1. **Registers the OpenCode session with ethos's own roster first, before
   any claim is attempted.** `ethos session create --session <id>
   --root-id <id> --primary-id <id>` (`cmd/ethos/session.go:684-692`'s
   `runSessionCreate`, wrapping `session.Store.Create`,
   `internal/session/store.go:49`) writes the session's roster file —
   the same file `RefuseIfSessionGone`
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
`ClaimMission` (spec §5.3) already models `ethos mission claim` for Claude
Code; OpenCode reuses the identical primitive, the harness-neutral seam the
spec's own `ETHOS_SESSION` finding establishes (spec §"Scope":
"`ETHOS_SESSION` is ALREADY a harness-neutral integration seam, with zero
new ethos-side mechanism needed for OpenCode to use it"). What *is* new,
corrected in this round, is step 1: the roster-registration call the claim
depends on, which round 1 omitted — `session create` is an existing,
`Hidden: true` CLI primitive (`cmd/ethos/session.go:79`), not new Go
surface, so §5's "what is genuinely new ethos-side code" enumeration is
unaffected by this fix.

#### 2.1.1 Reconciling an already-registered session after a plugin restart

A plugin process restart (OpenCode server restart, plugin reload) does not
invalidate any roster entry §2.1 already wrote — rosters are files under
`.punt-labs/ethos/sessions/`, independent of the plugin process's own
lifetime. On restart, the plugin's `setup`/`effect` function re-runs from
scratch and re-subscribes via `ctx.event.subscribe()` (§1.1), but
`session.created` fires exactly once per session, at creation — a restarted
plugin does not receive it again for a session that already exists. The
reconciliation the plugin needs on restart is therefore not "did this
session's `session.created` already pass through me" (unanswerable from
the event stream alone) but "does this session already have a roster
entry," answered directly and idempotently: call `ethos session create`
for every currently-live OpenCode session the plugin can enumerate via
`ctx.session.list(...)` (`.tmp/effect.mdx:833-837`'s paginated
`ctx.session.list({ directory, limit, cursor })`) at `setup` time, before
resubscribing to new events. `ss.Create`'s own `writeRoster`
(`internal/session/store.go:858-877`) always overwrites the roster file
atomically (temp file + rename, no `O_EXCL` check), so re-running `session
create` for a session that is already registered is a safe no-op on the
roster shape — it rewrites the same root/primary participant rows rather
than erroring on an existing file. This mirrors `ClearActiveMission`'s own
stated idempotence principle (`internal/mission/active.go`, "clearing an
already-clear slot is a no-op") applied to the create path instead of the
clear path. A session whose claim was already written (§2.4) is untouched
by this reconciliation step — `session create` touches only the roster
file, never the active-mission or delegation-binding sidecars — so
re-registering a session's roster on plugin restart cannot clobber a claim
already in place.

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
  resolves `witness?` as `parentID` (spec §5.6's corrected, round-4 guard:
  the full ancestor set under transitive closure, not a single hop — see
  §2.3.1 below for why the implementation can, in practice, stop after one
  hop). If `witness?` holds an active mission claim, the plugin allocates
  a new delegation under that mission for the forked child and writes the
  child's own claim (spec §5.6's `claims' = claims (+) {sid? |-> claims(witness?)}`).

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
  checks whether the firing session is already claimed
  (`sid? not-in dom claims`); if not, it reads the session's own record via
  `ctx.session.get({ sessionID })` — part of the hook-plugin's own
  `SessionDomain` (`.tmp/session-effect.ts:153-173`'s `Pick<SessionApi<unknown>,
  ... "get" ...>`) — for its `parentID`, and applies the identical
  `AllocateDelegationForkPropagated` guard.

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

The real implementation walks this with the identical safeguard
`internal/process/tree.go`'s `maxWalkDepth` already uses for Claude Code's
own ancestor walk (`tree.go:16`, `const maxWalkDepth = 10`) — a bounded
loop, not recursion, following `parentID` upward via repeated
`ctx.session.get({ sessionID: parentID })` calls until either a claim is
found, the chain runs out (a session with no `parentID`, i.e. a root), or
the depth cap is hit. This is the real-code analogue of the spec's inline
set comprehension restricted to `dom sessions` (spec §5.6: "bounded by the
currently-live session set... not a global declaration over an unbounded
carrier"): the real walk is bounded by a fixed small constant for the same
reason the spec bounds its own comprehension — to guarantee termination
against a malformed or adversarial chain, not because deep fork lineages
are expected to be common.

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

- **`ethos session create`** (§2.1) gains an optional `--parent-session
  <id>` flag. On `ForkSession` (§2.2), the plugin passes the forking
  session's own `parentID` (confirmed directly on the wire,
  `.tmp/session-event.ts:187-201`/`.tmp/api.ts:559-571`'s
  `SessionEvent.Forked` payload) as this flag's value when it first
  registers the child session's roster — the same roster-registration step
  §2.1.1 already requires before any claim.
- A root session (§2.1, no `parentID` on its `session.created` payload) is
  created with `ParentSession` left empty — there is nothing to record, and
  an empty value reads unambiguously as "this session has no ethos-known
  parent," matching the spec's own `noParent` case (spec §5.1).

`claim-forked --session <child> --witness <parent>`'s guard then resolves
`witness?`'s ancestor-set membership the same way §2.3.1's Go walk already
does — `maxWalkDepth`-bounded, one step at a time — except each step now
reads `ParentSession` off the *Go-stored* roster (`ss.Load(sessionID)
.ParentSession`) instead of trusting a caller-supplied chain: starting
from `child`, follow `ParentSession` upward, checking at each hop whether
the current session ID equals `witness`, until either a match is found
(the guard passes), the chain terminates at an empty `ParentSession` (no
match found anywhere in the ancestor set — the guard refuses), or the depth
cap is hit (refuse, fail closed, same discipline as §2.3.1). The plugin's
own `--witness` argument is now merely *which* session to check for —
`claim-forked` itself walks the lineage from data it alone wrote via
`session create`, so a plugin that lies about `--witness` simply gets a
refusal: the walk from `child` will not reach a session the plugin never
registered as an ancestor, regardless of what the flag claims.

This is additive to `Roster`'s existing fields (`internal/session/session.go`'s
struct, listed in `session.go:25-32`) and requires no change to any
existing `Create`/`CreateInCheckout` caller that does not pass
`--parent-session` — an empty string is the correct default for every
current caller (Claude Code's SessionStart hook, `ethos session create`
run without the new flag), none of which have an OpenCode-style fork
concept to record.

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
discovery mechanism. `process.UnderClaudeCode()`'s three signals
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
sets `event.env.ETHOS_SESSION = <sessionID>` before the shell command
runs, exactly the shape `.tmp/migrate-v1.mdx:104-137`'s `"shell.env"` ->
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
close the gap early. The only behavior it specifies for that window is the
same fail-closed rule round 2 already stated: **set `ETHOS_SESSION` only
when the plugin can establish, with no ambiguity, which single session is
responsible for the shell command about to run** (e.g., a deployment mode
confirmed to run one session at a time per plugin process) — and otherwise
leave `event.env` untouched for that invocation, which degrades to the
existing silent "no trailer" case (§4's first table row's negation) rather
than risking a wrong one. This is the same fail-closed discipline §4.1
already states for fork-miss reconciliation, applied here to a second
place this design could have been tempted to guess — now bounded to a
window with a known closing condition (#54842's merge) rather than an
open-ended one.

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

## 4. Fail-closed behavior for ambiguous attribution

This design adds **no new case** to `ResolveCommitAttribution`'s four-way
split (spec §"ResolveCommitAttribution", §4.2 below) and changes no line of
`runHookCommitTrailers` or `WriteCommitTrailers`. The existing fail-closed
behavior already covers every OpenCode-originated commit, because
`resolve.SessionID` is, by design, blind to which harness set
`ETHOS_SESSION`:

| Case | `ETHOS_SESSION` | Outcome |
|---|---|---|
| OpenCode plugin correctly propagated it into the committing shell, and that session holds a usable claim | present, names a live session with a claim | Trailers emitted — spec's 4th case. |
| OpenCode plugin correctly propagated it, but the named session has no claim (unclaimed leader work, a released claim) | present, resolves, but `candidate? not-in dom claims` or dead | Silent, no trailer, **no diagnostic** — spec's 3rd case, the ordinary "mission-free commit" state. |
| OpenCode plugin's propagation did not reach this particular commit path (§3.3's open item), OR the named session was purged/never existed | present but resolves to nothing usable, OR `UnderClaudeCode()` is also false | `ErrNotUnderClaudeCode` path if no Claude signal either -> **silent**, OR, if some Claude signal is ALSO present (e.g. OpenCode running nested inside a Claude Code shell) and that resolution also fails -> **loud `ErrNoSession` diagnostic** on stderr. |
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
sidecars Claude Code's dispatch path writes (`internal/mission/active.go`),
through the identical CLI primitives (`ethos mission claim`, and a new
delegation-allocation call — §5 — that is itself a thin Go addition, not a
new storage format).

### 4.1 Never guess another session's ID

Concretely, the two places this design could have been tempted to guess,
and why it does not:

- **Fork-miss reconciliation (§2.3)** resolves `witness?` only by walking
  `parentID` links the OpenCode event payload (or `ctx.session.get`)
  itself names — never by picking "the most recently active session" or
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

1. **A `--parent-session` flag on the existing `ethos session create`
   primitive** (§2.3.2), writing a new `Roster.ParentSession` field. Not a
   new command — `session create` already exists
   (`cmd/ethos/session.go:79`, `Hidden: true`) — but the `ParentSession`
   field itself is new on `internal/session/session.go`'s `Roster` struct,
   and every reader that walks ancestor chains (§2.3.1's bounded walk,
   `claim-forked`'s guard below) depends on it being populated correctly
   by `ForkSession` (§2.2).
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
   avoids by writing both atomically
   (`internal/hook/pretooluse_dispatch.go:881-888`'s
   `WriteDelegationBinding` call, alongside the claim and skeleton writes
   in the same critical section). `claim-forked` must do the same: inside
   the one `mission.WithDispatchPendingLock` critical section that writes
   the child's `WriteActiveMission` claim, also call
   `mission.WriteDelegationBinding(globalRoot, childSession,
   mission.DelegationBinding{DelegationID: <newly allocated>, MissionID:
   <witness's claimed mission>, ParentSession: witness})` — mirroring the
   existing dispatch path's own three-write pattern (skeleton, claim,
   binding) rather than writing the claim alone.
3. **`.opencode/plugins/ethos.ts` (or an npm-published equivalent) —** the
   TypeScript plugin itself, implementing §1-§3's four hooks, calling the
   existing `ethos` binary as a subprocess (the same pattern
   `commit-msg.sh` already uses to call `ethos hook commit-trailers`) for
   every state-mutating action. No direct file access to
   `.punt-labs/ethos/` from TypeScript — every write goes through the Go
   binary, so there is exactly one implementation of the Registry's
   locking and validation discipline (`AcquireDispatchPendingLock`,
   per-session flocks), not two.
4. **`ethos enable --opencode`** (or equivalent flag/subcommand) deposits
   the plugin file and registers it in `opencode.json`'s `"plugins"` array
   (`.tmp/plugins-web.mdx:33-38`'s config shape), mirroring the existing
   `vox enable`/`z-spec enable` per-repo deposit pattern. **See §6.1 for a
   same-repo-only collision this specific mechanism has with one
   consuming repo's own dev-config gate** — not a general incompatibility
   with projects that use that repo's tooling elsewhere.

None of 1-4 is implemented in this mission. They are named here so the
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

### 6.1 Same-repo-only collision: `ethos enable --opencode` inside `punt-labs/points`'s own source tree

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
`ethos enable --opencode` inside the `points` repo's own checkout: that
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
harness-to-model config, and separately runs `ethos enable --opencode` for
the ethos OpenCode integration, does not hit this gate at all — the two
mechanisms write disjoint parts of `opencode.json` in that repo, and
`check-dev-config.sh` does not exist outside `points`'s own tree to
object. The one real, narrow case is: don't run `ethos enable --opencode`
inside `punt-labs/points`'s own source tree without first resolving the
"area-8 review" its own gate names.

This is **not** resolved by this design, deliberately: whether to (a) make
`ethos enable --opencode` idempotent against a pre-existing
`check-dev-config.sh`-style gate (there is no general mechanism for ethos
to know such a gate exists in an arbitrary consuming repo), (b) document
`ethos enable --opencode` as requiring the "area-8 review" `points`'s own
gate names before use inside `points`'s own repo, or (c) some other
reconciliation, is an operator decision this design flags rather than
makes. Named here as a narrow, same-repo-only open question for the
implementation stage or the operator to rule on — not silently ignored,
and not accepted as blocking this design's own scope (§6's `Non-goals`),
since it is a property of one repo's own gate script applied to its own
config file, not a general conflict between `points`-routed repos and
ethos-OpenCode-integrated repos, and not anything `ethos enable --opencode`
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
directionality exactly: it walks `parentID` upward only, never downward,
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
