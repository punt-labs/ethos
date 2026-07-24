# Setup/seed consistency design (ethos-5zwn, m-2026-07-24-003)

Status: proposed. Fixes findings F1/F2/F3/F7 from the fresh-machine
bundle audit (`.tmp/investigations/bundle-consistency.md`). Companion
beads — validation gate (ethos-hnxz) and content fixes (ethos-bljl) —
are noted as siblings, not absorbed here.

## Problem

On a fresh machine, the bundles onboarding path (`ethos seed` then
`ethos setup --bundle …`) produces identities whose attributes resolve
to nothing. The two subsystems disagree about what lives in the global
layer:

- **F1** — `setup` writes a `claude` agent and a human identity that
  reference `personality: principal-engineer`, `writing_style:
  concise-quantified`, and `talent: engineering`. `cmd/ethos/setup.go`
  hardcodes these at `:146`, `:157`, `:168-169` and deliberately bypasses
  referential validation (`saveIdentityNoRefs`, `:410-435`, comment:
  "principal-engineer is a convention, not a seeded file").
- **F2** — `ethos seed` deploys none of those slugs to the global layer.
  `internal/seed/embed.go:23` embeds only the *README* of the
  personalities and writing-styles dirs; `Seed()` never deploys either
  kind. The three referenced slugs exist nowhere a fresh machine can see
  them.
- **F3** — even `engineering`, present in both bundles' `talents/`,
  dangles for `claude`, because attribute resolution for a
  globally-stored identity consults the global layer only.
  `internal/identity/layered.go:216-219` — `attrChain`'s default branch
  appends `ls.global` alone, violating the documented DES-051 chain
  (repo → active bundle → global).
- **F7** — `setup` generates `.claude/agents/<handle>.md` only for bundle
  team members; the default agent and the human identity get none.

It works on existing machines only because the live repo carries
`principal-engineer.md`, `concise-quantified.md`, and `engineering.md` in
its `.punt-labs/ethos/` submodule (which resolves as the repo layer). The
bundles path has no such layer.

Acceptance criterion (from ethos-5zwn): under a fresh `HOME`, `ethos
seed` + `ethos setup --bundle foundation` and `--bundle gstack` must
yield **zero warnings** from `ethos show` on every identity, and `ethos
doctor` must exit clean.

## Leader rulings (recorded for morning review)

