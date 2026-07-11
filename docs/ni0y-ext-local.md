# `.local` ext split — safe ext-on-by-default (ethos-ni0y)

Design doc. Design only — no code. This is the ext-safety increment for
ethos-ni0y. It pairs with the vendor design (`docs/ni0y-vendor-command.md`,
mdm) and the repo-authoritative resolution design
(`docs/ni0y-resolution-mode.md`, bwk). This half makes ext safe to vendor
and commit by default, so the vendor half never needs heuristic redaction.

## Problem

Vendor must copy `.ext/` — it is not optional decoration. Discovery across
all 30 identities found ext contains **zero secrets**: it is the quarry
`memory_collection` + `session_context` wiring on every identity (the lines
that give each agent persistent memory, DES-022, `DESIGN.md:1395-1399`), a
little `vox` voice config, and one empty `beadle` `gpg_key_id`. A vendored
repo without ext loses every agent's memory wiring. So ext is useful and
must travel with the snapshot.

But the vendor design left a residual worry open
(`docs/ni0y-vendor-command.md:173-177`, §10.3): namespaces are free-form,
and nothing stops a consumer from later writing a secret into a namespace
file. Once `.ext/` is git-tracked, that secret is committed. The vendor doc
proposed `--no-ext` or publish-time redaction as possible answers.

Both are wrong shape. `--no-ext` throws away the memory wiring — the exact
content that makes vendoring worthwhile. Redaction requires ethos to guess
which values are secret, which violates the DES-008 invariant that ethos
never interprets ext values (`DESIGN.md:170-171`).

Jim's decision: don't redact, **partition**. Split each namespace into a
shareable file and a secret companion, so the secret is never in the file
vendor copies. The file layout becomes the safety boundary. No heuristics.

## 1. File convention

Per DES-008, ext lives at `<handle>.ext/<namespace>.yaml`
(`ext.go:36-38`, `DESIGN.md:137-146`). Add a companion beside it:

```text
~/.punt-labs/ethos/identities/
  mal.yaml
  mal.ext/
    quarry.yaml          # shareable — vendored, committable
    quarry.local.yaml    # secret / machine-specific — gitignored, NEVER vendored
    beadle.yaml
    beadle.local.yaml    # e.g. a private gpg passphrase, if one existed
```

- `<ns>.yaml` — the shareable base. Committable, vendored, byte-identical
  to today when no companion exists.
- `<ns>.local.yaml` — the secret or machine-specific overlay. Gitignored,
  never vendored, never leaves the machine.

This is the org's established pattern, not a new invention:

- `.envrc` (committed, inline non-secret) + `.envrc.local` (gitignored,
  machine-specific) — the canonical repo-environment split
  (`punt-labs/CLAUDE.md`, "Repo Environment").
- `vox.md` (committed voice config) + `vox.local.md` (gitignored local
  overrides) — the vox precedent (`.punt-labs/vox/vox.md` /
  `vox.local.md`, referenced in the vox MCP instructions).
- `.claude/settings.json` + `.claude/settings.local.json`, and the
  existing `.gitignore` rule `.claude/*.local.md` (`.gitignore:16-17`).

`.local` already means "gitignored companion" everywhere in this org. Ext
adopts the same word so no one has to learn a new rule.

## 2. Read / merge semantics

**The merge is per-namespace, base then `.local`, `.local` wins per key.**
It happens at the one place a namespace file is read into a map, so every
read surface inherits it.

Today three read paths unmarshal a namespace file:

- `loadExtensions` (`ext.go:221-249`) — assembles the merged identity view
  for `Store.Load` (`store.go:85-86`), which is what `get_identity`,
  `whoami`, and hook persona injection consume.
- `ExtGet` (`ext.go:41-75`) — the `ext get` CLI/MCP read.
- `extSetDirect`'s load-modify-write (`ext.go:110-128`) — reads current
  base before writing. This one reads **base only** (see §3).

Introduce one helper that all read paths route through:

```go
// readNamespace returns the merged view of a namespace: the base file
// overlaid by its .local companion, local winning per key. A missing
// base or missing .local is treated as empty; both missing is not-found.
func (s *Store) readNamespace(handle, ns string) (map[string]string, error)
```

It reads `<ns>.yaml` into `m`, then reads `<ns>.local.yaml` (if present)
and does `for k, v := range local { m[k] = v }`. Per-key overlay, not
whole-file replacement: a `.local` that sets only `gpg_passphrase` leaves
every base key intact and adds the one secret.

`loadExtensions` and `ExtGet` call `readNamespace`. The merged `ext` map
returned by `get_identity` / `whoami` is the union:

```yaml
ext:
  quarry:
    memory_collection: memory-mal     # from base quarry.yaml
    session_context: |                # from base quarry.yaml
      ## Memory ...
    api_token: sk-...                 # from quarry.local.yaml, overlaid
```

The consumer sees one flat namespace map and cannot tell which key came
from which file — exactly as if it were one file. That is the point:
`.local` is invisible to readers, visible only to git and vendor.

**Back-compat is structural.** When no `<ns>.local.yaml` exists,
`readNamespace` returns the base map unchanged, so `loadExtensions` and
`ExtGet` produce byte-identical output to today. The `.local` read is a
pure addition guarded by `os.IsNotExist`.

`ExtList` must report a namespace when **either** file exists. Today it
lists `<ns>.yaml` stems (`ext.go:208-215`); extend it to also count a
namespace present only as `<ns>.local.yaml`, and to strip the `.local`
suffix so `quarry.local.yaml` never appears as a phantom namespace named
`quarry.local`. Dedupe the union.

## 3. Write targeting

**Recommendation: a `--local` flag on `ext set`. Default writes base;
secrets go to `--local`.**

```text
ethos ext set mal quarry memory_collection memory-mal    # → quarry.yaml (base)
ethos ext set mal quarry api_token sk-... --local        # → quarry.local.yaml
```

Rationale: the safe default is the common case (base, shareable), and
writing a secret is the deliberate, flagged act — you must say `--local`
to keep something out of git. This matches "secrets are the exception"
(0-1 keys per identity in the discovery).

Per operation:

- **`ext set [--local]`** — targets base, or `.local` with the flag. The
  load-modify-write reads and rewrites **only the targeted file**, never
  the merged view. Writing base must not fold `.local` keys into base
  (that would leak the secret into the committable file). `extSetDirect`
  keeps reading base-only for the base write; a `--local` write reads and
  rewrites `<ns>.local.yaml` alone.
- **`ext get [key]`** — always returns the **merged** view (§2). Reading
  is layer-agnostic; the caller wants the effective value.
- **`ext list`** — one entry per namespace, union of base and `.local`
  (§2). Does not reveal which keys are local.
- **`ext del [key] [--local]`** — deletes from the file the flag names,
  default base. Deleting a key that also exists in the other file leaves
  the other file's value resolving; print a warning to stderr naming the
  surviving file so the operator is not surprised: `key "api_token" still
  resolves from quarry.local.yaml`. `ext del <ns>` with no key removes the
  base file; `ext del <ns> --local` removes the companion; removing the
  whole namespace (both files) requires two calls or a future `--all` — out
  of scope here.

**Limits (DES-008 caps, `ext.go:14-20`).** Check the cap on the file being
written, per file, not on the union. Base and `.local` each cap at 64 keys.
The honest reading of "keys per namespace" is the union, but per-file is
simpler and the secret file is expected to hold 0-1 keys; the union can
exceed 64 only if someone puts 64 secrets beside 64 base keys, which the
cap's intent (bound file size) does not care about. Flag as a minor open
question if the evaluator prefers a union cap.

MCP: the `ext_set` and `ext_del` tools (`DESIGN.md:232-238`) gain a
`local bool` parameter, default `false`, mirroring the flag.

## 4. Gitignore

The rule protects any consuming repo that carries `.punt-labs/ethos/`:

```gitignore
# ethos: secret / machine-specific ext companions never get committed
.punt-labs/ethos/**/*.local.yaml
```

