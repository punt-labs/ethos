# Seed content upgrade — propagate shipped improvements without clobbering edits

## Problem

`ethos seed` deploys embedded library content (roles, talents,
personalities, writing-styles, archetypes, pipelines, bundles, skills,
READMEs) to `~/.punt-labs/ethos/` and `~/.claude/skills/`. On a fresh
machine every file is new and gets written. On a machine that already has
the content, seed refuses to touch anything that exists.

That refusal is a no-clobber guard. When a destination file exists and is
non-empty, `classifyExisting` records it as skipped and returns without
writing (`internal/seed/seed.go:221-224`). The command then prints
`skipped (exists):` for each (`cmd/ethos/seed.go:50-52`). A confirmed live
run: re-installing 4.6.0 deployed only genuinely-new files and printed
"skipped (exists)" for 111 existing ones.

The consequence: **a released improvement to a shipped file never reaches a
returning user.** If 4.7.0 ships a stronger `concise-quantified` writing
style, a user who installed 4.6.0 keeps the 4.6.0 text forever. The only
override is `ethos seed --force`, which overwrites *everything*
unconditionally (`internal/seed/seed.go:170-176`, flag at
`cmd/ethos/seed.go:24`) — it cannot tell an unmodified shipped file from
one the user hand-edited, so it is unsafe as the default upgrade path.

The installer calls plain `ethos seed` with no `--force`
(`install.sh:321`), so a re-install upgrades nothing.

### What the operator wants

Content improvements must propagate to existing installs on the next seed,
**without clobbering genuine user edits.** The operator notes: "no one
hand-edits our files; only our releases rev them" — so a false negative (we
skip a file that was actually unmodified) is worse than a false positive,
but both must be avoided by construction where possible, and any skip must
be safe (no data loss) and recoverable (`--force`).

## Scope: seed manages library content, never user identities

Grounded in `internal/seed/seed.go:25-66`, `Seed` deploys exactly these
categories:

| Category | Source | Destination | seed.go |
|----------|--------|-------------|---------|
| Roles | `sidecar/roles/*.yaml` | `<dest>/roles/` | :29 |
| Talents | `sidecar/talents/*.md` | `<dest>/talents/` | :32 |
| Personalities | `sidecar/personalities/*.md` | `<dest>/personalities/` | :37 |
| Writing-styles | `sidecar/writing-styles/*.md` | `<dest>/writing-styles/` | :38 |
| Archetypes | `sidecar/archetypes/*.yaml` | `<dest>/archetypes/` | :41 |
| Pipelines | `sidecar/pipelines/*.yaml` | `<dest>/pipelines/` | :44 |
| Skills | `sidecar/skills/*/SKILL.md` | `<skillsRoot>/*/` | :47-52 |
| READMEs | `sidecar/**/README.md` | `<dest>/**/` | :55 |
| Bundles | `sidecar/bundles/**` | `<dest>/bundles/<name>/` | :60 |

It does **not** deploy user identities. There is no `seedFS` call for
`sidecar/identities/` or `sidecar/teams/`; those paths are not even in the
embed set (`internal/seed/embed.go:5-30`). User identities are owned by
`ethos setup`, which writes `~/.punt-labs/ethos/identities/<handle>.yaml`.
Bundle-internal identities land in the read-only bundle layer
(`<dest>/bundles/<name>/…`, DES-051), which is library content shipped as
part of a bundle, not a user's own identity.

**This design touches only the nine library categories above.** The
upgrade machinery must never write, read-for-decision, or hash anything
under `~/.punt-labs/ethos/identities/`. Setup owns that tree.

## Design

### Core idea

Two facts let seed decide, per file, whether a write is safe:

1. **What this release ships** — the content embedded in the running
   binary. Its SHA-256 is the *current shipped hash* (`cur`). Computed at
   runtime from the embedded bytes seed already reads; no build step, no
   drift.
