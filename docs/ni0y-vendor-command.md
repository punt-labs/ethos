# `ethos vendor` — complete identity snapshot (ethos-ni0y)

Design doc. Design only — no code. Pairs with a parallel
repo-authoritative resolution-mode design (bwk). This half produces the
complete repo copy that the other half treats as sufficient.

## 1. Problem

A repo can carry its own `.punt-labs/ethos/` directory as the highest-
precedence resolution layer (`cmd/ethos/identity.go:30-43`,
`resolve.FindRepoEthosRoot`). Today that copy is a git submodule or a
hand-assembled directory. Nothing produces a **complete, self-standing**
copy. Two gaps make every hand copy silently lossy:

- **`ethos export` is lossy by contract.** `export.go:20`: "Both exports
  are lossy: structural data (roles, teams, extensions) drops because the
  target format cannot represent it." It targets foreign formats
  (soulspec, claude-md), operates on **one** handle, and walks no
  references. `.ext/` dirs, roles, teams, and (for soulspec) even talent
  content are dropped.
- **Transitive dependencies are missed.** An identity references a
  personality, a writing style, and talents (`identity.go:16-18`). A team
  references member identities and roles (`team.go:16-19`). The identity
  an agent works alongside — e.g. `claudia`, global-only, used for prose
  curation — is reachable only through team membership. Copy `claude`
  naively and `claudia` is left behind. Today global fallback
  (`layered.go:93`) still satisfies her. Remove that fallback (bwk's
  mode) and the vendored repo breaks.

There is no command that walks the reference graph to a fixed point and
snapshots every file the closure needs. `ethos vendor` is that command.

## 2. Command surface

**Recommendation: top-level `ethos vendor`.** Not `ethos team vendor`.

```text
ethos vendor [flags] [handle...]

Snapshot a complete, self-standing identity set into a repo's
.punt-labs/ethos/ directory. Walks every transitive reference —
personalities, writing styles, talents, roles, teams, member
identities, and .ext/ dirs — so repo-local resolution needs no
global fallback.

Usage:
  ethos vendor <handle>...            # vendor named identities + closure
  ethos vendor --team <name>          # vendor a whole team + closure
  ethos vendor --all                  # vendor every resolvable identity

Flags:
  --team <name>      Vendor a team and all its members (repeatable)
  --all              Vendor every identity resolvable in any layer
  --to <dir>         Target directory (default .punt-labs/ethos)
  --from <layer>     Source: resolved|global|bundle (default resolved)
  --with-teams       Follow identity→team membership edges (default true)
  --prune            Remove previously-vendored files no longer in closure
  -n, --no-clobber   Never overwrite an existing target file
  --dry-run          Print the plan, write nothing
  --json             Machine-readable output

Examples:
  ethos vendor claude
    wrote 6 identities, 3 personalities, 2 writing-styles, 5 talents,
    4 roles, 1 team, 8 ext files → .punt-labs/ethos (complete)

  ethos vendor --team engineering --dry-run
    plan: 9 identities, 4 roles, 1 team (complete); 0 files written

  ethos vendor --all --prune
    wrote 12 identities … pruned 1 stale file → .punt-labs/ethos (complete)
```

**Why top-level, not under `team`.** `export` (lossy, foreign-format,
single identity) and `vendor` (lossless, native-format, transitive
closure) are the two export verbs; siting them as peers aids discovery.
Vendoring is a storage-snapshot concern, orthogonal to the membership
CRUD that `ethos team` owns (`team.go:44-114`). A team is one possible
*entry point* to the closure, not the operation's subject — you can
vendor bare handles or `--all` with no team at all. One command, one job.

**Why the flags.**

- `[handle...]`, `--team`, `--all` are three seeds for the same walk;
  at least one is required (else exit 2, usage error).
- `--to` defaults to `.punt-labs/ethos` — the repo layer
  `FindRepoEthosRoot` reads (`resolve.go:138`). Override for staging.
- `--from resolved` (default) reads the layered view (repo → bundle →
  global) so overrides are captured correctly. `--from global` / `--from
  bundle` snapshot a single layer. **Caveat, documented in help:** only
  `resolved` guarantees completeness; a single-layer source may still
  reference content that lives elsewhere, breaking the invariant in §9.
- `--with-teams` on by default is what pulls `claudia` in. `--no-teams`
  restricts to the literal seed identities (narrower, and no longer
  guaranteed complete for agents that rely on teammates).
- `--dry-run` mirrors `team migrate`'s plan-first affordance
  (`team_bundle.go:517`).

## 3. Transitive-closure algorithm