Global ext (`~/.punt-labs/ethos/`) is already untracked — no rule needed
there. The rule matters only for the **repo layer**, which is where
vendored or hand-authored ext could otherwise be committed.

**Who emits it.** `ethos vendor` writes it. Vendor is the command that
creates and owns `.punt-labs/ethos/` (`ni0y-vendor-command.md:135-155`);
it should ensure the ignore rule exists as part of materialization —
append the line to the repo's `.gitignore` if absent, idempotently. Belt
and suspenders: vendor never copies a `.local` file (§5), so the rule is
not what keeps vendored secrets out — it protects the operator who later
runs `ext set --local` against a repo-layer identity. `ethos setup`
(the repo wizard, `CLAUDE.md` build section) should emit the same rule for
repos that assemble `.punt-labs/ethos/` by hand rather than via vendor.

## 5. Vendor interaction — the invariant

**Vendor copies `<ns>.yaml`. Vendor ALWAYS skips `<ns>.local.yaml`. No
exceptions, no heuristics.**

This is the safety boundary, and it resolves the vendor design's open
question §10.3 (`ni0y-vendor-command.md:377-379`). The vendor `.ext/`
handler (`ni0y-vendor-command.md:157-171`) copies the ext tree verbatim;
amend it to a single filter: skip any entry whose name ends in
`.local.yaml`. That one rule replaces `--no-ext` and replaces
publish-time redaction:

> **Invariant.** A file committed or vendored into `.punt-labs/ethos/`
> contains no ext secret, because ext secrets live only in
> `<ns>.local.yaml`, which is never copied and is gitignored. The file
> layout is the boundary; ethos never inspects a value to decide.

Because the skip is name-based and total, vendor keeps honoring DES-008 —
it still never reads or interprets a value. It just never copies the file
whose name declares it local.

The vendor design's `VendorResult.ExtFiles` (`ni0y-vendor-command.md:259`)
lists only the base files copied. Recommend vendor also print a one-line
note when it skipped companions — `skipped 3 .local ext files (not
vendored)` — so the operator sees the boundary held, not a silent drop.

## 6. Validation

DES-008's structural constraints (`ext.go:262-280`, `DESIGN.md:241-254`)
apply to `<ns>.local.yaml` **identically**. The companion is a namespace
file with the same namespace-name grammar, key grammar, value length cap,
and per-file key cap. Validation keys off the namespace stem, which is the
same for both files (`quarry.local.yaml` validates as namespace `quarry`).
No new grammar, no `.local`-specific rule. The only validation change is
that `ExtList` and the write path recognize the `.local.yaml` suffix.

## 7. Interaction with repo-authoritative mode

**The `.local` split does not change where ext resolves from. It is a
within-directory merge that applies to whichever ext dir a store reads.**

Today ext resolves from the **global** layer only
(`layered.go:54-55`, `ExtDir` → global `layered.go:479-482`; DES-044,
`DESIGN.md:4038-4052`). Under `.local`, the global ext dir merges global
`<ns>.yaml` + global `<ns>.local.yaml`. That is the whole change in
layered mode — nothing about the layer choice moves.

The layer question is owned by the two sibling ADRs, not this one. Both
flag the same coupling: for a **vendored** repo ext to be consulted,
repo-authoritative mode must teach resolution to read ext from the **repo**
layer (`ni0y-vendor-command.md:165-171, 369-372`;
`ni0y-resolution-mode.md:152-155`). When that lands, the repo ext dir will
merge repo `<ns>.yaml` + repo `<ns>.local.yaml` by the same
`readNamespace` helper — the split composes with the layer change for free,
because the merge is per-directory.

**Flag for the leader:** `.local` does **not** by itself decide the
global-vs-repo read. It is orthogonal. The repo-authoritative ADR still
owns the decision to read ext from the repo; this ADR only guarantees that
whichever directory is read, its base and `.local` merge the same way. If
repo-authoritative mode ships reading ext from the repo, a repo-layer
`<ns>.local.yaml` becomes the natural home for a per-checkout secret that
must not be committed — which is exactly why §4's gitignore rule targets
the repo layer.