2. **What we last wrote here** — recorded in a *local install manifest* on
   the machine. Its recorded hash for a path is `mf`.

With `cur`, `mf`, and the hash of the file on disk (`local`), the decision
is exact:

- `local == mf` and `cur != mf` → the file is untouched since our last
  seed, and the release ships something newer → **upgrade** (overwrite with
  `cur`, record `cur`).
- `local == cur` → already current → **unchanged**.
- `local != mf` → changed since we wrote it → **user edit** → **skip +
  warn** (`--force` to overwrite).
- absent → **deploy** `cur`.

The manifest is what makes this exact and *bounded*: once a machine has a
manifest entry for a path, the decision needs only `mf` and `cur` — two
hashes — no matter how many releases the user skipped. `mf` is always the
last thing we wrote; if the user never touched it (`local == mf`), it is
safe to upgrade to `cur` however far apart the two releases are.

### The bootstrap problem

An install that predates this feature has **no manifest**. On its first
manifest-aware seed, every existing file has `local` present but `mf`
absent. We cannot use the "`local == mf`?" test. We must still distinguish
an untouched old shipped file (upgrade it) from a hand-edited one (preserve
it).

The answer: embed a **frozen catalog of legacy shipped hashes** — for each
seeded path, the set of content hashes that path had across all releases
*before* this feature shipped. If a pre-manifest file's `local` hash is in
that set, it is provably an untouched shipped file from some old release,
and we upgrade it. If it matches nothing, it is a user edit (or unknown
provenance) and we skip + warn.

Why the legacy catalog is **bounded**:

- It only needs to cover the **pre-manifest era**. Once manifest-aware seed
  ships (call it `v_M` = the first release with this feature), every seed
  writes a manifest. So any machine *without* a manifest is definitionally
  running content from a release older than `v_M`. The legacy catalog is
  generated **once**, at `v_M`, from the release history up to `v_M-1`, and
  is **frozen forever after**. Post-`v_M` releases add nothing to it —
  post-`v_M` installs always have a manifest and never consult it.
- A **floor** bounds it further. The generator walks back only to a chosen
  oldest tag (the *legacy floor*). An unmodified file older than the floor
  matches nothing → skip + warn → the user runs `--force`. Given "no one
  hand-edits our files," `--force` is safe for that user, so the floor
  trades a bounded catalog for a rare, safe, recoverable false-positive
  skip on ancient installs.

So the only artifact that could grow is frozen at ship time. The running
binary carries: `cur` (one hash per file, derived at runtime, constant) +
the frozen legacy catalog (fixed at `v_M`, never grows).

### Data structures

**Current shipped hashes** — not stored. Computed at runtime: as seed reads
each embedded file it already has the bytes; `sha256.Sum256(data)` gives
`cur`. This guarantees `cur` is exactly the embedded content — impossible to
drift from what ships.

**Legacy catalog** — `internal/seed/legacy_hashes.json`, embedded via
`//go:embed`. Generated once at `v_M`, committed, then frozen. Format:

```json
{
  "schema": 1,
  "floor": "v4.0.0",
  "frozen_at": "v4.7.0",
  "paths": {
    "writing-styles/concise-quantified.md": [
      "sha256:aa11…", "sha256:bb22…"
    ],
    "roles/architect.yaml": ["sha256:cc33…"]
  }
}
```

Keys are dest-relative logical paths (same keys as the local manifest, see
below). Values are the distinct content hashes that path held across
releases `[floor, v_M-1]`. `floor` and `frozen_at` are recorded for
auditability. **This file is never regenerated after `v_M`.** A comment at
its head and a check in the generator (refuse to run if the file already
exists without `--regenerate`) enforce the freeze.

**Local install manifest** — `~/.punt-labs/ethos/.seed-manifest.json`,
written atomically (reuse `atomicWrite`, `internal/seed/seed.go:271`).
Format:

