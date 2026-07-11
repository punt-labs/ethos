# Ext read carve-out for repo-only mode (ethos-ni0y)

Design doc. Design only — no code. This closes a self-inconsistency the
vox consumer sanity-check caught in DES-057 (PR #345,
issuecomment-4947892902). It is an amendment folded into DES-057 Part A
(`docs/ni0y-resolution-mode.md`) and Part C (`docs/ni0y-ext-local.md`),
and an explicit, precise carve-out to the SETTLED DES-044 invariant.

## The gap

DES-057 reconciled three halves. Two of them touch repo ext and agree;
the third contradicts them.

- **Vendor copies ext into the repo.** The closure copies each
  identity's `<h>.ext/` tree verbatim into `.punt-labs/ethos/identities/`
  (`docs/ni0y-vendor-command.md:157-171`, §4).
- **The completeness predicate validates repo ext is present.** For every
  `identities/<h>.yaml` in the snapshot, `<h>.ext/` must be "present
  verbatim" in the snapshot directory `D`
  (`docs/ni0y-vendor-command.md:358`, §9).
- **But Part A preserved DES-044: ext resolves from global.** The
  resolution-mode design left the ext read path pinned to global —
  "extensions still target global" (`docs/ni0y-resolution-mode.md:110-116`),
  and the `global` field "still backs extensions
  (`id.Ext = ls.global.loadExtensions`)"
  (`docs/ni0y-resolution-mode.md:152-155`). Part C agreed it does not move
  the layer: "`.local` does not change where ext resolves from … The repo-
  authoritative ADR still owns the decision to read ext from the repo"
  (`docs/ni0y-ext-local.md:230-257`).

Net effect in `resolution: repo-only`: identities and attributes read
from the repo, but ext still reads from global
(`internal/identity/layered.go:55`, `ExtDir` → global at
`internal/identity/layered.go:480-482`). A global-less checkout therefore
reads ext from an absent global directory, gets nothing, and every agent
loses its quarry `memory_collection` + `session_context` wiring — the
lines DES-022 added to give each agent persistent memory
(`docs/ni0y-ext-local.md:12-17`). Acceptance criterion (a) silently
fails, and the completeness predicate (validates the repo file present)
disagrees with the resolution read path (reads global). The predicate and
the read path point at different directories.

The fix: in repo-only mode, route ext reads to the repo layer through the
same `readNamespace` base+`.local` merge Part C defines
(`docs/ni0y-ext-local.md:85-90`), and fold a manifest-recorded-but-absent
repo ext into `ErrIncompleteRepoSet` so a global-less checkout fails at
the same gates every other repo-only miss fails at. DES-044 continues to
hold unchanged in `layered` mode.

## 1. The gated ext read surfaces

Every ext method on `LayeredStore` today hardcodes `ls.global`. These are
the surfaces to gate on `repoAuthoritative` (the field Part A adds,
`docs/ni0y-resolution-mode.md:135-155`):

| Surface | Site | Consumes |
|---------|------|----------|
| `Load` — merged identity view | `layered.go:55` (`ls.global.loadExtensions`) | `get_identity`, `whoami`, hook persona injection, agent generation — the memory wiring |
| `ExtGet` | `layered.go:485-486` | `ext get` CLI/MCP |
| `ExtList` | `layered.go:506` | `ext list`, and `loadExtensions`'s own namespace enumeration (`ext.go:222`) |
| `ExtDir` | `layered.go:480-482` | path callers: vendor source read, `Save` mkdir |
| `Save` ext-dir create | `layered.go:263` (`ls.global.ExtDir`) | identity create |
| `relocateRepoVoice` | `layered.go:133,138` (`ls.global.ExtSet`) | legacy voice migration |

The identity-internal path is `loadExtensions` (`ext.go:221-249`), a
`*Store` method. It resolves ext files relative to *its own* store's
identities directory via `ExtDir`/`extPath` (`ext.go:28-38`). Part C
routes it through `readNamespace` (`docs/ni0y-ext-local.md:83-98`), also a
`*Store` method. So switching layers is not a rewrite of the read logic —
it is a choice of *which `*Store`* to call. Call `ls.repo.loadExtensions`
and the base+`.local` merge happens against the repo ext dir for free.

### One selector

Introduce a single selector, mirroring Part A's "one policy, threaded
uniformly" (`docs/ni0y-resolution-mode.md:107-118`). Every ext method
routes through it instead of naming `ls.global` directly:

