# PR/FAQ: Native OpenCode 2.x Integration for Ethos

## Press Release

**Ethos ships a native OpenCode plugin -- mission traceability survives
the switch to a second coding-agent harness.**

Priya runs an ethos-owned mission (`m-2026-07-01-003`, worker `bwk`,
evaluator `rsc`) through Claude Code every day. When she closes the
mission, `git log` on the merge commit shows `Mission:
m-2026-07-01-003` and `Delegation: d-...` trailers, stamped
automatically by ethos's commit-msg hook. Six months later she asks
the same worker, bound to the same mission, to finish a round
through OpenCode instead -- her team is piloting it for its faster
tool-result turnaround. The commit lands clean, `make check` passes,
the PR merges. But `git log` on that commit shows no `Mission:`
trailer at all. `ethos audit show --delegation <id>` has nothing to
reconstruct. The only record that this commit was mission-owned work
lives in Priya's memory of what she asked for.

This is not a edge case -- it is the default outcome today. Ethos's
commit-msg hook resolves "whose session is this" via
`resolve.SessionID` (`internal/resolve/resolve.go:311-359`), which
checks an `ETHOS_SESSION` environment override first and otherwise
requires a corroborated Claude Code PID lookup -- confirmed by direct
read, the function has **no process-tree-walk fallback at all**, an
earlier draft of this document said it did and was wrong. OpenCode
sets neither signal today, so `SessionID` returns `ErrNotUnderClaudeCode`
and the hook takes the "no session was ever expected" branch: no
trailer, and correctly **no diagnostic either**, because that branch
means "nothing was expected to happen here," not "something failed."
That is a different, narrower silence than this document's earlier
draft claimed -- `runHookCommitTrailers` DOES print a stderr diagnostic
(`ethos: commit-trailers: ...; no trailer added`) whenever a session
WAS expected and failed to resolve; it is not true that "a git hook has
no channel to tell the operator." The actual gap this integration
closes is narrower and more precise: OpenCode is not recognized as a
session-bearing harness at all today, so no session is ever expected
from it, and the silent branch -- correct for its own case -- fires
instead of the diagnostic branch that would fire for an ambiguous
Claude Code resolution failure.

Starting with this release, ethos ships an OpenCode plugin
(`ethos enable --opencode`, mirroring the existing per-repo
`vox`/`z-spec` enablement pattern) that calls the same `ethos mission
claim <id>` primitive Claude Code-bound workers already use for
Tier B dispatch, triggered from OpenCode's own `session.created` and
`session.updated` lifecycle events, subscribed via `ctx.event.subscribe()`
inside the plugin's `Plugin.define({id, setup(ctx)})` entry point,
instead of a Claude Code SessionStart hook. The commit-msg hook is
unchanged git-level machinery; it now
reads the claimed-mission binding from the same global,
session-ID-keyed `.punt-labs/ethos/` state it already reads for Claude
Code, regardless of which harness produced the commit. Priya's
OpenCode-driven commit
gets the same `Mission:`/`Delegation:` trailers her Claude
Code-driven commits already get. Nothing about Circuit's PR-watch
tracking changes -- the mutable PR head SHA stays GitHub/Circuit-owned,
exactly as it does today.

## FAQ

### Why not just reuse the Claude Code hook mechanism verbatim?

This document targets OpenCode V2 (confirmed released, v2.0.26 installed
locally, in production use on multiple operator machines). The hook API
shown below is sourced from
`services/www/src/docs/content/build/plugins/migrate-v1.mdx` on
`github.com/anomalyco/opencode`, confirmed via context7. An earlier draft
of this document used V1 hook syntax sourced from the stable
`opencode.ai/docs/plugins/` site before V2's release was confirmed;
that syntax has been replaced throughout.

Because the two mechanisms are not compatible artifacts, and "reuse
verbatim" is a category error, not a cost-saving option.

