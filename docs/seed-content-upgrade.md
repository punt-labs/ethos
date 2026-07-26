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
hand-edits our files; only our releases rev them" — and further ruled that
**installs predating this feature need no migration, bridge, or bootstrap
machinery.** A pre-feature file may simply be upgraded on first run and
thereafter enter the manifest era. This design honours both: from the point
a machine first runs a manifest-aware seed, an edit it makes is preserved;
before that point, there is nothing to preserve.

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

The whole rule, stated once: **write `cur` unless (a) the file on disk
already equals `cur`, or (b) a manifest entry exists and the file on disk
differs from `mf` (a proven user edit).**

Everything below is that rule spelled out.

- `local == cur` → already current → **unchanged**.
- manifest entry exists and `local == mf` and `cur != mf` → untouched since
  our last seed, newer release available → **upgrade** (write `cur`, record
  `cur`).
- manifest entry exists and `local != mf` → changed since we wrote it →
  **user edit** → **skip + warn** (`--force` remedy).
- absent → **deploy** `cur`, record `cur`.
- no manifest entry and `local != cur` → **upgrade** (write `cur`, record
  `cur`).

The last line is the entire treatment of a pre-feature file: with no entry
to prove an edit, we upgrade to the current release and record the entry.
From then on the machine is in the manifest era and its edits are
preserved. This is self-healing and needs zero migration code — per the
operator's ruling that pre-feature installs require no bootstrap.

### Why the manifest, and why it is bounded

The manifest makes the decision **exact and bounded**. Once a path has an
entry, the decision needs only two hashes — `mf` and `cur` — no matter how
many releases the user skipped. `mf` is always the last thing we wrote; if
the user never touched it (`local == mf`), it is safe to upgrade to `cur`
however far apart the two releases are. There is no per-release history to
store: the manifest carries exactly one hash per path, overwritten on each
write. Nothing in this design grows without bound.

### Data structures

**Current shipped hashes** — not stored. Computed at runtime: as seed reads
each embedded file it already has the bytes; `sha256.Sum256(data)` gives
`cur`. This guarantees `cur` is exactly the embedded content — impossible to
drift from what ships.

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

### Decision table

`cur` = current embedded hash. `local` = hash of file on disk. `mf` = hash
recorded in the local manifest for this path (only when an entry exists).

The manifest is consulted **per path**: "manifest entry present" means this
specific path has an entry, not merely that the manifest file exists.

| `local` state | manifest entry present | manifest entry absent |
|---------------|------------------------|-----------------------|
| absent | deploy `cur`; record `cur` | deploy `cur`; record `cur` |
| `== cur` | unchanged (no write); record `cur` | unchanged (no write); record `cur` |
| `!= cur`, `== mf` | **upgrade** → write `cur`; record `cur` | n/a (no `mf` without an entry) |
| `!= cur`, `!= mf` | **skip + warn** (user edit; `--force` remedy) | **upgrade** → write `cur`; record `cur` |
| zero-byte file | repair → write `cur`; record `cur` | repair → write `cur`; record `cur` |

The zero-byte row preserves the current partial-write repair
(`internal/seed/seed.go:225-237`): a zero-byte file is a partial from an
interrupted seed and is rewritten to `cur` regardless of manifest state,
then recorded. It is never treated as a user edit.

The "manifest entry absent, `local != cur`" cell is the collapsed
bootstrap: one upgrade, no lookup, no backup, no skip. We accept the
operator's stated risk — nobody hand-edits our files — so a pre-feature
file is upgraded on first run and enters the manifest era.

### `--force` under the new model

Default seed now upgrades every *safe* file automatically. `--force`'s job
shrinks to one thing: **also overwrite the files default seed skipped as
user edits.** With `--force`, every seeded path is written to `cur`
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
    Updated   []string // shipped files upgraded to this release
    Unchanged []string // already at this release's content
    Skipped   []string // manifest-tracked and locally edited — differ from mf
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

## Tests

