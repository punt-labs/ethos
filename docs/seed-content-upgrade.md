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
hand-edits our files; only our releases rev them," and ruled that
installs predating this feature get **no migration code at all** — few
machines exist, migrations are clutter and debt for no reason, and the five
affected computers will be brought current by hand. So the software carries
**zero pre-feature reasoning**: the existing no-clobber skip stays exactly as
it is today for any file the manifest does not yet track, and the new
upgrade behavior activates only once a file has a manifest entry.

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
   the machine. Its recorded hash for a path is `mf`, present only once seed
   has written that path under the new model.

The new upgrade behavior activates **only for a file the manifest tracks.**
For a tracked file, the rule is: upgrade to `cur` when the file on disk is
still exactly what we last wrote (`local == mf`) and the release ships
something newer; otherwise, if the file differs from `mf`, it is a proven
user edit and is skipped. For an **untracked** file the behavior is exactly
today's no-clobber guard — deploy if absent, otherwise skip — with one
addition: a file that already equals `cur` is recorded so it enters the
tracked set for free.

Spelled out:

- absent → **deploy** `cur`, record `cur`.
- `local == cur` → **unchanged**; record `cur` if not already tracked
  (adopt). No write.
- tracked, `local == mf`, `cur != mf` → **upgrade** (write `cur`, record
  `cur`).
- tracked, `local != mf` → **user edit** → **skip + warn** (`--force`
  remedy).