Claude Code hooks are shell commands (or MCP tool calls) invoked by
the Claude Code process itself, receiving Claude-Code-shaped JSON on
stdin with fields like `session_id`, `transcript_path`, `source`
(`"startup"`/`"resume"`/`"clear"`/`"compact"`/`"fork"`), and
`agent_type`, configured in `.claude/settings.json`'s `hooks` block
[code.claude.com/docs/en/hooks.md, pp.36-37, quarry-indexed]. OpenCode's
extension mechanism is a JS/TS module -- not a shell subprocess -- loaded
from `.opencode/plugins/` or `~/.config/opencode/plugins/` or an npm
package named in `opencode.json`'s `"plugins"` array (each entry a
`{package, options}` object), exporting a default
`Plugin.define({id, async setup(ctx) {...}})` object whose `setup`
function registers handlers against OpenCode's own hook surfaces --
`ctx.session.hook("context"|"prompt"|"compaction", ...)` for
session-level hooks, `ctx.tool.hook("execute.before"|"execute.after",
...)` for tool hooks, `ctx.shell.hook("create.before", ...)` for shell
hooks, `ctx.permission.hook("evaluate", ...)` for permission hooks --
plus `ctx.event.subscribe({signal})`, consumed via `for await` inside
`setup`, for session lifecycle event types such as `session.created`,
`session.compacted`, `session.deleted`, `session.diff`, `session.error`,
`session.idle`, `session.status`, and `session.updated`
[github.com/anomalyco/opencode `services/www/src/docs/content/build/plugins/migrate-v1.mdx`,
confirmed via context7]. Claude Code will never invoke a
`.opencode/plugins/*.ts` file; OpenCode has no concept of
`.claude/settings.json`'s `hooks` block. There is nothing to "reuse
verbatim" -- the integration has to be written twice, once per harness,
in that harness's own idiom.

What *is* shared, and does not need rewriting: the git-level
commit-msg hook that stamps `Mission:`/`Delegation:` trailers. That
hook already runs independent of which process made the commit --
it's a plain git hook, not a Claude Code hook. The only change is
*what it reads* to decide whose mission a commit belongs to: today it
resolves a session via `resolve.SessionID` (`ETHOS_SESSION` env, then a
corroborated Claude PID -- no process-tree walk, confirmed by direct
source read); the fix is to have the harness-native plugin ensure
`ETHOS_SESSION` resolves to the right identity for OpenCode too.

**Corrected here, a real gap an earlier draft glossed over.** It is
not enough for the plugin to call `ethos mission claim <id>` with no
further argument. `runMissionClaim`
(`cmd/ethos/mission.go:2076-2129`) writes its active-mission sidecar
under the caller's OWN resolved session -- it does not register a new
OpenCode session with ethos, and it does not allocate a delegation.
And that sidecar lives under the user-GLOBAL,
session-ID-keyed `$HOME/.punt-labs/ethos/sessions/<session-id>/` tree
(`internal/mission/active.go`'s `ActiveMissionPath`), not a per-worktree
file as an earlier draft of this FAQ claimed -- `resolve.SessionID`
reads `ETHOS_SESSION` from THAT process's environment, so setting it
only for the `mission claim` subprocess has no effect on a LATER
committing shell unless the SAME value is exported into that shell's
environment too. The actual requirement, stated precisely: the plugin
must (1) register the OpenCode session with ethos (allocating a
delegation under the active mission, not merely writing a bare
sidecar), and (2) export `ETHOS_SESSION=<opencode-session-id>` into the
environment of every subprocess OpenCode spawns for that session,
including whatever shell eventually runs `git commit` -- for example
via `ctx.tool.hook("execute.before", ...)` setting the env on each
tool invocation, if OpenCode's own process model allows a plugin to
influence a spawned shell's environment this way (confirming that
mechanism is a prerequisite for this design, not assumed here). Only
once BOTH of those are true does "worktree independence" hold for
OpenCode the way it already holds for Claude Code -- it is a property
of each harness correctly keying its own session identity, not of
where a correlation file happens to live on disk.

### What happens if OpenCode forks a session mid-mission?