- **Manifest schema round-trip** (rsc's coverage ask). A Go test asserts
  that the manifest marshals and unmarshals to the documented schema, and
  that after a seed **every** seeded path has a manifest entry whose `hash`
  equals the content actually on disk. This guards the invariant that seed
  and the manifest never drift — a seeded file without an entry, or an entry
  whose hash mismatches the file, fails the test.
- **Decision-table coverage.** Table-driven test, one case per cell above:
  absent, `== cur`, `!= cur && == mf`, `!= cur && != mf` (both manifest
  states), and the zero-byte repair. Each case sets up disk + manifest
  state, runs the decision, and asserts the action (deploy / unchanged /
  upgrade / skip / repair) and the resulting manifest entry.
- **`--force` overwrites all.** A file that default seed skips as a local
  edit is overwritten to `cur` under `--force`, and its manifest entry
  becomes `cur`.

## Dogfood plan (clean machine, ship v1 → install → ship v2 → re-seed)

Proves the two required outcomes: an untouched file **updates**, a
hand-edited file is **preserved**. Run against a throwaway `HOME` so no real
install is touched.

1. **Build "v1"** (v4.7.0, the first manifest-aware release) from a branch
   whose sidecar carries a known marker in one file, e.g.
   `writing-styles/concise-quantified.md` containing `MARK: v1`.
   `make build`.
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
     `updated:` (unmodified shipped file upgraded via `local == mf`).
   - `principal-engineer.md` still contains `LOCAL EDIT` → printed under
     `skipped (local edit):`, and the `--force` remedy line appeared.
   - The manifest entry for `concise-quantified.md` now records the v2 hash
     and `ethos_version` = v2; `principal-engineer.md`'s entry is unchanged
     (still its v1 hash, so the edit stays detectable).
7. **`--force` check:** re-run `ethos seed --force`; the previously skipped
   `principal-engineer.md` is overwritten to `cur` and its manifest entry
   becomes the v2 hash.

This runs entirely from the built binary against a scratch `HOME` — the
"dogfood before shipping" bar, not just unit tests.

## Rejected alternatives

- **A legacy hash catalog + history-walking generator.** An earlier draft
  embedded a frozen catalog of every pre-feature shipped hash, generated by
  walking git tags and hashing the sidecar at each. Rejected: the generator
  is fragile (the sidecar layout has moved across releases, so it must
  tolerate missing paths and could silently under-populate), it guards a
  case the operator says does not occur (nobody hand-edits our files), and
  the operator ruled explicitly that pre-feature installs get **no**
  migration or bootstrap machinery. The one-line "no entry, `local != cur`
  → upgrade" rule replaces all of it with zero generation risk.
- **A `.seed-backup/` safety net** (copy the old file aside before an
  upgrade). Rejected with the catalog: it exists only to soften a
  mis-upgrade of a pre-feature file, the same case the operator ruled out.
  It adds state to manage and clean up for no benefit under the ruling.
- **Ship `--force` from the installer.** Overwrites user edits on every
  re-install — the exact data loss this design prevents.
- **Timestamp / mtime comparison** (upgrade if the shipped file is
  "newer"). File mtimes are unreliable across `git clone`, `tar`, and
  package managers, and say nothing about *content*. Hashes are exact.
- **Version-stamp each file** (embed a `# ethos: 4.7.0` header). Pollutes
  content, breaks YAML/Markdown cleanliness, and a user edit that keeps the
  stamp would be mis-read as unmodified. Content hashing needs no in-band
  metadata.
- **Interactive prompt on each conflicting file.** seed runs inside
  `install.sh` and non-interactive contexts (`install.sh:321`); prompting
  would hang a piped installer. Skip + warn + `--force` is the
  non-interactive-safe path.
- **Three-way merge of user edits with new shipped content.** Enormous
  complexity for content nobody hand-edits. Skip-and-warn preserves the
  edit; `--force` takes the update. No merge engine warranted.
- **A separate `ethos seed --upgrade` command.** Splits the mental model
  and the code path. Making plain seed edit-safe means one command does the
  right thing by default; `--force` remains the single escape hatch.

## Open questions for the leader / operator

1. **`Result.Skipped` semantics change.** The field name stays but its
   meaning shifts from "already exists" to "manifest-tracked and locally
   edited." Any consumer that parses seed output (does the installer grep
   it?) must be checked. Recommend keeping the name for code continuity; the
   printed label changes to `skipped (local edit):`.