```json
{
  "schema": 1,
  "entries": {
    "writing-styles/concise-quantified.md": {
      "scope": "ethos",
      "hash": "sha256:dd44…",
      "ethos_version": "4.7.0",
      "written": "2026-07-26T10:04:11Z"
    }
  }
}
```

- **Key** — the logical seed path: dest-relative for the ethos root
  (`roles/architect.yaml`), or `skills/<name>/SKILL.md` for the skills
  root. `scope` (`ethos` | `skills`) records which root the key resolves
  against, so one manifest covers both destinations.
- **hash** — the content hash of what seed last wrote there.
- **ethos_version** / **written** — provenance for `ethos doctor` and audit;
  not used in the decision.

The manifest is a dotfile (`.seed-manifest.json`), sits at the seed root
(not under any managed category directory), and is never itself a seed
target — so it can never be mistaken for user content.

### The legacy catalog generator

A small internal tool, `cmd/gen-seed-manifest` (or a `make
gen-legacy-hashes` target), run once when the feature is being prepared:

1. Enumerate release tags from the floor to `HEAD` (`git tag --list 'v*'`,
   filtered `>= floor` and `< v_M`).
2. For each tag, for each seeded path, read the file at that tag
   (`git show <tag>:internal/seed/sidecar/<…>`) and hash it. A path absent
   at a tag contributes nothing (the sidecar layout has moved over time;
   the generator tolerates missing paths).
3. Accumulate the distinct hash set per dest-relative key.
4. Write `internal/seed/legacy_hashes.json`, sorted for a stable diff.

The generator is **not** part of `make build` or the release workflow — it
runs by hand at `v_M`, its output is committed, and thereafter the file is
frozen. Re-running requires an explicit `--regenerate` and should never be
needed.

### Decision table

`cur` = current embedded hash. `local` = hash of file on disk. `mf` = hash
recorded in the local manifest for this path. `legacy(p)` = the frozen
legacy hash set for path `p`.

The manifest is consulted **per path**: "manifest entry present" means this
specific path has an entry, not merely that the manifest file exists. When
a path has an entry, that entry is authoritative and `legacy` is **not**
consulted. `legacy` is consulted only to bootstrap a path with no entry.

**Path has a manifest entry (`mf` known):**

| `local` state | Action | Manifest after |
|---------------|--------|----------------|
| absent | deploy `cur` | entry = `cur` |
| `== cur` | unchanged (no write) | entry = `cur` |
| `== mf`, `!= cur` | **upgrade** → write `cur` | entry = `cur` |
| else (`!= mf`, `!= cur`) | **skip + warn** (user edit; `--force` remedy) | entry unchanged |

**Path has no manifest entry (bootstrap / pre-`v_M`):**

| `local` state | Action | Manifest after |
|---------------|--------|----------------|
| absent | deploy `cur` | entry = `cur` |
| `== cur` | adopt (no write) | entry = `cur` |
| `∈ legacy(p)`, `!= cur` | **upgrade** → write `cur` | entry = `cur` |
| else (matches nothing) | **skip + warn** (user edit / unknown; `--force` remedy) | **no entry** |