The reference graph has these edges:

| From | To | Source |
|------|----|--------|
| identity | personality, writing_style, talents[] | `identity.go:16-18` |
| identity | `.ext/` dir (all namespace files) | `ext.go:28`, DES-008 |
| team | member identities[], member roles[] | `team.go:16-19` |
| identity | teams containing it (reverse edge) | scan `teams.List()` |

Roles carry no outward identity/attribute edges (`role.go:28-36`); they
are closure leaves. Collaborations reference only roles already filled by
members (`team.go:110-114`), so they add no new nodes. The reverse
identity→team edge is the one that reaches `claudia`.

```text
seed  ← handles from args, --team expansion, or --all
IDS   ← seed              # worklist of identities
PERS, WS, TAL, ROLES, TEAMS, EXT ← ∅

repeat until no set grows:
    for each new h in IDS:
        id ← L.Load(h)                     # resolved refs → slugs
        PERS += id.personality
        WS   += id.writing_style
        TAL  += id.talents
        EXT  += h                          # copy whole <h>.ext/ tree
    if with_teams:
        for each t in L.teams.List():
            if t has a member m with m.identity ∈ IDS and t ∉ TEAMS:
                TEAMS += t
                for each member m of t:
                    IDS   += m.identity     # pulls claudia
                    ROLES += m.role

materialize: copy the winning source file for every node into --to
verify:      §9 completeness predicate; fail if any gap remains
```

**Termination.** The node universe (all identities, attributes, roles,
teams across all layers) is finite; every step only adds; growth is
monotone. Fixed point is reached in at most one pass per identity.

**Materialization** copies bytes of the *winning* file per resolution
precedence (repo → bundle → global). Target layout mirrors the source:

```text
.punt-labs/ethos/
  identities/<h>.yaml
  identities/<h>.ext/<ns>.yaml      # verbatim, per §4
  personalities/<slug>.md
  writing-styles/<slug>.md
  talents/<slug>.md
  roles/<name>.yaml
  teams/<name>.yaml
```

## 4. `.ext/` handling

`ethos export` drops `.ext/` entirely — it has no field for it and the
target formats cannot hold it. Vendor copies `identities/<h>.ext/` as a
**verbatim byte-for-byte tree**: every `<namespace>.yaml`
(`ext.go:36-38`), no interpretation, no re-serialization. Ethos does not
know what the keys mean (DES-008) and must not touch them.

**Source subtlety.** Extensions resolve exclusively from the **global**
layer today (`layered.go:54-55`, `ExtDir` → global at `layered.go:479-
482`). Vendor therefore reads each `<h>.ext/` from the global store even
when the identity YAML itself won at the repo or bundle layer. See §9 —
bwk's repo-authoritative mode must then teach resolution to *read* ext
from the repo layer, or the vendored ext is snapshotted but never
consulted.

**Security note (open question, §10).** Global `.ext/` is untracked
today. Vendoring it into `.punt-labs/ethos/` makes arbitrary tool-owned
key-values git-tracked. Namespaces are free-form; a consumer may have
written a secret. Flag for djb/operator: possibly `--no-ext`, or a
publish-time redaction policy, before this ships.

## 5. Idempotency and overwrite semantics

Vendor is a **sync**, not an append. Re-running against the same seed is
idempotent.

- **Default (sync).** Overwrite each managed file with current resolved
  bytes, so a re-vendor tracks upstream edits. When `--to` *is* the repo
  layer, repo-layer files resolve to themselves (repo wins), so their
  bytes are unchanged; only missing bundle/global content is filled in.
  Reading-then-writing the same layer is a fixed point.
- **`--no-clobber`.** `cp -n` semantics: never overwrite an existing
  target file. Use when the repo layer holds hand-authored overrides you
  do not want re-synced.
- **Stale files.** Vendor writes a manifest at
  `.punt-labs/ethos/.vendor.yaml` listing every path it created. `--prune`
  removes manifest-listed files absent from the new closure. Vendor
  **never** deletes a file it did not record — hand-authored repo-layer
  content is safe. Without `--prune`, stale files are left and reported as
  a warning.

## 6. `vendor` vs `export`

Keep both. They do different jobs; neither is deprecated.

| | `ethos export` | `ethos vendor` |
|--|----------------|----------------|
| Purpose | Hand identity to a **non-ethos** tool | Make a repo **self-standing under ethos** |
| Format | soulspec (`SOUL/IDENTITY/STYLE.md`), claude-md | native ethos layout |
| Scope | one handle | transitive closure |
| Fidelity | lossy by contract (`export.go:20`) | lossless; completeness enforced (§9) |
| `.ext/`, roles, teams | dropped | included |
| Output | foreign files / stdout | `.punt-labs/ethos/` tree |