OpenCode's `POST /api/session/{sessionID}/fork` (operation ID
`session.fork`) accepts an optional branded `before` message ID;
omitting it copies the full history
[github.com/anomalyco/opencode `V2_HTTP_API_AUDIT.md`, Group 4 table,
row 053, quarry-indexed, full page dump verified]. The V2 session
contract spec is explicit that forking "copies messages only through
its selected boundary but adopts the parent's newest instruction
values as its baseline"
[github.com/anomalyco/opencode `specs/v2/session.md`, "Instructions
Are Value Deltas" section, quarry-indexed, full page dump verified].
a fork gets its own, distinct OpenCode Session ID.

**Corrected here -- an earlier draft overstated how little ties a
fork's identity to its parent.** Forking does NOT publish a generic
`session.created` event, confirmed by direct read of
`anomalyco/opencode`'s own `packages/core/src/session.ts`: it publishes
a dedicated `SessionEvent.Forked`, whose payload carries `parentID`
DIRECTLY: `bus.publish(SessionEvent.Forked, { sessionID, parentID:
parent.id, boundary: {...}, ...inherited })`. The parent is handed to
the plugin's event subscriber in the SAME hook-plugin context the rest
of this integration already uses, at fork time, with no need to reach
across to the separate CLI-plugin data-cache API for this specific
lookup.

That means the mission-claim binding does **not** carry over
automatically, by construction, and it should not: if it did, any
unrelated fork of a mission-bound session would silently inherit
mission authorship for whatever the forked conversation does next --
exactly the kind of unattributed-commit risk this integration exists
to close. The correct handling, and the one this integration ships:
the plugin's `ctx.event.subscribe()` loop observes `SessionEvent.Forked`
for the child, reads `parentID` directly off that event's own payload,
and checks whether `parentID` (or any ancestor reachable from it) holds
an active mission claim. The CLI-plugin data-cache API's
`context.data.session.root(sessionID)`/`.family(sessionID)` remain
available as a general-purpose, independently queryable cross-check --
useful if a plugin needs lineage for a session OTHER than at the moment
of its own fork event -- but are not the PRIMARY mechanism for this
specific lookup, since `parentID` is simpler, same-context, and
requires no cross-plugin-surface dependency
[github.com/anomalyco/opencode `packages/core/src/session.ts`, directly
sourced via context7]. If so, the plugin opens a **new delegation round
under the same mission** for the forked session -- ethos's mission model
already supports multiple delegations per mission, so this is an
existing primitive applied to a new trigger, not new machinery. This
mirrors how Claude Code's own `--fork-session`/`/fork` is already
surfaced to
hooks via `source: "fork"` [code.claude.com/docs/en/hooks.md, p.36] --
a fork is treated as a new delegation, never a silent identity swap.

**Confidence: high** on every leg of this FAQ as now written -- the
HTTP endpoint and the spec's fork-copies-history claim (both directly
sourced, full documents read), and the `SessionEvent.Forked` event's
own `parentID` field, confirmed by direct read of
`packages/core/src/session.ts`'s actual `bus.publish` call -- the
highest-confidence source available for this specific claim, since it
is the implementation itself, not a documentation page describing it.
The open question in an earlier round (whether a GENERIC
`session.created` event's payload exposes parentage field-by-field) is
moot for this FAQ now: forking fires its own dedicated event with
`parentID` built in, so there is no generic-payload ambiguity to
resolve for the fork-lineage case specifically. The
`context.data.session.root`/`.family` CLI-plugin lookups remain a
separately useful, independently confirmed general-purpose mechanism
(see the restart FAQ below), just not the primary one for fork
handling.

### What happens on ethos service restart?

Two different processes can restart, and they fail differently.

**Ethos's own MCP server restarting** is low-risk for this
integration specifically, because the correlation state this
integration introduces is a flat file under the global,
session-ID-keyed `$HOME/.punt-labs/ethos/sessions/<session-id>/` tree
(`internal/mission/active.go`'s `ActiveMissionPath` -- the same
location, not a per-worktree one, Claude Code dispatch already writes
mission state to today), not an in-memory session object. A restarted
ethos binary re-reads that file on its next tool call. The documented risk for ethos restarts is a *different* one,
already known and unrelated to OpenCode: reinstalling ethos mid-session
does not restart an already-running MCP server, so a stale binary can
keep answering until the server is manually reconnected (this repo's
own `.punt-labs/ethos/CLAUDE.md` "Gotchas" section). That risk is
orthogonal to this integration and not introduced by it.

**OpenCode's own server process restarting** is the harder case, and
it is handled by OpenCode itself, not by ethos. The V2 session spec
defines a write-ahead execution claim: "Execution commits a
write-ahead claim when a process-local busy period starts. Success,
failure, and user interruption release the claim; shutdown
interruption and unclean process death preserve it. On startup,
managed Node and fetch runtimes resume claimed top-level Sessions,
append a durable continuation instruction, and count recovery
attempts. Recovery is bounded per claimed execution but does not
guarantee exactly-once provider requests or tool effects."
[specs/v2/session.md, "Execution Is Process-Local" section, directly
sourced]. The spec also states plainly: "`sessions.active()` snapshots
busy periods currently owned by this process. Durable execution
events and claims are historical and recovery records, not proof that
this process is still live." [same section]. Practically: if the
OpenCode server crashes and restarts mid-mission, OpenCode may resume
the session and re-attempt a step -- without guaranteeing the retried
tool call (e.g. a commit) runs exactly once. This integration's
correlation metadata (which mission/delegation a given session maps
to) is unaffected by this -- it's a durable file, re-read fresh -- but
the mission worker's own commit-per-step discipline (already required
by this repo's mission-contract success criteria) is the actual
safeguard against a double-applied effect, not anything this
integration adds.

The spec language above answers "is the claim durable," but not "can
the plugin cheaply check liveness without re-deriving it from
execution-claim semantics on every poll." For that, the CLI-plugin
data-cache API exposes exactly this as a maintained projection:
`context.data.session.status(sessionID)` returns the session's current
status directly, alongside `sync(sessionID)` to force a refresh and
`invalidate(sessionID)` to drop the cached value
[github.com/anomalyco/opencode `services/www/src/docs/content/build/plugins/cli.mdx`,
"Sessions" section, quarry-indexed, full page dump verified]. A
plugin-side liveness check for this integration -- "is the session
this mission is claimed against still live, or did the OpenCode server
restart and leave the claim durable-but-stale" -- reads as a direct
`context.data.session.status(sessionID)` call rather than requiring
the plugin to reconstruct liveness from the write-ahead-claim
recovery language itself. This is a materially cheaper answer than
polling `sessions.active()` and cross-referencing recovery-attempt
counts, and it is the mechanism this integration should use for that
check.

### What happens with OpenCode worktree or concurrent sessions?

OpenCode's own concurrency model, directly from the V2 spec's
`SessionRunCoordinator` ownership rules: "Explicit resumes join the
active execution for the same Session. Repeated wakes coalesce into
one follow-up drain. **Different Sessions run concurrently.**
Interruption stops locally owned execution without deleting pending
input." [specs/v2/session.md, "Execution Is Process-Local" section,
directly sourced]. One OpenCode server process can run many Sessions
at once, each independently.