- untracked, `local != cur` → **skip** (today's no-clobber, unchanged). No
  legacy lookup, no upgrade, no backup, no entry recorded.

The last line is the whole treatment of a pre-feature file: leave it
exactly as today's code leaves it. There is no bootstrap branch in the
software.

### Bringing the existing machines current

The five machines that predate this feature are brought into the manifest
era by a **one-time, hand-run `ethos seed --force`** (the operator runs it).
`--force` overwrites every seeded path to `cur` and records an entry for
each. Thereafter those machines are tracked and auto-upgrade on every
release like a fresh install. No code models this — it is the existing
`--force` path plus manifest recording.

### Why the manifest, and why it is bounded

The manifest makes the tracked-file decision **exact and bounded**. Once a
path has an entry, the decision needs only two hashes — `mf` and `cur` — no
matter how many releases the user skipped. `mf` is always the last thing we
wrote; if the user never touched it (`local == mf`), it is safe to upgrade
to `cur` however far apart the two releases are. The manifest carries
exactly one hash per path, overwritten on each write — no per-release
history, nothing that grows without bound.

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
recorded in the local manifest for this path (defined only when an entry
exists).

The manifest is consulted **per path**: "tracked" means this specific path
has an entry, not merely that the manifest file exists.

| `local` state | tracked (entry present) | untracked (no entry) |
|---------------|-------------------------|----------------------|
| absent | deploy `cur`; record `cur` | deploy `cur`; record `cur` |
| `== cur` | unchanged (no write) | unchanged (no write); record `cur` (adopt) |
| `!= cur`, `== mf` | **upgrade** → write `cur`; record `cur` | n/a (`mf` undefined) |
| `!= cur`, `!= mf` | **skip + warn** (user edit; `--force` remedy) | **skip** (today's no-clobber; no record) |
| zero-byte file | repair → write `cur`; record `cur` | repair → write `cur`; record `cur` |

The `untracked, != cur` cell is today's `classifyExisting` no-clobber skip
(`internal/seed/seed.go:221-224`), left untouched — this is where the five
pre-feature machines land until the operator's one-time `--force`.

The zero-byte row preserves the current partial-write repair
(`internal/seed/seed.go:225-237`): a zero-byte file is a partial from an
interrupted seed and is rewritten to `cur` regardless of manifest state,
then recorded. It is never treated as a user edit.

### `--force` under the new model

`--force` overwrites **every** seeded path to `cur` unconditionally and sets
its manifest entry to `cur` — regardless of tracked/untracked state or local
edits. It is both the escape hatch for a tracked file skipped as a user edit
and the one-time tool that brings a pre-feature machine into the manifest
era. `--force` is the documented remedy printed next to every skipped line.

### Installer change

`install.sh:321` keeps calling plain `ethos seed` — **no `--force`.** Under
the new model plain seed upgrades tracked files and never clobbers an edit,
so the installer gets the upgrade behavior for free. The only edits to
`install.sh` are messaging: the success line should reflect that seed may
now *update* content, and if seed reports skipped files, surface the
`--force` remedy hint rather than a bare "deployed" (`install.sh:322-324`).
Adding `--force` to the installer is explicitly rejected — it would clobber
user edits and re-clobber the pre-feature machines on every re-install, the
exact failure this design exists to avoid.

### User-facing output

seed is a **CLI command, not an MCP tool** — there is no MCP handler for it
(`internal/mcp/` has none), so DES-020's "every MCP tool needs a formatter
in `format_output.go`" does not add a formatter here. The output follows
the CLI standard: aligned, lowercase, actionable.

The `Result` struct (`internal/seed/seed.go:14-19`) gains an `Updated`
category and keeps `Skipped` with its existing meaning (a no-clobber skip of
an untracked file), plus an `Edited` category for a tracked file that
differs from `mf`:

```go
type Result struct {
    Deployed  []string // new files written (were absent)
    Updated   []string // tracked files upgraded to this release
    Unchanged []string // already at this release's content
    Skipped   []string // untracked, differs from cur — today's no-clobber
    Edited    []string // tracked and locally edited — differs from mf
    Repaired  []string // zero-byte partials overwritten
    Errors    []string
}
```

Keeping `Skipped` unchanged means no existing consumer of that field breaks;
the new tracked-edit case gets its own field and label.

Command output (`cmd/ethos/seed.go`), one line per file, then a summary:

```text
  deployed:  roles/product-lead.yaml
  updated:   writing-styles/concise-quantified.md
  unchanged: roles/architect.yaml
  skipped (exists): talents/go.md
  skipped (local edit): personalities/principal-engineer.md

Seeded 42 files: 3 new, 5 updated, 31 unchanged, 2 skipped, 1 local edit.
3 files were skipped; re-run 'ethos seed --force' to overwrite them.
```

The remedy line prints only when `len(Skipped)+len(Edited) > 0`. `updated`
is the line that proves the feature works: it did not exist under the old
model.

## Tests

- **Manifest schema round-trip** (rsc's coverage ask). A Go test asserts
  that the manifest marshals and unmarshals to the documented schema, and
  that after a seed of a **fresh** destination, every seeded path has a
  manifest entry whose `hash` equals the content actually on disk. This
  guards the invariant that a written file and its manifest entry never
  drift.
- **Untracked skip leaves no entry.** A pre-existing file that differs from
  `cur` and has no entry is skipped and gains **no** entry (today's
  behavior, asserted so a future change cannot silently record or upgrade
  it).
- **Decision-table coverage.** Table-driven test, one case per cell above:
  absent, `== cur` (tracked and untracked), `!= cur && == mf`,
  `!= cur && != mf` (tracked → edit-skip; untracked → no-clobber skip), and
  the zero-byte repair. Each case sets up disk + manifest state, runs the
  decision, and asserts the action and the resulting manifest entry.
- **`--force` overwrites all and records all.** A tracked edited file and an
  untracked differing file are both overwritten to `cur` under `--force`,
  and both gain a manifest entry equal to `cur` — the one-time-migration
  path the operator runs by hand.

## Dogfood plan (clean machine, ship v1 → install → ship v2 → re-seed)

Proves the two required outcomes: an untracked file **updates** once
tracked, a hand-edited tracked file is **preserved**. Run against a
throwaway `HOME` so no real install is touched.

1. **Build "v1"** (v4.7.0, the first manifest-aware release) from a branch
   whose sidecar carries a known marker in one file, e.g.
   `writing-styles/concise-quantified.md` containing `MARK: v1`.
   `make build`.
2. **Install v1** into a scratch home: run the binary with `HOME=$TMP`
   (`.tmp/seed-dogfood/home`). Confirm `~/.punt-labs/ethos/…/concise-
   quantified.md` contains `MARK: v1` and a `.seed-manifest.json` records
   its hash with `ethos_version` = v1. Every deployed file is tracked from
   this first run.
3. **Hand-edit a second file** in the scratch home, e.g. append `LOCAL
   EDIT` to `personalities/principal-engineer.md`. This is the tracked file
   that must survive.
4. **Build "v2"**: bump the marker to `MARK: v2` in
   `concise-quantified.md`, rebuild. This is the shipped improvement.
5. **Re-seed v2** into the same scratch home (plain `ethos seed`, no
   `--force`).
6. **Assert:**
   - `concise-quantified.md` now contains `MARK: v2` → printed under
     `updated:` (tracked, `local == mf`, upgraded).
   - `principal-engineer.md` still contains `LOCAL EDIT` → printed under
     `skipped (local edit):`, and the `--force` remedy line appeared.
   - The manifest entry for `concise-quantified.md` now records the v2 hash
     and `ethos_version` = v2; `principal-engineer.md`'s entry is unchanged
     (still its v1 hash, so the edit stays detectable).
7. **Pre-feature-machine check (untracked skip).** Delete the manifest and
   re-seed v2. The unmodified `concise-quantified.md` (now untracked and
   `!= cur`) is **skipped (exists)** — not upgraded, no entry recorded —
   proving the software carries no bootstrap branch. Then run
   `ethos seed --force`: it overwrites to `cur` and records entries for all,
   simulating the operator's one-time hand-clean; a subsequent plain re-seed
   now upgrades normally.

This runs entirely from the built binary against a scratch `HOME` — the
"dogfood before shipping" bar, not just unit tests.

## Rejected alternatives

- **A bootstrap-upgrade branch for untracked files** ("no entry and
  `local != cur` → upgrade"). Rejected by the operator: there are few
  machines, a migration branch is clutter and debt, and the five affected
  computers are hand-cleaned with a one-time `--force`. The software keeps
  today's no-clobber skip for untracked files and carries zero pre-feature
  reasoning.
- **A legacy hash catalog + history-walking generator.** An earlier draft
  embedded a frozen catalog of every pre-feature shipped hash, generated by
  walking git tags and hashing the sidecar at each. Rejected: fragile
  generation (the sidecar layout has moved across releases, risking silent
  under-population), unbounded generation risk, and it guards a case the
  operator ruled out — no pre-feature migration.
- **A `.seed-backup/` safety net** (copy the old file aside before an
  upgrade). Rejected with the catalog: it exists only to soften a mis-upgrade
  of a pre-feature file, the ruled-out case; adds state to manage for no
  benefit.
- **Ship `--force` from the installer.** Overwrites user edits and
  re-clobbers the hand-cleaned machines on every re-install — the exact data
  loss this design prevents.
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

1. **Manifest as a doctor check.** Should `ethos doctor` verify the
   manifest exists and is consistent (tracked files point at existing files;
   hashes match)? Recommend a follow-up bead, not v1.
2. **`--dry-run`.** Worth adding so a user can preview `updated` /
   `skipped` before writing? Recommend yes as a fast follow; not required
   for v1.
3. **Skills root scope.** Skills live under `~/.claude/skills/`, outside the
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
> Make plain seed **upgrade tracked shipped files and preserve proven user
> edits**, decided by content hash. New behavior activates only for a file
> the manifest already tracks; an untracked file keeps today's no-clobber
> skip.
>
> - **Current shipped hash (`cur`)** is computed at runtime from the
>   embedded bytes — no build step, no drift.
> - **A local install manifest** (`~/.punt-labs/ethos/.seed-manifest.json`)
>   records, per seeded path, the hash seed last wrote (`mf`) plus
>   provenance — one hash per path, overwritten each write, so nothing grows.
> - **Decision:** absent → deploy + record. `local == cur` → unchanged
>   (record if untracked, to adopt). Tracked and `local == mf` and
>   `cur != mf` → upgrade + record. Tracked and `local != mf` → skip + warn
>   (user edit; `--force` remedy). **Untracked and `local != cur` → today's
>   no-clobber skip, unchanged.** Zero-byte partials are repaired to `cur`
>   regardless.
> - **No pre-feature migration in the software.** The five existing machines
>   are hand-cleaned once with `ethos seed --force`, which overwrites to
>   `cur` and records every entry; thereafter they auto-upgrade.
> - **`--force`** overwrites every path to `cur` and records all entries —
>   the escape hatch and the one-time hand-clean tool.
> - **Installer unchanged except messaging** — still plain `ethos seed`, now
>   upgrade-capable; no `--force`.
> - **Output** gains `deployed` / `updated` / `unchanged` / `skipped
>   (exists)` / `skipped (local edit)` lines and a `--force` remedy hint
>   (CLI standard; seed has no MCP surface, so no DES-020 formatter is
>   added). `Result.Skipped` keeps its meaning; a new `Edited` field carries
>   the tracked-edit case.
>
> ### Rulings
>
> - **No pre-feature migration** (operator). Few machines; a migration
>   branch is clutter and debt. The software keeps the existing no-clobber
>   skip for untracked files; the affected computers are hand-cleaned with a
>   one-time `--force`.
> - **Content hash, not mtime or in-band version stamps.** mtimes are
>   unreliable across clone/tar/package managers; stamps pollute content and
>   are forgeable by an edit that keeps the stamp.
> - **The manifest carries one hash per path.** No per-release history, no
>   unbounded growth.
> - **Scope: library content only.** The machinery never reads, writes, or
>   hashes `~/.punt-labs/ethos/identities/` — `ethos setup` owns identities.
> - **Skip is safe and recoverable.** A skipped file is preserved, not lost;
>   `--force` is the documented remedy.
>
> ### Rejected alternatives
>
> - **A bootstrap-upgrade branch for untracked files.** Operator ruled no
>   pre-feature migration — few users, hand-clean the affected machines. The
>   no-clobber skip stays for untracked files.
> - **Legacy hash catalog + history-walking generator.** Fragile generation
>   (sidecar layout has moved across releases; risks silent
>   under-population), unbounded generation risk, guards the ruled-out
>   pre-feature case.
> - **`.seed-backup/` safety net.** Softens a mis-upgrade of a pre-feature
>   file — the ruled-out case; adds state for no benefit.
> - **Installer `--force`** (clobbers edits and re-clobbers hand-cleaned
>   machines every re-install).
> - **mtime comparison** (unreliable, content-blind).
> - **Per-file version stamps** (pollute content, forgeable).
> - **Interactive per-file prompt** (hangs the piped installer).
> - **Three-way merge** (unwarranted for content nobody hand-edits).
> - **A separate `ethos seed --upgrade` command** (splits the mental model;
>   plain seed should do the right thing by default).