2. **Manifest as a doctor check.** Should `ethos doctor` verify the
   manifest exists and is consistent (every managed file has an entry;
   entries point at existing files)? Recommend a follow-up bead, not v1.
3. **`--dry-run`.** Worth adding so a user can preview `updated` /
   `skipped` before writing? Recommend yes as a fast follow; not required
   for v1.
4. **Skills root scope.** Skills live under `~/.claude/skills/`, outside the
   ethos root. Confirmed the single manifest with a `scope` field covers
   both; the alternative (a second manifest under the skills root) is
   rejected as split state. Confirm the single-manifest approach.

## ADR proposal (for the leader to land in DESIGN.md)

> ## DES-065: Manifest-aware seed — propagate shipped content without clobbering edits (PROPOSED)
>
> **Status**: Proposed. Ships v4.7.0. Full design in
> `docs/seed-content-upgrade.md`.
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
> Make plain seed **upgrade shipped files and preserve proven user edits**,
> decided by content hash.
>
> - **Current shipped hash (`cur`)** is computed at runtime from the
>   embedded bytes — no build step, no drift.
> - **A local install manifest** (`~/.punt-labs/ethos/.seed-manifest.json`)
>   records, per seeded path, the hash seed last wrote (`mf`) plus
>   provenance — exactly one hash per path, overwritten each write, so
>   nothing grows.
> - **The whole rule:** write `cur` **unless** (a) `local == cur` already,
>   or (b) a manifest entry exists and `local != mf` (a proven user edit →
>   skip + warn, `--force` remedy). A file with no manifest entry that
>   differs from `cur` is simply upgraded and recorded — the operator ruled
>   pre-feature installs need **no** migration or bootstrap, so this
>   self-heals on first run.
> - **Zero-byte partials** are repaired to `cur` regardless of manifest
>   state (unchanged from today).
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
> - **No pre-feature migration** (operator). Installs predating the manifest
>   get no legacy catalog, generator, or backup net; an unmodified
>   pre-feature file is upgraded on first run and enters the manifest era.
> - **Content hash, not mtime or in-band version stamps.** mtimes are
>   unreliable across clone/tar/package managers; stamps pollute content and
>   are forgeable by an edit that keeps the stamp.
> - **The manifest carries one hash per path.** No per-release history, no
>   unbounded growth.
> - **Scope: library content only.** The machinery never reads, writes, or
>   hashes `~/.punt-labs/ethos/identities/` — `ethos setup` owns identities.
> - **Skip is safe and recoverable.** A misclassified file is preserved, not
>   lost; `--force` is the documented remedy.
>
> ### Rejected alternatives
>
> - **Legacy hash catalog + history-walking generator.** Fragile generation
>   (sidecar layout has moved across releases; risks silent
>   under-population), unbounded generation risk, and it guards a case the
>   operator says does not occur — the operator ruled no pre-feature
>   migration. The one-line "no entry, `local != cur` → upgrade" replaces it
>   with zero generation code.
> - **`.seed-backup/` safety net.** Exists only to soften a mis-upgrade of a
>   pre-feature file — the ruled-out case; adds state for no benefit.
> - **Installer `--force`** (clobbers edits every re-install).
> - **mtime comparison** (unreliable, content-blind).
> - **Per-file version stamps** (pollute content, forgeable).
> - **Interactive per-file prompt** (hangs the piped installer).
> - **Three-way merge** (unwarranted for content nobody hand-edits).
> - **A separate `ethos seed --upgrade` command** (splits the mental model;
>   plain seed should do the right thing by default).