V1's flat `directory` context field survives in V2 as
`ctx.location.directory`; the matching flat `worktree` field does not
-- V2 instead exposes worktree handling as a structured, per-project
domain API (`ctx.worktree.create`/`.list`/`.refresh`/`.remove`, each
requiring a `projectID`)
[github.com/anomalyco/opencode `services/www/src/docs/content/build/plugins/migrate-v1.mdx`
for the `directory` -> `ctx.location.directory` rename;
`services/www/src/docs/content/build/plugins/index.mdx` for the
`ctx.worktree.*` domain API shape; both confirmed via context7].

**Open question, corrected here -- an earlier draft asserted this was
settled when it was not.** `ctx.location` is the V2 plugin's own
load-time location, documented as a property of the PLUGIN INSTANCE,
not necessarily of every session or event that instance later
receives. If one running plugin instance can observe lifecycle events
for a session belonging to a DIFFERENT checkout than the one it was
loaded in, keying a correlation write by `ctx.location.directory`
could silently file that session's state under the WRONG worktree.
Whether that cross-checkout scenario is actually reachable in
practice -- and, if so, what field (if any) identifies an EVENT's own
affected session's location independently of the plugin's load-time
`ctx.location` -- is not resolved by anything read for this document
and is left as an explicit prerequisite for the next pipeline stage's
design, not assumed settled here.

This integration's correlation state also does NOT live in a
per-worktree file, correcting an earlier draft's claim: it lives under
the user-global, session-ID-keyed
`$HOME/.punt-labs/ethos/sessions/<session-id>/` tree (see the restart
FAQ above), so worktree independence for OpenCode -- like for Claude
Code today -- is a property of each harness correctly keying a
DISTINCT session identity per worktree's work, not of the correlation
file's own location on disk. This repo's own worktree convention
(`.claude/worktrees/agent-<id>` per in-flight agent, documented in the
org `CLAUDE.md`) already demonstrates multiple ethos-bound missions
running in separate worktrees of the same repo simultaneously, today,
under Claude Code, via exactly this mechanism -- distinct session IDs,
not distinct storage locations.

### Why a bundled plugin instead of a standalone adapter or config-only wiring?

Three options were weighed:

