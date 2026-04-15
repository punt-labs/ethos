# Build Plan: Team Bundle Activation

Epic bead: ethos-2hh
Target release: v3.7.0
Design record: [DES-051](../DESIGN.md)

---

## Problem statement

The `punt-labs/team` git submodule serves two unrelated purposes:

1. Distribute **gstack** — the generic starter content (archetypes,
   pipelines, personalities, writing styles) that every ethos user
   benefits from.
2. Bind **Punt Labs' internal team** — the identities, roles, and
   collaborations specific to the `punt-labs/*` organization.

Conflating them has three consequences:

- A first-time user outside Punt Labs cannot adopt gstack without
  cloning a submodule whose identity content is wrong for their
  team.
- A user with a private team (`acme-corp`) has no clean way to
  switch the active team. The repo-local layer is a single
  submodule; two teams cannot coexist.
- gstack updates require a commit in `punt-labs/team`, which mixes
  generic ethos content into a private org registry.

The repo-local layer was designed as "one team per repo." The
ecosystem needs "one or more teams available, one active at a time."

## Goal

Introduce **team bundles** — self-contained directories of ethos
content — that can be:

- **Shipped with ethos** (gstack, embedded and seeded to
  `~/.punt-labs/ethos/bundles/`).
- **Added as submodules** (`.punt-labs/ethos-bundles/<name>`).
- **Authored privately** (plain directory, git-ignored).

Exactly one bundle is "active" per repo, selected by
`ethos team activate <name>`. Existing repos with a legacy
`.punt-labs/ethos/` submodule keep working with no changes.

## Design

### Directory layout

```text
~/.punt-labs/ethos/                     # Global user store (unchanged)
│  identities/, personalities/, ...
└─ bundles/                             # NEW: global bundles
   └─ gstack/                           # Seeded from embedded sidecar
      ├─ bundle.yaml
      ├─ archetypes/
      ├─ pipelines/
      ├─ personalities/
      ├─ writing-styles/
      ├─ roles/
      └─ teams/

<repo>/
├─ .punt-labs/ethos.yaml                # Repo config (adds active_bundle)
├─ .punt-labs/ethos/                    # Repo-local layer (unchanged)
│  └─ identities/, teams/, ...          #   Legacy submodule lives here
└─ .punt-labs/ethos-bundles/            # NEW: repo-local bundles
   ├─ punt-labs/                        #   Submodule of punt-labs/team
   └─ acme-corp/                        #   Private submodule
```

### Three-layer resolution

Today's two-layer chain (repo → global) becomes three:

```text
repo-local  →  active bundle  →  global
  (writable)     (read-only)     (writable)
```

Precedence rules:

- **Read** (`Load`, `List`, `Exists`): repo wins, then bundle, then
  global. First hit returns.
- **Write** (`Save`, `Delete`): always target the global layer.
  Bundles are read-only by contract — you cannot mutate shared
  content from a consuming repo.
- **No active bundle**: the middle layer is skipped. Behavior is
  identical to today.

This mirrors the existing pattern in
[`internal/identity/layered.go`](../internal/identity/layered.go);
the bundle constructor (`NewLayeredStoreWithBundle`) is additive.

### Bundle schema

Every bundle has a `bundle.yaml` manifest at its root:

```yaml
name: gstack
version: 1
description: "gstack starter team — Boil the Lake pipelines"
ethos_min_version: "3.6.0"   # optional
```

Content lives in the standard subdirectories — all optional:

```text
bundle.yaml
archetypes/*.yaml
pipelines/*.yaml
personalities/*.md
writing-styles/*.md
talents/*.md
roles/*.yaml
teams/*.yaml
identities/*.yaml
```

A bundle is just a directory. Any filesystem path that holds a
valid `bundle.yaml` is a bundle. Git submodule, symlink, plain
directory — ethos does not care.

### Activation mechanism

`.punt-labs/ethos.yaml` gains an `active_bundle` field:

```yaml
agent: claude
active_bundle: gstack
```

`ethos team activate <name>` writes this field. `ethos team
deactivate` removes it. The field is the single source of truth —
no symlinks, no environment variables, no hidden state.

The YAML writer preserves comments and key ordering via
`yaml.Node`; unrelated fields are untouched.

### CLI surface

```text
ethos team available                            List all discoverable bundles
ethos team activate <name>                      Set active_bundle in repo config
ethos team active                               Show current active bundle
ethos team deactivate                           Remove activation
ethos team add-bundle <git-url> [--name <x>]    Submodule add to ethos-bundles/
ethos team migrate                              Convert legacy submodule to bundle
```

Example session:

```text
$ ethos team available
NAME       SOURCE    VERSION  DESCRIPTION
gstack     global    1        gstack starter team
punt-labs  repo      1        Punt Labs engineering team
acme-corp  repo      1        Acme Corp private team

$ ethos team activate punt-labs
active_bundle: punt-labs

$ ethos team active
punt-labs (repo: .punt-labs/ethos-bundles/punt-labs)

$ ethos show bwk
# resolves from .punt-labs/ethos/ first, then punt-labs bundle,
# then ~/.punt-labs/ethos/
```

### Backward compatibility

The legacy submodule pattern (`.punt-labs/ethos/` as a git
submodule of `punt-labs/team`) keeps working unchanged:

- If `.punt-labs/ethos/` exists as a directory and `active_bundle`
  is unset, the resolver treats it as the repo-local layer — the
  current behavior.
- Nothing breaks. Nothing requires migration.

Migration is opt-in via `ethos team migrate` (PR 6). Users who
never run it are unaffected.

## Rejected alternatives

- **Symlink-based activation** (`active -> bundles/gstack`): not
  portable to Windows; breaks `go:embed` tests; no audit trail
  in git diffs; two sources of truth if the symlink drifts from
  config.
- **Replace the repo layer with the bundle**: breaks every
  existing ethos consumer. The repo layer's role — project-
  specific overrides — is orthogonal to the bundle's role —
  shared content.
- **Network bundle registry** (like `npm`): premature. No adoption
  signal yet. File-based bundles with git-submodule distribution
  cover every known use case today.
- **Per-command `--bundle <name>` flag**: requires threading the
  flag through every code path. Activation is persistent state;
  flags are transient. Mixing them creates surprise.

## Migration story

### Profile A — Punt Labs employee with existing checkout

Before:

```text
.punt-labs/ethos/   (submodule of punt-labs/team)
```

After `ethos team migrate --apply`:

```text
.punt-labs/ethos-bundles/punt-labs/   (submodule of punt-labs/team)
.punt-labs/ethos.yaml                 (active_bundle: punt-labs)
```

Zero content loss. `ethos show <anyone>` resolves identically.

### Profile B — New user outside Punt Labs

```bash
ethos seed                     # Deploys embedded gstack to global
ethos team activate gstack     # Writes active_bundle: gstack
```

No submodule required. Gstack content is available from global.

### Profile C — Private team

```bash
ethos team add-bundle git@github.com:acme/team.git --name acme-corp
ethos team activate acme-corp
```

Team content lives in the private repo. gstack remains available
from global for pipelines and archetypes.

## PR sequence

Six PRs, each independently reviewable and shippable:

| PR | Scope | Depends on |
|----|-------|-----------|
| 1 | Design doc + DES-051 (this PR) | — |
| 2 | Bundle resolver + config plumbing | 1 |
| 3 | Three-layer layered stores | 2 |
| 4 | CLI commands (`available`/`activate`/etc.) | 3 |
| 5 | Embedded gstack bundle + seed deploy | 4 |
| 6 | Migration command + user docs | 5 |

See the approved plan attached to the epic bead (ethos-2hh) for
per-PR file lists and test matrices.

## Risks and open questions

- **YAML round-trip fidelity.** `active_bundle` must be written
  without disturbing comments or key order. Mitigation: use
  `yaml.Node` for in-place edits; fall back to whole-file rewrite
  only if the parse tree cannot be reconstructed.
- **Windows path handling.** `filepath.Join` everywhere; CI must
  exercise a Windows runner for the bundle package.
- **`add-bundle` invasiveness.** Running `git submodule add` from
  ethos mutates the consuming repo's git state. Dry-run is the
  default; `--apply` opts in.
- **gstack content removal in `punt-labs/team`.** Coordinate with
  the paired PR: keep gstack content in the submodule for one
  release with a deprecation warning, remove after PR 6 ships.
- **Bundle name collisions.** Repo-local bundles shadow global
  bundles with the same name. `ethos team available` displays
  source explicitly to prevent confusion.
- **Downstream consumers.** Biff, Vox, and Beadle read
  `~/.punt-labs/ethos/identities/` directly. Bundles live in
  `~/.punt-labs/ethos/bundles/<name>/identities/` — a new path.
  Audit consumers in PR 3; file follow-up beads if any hard-code
  the old path.
- **Performance.** Three directory listings per `List` call
  instead of two. Expected sub-millisecond. Benchmark in PR 3.

## Test strategy

Test pyramid:

- **Unit** — bundle manifest parse, discovery, name resolution,
  legacy-dir compat. New package `internal/bundle/`.
- **Layered-store integration** — for each of identity, team,
  role, attribute: table-driven precedence tests covering (repo
  only, bundle only, global only, repo+bundle, bundle+global,
  all three). **Invariant**: every existing 2-layer test passes
  when `bundleRoot == ""`.
- **CLI** — `cmd/ethos/team_bundle_test.go` covers `activate`,
  `deactivate`, `available`, `active` with fixtures.
- **End-to-end subprocess** — uses the pattern in
  [`cmd/ethos/subprocess_test.go`](../cmd/ethos/subprocess_test.go):
  seed gstack → activate → `ethos show <identity>` → assert
  resolution succeeds from the bundle layer.
- **Migration** — test fixture repo with a mock legacy submodule;
  `migrate --apply` produces the expected new layout.

Patterns to reuse (do not reimplement):

- Layered store structure:
  [`internal/identity/layered.go`](../internal/identity/layered.go),
  [`internal/team/layered.go`](../internal/team/layered.go).
- Seed deployment:
  [`internal/seed/seed.go`](../internal/seed/seed.go) — extend
  `Seed` to also walk `sidecar/bundles/**`.
- Repo config I/O:
  [`internal/resolve/resolve.go`](../internal/resolve/resolve.go)
  `LoadRepoConfig` at line 139; `FindRepoEthosRoot` at line 124.
- Subprocess test harness:
  [`cmd/ethos/subprocess_test.go`](../cmd/ethos/subprocess_test.go).