Mapping to the mission's `{absent, matches-current, matches-older-shipped,
matches-nothing} × {manifest present/absent}` grid:

| `local` | manifest entry present | manifest entry absent |
|---------|------------------------|-----------------------|
| absent | deploy `cur`; record | deploy `cur`; record |
| matches-current (`== cur`) | unchanged; record | adopt; record |
| matches-older-shipped | if `== mf`: upgrade+record. Else (matches a legacy hash but `!= mf`): skip + warn¹ | if `∈ legacy`: upgrade+record. Else: skip + warn |
| matches-nothing | skip + warn; no record change | skip + warn; no record |

¹ **Edge case — matches an older shipped hash but not `mf`.** Once a path
has a manifest entry, `mf` is authoritative: `mf` is exactly what we last
wrote, so `local != mf` means the file changed since. If it now happens to
equal some *legacy* hash, the only way that occurred is a user (or another
tool) rewriting our file to an ancient shipped version. That is
indistinguishable from a deliberate edit, so we **skip + warn** — the
conservative, no-data-loss choice. The legacy set is deliberately **not**
consulted for a path that has a manifest entry.

The zero-byte-repair case (`internal/seed/seed.go:225-237`) is unchanged: a
zero-byte file is a partial write from an interrupted seed and is repaired
to `cur` regardless of manifest state, then recorded.

### `--force` under the new model

Default seed now upgrades every *safe* file automatically. `--force`'s
job shrinks to one thing: **also overwrite the files default seed skipped
as user edits.** With `--force`, every seeded path is written to `cur`
unconditionally and its manifest entry set to `cur` — the machine is forced
to exactly this release's content, and any local edits are gone (which is
what `--force` means). `--force` is the documented remedy printed next to
every `skipped (local edit)` line.

### Installer change

`install.sh:321` keeps calling plain `ethos seed` — **no `--force`.** Under
the new model plain seed is already upgrade-capable and edit-safe, so the
installer gets the upgrade behavior for free while never clobbering an edit.
The only edits to `install.sh` are messaging: the success line should
reflect that seed may now *update* content, and if seed reports skipped
local edits, surface the `--force` remedy hint rather than a bare "deployed"
(`install.sh:322-324`). Adding `--force` to the installer is explicitly
rejected — it would clobber user edits on every re-install, the exact
failure this design exists to avoid.

### User-facing output

seed is a **CLI command, not an MCP tool** — there is no MCP handler for it
(`internal/mcp/` has none), so DES-020's "every MCP tool needs a formatter
in `format_output.go`" does not add a formatter here. The output follows
the CLI standard: aligned, lowercase, actionable.

The `Result` struct (`internal/seed/seed.go:14-19`) gains categories so the
command can distinguish outcomes:

```go
type Result struct {
    Deployed  []string // new files written (were absent)
    Updated   []string // unmodified shipped files upgraded to this release
    Unchanged []string // already at this release's content
    Skipped   []string // differ from all known shipped hashes — local edits
    Repaired  []string // zero-byte partials overwritten
    Errors    []string
}
```

Command output (`cmd/ethos/seed.go`), one line per file, then a summary:

```text
  deployed:  roles/product-lead.yaml
  updated:   writing-styles/concise-quantified.md
  unchanged: roles/architect.yaml
  skipped (local edit): personalities/principal-engineer.md