```go
// extStore returns the *Store whose <handle>.ext/ directory backs the
// identity. In layered mode this is always global (DES-044). In
// repo-only mode it is the in-repo layer the identity resolves from:
// repo if present there, else the active repo-local/legacy bundle.
func (ls *LayeredStore) extStore(handle string) *Store {
    if !ls.repoAuthoritative {
        return ls.global
    }
    if ls.repo != nil && ls.repo.Exists(handle) {
        return ls.repo
    }
    if ls.bundle != nil && ls.bundle.Exists(handle) {
        return ls.bundle
    }
    return ls.repo // read then surfaces the miss (§3); nil-safe guard at call site
}
```

`Load` already knows the winning layer — `loadRaw` returns `source`
(`layered.go:71-98`). Prefer that inside `Load` (no second existence
probe); the standalone `Ext*` methods, which receive only a handle, use
`extStore`'s repo→bundle probe. Both land on the same directory.

The repo ext dir is located exactly as today, only against the repo
store's root: `ls.repo.ExtDir(handle)` yields
`.punt-labs/ethos/identities/<handle>.ext/` (`ext.go:28-33`). Its
namespace files compose with Part C's merge — repo `<ns>.yaml` overlaid by
repo `<ns>.local.yaml`, local winning per key.

## 2. Layer semantics: the ext dir is bound to the identity's source layer

**Recommendation: ext resolves from the single in-repo layer where the
identity resolves — repo, else the repo-local/legacy bundle. Global is
dropped. There is no cross-layer ext-namespace merge; the only merge is
Part C's within-directory base+`.local`.**