The operator delegated overnight authority; these are decided. Two carry
a worker flag where the ruling meets a code invariant — see
[Flags for morning review](#flags-for-morning-review).

- **R1** — resolve the F1/F3 contradiction as **both**: (a) `attrChain`
  honors DES-051 uniformly (repo → active bundle → global) for all
  identities including globally-stored ones — F3 is a conformance bug,
  not a design choice; and (b) `ethos seed` ships the conventional
  attributes `setup` references, so a fresh machine resolves even with no
  bundle active. Belt and suspenders.
- **R2** — remove `setup`'s ref-integrity bypass once R1 makes the refs
  resolve; `setup` validates what it writes, hard.
- **R3** — `setup` generates the agent file for the default agent + human
  at setup time, using the same generator SessionStart uses.
- **R4** — scope guard: no redesign of the bundle system. This is a
  conformance + seed-content fix. The validation gate (ethos-hnxz) and
  content fixes (ethos-bljl) are separate beads. Exception: if embedding
  the sidecars (R1b) sweeps some ethos-bljl dead files in passing, that
  portion is in-scope.

## Design

Four coordinated parts, shipped together. Parts A and B are independent;
C depends on both (its hard validation only passes once refs resolve); D
is separable.

### Part A — attrChain conformance (F3, R1a)

`internal/identity/layered.go` resolves attribute *content* (personality,
writing_style, talents) for an identity through a chain chosen by the
identity's **source layer**:

```text
repo    → repo, bundle, global
bundle  →       bundle, global
global  →               global   ← the bug
```

The `global` case drops both higher layers, so a globally-stored identity
never sees the active bundle. This contradicts DESIGN.md's DES-051
promise (`DESIGN.md:5379-5382`) that attributes resolve repo → active
bundle → global regardless of where the identity record lives.

**Change.** Collapse the source switch to a single chain built from
whichever layers are present, for every source:

```text
chain = [repo?, bundle?, global]   // skip nil layers; global always last
```

This is `attrChain`'s current `repo` case, applied uniformly. Effects:

- **global-sourced identities** (`claude`, human) now resolve
  personality/writing_style/talents through the active bundle then
  global — the F3 fix.
- **bundle-sourced identities** now also see the repo layer (previously
  bundle → global). This is DES-051-correct: repo has highest precedence,
  so a repo-local override of an attribute slug should win. In practice
  no fixture defines a repo attribute that shadows a bundle one, so
  resolved content is unchanged; the behavior is only newly *available*.
- **ext resolution is untouched** — extensions always resolve from global
  (`layered.go:14-15`), a separate rule this design does not alter.
- **roles are untouched** — identity roles are not resolved in
  `resolveAttributesLayered`; role content already resolves three-layer
  via `role.NewLayeredStoreWithBundle` (`cmd/ethos/role.go:25`). The only
  bundle-blind path is the identity-internal attribute chain, and it
  covers exactly three types: personality, writing_style, talents.

The three attribute types above are the complete set that was
bundle-blind. Everything else (identity lookup, attribute store, role,
team) already threads the bundle layer via
`NewLayeredStoreWithBundle`.

**Blast radius — decided behavior, not a defect.** The fix changes *what
content a global identity resolves*, not just whether it resolves. A
global `claude` with `talent: engineering` now reads foundation's
`engineering.md` text in a foundation repo, gstack's in a gstack repo,
and the global-seed copy (Part B) only when no bundle is active. The
three `engineering.md` copies differ substantively today
(foundation: "general engineering discipline"; gstack: "gstack builder
framework"; the submodule/seed copy: "systems design in Go and Python"),
so a global identity's persona content is now **bundle-dependent**. This
is precisely the property F3 flagged as questionable ("a global identity
shouldn't shift persona with whatever bundle a repo activates"), and R1a
ratifies overriding it — so it is decided, not accidental. A consequence:
**Part B's global-seed copies are shadowed whenever an active bundle
ships the same slug**, so the global seed is load-bearing only on the
no-bundle path. A future reader must not mistake the global seed for the
authoritative content. Today's affected set is the `engineering` talent
on `claude`/human under both shipped bundles.

### Part B — seed the conventional attributes (F1/F2, R1b)

`ethos seed` must deploy to the global layer the attributes `setup`
references, so resolution succeeds with no bundle active.

**Content source.** The three slugs are not net-new: `principal-engineer.md`,
`concise-quantified.md`, and `engineering.md` already exist in the team
registry (`.punt-labs/ethos/` in the live repo). Vendoring copies into
`internal/seed/sidecar/` makes R1b a file-move, not authoring:

- `internal/seed/sidecar/personalities/principal-engineer.md`
- `internal/seed/sidecar/writing-styles/concise-quantified.md`
- `internal/seed/sidecar/talents/engineering.md`

**Two copies, no auto-sync.** After vendoring, the same slug exists in
both `internal/seed/sidecar/` and the team submodule. They serve
different masters: the embedded sidecar is authoritative for `ethos seed`
(what a fresh machine gets), the submodule is authoritative for the team
registry (what a repo's identities resolve against). Nothing keeps them
in sync — the sidecar copy is a point-in-time snapshot that can drift
from the submodule silently. This is the same latent risk as the
per-bundle content divergence in [Part A](#part-a--attrchain-conformance-f3-r1a):
acceptable under R4, and the divergence that matters is caught by the
sibling validation gate (ethos-hnxz) and content-consistency work
(ethos-bljl), not by this design.

**Embed manifest** (`internal/seed/embed.go`). Add content globs for the
two kinds that today embed only their README:

```go
//go:embed sidecar/personalities/*.md
var Personalities embed.FS

//go:embed sidecar/writing-styles/*.md
var WritingStyles embed.FS
```

`engineering.md` needs no manifest change — it lands under the existing
`sidecar/talents/*.md` glob.

**Deploy** (`internal/seed/seed.go`, `Seed()`). Add two `seedFS` calls
alongside the existing roles/talents deployment:

```go
seedFS(Personalities, "sidecar/personalities", filepath.Join(destRoot, "personalities"), ".md", force, r)
seedFS(WritingStyles, "sidecar/writing-styles", filepath.Join(destRoot, "writing-styles"), ".md", force, r)
```

`seedFS` already skips `README.md` (`seed.go:69-71`), so READMEs continue
to deploy only via `seedReadmes`.

**Sweep of dead sidecars (R4 exception).** Adding the two globs also
deploys the existing but never-shipped sidecar files — personalities
`sprint-architect`, `sprint-implementer`, `sprint-qa`, `sprint-reviewer`,
`sprint-security`, `product-thinker`, and writing-styles
`architect-prose`, `implementer-prose`, `product-prose`, `qa-prose`,
`reviewer-prose`, `security-prose`. These become live starter content
instead of dead source. This resolves the dead-embedded-file portion of
ethos-bljl in passing; the remaining ethos-bljl items (F4 gstack edge,
F6 bundle self-containment) stay out of scope.

### Part C — setup write path and hard validation (F1, R2)

**Write layer stays global.** `setup` writes the human and `claude`
identities to the global store (`cmd/ethos/setup.go:129`,
`identity.NewStore(globalRoot)`). R1 makes this correct: a globally-stored
identity now resolves attributes from the active bundle (Part A) or the
global seed (Part B). No change to the write layer — stated explicitly
because the alternative (write to the repo layer) is a real option that
this design rejects (see [Rejected alternatives](#rejected-alternatives)).

**Remove the bypass.** Delete `saveIdentityNoRefs`
(`cmd/ethos/setup.go:410-435`) and its two call sites (`:148`, `:171`);
use `identity.Store.Save`, which already calls `ValidateRefs`
(`store.go:331-332`). After Part B, `ValidateRefs` against the global root
finds all three slugs and passes.

**Validation is against the global layer only.** `Save` runs
`ValidateRefs` on the global store, not the full repo → bundle → global
chain. This is deliberate and correct: a global identity must be
self-sufficient at the layer it lives in — resolvable in any repo
regardless of which bundle is active. It also changes the interactive
wizard's typo path: a user who types a `writing_style` slug the wizard
does not recognize (`resolveStyleChoice` accepts an arbitrary typed slug,
`setup.go:341-343`) now gets the actionable hard failure above instead of
a silently-written dangling ref. That is R2-correct — fail visibly.

**Ordering contract.** `Save` now fails hard if the referenced attributes
are absent — i.e. if `setup` runs before `seed`. The normal install path
seeds first (`install.sh:241`, before per-repo enablement), so this only
bites a manual `ethos setup` on an unseeded machine. `setup` surfaces an
actionable error naming the missing attribute:

```text
ethos: setup: identity "claude" references personality "principal-engineer",
which is not installed; run "ethos seed" first
```

This is R2's intent: fail visibly rather than write a dangling identity.
`setup`'s long help already tells fresh installs to run `seed` first
(`setup.go:32-33`); the error makes that a hard contract.

### Part D — agent files at setup (F7, R3)

`setup` already invokes `hook.GenerateAgentFiles` — the same function the
SessionStart hook runs (`cmd/ethos/setup.go:281`) — so every bundle team
agent gets its `.claude/agents/<handle>.md` at setup time. Verified in the
audit: `foundation-*` / `gstack-*` files are written during `setup`, so
sub-agents already work before the first Claude Code restart. Part D keeps
this and does not duplicate the generator.

R3 additionally asks for agent files for the **default agent** and the
**human**. That request meets two generator invariants — see the flag
below. The design's chosen behavior:

- **Default agent (`claude`)**: no sub-agent file. `GenerateAgentFiles`
  skips the main agent by design (`generate_agents.go:80-82`,
  `if m.Identity == mainAgent { continue }`) because the main agent *is*
  the running Claude Code session, not a spawnable sub-agent. Generating
  a `claude.md` sub-agent would misrepresent it. The real F7 concern for
  `claude` — that its persona is empty at SessionStart injection — is
  fixed by Part A (its attributes now resolve).
- **Human**: no agent file. The generator emits only for `kind == "agent"`
  (`generate_agents.go:89-91`); a human is not a spawnable agent.

So Part D is: keep the existing bundle-agent generation (already correct),
and rely on Part A for the persona-injection path. See the morning flag
for the alternative if the operator wants a distinct main-agent file.

## Migration

Existing machines that already show warnings converge without manual
repair:

1. Update the binary (ships Part A).
2. Re-run `ethos seed`. It is no-clobber (`seed.go`, `writeFile` skips
   existing files unless `--force`), and the three new slugs are absent
   on affected machines, so they deploy on the first re-run — no `--force`
   needed. The first re-seed also lands the ~13 previously-dead sidecars
   (`sprint-*`, `product-thinker`, `*-prose`; see Part B) into the global
   `personalities/` and `writing-styles/` dirs. This is benign — additive
   starter content, no-clobber — but it is a visible delta an operator
   will see on the machine.
3. `ethos show` is now clean; identities need no rewrite because their
   refs were always correct, merely unresolvable.

Machines using the legacy submodule layout (`.punt-labs/ethos/` as a
directory, like the live repo) are unaffected: they already resolve the
three slugs via the repo layer, and Part A's chain is a superset of the
old repo-source chain, so resolved content is byte-identical.

`setup` does not need re-running unless the identities themselves are
missing; if so, run `seed` then `setup`.

## Test strategy

Fresh-`HOME` table tests, the same shape as the audit's manual
reproduction:

1. **Resolution table test** — for each of `{foundation, gstack,
   no-bundle (--solo)}`: set `HOME` to a temp dir, run `seed`, run
   `setup --file …`, then `Load` every identity and assert
   `id.Warnings` is empty. Cover `claude`, the human, and every bundle
   agent. The no-bundle row proves R1b's belt: `claude`/human resolve
   `principal-engineer` / `concise-quantified` / `engineering` from the
   **global seed** with no bundle active.
2. **attrChain unit test** — construct a `LayeredStore` with a
   global-sourced identity referencing a talent present only in the
   bundle layer; assert it resolves. This pins the F3 fix independent of
   `setup`.
3. **Hard-validation test** — run `setup` with a fresh `HOME` and **no**
   prior `seed`; assert it fails with the missing-attribute error naming
   the slug (R2). Then `seed` and re-run; assert success.
4. **doctor test** — configure a git user/email in the test env, run
   `doctor`, assert exit 0. Note: the audit's sandbox showed "Human
   identity FAIL — no match" purely because the scratch env had no git
   config; that is a harness artifact, not a product failure. The test
   must set git identity so the human-match check passes.

`make check` (go vet, staticcheck, `go test -race`, validate-content) must
stay green.

## Rejected alternatives

- **setup writes identities to the repo layer** (`.punt-labs/ethos/
  identities/`) instead of global. Rejected: `claude` and the human are
  user-scoped identities shared across every repo the user works in;
  repo-layer storage duplicates them per-repo and breaks the
  single-identity model. DES-051 also specifies writes target global by
  contract (`DESIGN.md:4262-4263`).
- **Bundle-only resolution (R1a alone, drop R1b)** — rely on the
  attrChain fix and skip seeding global attributes. Rejected: fails the
  no-bundle acceptance row. `ethos setup --solo` and any repo with no
  active bundle would leave `claude`/human unresolved. R1's
  belt-and-suspenders is required.
- **Narrow attrChain fix (patch only the global branch)** — considered.
  Fixes F3 exactly but leaves the source-discriminated asymmetry class in
  place (bundle-source still skips repo). Rejected in favor of the uniform
  single chain, which is the literal reading of R1a ("uniformly … for all
  identities") and removes the whole class.
- **Generate a `claude.md` via the sub-agent generator** (a literal
  reading of R3). Rejected on invariant grounds — see the flag below.

## Flags for morning review

Two points where a ruling meets a code invariant. Both are recorded here
per the "design around them, record for review" instruction; neither
blocks Parts A–C.

1. **R1b wording vs. reality.** R1b says "embed the dead sidecar files
   (principal-engineer, concise-quantified, …)." Those two slugs do **not**
   exist as sidecar files today — they are conventions referenced only in
   README/skill prose (F2). The *dead* sidecar files that do exist are a
   different set (`sprint-*`, `*-prose`, `product-thinker`). Part B
   reconciles this by vendoring the three referenced files from the team
   submodule (where they do exist) into the sidecar, then embedding them —
   so R1b is executable as a file-move. No decision needed; recorded so
   the wording is not mistaken for an existing-file inventory.

2. **R3 vs. generator invariants (decision recommended).** R3 asks
   `setup` to generate agent files for the default agent and the human.
   The generator deliberately (a) skips the main agent
   (`generate_agents.go:80-82`) because it is the session, not a
   sub-agent, and (b) emits only for `kind == "agent"`
   (`:89-91`), so a human is ineligible. Generating either would either
   misrepresent `claude` as a spawnable sub-agent or produce a nonsensical
   human agent file. F7 is also not part of the acceptance criterion
   (which is satisfied by Parts A–B). **Recommendation:** ratify Part D as
   written — keep bundle-agent generation, generate nothing for
   `claude`/human, and treat the F7 "empty persona" concern as fixed by
   Part A's resolution path. If a main-agent descriptor is genuinely
   wanted, it needs a distinct "main agent" template separate from the
   sub-agent generator — that is new design, outside R4's conformance
   scope, and should be its own bead.

## Sibling beads (noted, not absorbed)

- **ethos-hnxz** — bundle validation gate. `validate-content` is
  two-layer and bundle-blind (`cmd/validate-content/main.go:72`);
  `bundle.Validate` is structural-only by its own admission
  (`internal/bundle/validate.go:16-29`). Adding a bundle pass to
  `make check` is what would have caught F1–F4. Out of scope here.
- **ethos-bljl** — content fixes: F4 (gstack's `collaborates_with`
  product-lead→architect edge that warns on every setup/SessionStart) and
  F6 (bundles silently depend on global `code-review` / `security`
  talents). The dead-embedded-file portion of ethos-bljl is swept in
  passing by Part B (R4 exception); these two content items are not.