Seeded 42 files: 3 new, 5 updated, 33 unchanged, 1 local edit skipped.
1 file looks locally edited; re-run 'ethos seed --force' to overwrite it.
```

The remedy line prints only when `len(Skipped) > 0`. `updated` is the line
that proves the feature works: it did not exist under the old model.

## Dogfood plan (clean machine, ship v1 → install → ship v2 → re-seed)

Proves the two required outcomes: an untouched file **updates**, a
hand-edited file is **preserved**. Run against a throwaway `HOME` so no real
install is touched.

1. **Build "v1"** at `v_M` from a branch whose sidecar carries a known
   marker in one file, e.g. `writing-styles/concise-quantified.md`
   containing `MARK: v1`. `make build`.
2. **Install v1** into a scratch home: run the binary with `HOME=$TMP`
   (`.tmp/seed-dogfood/home`). Confirm `~/.punt-labs/ethos/…/concise-
   quantified.md` contains `MARK: v1` and a `.seed-manifest.json` records
   its hash with `ethos_version` = v1.
3. **Hand-edit a second file** in the scratch home, e.g. append `LOCAL
   EDIT` to `personalities/principal-engineer.md`. This is the file that
   must survive.
4. **Build "v2"**: bump the marker to `MARK: v2` in
   `concise-quantified.md`, rebuild. This is the shipped improvement.
5. **Re-seed v2** into the same scratch home (plain `ethos seed`, no
   `--force`).
6. **Assert:**
   - `concise-quantified.md` now contains `MARK: v2` → printed under
     `updated:` (unmodified shipped file upgraded).
   - `principal-engineer.md` still contains `LOCAL EDIT` → printed under
     `skipped (local edit):`, and the `--force` remedy line appeared.
   - The manifest entry for `concise-quantified.md` now records the v2 hash
     and `ethos_version` = v2; `principal-engineer.md` has **no** manifest
     entry.
7. **Bootstrap check (no manifest):** repeat 1–2, then delete
   `.seed-manifest.json` before step 5 to simulate a pre-`v_M` install.
   With `concise-quantified.md` still at the untouched v1 hash (which is in
   the frozen legacy set once v1 is a released tag ≤ floor…`v_M-1`), re-seed
   v2 must still upgrade it via the legacy path, and the hand-edited file
   must still be preserved.
8. **`--force` check:** re-run `ethos seed --force`; the previously skipped
   `principal-engineer.md` is overwritten to `cur` and gains a manifest
   entry.

This is table-driven-friendly and runs entirely from the built binary
against a scratch `HOME` — the "dogfood before shipping" bar, not just unit
tests.

## Rejected alternatives

- **Ship `--force` from the installer.** Overwrites user edits on every
  re-install — the exact data loss this design prevents. Rejected outright.
- **Timestamp / mtime comparison** (upgrade if the shipped file is
  "newer"). File mtimes are unreliable across `git clone`, `tar`, and
  package managers, and say nothing about *content*. Hashes are exact.
- **Version-stamp each file** (embed a `# ethos: 4.7.0` header). Pollutes
  content, breaks YAML/Markdown cleanliness, and a user edit that keeps the
  stamp would be mis-read as unmodified. Content hashing needs no in-band
  metadata.
- **Store per-release hashes for the whole history, unbounded.** Grows
  forever. The manifest makes post-`v_M` history unnecessary; only the
  frozen pre-`v_M` legacy set is kept, floor-bounded.
- **Interactive prompt on each conflicting file.** seed runs inside
  `install.sh` and non-interactive contexts (`install.sh:321`); prompting
  would hang a piped installer. Skip + warn + `--force` is the
  non-interactive-safe path.
- **Three-way merge of user edits with new shipped content.** Enormous
  complexity for content nobody hand-edits ("only our releases rev them").
  Skip-and-warn preserves the edit; `--force` takes the update. No merge
  engine warranted.
- **A separate `ethos seed --upgrade` command.** Splits the mental model
  and the code path. Making plain seed edit-safe means one command does the
  right thing by default; `--force` remains the single escape hatch.

## Open questions for the leader / operator

1. **Legacy floor.** Which tag is the floor? Current release is `v4.6.0`;
   the feature ships at `v_M` (proposed `v4.7.0`). Recommend floor =
   `v4.0.0` (or the oldest release realistically still in the field). Older
   untouched files fall to skip + warn + `--force` — safe but noisier for
   ancient installs.
2. **`Result.Skipped` semantics change.** The field name stays but its
   meaning shifts from "already exists" to "differs from all known shipped
   hashes (local edit)." Any consumer that parses seed output (does the
   installer grep it?) must be checked. Recommend keeping the name for code
   continuity; the printed label changes to `skipped (local edit):`.
3. **Manifest as a doctor check.** Should `ethos doctor` verify the
   manifest exists and is consistent (every managed file has an entry;
   entries point at existing files)? Recommend a follow-up bead, not v1.
4. **`--dry-run`.** Worth adding so a user can preview `updated` /
   `skipped` before writing? Recommend yes as a fast follow; not required
   for v1.
5. **Skills root scope.** Skills live under `~/.claude/skills/`, outside the
   ethos root. Confirmed the single manifest with a `scope` field covers
   both; the alternative (a second manifest under the skills root) is
   rejected as split state. Confirm the single-manifest approach.