**Config-only wiring** -- document a `.opencode/plugins/ethos.ts` file
for the user to hand-install. Lowest build cost, lowest reliability:
every repo owner has to independently discover and wire it, and
nothing re-deposits an upgraded version when ethos ships a fix, the
same failure mode the "first-class" bar exists to close. Rejected:
this reproduces exactly the 12-step manual-config dropout pattern this
repo's own `docs/prfaq-onboarding.md` documents ethos already fixed
for Claude Code setup -- shipping the OpenCode side as hand-rolled
config would be a regression against ethos's own stated product bar.

**Standalone adapter process** -- a separate long-running process that
observes OpenCode's `event.subscribe()` server-sent-event stream
[opencode.ai/docs/sdk/, p.36, directly sourced] out-of-process and
writes the correlation file. This avoids coupling ethos's Go binary to
OpenCode's Bun/Node plugin runtime, but doubles the "stale process"
failure surface ethos already has to manage for its own MCP server
(see the restart FAQ above) -- now two independently-restartable
processes must both be current for the integration to work, instead
of one. It is also strictly observation-only; it cannot act inside
`ctx.tool.hook("execute.before", ...)` the way an in-process plugin hook can, which
costs nothing for this specific non-goal (correlation metadata only,
no gating) but forecloses that door for later work. Rejected as the
first-class answer, though it remains a viable interim stopgap if the
plugin build slips.

