# MCP write methods and the mission write-set

Design note resolving bead `ethos-7b6c`. Found by djb during PR #424
(DES-068, scoped MCP grants to specialist roles). Mission
`m-2026-08-05-001`; design only, no code change in this round.

## Summary

The threat: a delegated agent writes a file the mission contract never
authorized, and no mechanical control stops it.

Two findings, in order of importance.

1. **The `extractTargetPath` gap is real but narrow.**
   `internal/hook/pretooluse.go:237-251` returns a target path only for
   `Write` and `Edit`; every other tool returns `""`, and
   `pretooluse.go:114-118` treats `""` as "allow unconditionally". So an
   MCP tool carrying a write method is never checked against the
   allowlist.
2. **The gap is smaller than the bead assumed, because the gate itself
   is narrower than the bead assumed.** The allowlist is only ever set
   for **verifier** spawns —
   `internal/hook/subagent_start.go:179-194` sets
   `ETHOS_VERIFIER_ALLOWLIST` inside the `len(verifierMissions) > 0`
   branch and nowhere else. For a worker,
   `pretooluse.go:109-112` reads an empty env var and returns allow
   before `extractTargetPath` is ever called. A worker's plain `Write`
   to a path outside its `write_set` is already ungated today. This is
   stated in `DESIGN.md:4560-4562`: "For workers (non-verifier spawns),
   the env vars are unset and the hook is a passthrough."

Consequence for the remedy: extending `extractTargetPath` to map MCP
tools to paths would harden **verifier** sessions only. It would not
constrain workers at all, and workers are where the design work
happens. Any remedy sold as "now the write-set covers MCP writes" would
be false.

Third input to the decision: 36 of 39 roles in
`.punt-labs/ethos/roles/` grant `Bash`. `ethos identity create` is a CLI
command. A deny on the `identity` MCP tool is reachable around via
`Bash` in 36 of 37 MCP-granted roles. This is already recorded as the
reasoning for the one exception — DES-068 (`DESIGN.md:7021-7023`) omits
`identity` from `tech-writer` precisely because `tech-writer` is the
only role without `Bash`. So an MCP path-map is a guardrail against
accident, not a boundary against intent. Calling it a boundary would be
the more dangerous outcome than the gap itself.

## Mechanism, grounded in the code

### The gate

`internal/hook/pretooluse.go`:

- `:237-251` — `extractTargetPath` switches on tool name; `Write` and
  `Edit` return `tool_input["file_path"]`; `default` returns `""`.
- `:114-118` — `target == ""` short-circuits to
  `preToolUseAllow()` with the comment "Tool does not target a file
  path — allow unconditionally."
- `:109-112` — before that, an unset `ETHOS_VERIFIER_ALLOWLIST` returns
  allow, so nothing downstream runs.
- `:120-151` — the actual containment check
  (`pathAllowed` then `mission.PathContainedBy`) plus the DES-052
  stat-then-allow branch for `ETHOS_VERIFIER_EXTRACT_INTO`.

`internal/hook/subagent_start.go:179-194` is the only writer of
`ETHOS_VERIFIER_ALLOWLIST`, inside the verifier-isolation branch.

### The exploit path named in the bead

`mcp__plugin_ethos_self__identity` with `method=create`
(`internal/mcp/tools.go:171,192-193`) reaches
`LayeredStore.Save` (`internal/identity/layered.go:358-394`):

- `:359` — `ValidateRefs` first; an identity naming an absent
  personality, writing style, or talent is refused.
- `:362-366` — `writeStore()` prefers the **repo** layer, so the file
  lands at `.punt-labs/ethos/identities/<handle>.yaml`, git-tracked,
  inside the repo, outside any `write_set`.
- `:375-381` — `os.OpenFile(..., O_WRONLY|O_CREATE|O_EXCL, 0o600)`.
  Create-only. An existing identity cannot be overwritten or modified
  through this path; the call fails with "identity %q already exists".
- `:393` — also creates an ext directory.

So the write primitive available is **create a new identity file**, not
**modify an existing one**. That distinction carries most of the
severity argument.

### A second in-repo write path, not named in the bead

The z-spec tools granted to `z-specialist` and `b-specialist` write
report sidecars next to the spec source:

- `punt_zspec/commands/check.py:10,45` calls `save_fuzz`
- `punt_zspec/commands/model_check.py:11,58` calls `save_report`
- `punt_zspec/commands/test.py:11,62` calls `save_report`
- `punt_zspec/commands/animate.py:11,51` calls `save_report`
- `punt_zspec/report.py:44-51,77-79` — the destination is
  `tex_path.parent / (tex_path.stem + ".report.json")` and
  `".fuzz.json"`; `_save_json` is a plain `write_text`, no `O_EXCL`, so
  these **overwrite**.

That is an in-repo, overwriting, ungated write reachable from a granted
MCP tool. It is a stronger primitive than `identity create`, and it was
missed in the PR #424 review. Paths verified against the installed
plugin cache at version 0.17.0.

## Current exposure

Read from `.punt-labs/ethos/roles/*.yaml` (39 role files; 37 grant MCP
tools; `ceo` and `coo` carry no `tools:` key). Counts are of role files
naming the tool, not estimates. Every grant appears twice in the YAML —
once under the released plugin prefix and once under `<tool>-dev` — and
is counted once here.

| Granted MCP tool | Roles | Write method | What it writes | In repo? | Overwrites? |
|---|---:|---|---|---|---|
| `ethos_self__identity` | 36 | `create` | `.punt-labs/ethos/identities/<handle>.yaml` plus ext dir | Yes, git-tracked | No — `O_EXCL` |
| `z-spec_zspec__check` | 2 | always | `<spec>.fuzz.json` beside the `.tex` | Yes | Yes |
| `z-spec_zspec__model_check` | 2 | always | `<spec>.report.json` beside the `.tex` | Yes | Yes |
| `z-spec_zspec__test` | 2 | always | `<spec>.report.json` beside the `.tex` | Yes | Yes |
| `z-spec_zspec__animate` | 2 | always | `<spec>.report.json` beside the `.tex` | Yes | Yes |
| `ethos_self__session` | 37 | `join`, `leave`, `iam` | `~/.punt-labs/ethos/sessions/<id>.yaml` | No | n/a |
| `quarry_quarry__remember` | 37 | always | `~/.punt-labs/quarry/data` (`config.py:27`) | No | n/a |
| `quarry_quarry__ingest` | 37 | always | same store; source is HTTP(S) only | No | n/a |
| `quarry_quarry__use` | 37 | selects DB | daemon/session state | No | n/a |
| `biff_tty__plan` | 37 | always | biff-side status string | No | n/a |
| `quarry_quarry__find`, `show`, `status`, `list` | 37 | none | — | — | — |
| `biff_tty__read_messages` | 37 | marks read | inbox read-state, biff-side | No | n/a |
| `z-spec_zspec__browse`, `get_report` | 2 | none | — | — | — |

Three roles lack `identity`: `ceo`, `coo` (no `tools:` key at all) and
`tech-writer` (DES-068's deliberate exception).

**The in-repo set is exactly two tool families**: `ethos identity
create` (36 roles, create-only) and the four z-spec report writers
(2 roles, overwriting). Everything else writes outside the repo
entirely — it cannot dirty a working tree, cannot enter a PR diff, and
cannot alter a reviewed artifact.

## Remedies

### (a) Map MCP write-tools to target paths in `extractTargetPath`

Shape: extend the switch to recognize each known MCP write tool and
return the path it will write, then let the existing `pathAllowed`
check gate it.

Cost of keeping it correct. The mapping is a claim about **another
repo's internals**, made in ethos, checked by nobody. Three concrete
problems:

1. The z-spec destination is derived at runtime from a spec path plus a
   stem transform (`report.py:44-51`). Reproducing it in Go means
   duplicating a rule that lives in a separately-versioned Python
   package. The plugin cache on this machine holds both 0.16.0 and
   0.17.0; the mapping would have to track every such release.
2. A mapping that is *wrong* fails **open** — it returns the wrong
   path, the wrong path happens to be inside the write-set, and the
   write proceeds while the log says it was checked. That is worse
   than no check.
3. Sync ownership. There is no workable answer to "a convention MCP
   tool authors follow", because ethos does not own quarry's, biff's,
   or z-spec's MCP servers, and a third-party server will never follow
   an ethos convention. So the only viable form is a manual allowlist
   entry per tool, maintained by whoever adds the grant — which is
   discipline, and discipline is what failed in PR #424 (the z-spec
   writers were granted without anyone noticing they write).

