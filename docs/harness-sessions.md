# Harness-neutral sessions design (ethos-leh7, m-2026-07-24-008)

Status: Settled (DES-061; operator-delegated rulings R1-R6, 2026-07-24).
Problem statement is the harness-independence investigation
(`.tmp/investigations/harness-independence.md`, 8 findings F1-F8); its
design sketch is the starting shape. This design turns that sketch into a
buildable contract.

## Problem

`ethos iam` and the session flow work only inside Claude Code. From a
plain terminal, or a non-Claude harness such as Codex, they fail. The
product intent is CLI-first: ethos must work for any harness.

The root cause (investigation F1): `iam` cannot create a session, it can
only join one. `runIam` calls `session.Store.Join`, which calls `Load`
and errors if the roster file is absent (`internal/session/store.go:88-91`,
`75-79`). The only thing that creates a roster in normal use is the
SessionStart **hook** (`internal/hook/session_start.go:68, 134-163`),
driven by `hooks/session-start.sh:14`, which fires only inside Claude
Code and takes its session ID from a Claude-supplied stdin JSON payload.
No hook, no roster, no `iam`.

Proven empirically, in a detached process with no `claude` ancestor
(ppid=1) and a clean `HOME`:

```text
$ ethos iam bwk
ethos: no session found in process tree. Use --session to specify one   # exit 1
```

Three supporting facts from the investigation shape the design:

- **F2/F4** — session discovery walks the process tree for a command
  named exactly `claude` (`internal/process/tree.go:64-70`) and falls back
  to `os.Getppid()` when none is found. Under any other harness the walk
  finds no session file. `whoami` is the one path that already works
  standalone, because `resolve.Resolve` falls through to git config and
  `$USER` (`internal/resolve/resolve.go:45-96`). It is the model to copy.
- **F3** — `ETHOS_SESSION` is honored by the CLI (`cmd/ethos/iam.go:57-59`)
  but is not sufficient alone: the roster must still exist, so setting only
  the env var fails with "session not found."
- **F5/F6** — the MCP `serve` surface has the same coupling, honors no
  `ETHOS_SESSION`, and its `iam` ignores `ETHOS_AGENT_ID` where the CLI
  honors it — two surfaces disagree on the agent key.
- **F7** — the session ID is used only as a filename base
  (`internal/session/store.go:35-37`); its shape is unconstrained. An
  arbitrary string works (`codex-fake-123` did).
- **F8** — there is no `ethos session start` verb. `session create` exists
  but is hidden and demands three required flags a user must invent.

The minimal hack that works today: export `ETHOS_SESSION`, call the hidden
`ethos session create --session … --root-id … --primary-id …`, then run
`iam`. That two-step dance is exactly the work a first-class primitive
should do in one call.

## Leader rulings (operator-delegated; recorded for review)

The operator delegated authority; these are decided.

- **R1** — `ethos session start` is the new one-call primitive. It mints
  an opaque session ID (shape unconstrained per F7), creates the roster
  with root and primary from the resolvable identity (the whoami chain),
  prints the ID and an eval-able `export ETHOS_SESSION=<id>` line, and is
  idempotent under an existing `ETHOS_SESSION` (reports it rather than
  minting a second). A matching `ethos session end` tears down, symmetric
  with the SessionEnd hook.
- **R2** — session **resolution** becomes an explicit strategy chain, in
  order: `--session` flag > `ETHOS_SESSION` env > process-tree walk
  (kept for Claude Code zero-config) > an actionable error naming
  `ethos session start`. No silent global fallback anywhere in the chain.
- **R3** — the walker stays `claude`-only. Generalizing process-name
  matching to other harnesses is speculative; Codex users take the env
  path. The walk is **bypassed entirely** when an explicit ID is present.
- **R4** — MCP `serve` honors the same chain: an `ETHOS_SESSION` hatch and
  `ETHOS_AGENT_ID` parity with the CLI (F5/F6).
- **R5** — roster staleness for a non-PID primary uses the existing 24h
  TTL, documented as the decided behavior. No new liveness machinery this
  round.
- **R6** — the Claude Code hooks keep creating sessions exactly as today
  (no regression to the zero-config path). `session start` and the hooks
  converge on the same store and schema.

## Design

### The primitive: `ethos session start`

One command creates a session from nothing. It is the un-hidden,
identity-aware successor to the plumbing that `session create` performs
today.

Behavior:

1. **Idempotency check (R1).** If `ETHOS_SESSION` is set and names a roster
   that loads, print that ID and its `export` line and exit 0 — do not
   mint a second session. This makes `eval "$(ethos session start)"` safe
   to run repeatedly in a shell rc or a harness init.