## 8. Proposed record

**Recommendation: a new ADR (DES-0NN), not an in-place DES-008 edit.**

DES-008 is SETTLED and its file-per-namespace rationale
(`DESIGN.md:256-284`) stands unchanged. The precedent in this repo is to
extend a settled ADR with a new, cross-referencing ADR rather than mutate
it: DES-022 (`DESIGN.md:1362`) and DES-044 (`DESIGN.md:4038`) both extend
DES-008 as their own records. Follow that. The new ADR amends DES-008 by
reference and cites this doc.

Draft (DESIGN.md house style; leave the number as DES-0NN for the leader):

> ## DES-0NN: `.local` ext companions — commit-safe extensions (PROPOSED)
>
> **Status**: Proposed. Amends DES-008. Pairs with `ethos vendor`
> (ethos-ni0y) and repo-authoritative resolution (ethos-ni0y).
>
> ### Problem
>
> Ext must be vendored: it carries every agent's quarry memory wiring
> (DES-022) and vox config — a vendored repo without ext loses that.
> Discovery across all 30 identities found ext holds zero secrets today.
> But namespaces are free-form (DES-008) and ethos never interprets a
> value, so nothing stops a consumer from later writing a secret into a
> namespace file. Once `.ext/` is git-tracked by `ethos vendor`, that
> secret is committed. `--no-ext` discards the useful wiring; heuristic
> redaction would force ethos to guess which values are secret, violating
> DES-008's no-interpretation invariant.
>
> ### Decision
>
> Split each ext namespace into two files: `<ns>.yaml` (shareable —
> committable and vendored) and `<ns>.local.yaml` (secret or
> machine-specific — gitignored and never vendored). On read, a namespace
> is the base map overlaid by its `.local` companion, `.local` winning per
> key; the merged view is byte-identical to today when no companion
> exists. `ethos ext set` writes base by default and `.local` under a
> `--local` flag; `ext get` and `ext list` return the merged view without
> revealing which keys are local. `ethos vendor` copies `<ns>.yaml` and
> always skips `<ns>.local.yaml`; a repo `.gitignore` rule
> (`.punt-labs/ethos/**/*.local.yaml`) keeps companions out of git. The
> file layout is the safety boundary — ethos never inspects a value to
> classify it. This mirrors the org's `.envrc`/`.envrc.local` and
> `vox.md`/`vox.local.md` conventions.
>
> ### Reasoning
>
> - Partition beats redaction: a secret that lives only in a
>   never-copied, gitignored file cannot leak, and ethos keeps its
>   DES-008 promise never to interpret a value.
> - The safe default is base (shareable); `--local` is the deliberate,
>   flagged act, matching "secrets are the exception."
> - One `readNamespace` merge point means every read surface —
>   `get_identity`, `whoami`, hook persona injection, `ext get` —
>   inherits the overlay with no per-surface code.
> - Reusing the org's `.local` convention means no new rule to learn;
>   `.local` already means "gitignored companion" for `.envrc` and vox.
> - The split composes with repo-authoritative ext reads: the merge is
>   per-directory, so it applies unchanged whether ext resolves from
>   global or (future) repo.
>
> ### Rejected alternatives
>
> - **`--no-ext` on vendor** — discards the quarry memory wiring that is
>   the reason to vendor ext at all.
> - **Heuristic redaction at publish time** — ethos would have to guess
>   which values are secret, violating DES-008's no-interpretation
>   invariant; `gpg_key_id` is a public key ID, not a secret, so any
>   key-name heuristic is already wrong without curation.
> - **Ext off by default** — a vendored repo then silently loses agent
>   memory; the discovery shows ext is useful, not dangerous.
> - **A per-key `secret: true` marker inside one file** — ethos would
>   have to parse and split on write, and a marker in a committed file is
>   one editing mistake away from committing the secret anyway. The file
>   boundary is unambiguous; a field is not.
> - **Encrypt secret values in place** — key management, and still one
>   file; solves a problem (at-rest encryption) this increment does not
>   have (the values are simply gitignored).
>
> ### Implications
>
> - `Store.ExtGet` / `loadExtensions` route through a `readNamespace`
>   base+`.local` merge; `ExtList` and the write path recognize the
>   `.local.yaml` suffix. No `.local` = byte-identical to today.
> - `ext set` / `ext del` and the `ext_set` / `ext_del` MCP tools gain a
>   `--local` / `local` selector, default base.
> - `ethos vendor` skips `*.local.yaml` unconditionally (resolves the
>   vendor design's ext-secret open question) and ensures the repo
>   `.gitignore` rule exists.
> - Validation constraints (DES-008) apply to `.local` identically.

## 9. Security note (flag for a djb pass)

One residual leak path survives the split: **a secret written into the
base `<ns>.yaml` by mistake** instead of `--local`. The partition is a
convention plus a default, not enforcement — ethos still cannot know a
value is secret (DES-008), so nothing stops `ext set mal beadle
gpg_passphrase hunter2` (no `--local`) from landing in the committable
file.

Options, for djb to rule on:

- **Do nothing** — rely on the default + review. The blast radius is one
  key, and code review + the gitignore-doesn't-catch-it visibility (the
  value shows up in the vendored diff) surface it before merge.