**Bundled OpenCode plugin (recommended)** -- ship the plugin as a
first-class artifact of the ethos release, deposited and marked the
same idempotent way `vox`/`z-spec` are enabled per-repo today (`ethos
enable --opencode` writes a marker, e.g.
`.punt-labs/ethos/opencode/enabled`, alongside the plugin file). Its
job is deliberately narrow: a `Plugin.define({id, setup(ctx)})` entry
point whose `setup` subscribes via `ctx.event.subscribe()` for
`session.created`/`session.updated` and, on each, calls the *existing*
`ethos mission claim <id>` CLI primitive (already shipped for Tier B
dispatch binding, per this repo's own Build & Run docs) to write the
correlation file; let the unchanged git-level commit-msg hook read it.
This asks for exactly one new trigger on an
already-existing primitive -- no new ethos mechanism, no second
long-running process, and it follows the same "deposit + marker +
`@`-import" enablement pattern every other punt-labs tool
(vox, z-spec, quarry) already uses, so a developer who has enabled any
one of those tools in this repo already knows the shape of what
`ethos enable --opencode` does.

**A known downstream adoption blocker, disclosed here rather than
discovered later:** `punt-labs/points` currently requires every active
OpenCode plugin to be built-in, so enabling this integration there as
written would fail that repo's own `make doctor` check, and registering
it through `opencode.json` would fail its `make check-dev`. This is a
real cross-repo policy conflict, not a defect in THIS recommendation --
closing it requires `points` to explicitly review and admit this one
reviewed, pinned ethos plugin without opening its built-in-only policy
to arbitrary external plugins, a decision that repo owns and this
document does not make on its behalf.

**This also matches the in-house precedent for "one control plane,
many harnesses."** `model-provider`/Bifrost already routes model
selection across four different coding-agent harnesses (Claude Code,
OpenCode, codex, pi) by writing into *each harness's own native
config surface* rather than requiring a shared binary all four run
("Model routing (Bifrost)," this org's own `CLAUDE.md`) -- the lesson
transfers directly: meet each harness where its own extension point
already is, rather than inventing a cross-harness daemon.

### Non-goals (explicitly confirmed in scope)

This work does **not** design Circuit's PR-watch workflow tracking.
The mutable PR head SHA remains GitHub/Circuit-owned, unaffected by
anything here. This integration covers exactly one thing: **stable
correlation metadata** -- OpenCode Session ID, Bead ID, ethos mission
ID, delegation ID -- written once, read by the commit-msg hook, never
mutated by downstream tooling. That distinction (immutable
correlation metadata vs. mutable live state) is not novel to this
integration; it is the same distinction SLSA build provenance draws
between provenance (generated once, attached, verified, never
rewritten) and the artifact's own mutable build/deploy state
[slsa.dev/spec/v1.0/levels, p.5, quarry-indexed; this org's `docs/CI.md`
§9, quarry-indexed]. Ethos's correlation metadata is provenance, not a
live tracker.

## Exemplars: binding one control plane's identity/audit layer across multiple runtimes

At least three real precedents for "bind one tool's identity or audit
state across more than one coding-agent/service runtime," with what's
transferable to this problem:

1. **W3C Trace Context (`TRACEPARENT`/`TRACESTATE`)** -- a
   vendor-neutral HTTP header pair that propagates a single trace ID
   across service boundaries regardless of which instrumentation
   library or language produced a given hop. Claude Code itself
   already reads `TRACEPARENT`/`TRACESTATE` from the environment for
   distributed trace linking [code.claude.com/docs/en/changelog.md,
   quarry-indexed: "read `TRACEPARENT`/`TRACESTATE` from the
   environment for distributed trace linking"]. Transferable: a single
   correlation identifier that travels as inert metadata works across
   runtimes that otherwise share nothing -- exactly the shape of
   "mission ID + delegation ID" traveling from ethos through whichever
   harness is driving a given round.

2. **SLSA build provenance** -- provenance is generated once, by the
   build process, and attached to the artifact; every downstream
   consumer (a registry, a deploy pipeline, a security scanner)
   verifies it but never rewrites it [slsa.dev/spec/v1.0/levels, p.5,
   quarry-indexed; this org's `docs/CI.md` §9, quarry-indexed].
   Transferable: the immutable/mutable split this PR/FAQ's non-goals
   section draws (stable correlation IDs vs. Circuit's mutable PR head
   SHA) is a direct instance of the same provenance-vs-live-state split
   SLSA formalizes for builds.

3. **Ethos's own `Mission:`/`Delegation:` git trailers** -- ethos
   already does exactly this pattern for Claude Code today: a
   commit-msg hook stamps trailers derived from session state at
   commit time, and `ethos audit show --delegation <id>` reconstructs
   the full trail from that trailer. This is not a precedent from
   outside the org -- it is the exact mechanism this PR/FAQ proposes
   extending to a second harness. **Corrected here:** an earlier draft
   cited this history to `docs/MODERNIZATION_CASE_STUDY.tex`, a file
   confirmed, by direct check, NOT to exist anywhere in this
   repository. The real, verifiable incident is commit `d3ba3c4` (bead
   `ethos-jawp`, PR #412): the commit-msg hook's fallback keyed trailers
   off the delegation-binding sidecar, which was never cleared, so a
   STALE binding from an earlier session tagged unrelated commits with
   the WRONG mission -- a mis-attributed trailer, not a silently
   missing one as an earlier draft of this document characterized it.
   The root cause is the same class this integration has to design
   against regardless of which direction the failure points: a sidecar
   that outlives the session state it was written for, discovered in
   production once already and not something to discover again in a
   second harness before fixing it formally the first time.

A fourth, narrower precedent directly informed the plugin-vs-adapter-
vs-config decision above: the in-house `model-provider`/Bifrost tool,
which already routes one model selection across four different
coding-agent harnesses by writing into each harness's own native
config surface rather than requiring a shared daemon all four run
("Model routing (Bifrost)," this org's own `CLAUDE.md`).

## What I could not verify, and confidence levels

- **High confidence, directly sourced, full documents read in full:**
  the plugin loading mechanism and event names (`opencode.ai/docs/plugins/`,
  32 pages, read in full); the SDK's `session.*` client method table
  (`opencode.ai/docs/sdk/`, 38 pages, read in full); the V2 session
  contract's `SessionExecution`/`SessionRunCoordinator` ownership
  rules, write-ahead claim/recovery model, and fork/instruction-epoch
  semantics (`specs/v2/session.md` on the `anomalyco/opencode` `dev`
  branch, fetched directly via its GitHub blob view and read in full,
  17 pages); the `session.fork` HTTP endpoint's `before`-message-ID
  contract change (`V2_HTTP_API_AUDIT.md`, same repo/branch, fetched
  directly and read in full, 28 pages); ethos's own existing
  `Mission:`/`Delegation:` trailer mechanism and its prior
  mis-attribution incident (commit `d3ba3c4`, bead `ethos-jawp`, PR
  #412 -- corrected from an earlier draft's citation to a file
  confirmed not to exist in this repo); the W3C
  Trace Context precedent via Claude Code's own `TRACEPARENT`/
  `TRACESTATE` support; the SLSA provenance precedent; the CLI-plugin
  `context.data.session.{list,get,root,family,cost,status,sync,invalidate}`
  data-cache API, plus its sibling `.pending`, `.message`, `.permission`,
  and `.form` namespaces (`services/www/src/docs/content/build/plugins/cli.mdx`
  on the `anomalyco/opencode` `dev` branch, fetched directly via its
  GitHub blob view and read in full, 28 pages -- this source was not
  reachable in round 1 and was supplied by the leader for round 2;
  see below).

- **Medium confidence, partially inferred:** the exact field set a
  `session.created`/`session.updated` event's payload carries (as
  opposed to a *tool's* execution context, which the `custom-tools`
  doc does confirm includes `agent`, `sessionID`,
  `messageID`, `directory`, `worktree`
  [opencode.ai/docs/custom-tools/, p.9, directly sourced]). A prior,
  separate research effort (quarry `session-b8a0261f`, conducted for
  biff's notification-channel problem, not this traceability problem)
  asserted from a now-deleted source clone that "the plugin DOES have
  sessionID in every hook" -- I am citing that as a secondhand,
  code-level claim from a peer agent's transcript, not something I
  independently re-verified against source, because the shallow clone
  referenced (`~/Coding/punt-labs/biff/.tmp/opencode-src`) no longer
  exists on this machine (confirmed absent; the same transcript
  records it was deliberately deleted after that research closed).
  Per this mission's own instructions, I have treated that prior
  session's factual claims about OpenCode's event model as reliable
  background, while explicitly not treating its conclusions (which
  solved a different problem) as answering this one.

- **Resolved in round 2, was a correctly-disclosed gap in round 1:**
  the mission brief named a
  `context.data.session.{list,get,root,family,cost,status,sync,invalidate}`
  API surface, described as coming from "the CLI plugin docs." Round 1
  searched the full text of every OpenCode document reachable from this
  session's tools -- `opencode.ai/docs/plugins/`, `/docs/sdk/`,
  `/docs/custom-tools/`, and both V2 spec documents fetched directly
  from the `anomalyco/opencode` repository -- and found no
  `context.data.session` surface in any of them, because this session
  had no context7 or WebFetch tool, only quarry's CLI (ingest + find)
  run through Bash. The leader independently re-checked via context7
  after round 1 and located the source:
  `services/www/src/docs/content/build/plugins/cli.mdx` on the
  `anomalyco/opencode` `dev` branch -- a documentation page for a
  distinct plugin surface (`@opencode/plugin/tui`, the **CLI/TUI
  plugin**, loaded via a package's `./tui` export) from the one the
  rest of this document scopes to (`.opencode/plugins/*.ts` **hook
  plugins**, loaded via `.opencode/plugins/` or the `opencode.json`
  `"plugins"` array). The two surfaces are siblings, not alternatives: a
  single package can export both a main hook-plugin entry and a `./tui`
  CLI-plugin entry for "automatic loading"
  [cli.mdx, "Publish and load" section, directly sourced]. Fetched
  directly via GitHub blob view in round 2 and read in full (28 pages).
  Confirmed verbatim: `context.data.session.list()`,
  `.get(sessionID)`, `.root(sessionID)`, `.family(sessionID)`,
  `.cost(sessionID)`, `.status(sessionID)`, `.sync(sessionID)` (async),
  `.invalidate(sessionID)`, plus sibling `.pending.*`, `.message.*`,
  `.permission.*`, and `.form.*` namespaces on the same `session`
  object [cli.mdx, "Sessions" section]. This did not require retracting
  anything written in round 1 -- the gap was correctly scoped and
  disclosed, not guessed at, and the CLI-plugin data-cache surface does
  not supersede any claim already made about the hook-plugin surface.
  It strengthens two FAQs above (fork lineage lookup, restart
  liveness check) and is now cited there directly.

- **A fast-release-cadence risk worth naming explicitly:** the public
  `opencode.ai/docs/sdk/` page's `session.*` method table (crawled
  2026-08-29, re-crawled fresh as part of this research) does **not**
  list `session.fork` at all -- only `session.children`. The
  authoritative, current `V2_HTTP_API_AUDIT.md` on the `dev` branch
  (regenerated 2026-09-13 per its own header) explicitly lists
  `POST /api/session/{sessionID}/fork` as `Change`-dispositioned for
  V2. The stable docs site lags the `dev`-branch HTTP API audit by at
  least the fork endpoint. Any ethos-side implementation should treat
  the `specs/v2/` and `V2_HTTP_API_AUDIT.md` documents on the `dev`
  branch as the source of truth over the public docs site, and expect
  both to keep moving.