`export`'s job is representation change for an external consumer;
`vendor`'s is closure capture for internal resolution. Overloading
`export --to ethos` would fold a graph walk into a single-identity
format converter — rejected in §8.

## 7. MCP tool + DES-020 formatter

DES-020 requires every MCP tool to return formatted text through a
`format_output.go` formatter. Add a `vendor` tool
(`internal/mcp/vendor_tools.go`) alongside the others registered in
`tools.go`:

```text
tool "vendor"
  method   enum(plan, apply)  default plan      # plan == dry-run
  handles  array<string>
  team     array<string>
  all      bool               default false
  target   string             default ".punt-labs/ethos"
  with_teams bool             default true
  prune    bool               default false
  no_clobber bool             default false
```

Handler returns a `VendorResult` (below). Register in
`format_output.go`'s switch (`format_output.go:61-80`):

```go
case "vendor":
    return formatVendor(w, method, result)
```

`formatVendor` emits (DES-020 two-channel): **panel** — a one-line count
summary and completeness verdict; **context** — a category/count table
plus any completeness gaps and pruned paths. Mirror `formatTeam`'s shape.

`VendorResult` (the `--json` / `structuredContent` payload):

```go
type VendorResult struct {
    Target        string   `json:"target"`
    Identities    []string `json:"identities"`
    Personalities []string `json:"personalities"`
    WritingStyles []string `json:"writing_styles"`
    Talents       []string `json:"talents"`
    Roles         []string `json:"roles"`
    Teams         []string `json:"teams"`
    ExtFiles      []string `json:"ext_files"`
    Pruned        []string `json:"pruned,omitempty"`
    Complete      bool     `json:"complete"`
    Gaps          []string `json:"gaps,omitempty"`
    DryRun        bool     `json:"dry_run"`
}
```

## 8. Proposed ADR (DESIGN.md house style)

> ## DES-0NN: `ethos vendor` — complete identity snapshot (PROPOSED)
>
> **Status**: Proposed. Pairs with the repo-authoritative resolution ADR
> (bwk). This ADR defines the producer; that one defines the consumer.
>
> ### Problem
>
> A repo's `.punt-labs/ethos/` is the highest-precedence resolution layer,
> but nothing produces a complete copy of it. `ethos export` is lossy by
> contract (`export.go:20`) — it drops `.ext/` dirs, roles, teams, and
> operates on a single handle with no reference walk. Transitive
> dependencies are missed entirely: an agent's teammates (e.g. `claudia`,
> reachable only through team membership) are left behind, and today only
> global fallback (`layered.go:93`) hides the gap. Remove that fallback and
> the vendored repo silently breaks.
>
> ### Decision
>
> Add a top-level `ethos vendor` command that walks the identity reference
> graph to a fixed point and snapshots every file the closure needs into a
> target directory (default `.punt-labs/ethos/`). The closure covers
> personalities, writing styles, talents, roles, teams, member identities
> (via the reverse identity→team edge), and each identity's `.ext/` tree
> copied verbatim. The command reads the resolved layered view (repo →
> bundle → global) so overrides are captured, then verifies completeness:
> repo-only resolution of the snapshot must produce zero not-found
> warnings. Vendor is a sync — idempotent, manifest-tracked, with `--prune`
> for stale files and `--no-clobber` for hand-authored overrides. It ships
> with an MCP `vendor` tool and a DES-020 `formatVendor` formatter.
>
> ### Reasoning
>
> - Completeness is *enforced*, not assumed: the closure predicate is a
>   testable postcondition, which is exactly what bwk's repo-only mode
>   needs as its precondition.
> - The reverse team edge is the only way to reach teammates like
>   `claudia`; making it the default is what distinguishes vendor from a
>   naive per-handle copy.
> - `.ext/` verbatim copy honors DES-008 — ethos snapshots the bytes
>   without interpreting consumer-owned keys.
> - Native layout (not a foreign format) means the snapshot *is* a
>   resolution layer; no import step, no fidelity loss.
> - Sync-with-manifest keeps re-runs safe: vendor deletes only what it
>   recorded, never hand-authored content.
>
> ### Rejected alternatives
>
> - **`ethos export --to ethos`** — folds a transitive graph walk into a
>   single-identity format converter whose contract is "lossy." Two jobs,
>   one command; muddies both.
> - **Git submodule of the whole team repo (status quo)** — pins content
>   you don't use, still misses global-only identities and `.ext/` dirs,
>   and dirties the tree.
> - **Symlink or point the repo layer at global** — not portable, not
>   self-standing; defeats offline/air-gapped use, the whole point of a
>   vendored snapshot.
> - **Keep global fallback (do nothing)** — the exact behavior bwk's mode
>   removes; a vendored repo must be self-contained.
> - **Vendor only named identities, no team walk** — misses transitive
>   members (`claudia`); reproduces the silent gap this ADR closes.
>
> ### Implications
>
> - Extensions resolve from global only today (`layered.go:54-55`). For
>   vendored `.ext/` to be consulted, bwk's repo-authoritative mode must
>   read ext from the repo layer. Cross-ADR dependency.
> - A vendored `.punt-labs/ethos/` becomes a `SourceLegacy` bundle when no
>   `active_bundle` is set (`bundle/resolve.go:64-74`) and the repo layer
>   regardless (`identity.go:30`). Interaction with an *active* bundle
>   needs a ruling (§10).
> - `.ext/` content becomes git-tracked; a redaction/opt-out policy may be
>   required before shipping (§10).