2. **Mint the ID (R1, F7).** Generate a UUIDv4 with `github.com/google/uuid`
   (already a vendored dependency; promote from indirect to direct). This
   matches Claude Code's own `session_id` shape, so every harness produces
   the same kind of ID and no downstream consumer needs to branch on
   origin. A zero-dependency alternative — 16 bytes from `crypto/rand`,
   hex-encoded — is noted under [Rejected alternatives](#rejected-alternatives)
   and is a drop-in if the team prefers no direct UUID dependency.
3. **Resolve the identities.** Root is the human, resolved via the whoami
   chain (`resolve.Resolve`: session → git name → git email → `$USER`).
   Primary is the agent: `ETHOS_AGENT_ID` if set, else the repo's default
   agent (`resolve.ResolveAgent`), else the resolved human. `--persona`
   overrides the primary's persona and folds the first `iam` into the same
   call.
4. **Create the roster and current-pointer.** Call the same
   `session.Store.Create` the hook uses (R6), then `WriteCurrentSession`
   keyed by a stable key (see [Current-pointer key](#current-pointer-key)).
   Repo and host are resolved exactly as the hook resolves them
   (`resolveRepo`, `resolveHost`).
5. **Print the contract.** On success, stdout is:

   ```text
   export ETHOS_SESSION=<id>
   ```

   with the human-readable confirmation on stderr, so
   `eval "$(ethos session start)"` sets the env in the calling shell
   without capturing the prose. `--json` emits `{"session": "<id>", …}`.

`ethos session end` is the teardown: resolve the session via the R2 chain,
`Delete` the roster, and remove the current-pointer — the same two
operations `HandleSessionEnd` performs (`internal/hook/session_end.go:26-33`).
It shares that code path so hook and CLI cannot drift.

### Session-ID mint and store schema

The store schema is **unchanged**. The roster YAML, the `sessions/<id>.yaml`
layout, the `sessions/current/<key>` pointer, and the participant model all
stay as they are. F7 established that the ID is an opaque filename base, so a
UUID drops in with no migration. The only new code is the mint and the
resolver; the storage contract is untouched. This is deliberate — the
smallest change that removes the coupling.

### Current-pointer key

Today `WriteCurrentSession` is keyed by the claude PID
(`internal/hook/session_start.go:160`), and every reader recomputes that key
with `FindClaudePID()`. That is the coupling. The design keeps the PID key
for the Claude Code path (R6) and makes the key **strategy-derived** for the
general path:

- When a session is resolved from `ETHOS_SESSION` or `--session`, the ID is
  already in hand — no current-pointer lookup is needed, so the key does not
  matter.
- When `session start` runs outside Claude Code, it writes the pointer keyed
  by `FindClaudePID()` (which returns the parent shell PID as fallback) **and**
  the primary path for later reads is `ETHOS_SESSION`, which `start` printed
  for the caller to export. The pointer is a convenience for descendants that
  did not inherit the env, not the primary channel.

The pointer therefore never becomes the sole discovery channel outside Claude
Code; `ETHOS_SESSION` is. This avoids inventing a new cross-invocation key
(which would be its own design problem) while keeping the zero-config Claude
path byte-identical.

### The resolution chain and its consumers (R2, R4)

Resolution is unified behind one ordered strategy, applied everywhere:

```text
1. --session flag        (explicit; walk bypassed — R3)
2. ETHOS_SESSION env     (explicit; walk bypassed — R3)
3. process-tree walk     (FindClaudePID → ReadCurrentSession; Claude Code zero-config)
4. actionable error      ("no active session; run `ethos session start`") — no silent fallback
```

This is the shape `resolveSessionContext` already half-implements
(`cmd/ethos/iam.go:43-76`); the change is to make it the single authority,
add the step-4 error text, and route every consumer through it. Each
consumer today:

| Consumer | Today | Under this design |
|----------|-------|-------------------|
| `iam` (CLI) | 3-step chain, then `Join`; error "no session in process tree" | same chain + step-4 error naming `session start` |
| `session show` | `ETHOS_SESSION` then PID walk; "No active session." | routed through the chain; unchanged output |
| `session join`/`leave` | `resolveSessionContext` or `--session` | routed through the chain |
| `mission claim`/`log` | `currentSessionIDBestEffort`: `ETHOS_SESSION` then PID walk (`cmd/ethos/mission.go:60-70`) | routed through the chain; step-4 stays best-effort ("" — the legacy tracked-log append, correct for ad-hoc CLI) |
| audit attribution | live/sealed zone keyed by session ID (DES-058) | the resolved ID is the same ID `start` minted and audit writes under — one ID, one zone |
| MCP `session`/`iam` | `session_id` arg then PID walk; no env; `iam` uses `FindClaudePID` (F5/F6) | add `ETHOS_SESSION` after the arg; `iam` honors `ETHOS_AGENT_ID` then `FindClaudePID` — parity with CLI (R4) |

`mission claim`/`log` keeps its best-effort tail: outside a session the
right behavior is the legacy tracked-log append, not an error, so its step 4
returns "" rather than failing. Every interactive, session-required command
(`iam`, `session join`/`leave`) takes the hard step-4 error. This split is
intentional and stated so a future reader does not "fix" the best-effort
path into a failure.

### Agent-ID parity (R4, F6)

CLI `iam` keys the participant on `ETHOS_AGENT_ID` then `FindClaudePID()`
(`cmd/ethos/iam.go:44, 71-73`). MCP `handleIam` uses `FindClaudePID()` only
(`internal/mcp/tools.go:204`). The design makes MCP honor `ETHOS_AGENT_ID`
first, so the same declaration records the same agent key on both surfaces.
This is a one-line change with no schema impact.

### Fate of the hidden `session create` (recommendation)

**Keep it as plumbing; do not fold it away.** `session create` takes an
explicit `--session`, `--root-id`, and `--primary-id` and does no minting or
identity resolution. `session start` is the identity-aware, ID-minting front
door built **on top of** `Create`. Three reasons to keep `create`:

1. It is the exact primitive tests use to construct a known-ID roster
   without minting (the investigation's own repro used it).
2. The SessionStart hook's `createSessionRoster` and `session start` both
   bottom out in `session.Store.Create` — keeping `create` as a thin CLI
   over that same call means one storage path, three entry points (hook,
   `start`, `create`), which is R6's convergence.
3. Removing it would delete a working plumbing verb to no benefit; it stays
   `Hidden: true`, so it is not user surface.

So: `session start` is new and visible; `session create` stays hidden
plumbing; both call `Store.Create`. No behavior change to `create`.

### Staleness of harness sessions (R5, F-invariant)

`isStale` treats the primary participant's agent ID as a PID and checks
liveness; a non-numeric ID falls back to a 24h TTL
(`internal/session/store.go:583, 588-600`). A `session start` session whose
primary is a persona handle (e.g. `bwk`) or an `ETHOS_AGENT_ID` string is
non-numeric, so it ages out by the TTL. **This is the decided behavior
(R5):** no PID-liveness for harness sessions, no new machinery. A harness
that wants prompt cleanup calls `ethos session end`; otherwise `ethos
session purge` reclaims it after 24h. The design documents this rather than
adding process-liveness for arbitrary harnesses, which would re-introduce
the process coupling this work removes.

## Harness adapters

Each harness supplies a session ID and an agent ID, then calls the shared
core. The adapters are thin.

**Claude Code (unchanged, R6).** The SessionStart hook remains the adapter:
it receives `session_id` on stdin and calls `createSessionRoster`. Zero
config, byte-identical to today. No user action.

**Codex / plain terminal.** One line at harness (or shell) init:

```bash
# Start (or re-attach to) an ethos session for this shell.
eval "$(ethos session start --persona bwk)"
# ETHOS_SESSION is now exported; iam, session, and mission all resolve it.
ethos whoami          # already worked standalone
ethos session         # shows the roster
```

On teardown:

```bash
ethos session end     # deletes the roster and current-pointer
```

Because `session start` is idempotent under an existing `ETHOS_SESSION`
(R1), re-running it in a subshell or a re-sourced rc is safe.

**MCP client (non-Claude).** Pass `session_id` on the tool call, or set
`ETHOS_SESSION` in the server's environment; `iam` then resolves it and
honors `ETHOS_AGENT_ID` for the participant key (R4).

## Migration

**None.** The store schema is unchanged, so existing rosters, current-
pointers, and audit zones are untouched. The Claude Code path is
byte-identical (R6). `session start` is additive; `session create` is
unchanged; the resolution chain is the existing chain with an explicit
step-4 error. Existing machines gain the new verb on binary update and need
no re-seed, no re-setup, no roster rewrite. The only observable delta is
that `iam` outside a session now names `ethos session start` in its error
instead of only `--session`.

## Test strategy

The detached-process, no-`claude`-ancestor harness built for the
investigation (`.tmp/harness/detached_run.py`: double-fork + `setsid` so the
process reparents to launchd with no `claude` in its ancestry, run under a
scratch `HOME`) becomes the standalone test fixture. Cases:

1. **`session start` standalone** — detached, clean `HOME`, inside a repo.
   Assert exit 0, that stdout is a single `export ETHOS_SESSION=<uuid>`
   line, that the roster file exists, and that the ID parses as a UUID.
2. **`iam` after `start`** — `eval` the start output, run `ethos iam bwk`,
   assert exit 0 and that the roster records persona `bwk` for the resolved
   agent. This is the end-to-end proof the investigation ran by hand,
   automated.
3. **Idempotency (R1)** — with `ETHOS_SESSION` already set to a live
   roster, `session start` prints that same ID and mints no second roster
   (assert `session list` count unchanged).
4. **Resolution-chain order (R2)** — table test: `--session` beats
   `ETHOS_SESSION` beats the PID walk; with none set and no walk hit,
   `iam` fails with the step-4 error naming `session start`. Assert the
   walk is bypassed when an explicit ID is present (R3) by pointing
   `--session` at a real roster while `FindClaudePID` would resolve a
   different one.
5. **`session end`** — after `start`, `end` removes the roster and the
   current-pointer; a second `end` is a no-op (exit 0).
6. **MCP parity (R4)** — unit test the MCP `iam` handler: with
   `ETHOS_AGENT_ID` set it keys the participant on that value, not
   `FindClaudePID()`; with `ETHOS_SESSION` set and no `session_id` arg,
   `resolveSessionID` returns it.
7. **Staleness (R5)** — a roster with a non-numeric primary is not stale
   before 24h and is stale after; assert `purge` reclaims it past the TTL.
8. **Claude Code no-regression (R6)** — the existing `session_start_test.go`
   cases (`{"session_id":"s1"}` …) still pass unchanged, proving the hook
   path is byte-identical.

`make check` (go vet, staticcheck, `go test -race`, validate-content) stays
green.

## Rejected alternatives

- **Generalize the walker to match `codex`/`cursor`/`node` now.** Rejected
  per R3: it is speculative — each harness names its process differently and
  some (Node-hosted) share a generic name that would false-match unrelated
  processes. The env path (`ETHOS_SESSION` from `session start`) serves
  every non-Claude harness with no guessing. If a specific harness later
  earns zero-config support, adding its name is a one-line, well-scoped
  follow-up.
- **Auto-start a session on `iam` when none exists.** Rejected as ambient
  magic. Implicit session creation means an `iam` typo silently spawns a
  fresh session instead of erroring, sessions accumulate invisibly, and the
  audit trail gains sessions no one explicitly opened. R2's "no silent
  fallback" is precisely to prevent this. The user opens a session
  deliberately with `session start`; `iam` only ever joins.
- **Encode a PID or hostname into the minted ID.** Rejected: F7 says the ID
  is opaque, and embedding process identity re-couples the ID to the process
  model this work removes. A UUID carries no such coupling.
- **Zero-dependency `crypto/rand` hex ID instead of UUIDv4.** Not rejected
  on merit — it is a valid drop-in and avoids promoting `google/uuid` to a
  direct dependency. UUIDv4 is chosen only because the dependency is already
  vendored and the format matches Claude Code's own `session_id`, keeping
  IDs visually uniform across harnesses. If the team prefers no direct UUID
  dependency, swap the mint function; nothing else changes.
- **New cross-invocation current-pointer key** (e.g. a fixed per-repo file
  instead of the PID key). Rejected as unnecessary scope: `ETHOS_SESSION` is
  the primary discovery channel outside Claude Code, so the pointer stays a
  convenience keyed as today. Inventing a new key is a separate problem this
  design does not need to solve.

## Invariants that must hold

- **One ID, one audit zone (DES-058).** The session ID `start` mints is the
  same ID audit appends write under and the same ID the resolution chain
  returns to every consumer. Audit live/sealed zones and DES-058 tombstones
  key on it (`internal/session/store.go` tombstone fields); nothing keys on
  the ID's *shape*, so a UUID is safe, but the ID must be stable for the
  session's life — `start` sets it once and never re-mints under an existing
  `ETHOS_SESSION` (R1).
- **Current-pointer consistency.** Whenever a roster is created, its
  current-pointer is written in the same operation (hook and `start` both do
  this), and `session end`/SessionEnd remove both together — no half state
  where a pointer outlives its roster or vice versa.
- **Schema stability.** No field is added or removed from the roster or
  participant model; the storage contract other tools read is unchanged.
- **Claude Code path unchanged (R6).** The hook still calls
  `createSessionRoster` → `Store.Create` → `WriteCurrentSession` with the
  claude PID key; `session start` and `session create` bottom out in the
  same `Store.Create`. Three entry points, one storage path.