Coverage: verifier spawns only. Zero effect on workers.

### (b) Document the boundary as ungated

Shape: no code change. State plainly, where leaders read it, that the
`write_set` gate is a verifier-only control over `Write`/`Edit`, and
that MCP write methods and `Bash` are outside it.

Cost: nothing mechanical improves. Benefit: it removes a false belief,
which is the actual defect. A leader who scopes a `write_set` believing
it fences a worker in is mis-modeling the system today, independent of
MCP.

Docs to change and the exact leader-facing text are in
[Recommendation](#recommendation) below.

### Hybrid

Viable, and recommended. The boundary sits at **"does it write inside
the repo?"**, not at "is it security-sensitive". Rationale: the
`write_set` invariant is about which repo artifacts a delegation may
change — that is what a reviewer diffs and what a PR carries. A write
to `~/.punt-labs/quarry/data` cannot violate that invariant no matter
how it is spelled, so gating it buys nothing and adds mapping rot.

Within the in-repo set, the correct control is a **deny**, not a path
map. A deny needs no knowledge of the target path, so it cannot rot and
cannot fail open.

## Recommendation

Adopt (b) as the primary remedy, plus a narrow (a) implemented as a
deny-list, not a path-map.

### R1 — Deny, in verifier spawns only, the in-repo MCP write tools

In `extractTargetPath`'s caller, when `ETHOS_VERIFIER_ALLOWLIST` is
set, deny outright:

- any tool whose name ends `_self__identity` with `method=create`
- any tool whose name ends `_zspec__check`, `_zspec__model_check`,
  `_zspec__test`, `_zspec__animate`

Match on the suffix after the `mcp__plugin_<name>[-dev]_<server>__`
prefix so both plugin prefixes are covered by one entry — the
double-listing trap that DES-068 (`DESIGN.md:7013-7016`) already
documents. The deny message names the tool and points at the contract;
it reveals no path.

Why deny and not map: a verifier's job is to read a delta and judge it.
It has no legitimate reason to create an identity or regenerate a spec
report. Denying costs a verifier nothing and needs no knowledge of
where the tool writes.

### R2 — Classification is enforced by `make check`, not by discipline

Add to `cmd/validate-content` a check that every `mcp__` tool name
appearing in any `roles/*.yaml` is present in one of three explicit
lists in the ethos source:

- `read-only`
- `writes-outside-repo`
- `writes-in-repo` — which implies the R1 deny

An unclassified grant fails `make check`. The person adding the grant
must classify it, and the reviewer sees the classification in the diff.
This is the answer to "who keeps it in sync": nobody has to remember,
because the build refuses the grant until it is classified. It also
turns the PR #424 miss into an impossible state — the z-spec writers
could not have been added silently.

The classification is a claim about the tool's behavior, so it must
cite evidence in a comment (file:line in the tool's own repo, as this
document does). Re-verification on a tool's major release is an open
question, below.

### R3 — Documentation, exact placement and text

Three files:

1. `DESIGN.md` — the new ADR block (drafted below), and an amendment
   note under DES-035/DES-052's PreToolUse section pointing at it.
2. `docs/workflow.md` — in the mission/write-set section, the
   leader-facing warning.
3. `CLAUDE.md` (repo root, "Use Ethos to Build Ethos") — a one-line
   pointer, since that is what a leader reads before dispatching.

Warning text, verbatim, for `docs/workflow.md` and `CLAUDE.md`:

> **What `write_set` does and does not enforce.** `write_set` is
> enforced mechanically only for **verifier** spawns, and only for the
> `Write` and `Edit` tools. It is not a sandbox. A worker's `Write`,
> any `Bash` command, and any MCP tool's write method are outside the
> mechanical gate — for workers the `write_set` is a contract term the
> worker is expected to honor and the reviewer checks in the diff, not
> a fence. Scope MCP grants and `Bash` on that assumption.

### What this recommendation does not fix

Workers remain mechanically ungated for `Write`, `Edit`, `Bash`, and
MCP writes. That is DES-035's original scope decision, not a
regression, and changing it is a larger question than this bead — see
the open questions. R1-R3 close the verifier hole, make future grants
self-classifying, and remove the false belief. They do not turn
`write_set` into a sandbox, and the docs must not imply that they do.

## Rejected alternatives

**Full remedy (a): a path-map for every granted MCP write tool.**
Rejected. It fails open when the map is stale, the map is a claim about
another repo's runtime path derivation (`punt_zspec/report.py:44-51`),
and — because 36 of 39 roles hold `Bash` — the same write is reachable
via the CLI regardless. It buys false assurance, which is a worse
position than the documented gap.

**Block all non-`Write`/`Edit` MCP tools in mission-bound sessions.**
Rejected. It removes `quarry find`, `biff read_messages`, and
`identity whoami` — the read halves that DES-068 was created to
provide — while leaving `Bash` open, so it costs the whole benefit of
DES-068 and closes nothing.

**Require every future MCP grant to declare a target-path resolver up
front.** Rejected as stated, adopted in weakened form as R2. Ethos does
not own quarry's, biff's, or z-spec's servers and cannot impose a
convention on them; a resolver written in ethos is a guess that rots at
the other tool's next release, silently. R2 keeps the mandatory
declaration but reduces it from a path resolver (fails open when wrong)
to a three-way classification (fails closed: `writes-in-repo` denies).

**Drop `identity` from specialist roles entirely.** Rejected here, but
see the open questions. MCP scoping is per tool, not per method
(DES-068, `DESIGN.md:7017-7020`), so removing `create` means removing
`whoami`/`get`, which is the in-session persona resolution DES-068
exists to provide. And `Bash` reaches `ethos identity create` anyway in
every role that would lose it.

**Gate `Bash` by parsing commands.** Rejected. Command parsing is
unbounded — shell quoting, pipelines, `env`, aliases, a script written
first and executed second. A parser that is wrong fails open; a
deny-by-default on `Bash` breaks every worker.

## Open questions for the leader and operator

1. **Should the `write_set` gate bind workers at all?** Today it does
   not (`subagent_start.go:179-194`). This is the decisive question;
   the MCP gap is a detail beside it. If the answer is yes, that is a
   new ADR amending DES-035, and it should be decided before any
   further investment in per-tool mapping.
2. **Are the z-spec sidecar reports (`*.report.json`, `*.fuzz.json`)
   meant to be git-tracked?** If they are gitignored org-wide, the
   z-spec row leaves the in-repo exposure set and R1 shrinks to the
   `identity` deny alone. This is a question for the z-spec owner
   (jms/jra), not for ethos.
3. **How is an R2 classification re-verified when a tool releases?**
   Options: never (accept drift, the classification is coarse enough
   that it rarely changes), or a release-checklist item in the
   consuming repo. Recommend never, and rely on the coarseness: a tool
   moving from "writes outside repo" to "writes in repo" is a
   behavioral change its own release notes should carry.
4. **Should the repo identity layer be writable by specialists at
   all?** An alternative not costed here: a repo config flag making the
   repo identity store read-only outside an explicit admin path, which
   would close the `identity create` path for both MCP *and* `Bash` in
   one move. That is a larger change to DES-057 repo-authoritative
   resolution and needs its own design.
5. **Does the PR #424 accepted-risk note need revisiting** now that the
   z-spec writers are known to overwrite in-repo files? The original
   acceptance rested on `O_EXCL` create-only semantics, which do not
   apply to the z-spec path.

## DES ADR draft block for `DESIGN.md`

Insert after DES-068. Draft — not yet ratified.

````markdown
## DES-069: MCP write methods are outside the write-set gate (DRAFT)

**Status**: Draft. Resolves bead `ethos-7b6c`, raised during PR #424
(DES-068). Amends the PreToolUse enforcement described in DES-035 and
extended by DES-052.

### Problem

`internal/hook/pretooluse.go:237-251` derives an allowlist target path
only for `Write` and `Edit`; every other tool returns `""`, which
`:114-118` treats as allow-unconditionally. DES-068 granted 37 roles a
scoped MCP set, and two of those tool families write inside the repo:
`ethos_self__identity` with `method=create` (36 roles) reaches
`LayeredStore.Save` (`internal/identity/layered.go:358-394`), which
prefers the repo layer and writes a git-tracked
`.punt-labs/ethos/identities/<handle>.yaml`; the z-spec
`check`/`model_check`/`test`/`animate` tools (2 roles) write
`<spec>.report.json` and `<spec>.fuzz.json` beside the spec source.
Neither is checked against the mission `write_set`.

A second, larger fact was confirmed while investigating: the gate binds
**verifier** spawns only. `ETHOS_VERIFIER_ALLOWLIST` is set solely in
the verifier-isolation branch at
`internal/hook/subagent_start.go:179-194`, and `DESIGN.md:4560-4562`
states the worker case is a passthrough. Workers are therefore ungated
for `Write`, `Edit`, `Bash`, and MCP writes alike. Leaders scoping a
`write_set` have been assuming a fence that does not exist.

### Decision

1. **`write_set` is a verifier control, and is documented as such.**
   For workers it is a contract term checked by review, not a sandbox.
   The leader-facing statement lands in `docs/workflow.md` and the repo
   `CLAUDE.md`.
2. **In verifier spawns, deny the in-repo MCP write tools outright** —
   `_self__identity` with `method=create`, and `_zspec__check`,
   `_zspec__model_check`, `_zspec__test`, `_zspec__animate`. A deny,
   not a path map: it requires no knowledge of the target path, so it
   cannot rot and cannot fail open. Matching is on the suffix after the
   `mcp__plugin_<name>[-dev]_<server>__` prefix, covering both plugin
   prefixes with one entry (DES-068's double-listing rule).
3. **Every MCP grant is classified, enforced by `make check`.**
   `cmd/validate-content` requires each `mcp__` tool name in any
   `roles/*.yaml` to appear in one of `read-only`,
   `writes-outside-repo`, or `writes-in-repo`; the last implies the
   deny in (2). An unclassified grant fails the build. Classification
   entries cite file:line evidence in the tool's own repo.
4. **Tools writing outside the repo are explicitly not gated.** quarry
   `remember`/`ingest`/`use`, ethos `session`, biff `plan`: they cannot
   change a repo artifact, so they cannot violate the invariant
   `write_set` protects.

### Rejected alternatives

- **Path-mapping every MCP write tool.** Fails open when the map is
  stale; the map duplicates another repo's runtime path derivation
  (`punt_zspec/report.py:44-51`); and 36 of 39 roles hold `Bash`, so
  the same write is reachable via the CLI. False assurance is worse
  than a documented gap.
- **Blocking all non-`Write`/`Edit` MCP tools in mission-bound
  sessions.** Removes the read halves DES-068 exists to provide while
  leaving `Bash` open.
- **A mandatory target-path resolver per grant.** Ethos does not own
  the third-party MCP servers and cannot impose a convention; adopted
  in weakened, fail-closed form as decision (3).
- **Dropping `identity` from specialist roles.** MCP scoping is per
  tool, not per method (DES-068), so this also drops `whoami`/`get`;
  and `Bash` reaches `ethos identity create` regardless.
- **Parsing `Bash` commands to gate them.** Unbounded; a wrong parser
  fails open, a strict one breaks every worker.

### Consequences

Verifier sessions gain a fail-closed deny on the two in-repo MCP write
families. Future grants cannot be added without classification, so the
PR #424 miss (z-spec writers granted without anyone noting they write)
becomes an unrepresentable state. Workers remain mechanically ungated —
unchanged from DES-035, now stated rather than assumed. Whether the
gate should bind workers is deferred to a separate ADR.
````

## References

- `internal/hook/pretooluse.go:109-151`, `:237-251`
- `internal/hook/subagent_start.go:179-194`
- `internal/identity/layered.go:358-394`
- `internal/mcp/tools.go:164-197`
- `DESIGN.md:4539-4562` (DES-052 PreToolUse enforcement),
  `DESIGN.md:6979-7023` (DES-068)
- `.punt-labs/ethos/roles/*.yaml` (39 files)
- `punt_zspec/report.py:44-51,77-89`,
  `punt_zspec/commands/check.py`, `model_check.py`, `test.py`,
  `animate.py` (z-spec 0.17.0)
- `quarry/src/quarry/config.py:27`
- Bead `ethos-7b6c`; PR #424; mission `m-2026-08-01-009`