This keeps global out of the read chain (Part A's whole point) and keeps
the in-repo layer that wins the identity winning its ext — consistent with
how Part A drops global and prefers repo→bundle for identities and
attributes (`docs/ni0y-resolution-mode.md:198-217`).

It deliberately does **not** replicate attribute-style per-item fallthrough
(`layered.go:202-222`). Reasoning:

- Ext is a directory bound to one identity file, not a per-slug reference
  resolved independently. Attributes fall through because a repo identity
  may legitimately reuse a global personality; ext has no such sharing —
  `mal.ext/` belongs to `mal` alone.
- Vendor writes the identity YAML and its `<h>.ext/` tree into the *same*
  layer as one atomic closure step
  (`docs/ni0y-vendor-command.md:126,146-155`). A vendored identity and its
  ext always travel together, so source-layer binding can never strand
  ext, and cross-layer fallthrough would only add a merge dimension with
  no case to serve.
- Adding a second merge axis (across layers) on top of Part C's base+`.local`
  merge multiplies the reasoning for no benefit. One identity, one ext
  directory, one within-directory merge.

So: `source == "repo"` ⇒ read repo `<h>.ext/`; `source == "bundle"` ⇒
read bundle `<h>.ext/`; global never consulted. In the common vendored
case (vox, no active bundle) source is always repo, so ext reads from the
repo — which is exactly the file the completeness predicate validates.

## 3. Miss semantics — the crux

Ext is additive today: `Load` succeeds with no ext, `loadExtensions`
returns an empty map and no error (`ext.go:230-232`). So "any absent ext
is a miss" is wrong — many identities legitimately carry no ext, and
failing them all would break repo-only for every minimal set. Equally,
"only ext a consuming tool declared required" is wrong — ext is free-form
and DES-008 forbids ethos from interpreting values (`DESIGN.md:170-171`),
so ethos has no notion of "quarry is required" and inventing one would
teach ethos its consumers' semantics, the exact coupling DES-008 exists to
prevent.

**Recommendation: the required-ext set is the vendor manifest. An absent
ext is a miss iff `.vendor.yaml` records an ext base file for the identity
that is not present in the source-layer `<h>.ext/` directory.**

The manifest (`.punt-labs/ethos/.vendor.yaml`, `docs/ni0y-vendor-command.md:192-197`)
is ethos's own artifact — vendor writes it, listing every path it created.
Reading it is not interpreting a consumer value; it is checking file
presence against ethos's own record. That threads the needle:

- **vox holds.** Vendor records `mal.ext/quarry.yaml`. If a global-less
  checkout is missing it, the presence check fails and the memory wiring
  gap surfaces loud — criterion (a).
- **Minimal identities do not fail spuriously.** An identity vendor
  recorded with zero ext files contributes zero ext requirements. Absence
  of a manifest entry means absence of a requirement.
- **Hand-authored repo-only (never vendored, no manifest) requires no
  ext.** Nothing was ever promised, so nothing is silently dropped; ext
  reads additively from the source layer. Part A's loud failure still
  applies to identities and attributes — just not to ext, which had no
  recorded expectation. This is the honest limit: a global-less,
  manifest-less checkout has no record of what ext *should* exist. The
  manifest is that record; vendor is what writes it.

A miss produces a `MissingRef{Kind: "ext", Slug: "<h>/<ns>", Path:
"identities/<h>.ext/<ns>.yaml"}`, folded into the same typed
`ErrIncompleteRepoSet` Part A defines for attribute misses
(`docs/ni0y-resolution-mode.md:267-274`). Only base `<ns>.yaml` files are
required — `<ns>.local.yaml` is gitignored and never vendored
(`docs/ni0y-ext-local.md:196-209`), so its absence is never a miss.

### Where the miss surfaces

Ext joins Part A's existing miss-surface table
(`docs/ni0y-resolution-mode.md:278-284`) unchanged in shape — same typed
error, formatted per surface:

| Surface | Behavior on a manifest-recorded-but-absent ext |
|---------|------------------------------------------------|
| `ethos doctor` | FAIL — the deliberate pre-flight gate; walks the manifest and reports each missing ext file. |
| Agent-file generation (`internal/hook/generate_agents.go:42`) | Fail the affected agent, name it and the missing ext file; do not bake a memory-less agent `.md`. This is where the wiring is actually written into the agent, so the miss must be hard here. |
| Session start / persona injection (`internal/hook/session_start.go`) | Print the missing list to stderr and degrade (skip injection for that identity). Do not brick a live session — matching Part A's degrade choice. |
| Live `Load` / MCP reads | Read the source-layer ext additively; the completeness verdict is carried on the identity as it is for attribute misses in repo-only. |

The split is Part A's, not a new one: `doctor` and agent-generation are
the hard gates; session-start degrades. Ext does not invent a fourth
policy — it reuses the three Part A already settled.

**Cost note.** The manifest is one small YAML at the repo root. Read it
once at `LayeredStore` construction and cache the recorded ext set, so no
per-`Load` file read is added. Minor; flagged for the implementer.

## 4. Predicate and read-path now name the same requirement

Before this carve-out the predicate validated the repo file and the read
path read global — two different directories, hence the disagreement.
After it, both name the identical file:

- **Read path** (repo-only `extStore`, §1-2): reads
  `identities/<h>.ext/<ns>.yaml` from the source layer.
- **Completeness predicate** (vendor §9, doctor §5): requires every
  manifest-recorded `identities/<h>.ext/<ns>.yaml` to exist in that same
  source-layer directory.

The file the predicate asserts present is byte-for-byte the file the read
path opens. Sharpen vendor.md §9's vague "`<h>.ext/` present verbatim"
(`docs/ni0y-vendor-command.md:358`) to: "for every ext base file the
manifest records for `h`, the file exists in `D`'s
`identities/<h>.ext/`." One requirement, referenced identically by the
producer's verify step, the doctor gate, and the resolution read path.

## 5. DES-044 carve-out wording (amend-by-reference)

Following the precedent that a settled ADR is extended by a
cross-referencing amendment rather than mutated in place
(`docs/ni0y-ext-local.md:261-268`; DES-022 and DES-044 both extend
DES-008 this way). The DES-057 record carries this amendment:

> **DES-044 amendment — repo-only ext read carve-out.**
>
> DES-044 (`DESIGN.md:4038-4059`) holds unchanged in `layered` resolution
> — the default, and any unset `resolution`. Extensions resolve from and
> write to the global `<handle>.ext/` directory; the merged view is
> byte-identical to pre-carve-out behavior.
>
> In `resolution: repo-only`, extensions resolve from the repo instead:
> the source-layer `<handle>.ext/` directory under
> `.punt-labs/ethos/identities/`, read through the DES-057 Part C
> base+`.local` merge, with the global layer dropped. The ext directory is
> bound to the layer that resolves the identity (repo, else a
> repo-local/legacy bundle) — the same layer, atomically, that `ethos
> vendor` wrote the identity and its ext into. Writes target the repo
> (repo only; the bundle layer is read-only, DES-051).
>
> DES-044's rationale narrows accordingly. "Extensions are personal and
> live outside git" governs `layered` mode. In `repo-only` mode a vendored
> repo's `<handle>.ext/` base files are a committed, self-standing part of
> the snapshot, and per-checkout secrets are isolated to gitignored
> `<ns>.local.yaml` (DES-057 Part C). The invariant that ethos never
> interprets an ext value is untouched: layer selection is a directory
> choice, and the required-ext set is read from ethos's own `.vendor.yaml`
> manifest, never from a consumer value.

## 6. Back-compat: layered/unset is byte-identical

The carve-out is entirely gated on `repoAuthoritative`. When it is false —
every two-arg `NewLayeredStore` caller and every repo with unset or
`layered` resolution (`docs/ni0y-resolution-mode.md:71,132-134`) —
`extStore` returns `ls.global` and all six ext surfaces behave exactly as
today. Part C's base+`.local` merge in layered mode still reads global
`<ns>.yaml` overlaid by global `<ns>.local.yaml`; that is Part C's own
back-compat, unchanged here. No layered-mode read path moves.

## 7. Write side: ext writes follow reads to the repo

**Confirmed: in repo-only mode, ext writes go to the repo, consistent with
Part A's uniform-repo-writes** (`docs/ni0y-resolution-mode.md:166-181`,
open Q #2 recommending repo-targeted writes).

`ExtSet`/`ExtDel` route through the same `extStore(handle)` selector. In
repo-only that returns repo for a repo-sourced identity. Reads and writes
must agree on the layer: if writes went to global while reads came from
repo, an `ext set` would land in a directory reads never consult — the
exact footgun Part A calls out for attribute writes
(`docs/ni0y-resolution-mode.md:178-181`). So writes follow reads to the
repo.

A bundle-sourced identity's ext is read-only, matching `Update`'s bundle
rule (`layered.go:365-368`): an `ext set` against it errors with the same
message shape ("provided by the active bundle … edit the bundle
directly"), rather than silently writing a repo override the identity does
not resolve from. Base-vs-`.local` selection (Part C's `--local`,
`docs/ni0y-ext-local.md:125-146`) is orthogonal and unchanged — it selects
the file *within* whichever directory `extStore` picked.

Two write-adjacent sites need the same routing, else a value lands in
global and is invisible to repo-only reads:

- `Save`'s ext-dir create (`layered.go:263`) — `ls.global.ExtDir` becomes
  `extStore(id.Handle).ExtDir`.
- `relocateRepoVoice`'s migrated-voice write (`layered.go:133,138`) — the
  `ls.global.ExtSet` calls route through the selector, so a legacy voice
  migrated under repo-only lands in the repo ext dir the reads consult.

## Report summary

- **Gated surfaces** (§1): `Load` ext read (`layered.go:55`), `ExtGet`
  (`:485`), `ExtList` (`:506`), `ExtDir` (`:480`), plus the two write-
  adjacent sites `Save` (`:263`) and `relocateRepoVoice` (`:133,138`). All
  route through one `extStore(handle)` selector gated on
  `repoAuthoritative`; the identity-internal `loadExtensions`/`readNamespace`
  path (`ext.go:221`, Part C) is store-relative, so the switch is a choice
  of `*Store`, not a rewrite.
- **Layer rule** (§2): ext is bound to the identity's source layer (repo,
  else repo-local/legacy bundle); global dropped; no cross-layer ext
  merge, only Part C's within-directory base+`.local`.
- **Miss rule — the crux** (§3): the required-ext set is the `.vendor.yaml`
  manifest. A manifest-recorded ext base file absent from the source-layer
  `<h>.ext/` is a `MissingRef{Kind:"ext"}` folded into
  `ErrIncompleteRepoSet`. No manifest entry ⇒ no requirement (minimal and
  hand-authored identities never fail spuriously). Surfaces via Part A's
  existing table: doctor FAIL, agent-generation fail, session-start
  degrade, live Load additive. This makes (a) hold for vox without failing
  ext-less identities.
- **Predicate/read-path agreement** (§4): both name the identical
  source-layer file; vendor.md §9's "present verbatim" sharpened to
  manifest-parity against the same directory the read path opens.
- **DES-044 carve-out wording** (§5): amend-by-reference — DES-044 holds
  in `layered` (ext through global); in `repo-only` ext resolves through
  the repo (source layer, global dropped), writes to the repo; the
  no-interpretation invariant is untouched.
- **Back-compat** (§6): `layered`/unset ⇒ `extStore` returns global ⇒
  byte-identical.
- **Write side** (§7): confirmed repo-targeted in repo-only; bundle-sourced
  ext is read-only; `--local` selection orthogonal.

## Open questions for the leader

1. **Manifest as the required-ext set** (§3). Recommended, because it is
   the only record of expected ext on a global-less, manifest-less
   checkout, and it keeps ethos from interpreting values. The alternative —
   "if the source-layer `<h>.ext/` exists, every namespace a consumer
   expects must be inside it; if it doesn't exist, the identity has no
   ext" — needs no manifest but cannot distinguish "vendor forgot
   quarry.yaml" from "this identity has no ext," so it does not close
   vox's gap. Confirm the manifest rule, or accept the weaker rule and its
   blind spot.
2. **Live `Load` elevation** (§3). Recommended: live `Load` reads ext
   additively and carries the completeness verdict (as Part A does for
   attributes), with the hard gates at doctor and agent-generation.
   Confirm this matches Part A's degrade/gate split, or require live `Load`
   itself to hard-fail on a manifest-recorded-absent ext.
3. **Bundle-sourced ext writes** (§7). Recommended: error like `Update`
   does, rather than writing a repo override. Confirm, or allow a repo
   override that shadows the bundle ext.