## ADR proposal (for the leader to land in DESIGN.md)

> ## DES-065: Manifest-aware seed — propagate shipped content without clobbering edits (PROPOSED)
>
> **Status**: Proposed. Full design in `docs/seed-content-upgrade.md`.
>
> ### Problem
>
> `ethos seed` deploys embedded library content (roles, talents,
> personalities, writing-styles, archetypes, pipelines, bundles, skills,
> READMEs — never user identities, which `ethos setup` owns). Its
> no-clobber guard skips any existing non-empty file
> (`internal/seed/seed.go:221-224`; printed at `cmd/ethos/seed.go:50-52`),
> so a released improvement to a shipped file never reaches a returning
> user — a live 4.6.0 re-install skipped 111 existing files. The only
> override, `--force` (`internal/seed/seed.go:170-176`), overwrites
> everything blindly and cannot tell an unmodified shipped file from a
> hand-edited one, so it is unsafe as the upgrade path. The installer runs
> plain `ethos seed` (`install.sh:321`), upgrading nothing.
>
> ### Decision
>
> Make plain seed **upgrade unmodified shipped files and preserve user
> edits**, decided by content hash.
>
> - **Current shipped hash (`cur`)** is computed at runtime from the
>   embedded bytes — no build step, no drift.
> - **A local install manifest** (`~/.punt-labs/ethos/.seed-manifest.json`)
>   records, per seeded path, the hash seed last wrote (`mf`) plus
>   provenance. Once a path has an entry, the decision needs only `mf` and
>   `cur` — bounded to two hashes regardless of how many releases were
>   skipped.
> - **A frozen legacy hash catalog** (`internal/seed/legacy_hashes.json`,
>   embedded) covers the pre-manifest era so a first upgrade with no
>   manifest can still recognise an untouched old file. It is generated once
>   at the feature release (`v_M`) from release history down to a floor tag,
>   then **frozen** — post-`v_M` installs always have a manifest and never
>   consult it, so it never grows.
> - **Decision, per path:** absent → deploy. `local == cur` → unchanged.
>   Manifest entry present and `local == mf` → upgrade to `cur`. No entry
>   and `local ∈ legacy` → upgrade. Anything else (differs from all known
>   shipped hashes) → skip + warn with a `--force` remedy. (Full four-state
>   × manifest-present/absent table in the design doc.)
> - **`--force`** additionally overwrites the skipped (edited) files and
>   records `cur` for every path — the single escape hatch.
> - **Installer unchanged except messaging** — still plain `ethos seed`, now
>   upgrade-capable; no `--force`.
> - **Output** gains `deployed` / `updated` / `unchanged` /
>   `skipped (local edit)` lines and a `--force` remedy hint (CLI standard;
>   seed has no MCP surface, so no DES-020 formatter is added).
>
> ### Rulings
>
> - **Content hash, not mtime or in-band version stamps.** mtimes are
>   unreliable across clone/tar/package managers; stamps pollute content and
>   are forgeable by an edit that keeps the stamp.
> - **The manifest bounds history; the legacy catalog is frozen and
>   floor-bounded.** No unbounded per-release hash store.
> - **Scope: library content only.** The machinery never reads, writes, or
>   hashes `~/.punt-labs/ethos/identities/` — `ethos setup` owns identities.
> - **Skip is safe and recoverable.** A misclassified file is preserved, not
>   lost; `--force` is the documented remedy.
>
> ### Rejected alternatives
>
> - Installer `--force` (clobbers edits every re-install).
> - mtime comparison (unreliable, content-blind).
> - Per-file version stamps (pollute content, forgeable).
> - Unbounded full-history hash store (the manifest makes it unnecessary).
> - Interactive per-file prompt (hangs the piped installer).
> - Three-way merge (unwarranted for content nobody hand-edits).
> - A separate `ethos seed --upgrade` command (splits the mental model; plain
>   seed should do the right thing by default).