- **A warning-only lint** in `ethos vendor` and `ethos doctor` that flags
  base ext keys whose **name** matches a curated secret-ish list
  (`*_token`, `*_secret`, `password`, `passphrase`, `api_key`, ...) and
  suggests `--local`. Warning, never a hard block — ethos must not
  interpret values, and the list will have false positives (`gpg_key_id`
  is public). The curated list is precisely a security judgment call, so
  it is a djb deliverable, not a bwk one.

Recommend the warning-only lint, sourced from a djb-owned key-name
pattern list, run at the vendor/commit boundary where the git-tracking
happens. Flag routed to djb for the pattern list and for confirming the
"do-nothing on values, name-heuristic warning only" stance.

---

## Report summary

- **Merge semantics** (§2): one `readNamespace(handle, ns)` helper reads
  `<ns>.yaml` then overlays `<ns>.local.yaml` per key, local wins. All
  read paths (`loadExtensions` `ext.go:221`, `ExtGet` `ext.go:41`) route
  through it. No `.local` file = byte-identical to today. `get_identity` /
  `whoami` see one flat merged namespace map; readers cannot tell which
  key came from which file.
- **Write targeting** (§3): `ethos ext set --local` (and `ext_set`'s
  `local` param), default writes base. Base write must not fold `.local`
  keys into base. `ext get` returns merged; `ext list` returns the union
  once; `ext del --local` deletes from the companion with a
  still-resolves warning.
- **Gitignore + vendor exclusion invariant** (§4-5): repo rule
  `.punt-labs/ethos/**/*.local.yaml`, emitted by `ethos vendor` (and
  `ethos setup`). Vendor copies `<ns>.yaml`, ALWAYS skips
  `<ns>.local.yaml` — the file layout is the safety boundary, no
  heuristic redaction. This resolves vendor design open question §10.3.
- **Record** (§8): new ADR (DES-0NN) amending DES-008 by reference, not an
  in-place edit — matches how DES-022 and DES-044 extend DES-008. Draft
  provided.
- **Open questions / djb flags**:
  1. Per-file vs union key cap (§3) — recommend per-file; minor.
  2. `ext del` cross-file warning wording (§3) — confirm.
  3. Repo-vs-global ext read is owned by the resolution-mode ADR, not
     this one (§7); `.local` is orthogonal and composes either way.
  4. **djb pass** (§9): residual leak is a secret typed into base
     `<ns>.yaml` without `--local`. Recommend a warning-only,
     name-heuristic lint at the vendor/commit boundary, with the curated
     secret-key-name pattern list owned by djb. Confirm the
     "no value interpretation, name-warning only" stance.