## 9. Coupling to repo-authoritative mode (the guarantee)

bwk's mode makes repo-only resolution safe **iff** vendor's closure is
complete. Vendor must **guarantee** this postcondition, stated as a
testable predicate:

> A snapshot at directory `D` is **complete** iff a repo-only identity
> store rooted at `D` (no bundle, no global) resolves every identity in
> `D` with zero not-found warnings, and every team in `D` validates with
> `identityExists`/`roleExists` bound to `D` alone.

Concretely, for every `identities/<h>.yaml` in `D`:

- `personality` slug (if set) → `personalities/<slug>.md` ∈ D
- `writing_style` slug (if set) → `writing-styles/<slug>.md` ∈ D
- each `talent` slug → `talents/<slug>.md` ∈ D
- `<h>.ext/` present verbatim
- every team containing `h` ∈ D, and each such team's member identities
  and `roles/<name>.yaml` ∈ D

Vendor **runs this predicate as its final step** and fails (exit 1,
gaps on stderr) if any node is missing — the guarantee is verified, not
promised. `Complete: true` in the result means repo-only resolution is
safe. This is the contract bwk's mode consumes.

## 10. Open questions

1. **Ext read-side (blocking for bwk).** Layered resolution reads ext
   from global only (`layered.go:54-55`, `479-482`). Vendored repo ext
   will be ignored until repo-authoritative mode reads ext from the repo
   layer. Confirm this is in bwk's scope; it must land together.
2. **Active-bundle interaction.** When both a vendored `.punt-labs/ethos/`
   and an `active_bundle` exist, the repo layer wins (`identity.go:35-43`).
   Is a vendored repo expected to also clear `active_bundle`, or coexist?
   Needs an operator ruling.
3. **Ext secrets.** Vendoring global `.ext/` into a git-tracked dir may
   commit consumer-owned secrets. `--no-ext`, redaction, or a warning?
   djb/operator decision before ship.
4. **Repo config.** Vendor snapshots identity *content*, not
   `.punt-labs/ethos.yaml` (agent, team, active_bundle). Should it also
   write `team:`/`agent:`? Proposed: out of scope; config stays hand-owned.
5. **Non-identity content.** Missions, pipelines, archetypes, ADRs are not
   part of the identity closure. Proposed: explicitly out of scope for
   `vendor`.

---

## Report summary

- **Command surface:** top-level `ethos vendor [handle...]` with
  `--team`, `--all`, `--to` (default `.punt-labs/ethos`), `--from`
  (default `resolved`), `--with-teams` (default true), `--prune`,
  `--no-clobber`, `--dry-run`, `--json`. Peer of the lossy `export`.
- **Closure algorithm:** BFS to a fixed point over
  identity→{personality, writing_style, talents, ext} and the reverse
  identity→team edge that pulls in every teammate (the `claudia` case),
  team→{member identities, roles}. Roles/collaborations are leaves.
  Terminates because the node universe is finite and growth is monotone.
- **export vs vendor:** export changes representation for a non-ethos
  consumer and is lossy by contract (`export.go:20`); vendor captures the
  native closure so a repo is self-standing. Both kept.
- **The guarantee:** vendor verifies, as its last step, that repo-only
  resolution of the snapshot yields zero not-found warnings — the exact
  precondition bwk's repo-authoritative mode requires.
- **Open questions:** ext read-side coupling (blocking), active-bundle
  interaction, ext-secret exposure, repo-config scope, non-identity
  content scope.
</content>
</invoke>
