# Repo-authoritative resolution mode (ethos-ni0y)

Design for an opt-in mode where the repo layer is the sole source of
truth and the global fallback is disabled. Design only — no production
code. The leader reconciles this with the parallel `ethos vendor`
design (mdm) and lands the ADR.

## Problem

Ethos resolves identities, personalities, writing styles, talents,
roles, and teams through a three-layer chain: repo-local
(`.punt-labs/ethos/`) → active bundle → global
(`~/.punt-labs/ethos/`). Repo-local wins, but the global layer catches
the tail — a handle absent from the repo copy still resolves from
global.

The tail is the problem. A repo that vendored a partial
`.punt-labs/ethos/` set (vox did this in PR #284) is not self-standing.
Global-only handles such as `ylc` and `claudia` still resolve, so the
incompleteness is invisible. A fresh checkout on a machine without the
global home would resolve a different, smaller set — silently. The
vendored repo looks complete on the author's machine and breaks
elsewhere.

Concretely, the global tail is consulted at these read sites:

- Identity load: `internal/identity/layered.go:93` (`ls.global.Load`).
- Identity attribute chain: `internal/identity/layered.go:212,217,219`
  (`ls.global` appended to `attrChain`).
- Identity `FindBy`: `internal/identity/layered.go:341` (used by
  whoami — `resolve.Resolve` at `internal/resolve/resolve.go:62,74,86`).
- Identity `Exists`/`List`: `internal/identity/layered.go:352,304`.
- Role load: `internal/role/layered.go:70` (`ls.global.Load`).
- Team load: `internal/team/layered.go:67` (`ls.global.Load`).
- Attribute load: global is the top store whose `fallback` chain runs
  down to repo, so global is always the last-resort read
  (`internal/attribute/store.go:123`, chain built at
  `internal/attribute/layered.go:42-49`).

## Goal

An **opt-in** mode where the repo layer (plus a repo-local bundle, if
active) is authoritative and the global fallback is disabled for reads.
The vendored set becomes the sole read source. A global-less checkout
resolves purely from the repo, or fails loudly and names exactly what
is missing.

Back-compatibility is non-negotiable: unset config is byte-identical to
today's three-layer behavior.

## 1. The config knob

Add one field to `RepoConfig` (`internal/resolve/resolve.go:29-34`),
sibling to `active_bundle` in `.punt-labs/ethos.yaml`:

```yaml
# .punt-labs/ethos.yaml
resolution: repo-only        # optional; default "layered"
```

```go
type RepoConfig struct {
    Agent              string `yaml:"agent,omitempty"`
    Team               string `yaml:"team,omitempty"`
    ActiveBundle       string `yaml:"active_bundle,omitempty"`
    Resolution         string `yaml:"resolution,omitempty"` // "" | "layered" | "repo-only"
    MaxDelegationDepth int    `yaml:"max_delegation_depth,omitempty"`
}
```

Allowed values: `layered` and `repo-only`. Empty (unset) maps to
`layered`. An unknown value is a hard error at config load
(`LoadRepoConfig`, `internal/resolve/resolve.go:148`) — fail loud, never
silently default:

```
parsing .punt-labs/ethos.yaml: unknown resolution %q (want "layered" or "repo-only")
```

**Recommendation: a string enum named `resolution`, not a boolean.**

Reasoning:

- It names the *policy*, not the mechanism or the provenance. A future
  mode (`repo-bundle-only`, say) slots in without a schema change or a
  second boolean.
- `vendored: true` conflates two things — "this repo was produced by
  vendoring" and "disable global fallback." They are separable; a team
  could vendor and still want the global tail during migration. The
  config should state the resolution policy directly.
- `global_fallback: false` is a negative boolean. Reasoning about
  double negatives ("global_fallback is false so the fallback is off")
  is a known config footgun, and it reads worse in error messages than
  a named mode ("repo is in repo-only resolution").
- A string enum reads cleanly in `git diff` and in diagnostics: "repo
  configured `resolution: repo-only`."

Add a resolver alongside the existing ones
(`ResolveActiveBundle`, `internal/resolve/resolve.go:247`):

```go
// ResolveResolution returns the repo's resolution policy: "layered"
// (default) or "repo-only". Same error contract as ResolveActiveBundle.
func ResolveResolution(repoRoot string) (string, error)
```

## 2. Store mechanism — one policy, threaded uniformly

**Recommendation: a read-time policy that excludes the global layer
from the read chain, applied uniformly across all four layered stores.
Writes and extensions are unaffected — they still target global.**

The mode changes *resolution* (which layers a read consults), not
*write routing*. This keeps the mechanism small and preserves DES-044:
extensions are personal and stay in the global `.ext/` directory even
in repo-only mode.

Thread the policy as one value into each `NewLayeredStoreWithBundle`.
A boolean is enough; name it for the semantics, not the config:

```go
// identity
func NewLayeredStoreWithBundle(repo, bundle, global *Store, repoAuthoritative bool) *LayeredStore

// role / team
func NewLayeredStoreWithBundle(repoRoot, bundleRoot, globalRoot string, repoAuthoritative bool) *LayeredStore

// attribute
func NewLayeredStoreWithBundle(repoRoot, bundleRoot, globalRoot string, kind Kind, repoAuthoritative bool) *Store
```

The existing two-arg `NewLayeredStore` wrappers pass `false`, so no
behavior changes for callers that do not opt in.

### identity / role / team (explicit LayeredStore structs)

These three carry an explicit `global *Store` field consulted at the
end of each read. Add a `repoAuthoritative bool` field and guard the
global tail:

- `loadRaw`: skip the `ls.global.Load` branch
  (`internal/identity/layered.go:93`) when `repoAuthoritative`; return
  the miss error (§4) instead.
- `attrChain`: when `repoAuthoritative`, drop the trailing `ls.global`
  append in every case (`internal/identity/layered.go:212,217,219`).
- `FindBy` / `Exists` / `List`: skip the global branch
  (`layered.go:341,352,304`).
- role `Load` (`internal/role/layered.go:70`) and team `Load`
  (`internal/team/layered.go:67`): return the global result only when
  `!repoAuthoritative`; otherwise return the miss error.

The `global` field stays in the struct. It still backs extensions
(`id.Ext = ls.global.loadExtensions`,
`internal/identity/layered.go:55`) and writes (`writeStore`,
`layered.go:510`). Only the read tail is gated.

### attribute (fallback-chained Store)

The attribute store is built global-at-top with `fallback` running
down to repo (`internal/attribute/layered.go:42-49`), so global is
structurally the last-resort read. Excluding it is a construction
change, not a per-method guard: when `repoAuthoritative`, do not place
global at the head of the read chain. Build the top from the
highest-precedence layer that exists (bundle, else repo) and stop there:

```go
if repoAuthoritative {
    // repo(-> bundle) only; global is absent from the read chain.
    top := repo
    if bundle != nil {
        bundle.fallback = repo
        top = bundle
    }
    return top   // reads and writes target the vendored set
}
```

A miss then surfaces as the repo store's own not-found, which §4
aggregates into the actionable error. This construction also fixes a
latent footgun: in repo-only mode attribute writes target the vendored
set rather than a global directory that reads never consult.

### One injection point

Every surface funnels through `identityStore()`
(`cmd/ethos/identity.go:28`). The derived builders read their roots
from it: `layeredAttributeStore` (`cmd/ethos/attribute.go:26`),
`layeredRoleStore` (`cmd/ethos/role.go:24`), `layeredTeamStore`
(`cmd/ethos/team.go:24`). Read the policy once in `identityStore()`,
thread it through those three, and expose it as `LayeredStore` state
(a `RepoAuthoritative()` accessor, next to `RepoRoot()`/`BundleRoot()`)
so the derived builders pass the same value. Hooks (`cmd/ethos/hook.go`)
and the MCP server (`cmd/ethos/serve.go`) both call `identityStore()`,
so they inherit the mode for free.

## 3. Interaction with active_bundle / DES-051

**Recommendation: repo-only drops the global layer only. It keeps the
repo → bundle chain when the active bundle is repo-local or legacy.**

Reasoning:

- A repo-local bundle (`SourceRepo`,
  `.punt-labs/ethos-bundles/<name>/`) and the legacy dir (`SourceLegacy`,
  `.punt-labs/ethos/`) are themselves part of the vendored, git-tracked
  set. They travel with the checkout. Dropping them would defeat teams
  that vendor via a bundle submodule.
- A global bundle (`SourceGlobal`,
  `~/.punt-labs/ethos/bundles/<name>/`) does **not** travel with the
  checkout. Keeping it would reintroduce exactly the tail repo-only
  exists to remove.

So the rule is not "drop the bundle" but "keep only layers that live
inside the repo." The common vendored case (vox) has no `active_bundle`
at all — the legacy `.punt-labs/ethos/` dir is the repo layer, and
`resolveBundleRoot` already returns `""` for it
(`cmd/ethos/bundle.go:31`). Repo-only there means repo-only literally.

**Invariant:** `resolution: repo-only` combined with an `active_bundle`
that resolves from global scope is contradictory and must fail at
startup, not silently keep the global bundle:

```
resolution "repo-only" requires a repo-local or legacy bundle, but
active_bundle "gstack" resolves from ~/.punt-labs/ethos/bundles/gstack
```

`bundle.ResolveActive` already returns the bundle's `Source`
(`internal/bundle/resolve.go:29`), so `resolveBundleRoot` can enforce
this when the policy is repo-only.

## 4. Miss behavior — the crux

A repo-only miss must fail hard and name exactly what is missing. Never
a silent empty, never a soft warning.

### Identity miss

When an identity is absent from the repo (and repo-local bundle):

```
identity "claudia" not found in repo-only resolution
  searched: .punt-labs/ethos/identities/claudia.yaml
  (global fallback is disabled by resolution: repo-only)
```

This surfaces through `resolve.Resolve` (whoami) and through any direct
`Load`/`FindBy`.

### Attribute miss (the important case)

Today a referenced-but-missing attribute is a non-fatal warning on the
`Identity` (`resolveAttributesLayered`,
`internal/identity/layered.go:168,180,189`). In repo-only mode a
missing attribute file means the vendored set is incomplete — that is
an error, not a warning. Aggregate every missing attribute for the
identity into one message that lists the handle and each missing file:

```
identity "mal" is incomplete in repo-only resolution:
  personality "principal-engineer": missing personalities/principal-engineer.md
  writing_style "concise-quantified": missing writing-styles/concise-quantified.md
  talent "formal-methods": missing talents/formal-methods.md
the vendored set under .punt-labs/ethos/ must contain every referenced attribute
```

Contract:

- Collect all misses, then return one error — do not fail on the first,
  so a single run tells the operator the full gap.
- The error is typed (e.g. `identity.ErrIncompleteRepoSet` wrapping a
  `[]MissingRef{Kind, Slug, Path}`) so callers can format per surface
  and so the vendor tool and doctor can reuse the same check.
- In `layered` mode this stays a warning — behavior is unchanged.

### Where it surfaces

| Surface | Behavior on a repo-only miss |
|---------|------------------------------|
| `whoami` | Print the missing list to stderr, exit non-zero. |
| Agent-file generation (`GenerateAgentFiles`, `internal/hook/generate_agents.go:42`) | Fail generation for the affected agent, name it and its missing files; do not emit a half-populated agent file. |
| Session start / persona injection (`internal/hook/session_start.go`) | Print the missing list to stderr (Claude Code surfaces hook stderr) and skip persona injection for that identity. Do not hard-crash the session. |
| MCP reads (`serve` → `identityStore()`) | Return the typed error through the tool result, formatted per DES-020. |
| `ethos doctor` | New check (§5) — the authoritative pre-flight gate. |

**Open question for the leader:** should session-start be
fail-loud-and-abort or fail-loud-and-degrade? The table recommends
degrade (stderr + skip injection) so a missing attribute never bricks a
live Claude session, with `doctor` as the hard gate an operator runs
deliberately. Confirm this split or choose abort.

## 5. Surfaces that must honor the mode

All of these already route through `identityStore()`, so they inherit
the mode once §2 lands. Each still needs its miss-behavior wiring:

- **whoami** — `resolve.Resolve` via the layered `FindBy`.
- **Agent-file generation** — `GenerateAgentFiles`.
- **Session roster / persona injection** — SessionStart, PreCompact,
  SubagentStart hooks.
- **MCP reads** — the `serve` identity/team/role tools.
- **`ethos doctor`** — add a check that, in repo-only mode, walks every
  repo identity, resolves its referenced attributes/roles/teams within
  the repo(+repo-bundle) set, and reports each dangling reference as a
  FAIL. `doctor.RunAll` already receives the layered stores and
  `repoRoot` (`cmd/ethos/main.go:219`); it becomes the standard
  pre-flight validator for a vendored set. This check shares the typed
  miss result from §4.

## 6. Proposed ADR (house style)

> ## DES-0NN: Repo-authoritative resolution mode (SETTLED-when-shipped)
>
> **Problem**: Ethos resolves through repo-local → active bundle →
> global (DES-051), where the global layer catches the tail — a handle
> absent from the repo still resolves from global. A repo that vendored
> a partial `.punt-labs/ethos/` set (vox, PR #284) is therefore not
> self-standing: global-only handles (`ylc`, `claudia`) still resolve,
> masking the gap, and a checkout without the global home silently
> resolves a different set. Incompleteness is invisible on the author's
> machine and breaks elsewhere.
>
> **Decision**: Add an opt-in `resolution` field to
> `.punt-labs/ethos.yaml` with values `layered` (default) and
> `repo-only`. In `repo-only` mode the global layer is excluded from
> every read chain across the four layered stores (identity, role,
> team, attribute); the repo layer, plus a repo-local or legacy bundle,
> is the sole read source. Writes and extensions still target global
> (DES-044 preserved). A miss — a missing handle or a referenced-but-
> absent attribute — is a hard error that lists exactly which files are
> missing, aggregated across the whole identity. Unset `resolution` is
> byte-identical to today's three-layer behavior.
>
> **Reasoning**:
>
> - A vendored repo must be self-standing. The global tail is the one
>   thing that hides incompleteness; disabling it is the whole fix.
> - A read-time policy is the minimal mechanism: one boolean threaded
>   into each `NewLayeredStoreWithBundle`, gating only the global read
>   branch. Every surface already funnels through `identityStore()`, so
>   the mode reaches whoami, agent generation, hooks, and MCP from a
>   single injection point.
> - Fail-loud-with-a-list turns a silent divergence into an actionable
>   error at authoring time. `ethos doctor` runs the same completeness
>   check as a deliberate gate.
> - Keeping repo-local and legacy bundles in the chain preserves
>   DES-051 for teams that vendor via a bundle; excluding global
>   bundles keeps the mode honest.
> - A string enum leaves room for future modes and reads clearly in
>   diffs and diagnostics.
>
> **Rejected alternatives**:
>
> - **`vendored: true` boolean** — conflates provenance ("this repo was
>   vendored") with policy ("disable global fallback"). A repo can be
>   vendored yet still want the tail during migration. Name the policy.
> - **`global_fallback: false` boolean** — a negative boolean; double-
>   negative reasoning is a config footgun and reads poorly in error
>   messages.
> - **Drop the bundle layer too** — breaks teams that vendor via a
>   repo-local bundle submodule (DES-051). The right cut is "keep layers
>   that live inside the repo," not "keep only repo."
> - **Make it the default / a breaking change** — every existing repo
>   relies on the global tail. The mode must be opt-in and back-compat.
> - **Per-command `--repo-only` flag** — resolution policy is
>   persistent repo state, not a transient invocation choice; a flag
>   would have to thread through every read path (same reasoning as
>   DES-051's rejection of `--bundle`).
>
> **Implications**:
>
> - `.punt-labs/ethos.yaml` gains an optional `resolution` field;
>   `RepoConfig` and `LoadRepoConfig` validate it (unknown value is a
>   hard error). Writers reuse `setConfigKey`'s `yaml.Node` path
>   (DES-051) to preserve comments.
> - The four `NewLayeredStoreWithBundle` constructors gain a
>   `repoAuthoritative` argument; the two-arg wrappers pass `false`.
> - `resolveBundleRoot` rejects `repo-only` + a global-scoped
>   `active_bundle` at startup.
> - A typed incomplete-set error replaces attribute warnings in
>   repo-only mode; whoami, agent generation, session start, MCP, and
>   `ethos doctor` format it per surface.
> - Depends on `ethos vendor` (ethos-ni0y sibling) to produce a
>   complete set; see §7. Ships alongside or after vendor.

## 7. Dependency on `ethos vendor` (mdm)

Repo-only mode is the *verify* half; `ethos vendor` is the *produce*
half. Repo-only is only usable as a steady state once vendor can emit a
complete, closed set. State the coupling plainly:

For repo-only to be safe, `ethos vendor` must guarantee the reference
closure is complete:

1. Every identity referenced by the repo's agents, teams, and default
   agent is written to `.punt-labs/ethos/identities/`.
2. Transitively, every `personality`, `writing_style`, and `talent`
   slug referenced by those identities is written to the matching
   attribute directory.
3. Every role and team referenced (including collaboration graphs) is
   written.
4. The closure has no dangling references — vendor runs the same
   completeness check `ethos doctor` runs (§5) and refuses to write
   `resolution: repo-only` until it validates.

Sequencing: ship vendor first or in the same release. Repo-only mode
without vendor is not broken — it simply fails loud on any global-only
handle, which is the intended behavior — but it is not a usable steady
state until vendor can fill the set. The shared artifact between the
two designs is the typed completeness check (§4): vendor calls it
before writing the mode; doctor calls it to gate; the stores raise it
on a live miss.

## Open questions for the leader

1. **Session-start miss behavior** (§4): degrade (stderr + skip
   injection) or abort? Recommend degrade, with `doctor` as the hard
   gate.
2. **Attribute write routing** in repo-only mode (§2): the recommended
   construction sends attribute writes to the vendored set rather than
   global. Confirm this is wanted, or keep writes global-targeted and
   accept that a freshly created attribute would be invisible to
   repo-only reads (a footgun). Recommend routing writes to the repo.
3. **Config key name**: `resolution: repo-only` recommended. Confirm
   before implementation, since the vendor design references the same
   key when it writes the mode.
