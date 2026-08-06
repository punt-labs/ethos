# Seeded review agents

Design for DES-070. This is design only — no code changes in this round.

## The gap

Ethos already seeds two persona-bound review roles via
`internal/seed/sidecar/roles/reviewer.yaml` and `security-reviewer.yaml`.
Each has `responsibilities`, a `tools:` allowlist, `safety_constraints`
(never `Write`/`Edit`, `Bash` for verification only), and a fixed
`output_format` (a `FINDINGS:` block with verdict/confidence/severity).
`generate_agents.go` combines a role with a personality, writing style,
and talents to synthesize a *named person* — e.g. `reviewer` bound to
whichever identity is assigned it becomes an agent with a name, a
personality, and prose style, who happens to review code.

That pipeline is right for the mission worker/evaluator system, where a
specialist has to have a durable identity: memory, writing style,
biff presence, an ethos handle other tools address. It is wrong for a
different, real need: a **narrow-mandate, structured-output review
checklist** with no persona at all — an agent whose entire value is a
fixed scope, a fixed severity rubric, and nothing else. That shape
already exists in this repo, twice over, from a third party:

- `pr-review-toolkit`'s `code-reviewer.md` (`~/.claude/plugins/marketplaces/claude-plugins-official/plugins/pr-review-toolkit/agents/code-reviewer.md`):
  scoped to `git diff`, confidence-scored 0-100, reports only findings
  ≥80. No name, no personality — `model: opus`, a `description` field
  used for auto-invocation matching, and a body that is pure review
  procedure.
- `pr-review-toolkit`'s `silent-failure-hunter.md`: scoped narrowly to
  error handling, swallowed exceptions, and fallback logic. Same shape:
  `model: inherit`, no identity, a fixed five-part output format.

During the DES-069 PR cycle both of those local-review agents ran
clean while GitHub's Copilot/Bugbot caught three real defects, all
falling into one dimension neither third-party agent covers:
unverified claims about invariants, exhaustiveness, and exclusivity —
a comment or test asserting a property that the code does not actually
enforce. The leader wrote a third agent to close that gap,
`.claude/agents/invariant-completeness-reviewer.md`, repo-local to
ethos only, same shape as the other two: no name, `model: opus`, a
fixed claim → severity → falsifying case → fix output format, explicit
"what is NOT in scope" section pointing at the other two agents by
name so the three stay disjoint.

**Ethos ships zero agents of this shape to any repo it seeds.**
`ethos seed` deploys persona-bound roles (which a leader binds to a
named specialist for delegated implementation work) and separately a
consuming repo's own `.claude/agents/*.md` files are hand-written or,
in ethos's own case, one-off (`invariant-completeness-reviewer.md` was
authored ad hoc, not seeded, and does not exist in any other
`punt-labs` repo). Every repo that wants Copilot-catching local review
either depends on the third-party `pr-review-toolkit` plugin or has
nothing. That is the gap this design closes.

## The four dimensions

Evaluated against the three-agent baseline above plus one candidate
surfaced by prior competitive research (Qodo 2.0 ships a dedicated
test-coverage-gaps agent).

| # | Dimension | Source | Recommendation |
|---|-----------|--------|-----------------|
| 1 | General code-quality / CLAUDE.md compliance | `pr-review-toolkit` code-reviewer | **Include** |
| 2 | Error handling / silent failure / fallback | `pr-review-toolkit` silent-failure-hunter | **Include** |
| 3 | Invariant / exhaustiveness / test-tautology | `invariant-completeness-reviewer` (already written) | **Include as-is** |
| 4 | Test-coverage-gaps (missing test cases for new branches/edge conditions) | Qodo 2.0 dedicated agent | **Defer** |

**1 and 2: include, ported near-verbatim.** They are the two agents
this repo already depends on every review cycle; the dependency is the
whole problem statement the operator raised. Porting them removes the
third-party dependency without inventing new scope. Adjust only what
is ethos-specific: replace the hardcoded project-specific detail in
silent-failure-hunter (`logForDebugging`/`logError`/`logEvent`,
`constants/errorIds.ts` — clearly authored for a different, JS/TS
codebase) with "defer to this repo's CLAUDE.md for logging/error-ID
conventions" so the seeded copy is language-neutral. code-reviewer's
confidence-gate (≥80) and CLAUDE.md-compliance framing need no change.

**3: include, scope unchanged.** The mission's own success criteria
ask whether `invariant-completeness-reviewer`'s scope is right. It is:
the three agents already partition cleanly — each has an explicit "not
in scope, that's the other agent's job" paragraph naming the other two.
Widening it (e.g. to also catch missing test *coverage*, not just
untrue test *claims*) would blur it into dimension 4 and weaken the
"narrow mandate" property that makes these agents cheap to run and
easy to trust. Keep it as one of exactly three claim-verification
agents, not folded into a broader one.

**4: defer, name why.** Test-coverage-gaps ("this new branch has no
test exercising it") is a real, distinct dimension — it did not
overlap with any of 1-3 in the DES-069 postmortem, because none of
the three defects Copilot caught were coverage gaps; they were false
claims about coverage that already existed. Two reasons to defer
rather than build a fourth agent now: (a) no incident has yet shown
ethos's existing gates (`make check` requires `go test -race` to pass,
and PY-* standards enforce coverage ratchets) letting a coverage gap
through undetected — the motivating failure mode for 1-3 was concrete
and recent, for 4 it is hypothetical; (b) a coverage-gap agent
necessarily overlaps with `code-reviewer`'s existing "inadequate test
coverage" bullet under Code Quality, so shipping it now risks the
"seven overlapping agents" failure mode the mission explicitly warns
against. Revisit if a real PR cycle surfaces a coverage gap that (1),
(2), and (3) all missed — that would be the same kind of concrete
trigger that justified building agent 3.

Net: **three agents seeded** (code-reviewer, silent-failure-hunter,
invariant-completeness-reviewer), not four.

## Architecture: a new seed category, not the role pipeline

These three agents are personaless and structured-output. They do not
fit role → personality → writing-style → talent →
`generate_agents.go`, because that pipeline's entire job is producing
a *named person*: a `reviewer` role bound to an identity yields an
agent with a personality and voice reviewing code as that person. A
`code-reviewer` review-checklist agent has no person behind it by
design — third-party precedent (`pr-review-toolkit`) confirms this is
the natural shape: `name`, `description`, `model`, `color`
frontmatter, then a system prompt that is pure procedure. Forcing it
through the role pipeline would mean inventing a fake personality and
writing-style for an agent that explicitly should have neither, and
would tie its output format to whatever identity happens to bind the
role — the opposite of the fixed, comparable output format that makes
review findings machine-triageable.

**Decision: `internal/seed/sidecar/agents/` — bare markdown files
deployed verbatim.** Directory layout:

```text
internal/seed/sidecar/agents/
  code-reviewer.md
  silent-failure-hunter.md
  invariant-completeness-reviewer.md
  README.md
```

Each file is a complete Claude Code subagent definition — YAML
frontmatter (`name`, `description`, `model`, `color`) plus a system
prompt body — deployed to the consuming repo's `.claude/agents/`
exactly as written, the same mechanism `embed.go`/`seed.go` already use
for `roles/`, `talents/`, `personalities/`, and `writing-styles/`
(`seedFS` walks an `embed.FS` subtree and calls `place` per file). No
new deploy mechanism is needed — `seedFS(fsys, "sidecar/agents",
".claude/agents", ".md")` is a one-line addition to whatever call site
in `seed.go` already iterates the sidecar categories, using the
existing per-scope `decide`/`place`/`record` manifest logic (see
below). This is deliberately the *simpler* of the two options named in
the mission: no persona synthesis, no identity binding, no `tools:`
allowlist merge — just files.

**Rejected alternative: shoehorn into role/archetype machinery.**
Would require: (a) a role with `personality: null` / a null-object
personality to satisfy `generate_agents.go`'s synthesis step, (b)
teaching that generator a second output shape (raw pass-through
instead of persona-flavored prose) purely to serve three files that
never wanted persona flavor, (c) inventing a fake identity binding
(`handle: code-reviewer`?) for something that is not an identity —
violating the existing invariant "same schema for humans and agents,
`kind` is the only structural difference" by adding a third kind in
spirit if not in name. Rejected: more moving parts, a special case
threaded through a pipeline whose entire reason to exist is persona
synthesis, to produce output indistinguishable from a flat file copy.

**Manifest-aware deploy, same as DES-065, not something simpler.**
The mission asks whether these need the full manifest-aware
update-without-clobbering logic or whether something simpler suffices
since there is no persona content to preserve against user edits. They
still need DES-065's decide/place logic, for a different reason than
persona preservation: an operator *will* hand-edit a seeded review
agent's scope (e.g. narrow `code-reviewer` to skip a check that fires
too often in their codebase), exactly as they might hand-edit a
generated persona file, and an unconditional overwrite on the next
`ethos seed` would silently discard that edit. `internal/seed/decide`
already handles exactly this: tracked-and-untouched → upgrade
silently; tracked-and-edited → skip with a warning; untracked-and-
present → skip (collision, don't clobber a file ethos didn't create).
Reuse it unchanged. The "no persona content" premise doesn't change
the update-safety requirement — it only means there's no
recompute-and-merge step before `decide` runs, which the role pipeline
also doesn't have per-file (it recomputes the whole persona file each
`ethos seed`, same as sidecar/agents would recompute nothing, just
compare bytes).

**No interaction with DES-052/DES-069 write-set or MCP-grant
machinery.** These are pure Claude Code subagent definitions consumed
by Claude Code's own subagent-invocation mechanism (`description`
field drives auto-invocation matching; the body is a system prompt).
They carry no `tools:` field pointing at ethos's role model, are never
bound to an identity, are never dispatched via `ethos mission
dispatch`, and never appear as a mission's `--worker`/`--evaluator`.
They have no write-set to enforce because they are read-only reviewers
by construction (no `Write`/`Edit` tool granted in their own
frontmatter's implicit tool defaults — Claude Code subagents without
an explicit `tools:` restriction inherit the full tool set, so each
ported agent's system prompt states explicitly, as the third-party
originals already do, that it must never edit code, matching the
existing `reviewer.yaml`/`security-reviewer.yaml` safety_constraint
pattern in prose rather than in an ethos-enforced gate). This is a
deliberate scope boundary: DES-068's MCP-grant model exists because a
*specialist* can be dispatched into a live delegated-work session with
outbound tool risk; a read-only review checklist invoked ad hoc by a
leader carries none of that risk, so extending DES-068's machinery to
these agents would be solving a problem they don't have.

## Discovery and use after `ethos seed`

Concrete Phase 5 after this ships, for any repo that has run
`ethos seed`:

```text
11. `make check` must still pass.
12. Run `code-reviewer` (ethos-seeded, `.claude/agents/code-reviewer.md`)
    on the diff.
13. Run `silent-failure-hunter` (ethos-seeded) on the diff.
14. Run `invariant-completeness-reviewer` (ethos-seeded) on the diff.
15. Fix all valid findings; re-run until all three return zero findings.
```

Three agents replace two third-party ones plus adding the coverage
this repo already needed and built ad hoc. A leader invokes each with
Claude Code's normal `Task`/subagent-invocation surface — no ethos CLI
or MCP call is involved in *running* them, only in *seeding* them.
That mirrors how the current `pr-review-toolkit` agents are invoked
today; this design changes where the agent definitions come from, not
how a leader calls them.

**`ethos seed` should deposit a CLAUDE.md pointer, parallel to vox's
`@`-import.** `internal/enable/deposit.go` shows the pattern: vox
writes a vendored guide plus a manifest, and the *consuming repo's*
CLAUDE.md gets an `@`-import line added so a session picks it up
without the operator hand-wiring it. Seeded review agents should do
the analogous thing at a smaller scale: seed writes the three files
under `.claude/agents/`, and (new, in-scope for the eventual
implementation) also writes or upgrades a short vendored block in
`.punt-labs/ethos/CLAUDE.md` (the file ethos already vendors and
`@`-imports into the top-level CLAUDE.md per DES-057) naming the three
agents and the Phase-5 sequence above, so a leader who has never seen
this design still discovers the agents exist and how to sequence them.
This is a smaller version of the same manifest-tracked vendored-zone
mechanism `enable` uses — not a new mechanism, an additional file
seeded into the existing vendored `.punt-labs/ethos/CLAUDE.md`, which
is already manifest-tracked and already `@`-imported from the
top-level CLAUDE.md. Leaving it to the operator to wire in manually
(the rejected alternative) reproduces exactly the invisibility problem
that made this mission necessary in the first place: ethos already had
one working example (`invariant-completeness-reviewer.md`) that no
other repo could discover because nothing propagated it.

**Transition plan for ethos's own dogfooding: run both in parallel for
one PR cycle, then drop `pr-review-toolkit`.** Cutting over immediately
is riskier than it looks: the three ported agents are new prompts,
even though closely derived from working originals, and the operator's
goal is to *increase* review coverage, not trade one gap for another
while the seeded agents are unproven. One full PR cycle running all
five (two third-party plus three ethos-seeded) lets the leader diff
findings — if the ethos-seeded three catch everything the third-party
two catch, plus what `invariant-completeness-reviewer` already proved
it catches, drop the plugin dependency from this repo's Phase 5 in the
next PR. If a ported agent's findings meaningfully regress from its
third-party original (e.g. the language-neutral rewrite of
silent-failure-hunter loses precision by removing the concrete
logging-function names), fix the prompt before dropping the original,
not after.

## Plan to revise this repo's own CLAUDE.md / Phase 5

Not executed this round — recorded here as the concrete follow-up:

1. Implement `internal/seed/sidecar/agents/{code-reviewer,
   silent-failure-hunter}.md` by porting the two `pr-review-toolkit`
   originals, de-specializing the project-specific logging/error-ID
   references in silent-failure-hunter.
2. Move `.claude/agents/invariant-completeness-reviewer.md` into
   `internal/seed/sidecar/agents/invariant-completeness-reviewer.md`
   unchanged (it is already ethos-neutral prose) so it stops being a
   one-off and starts being seeded to every consuming repo, ethos
   included.
3. Wire `seedFS(fsys, "sidecar/agents", ".claude/agents", ".md")` into
   `seed.go`'s existing sidecar iteration, and extend the vendored
   `.punt-labs/ethos/CLAUDE.md` deposit to list the three agents and
   the Phase 5 sequence.
4. Run `ethos seed` in this repo (self-seed) to deploy the three
   agents locally, confirming manifest tracking behaves per DES-065.
5. For one PR cycle, run Phase 5 steps 12-14 (ethos-seeded) alongside
   the existing `pr-review-toolkit` invocations and diff findings.
6. If parity holds, edit this repo's own root `CLAUDE.md` Phase 5 to
   drop the `pr-review-toolkit` agent references (in
   "### Phase 5: Local Review", steps 12-13 currently) and replace
   with the three ethos-seeded agents; update the Plugins section to
   drop the `pr-review-toolkit` line if no other workflow still needs
   it.
7. Update `CHANGELOG.md` under `## [Unreleased]` and `README.md` for
   the new `ethos seed` output.

## Open questions for the leader/operator

Technical packaging calls (this worker's recommendation stands unless
overridden):

- Exact seed-manifest key naming for the new category (`agents/<name>`
  vs. a flat top-level key) — cosmetic, does not affect behavior.
- Whether `invariant-completeness-reviewer.md`'s `model: opus` pin
  should be relaxed to `model: inherit` (matching
  silent-failure-hunter) once seeded broadly, so cost scales with the
  invoking session rather than always forcing Opus in every consuming
  repo. Recommend `inherit` for the seeded copy; ethos's own repo can
  override locally if the Opus pin proved valuable during dogfooding.

Product-positioning calls (the leader should separate and decide,
not this design):

- **Does ethos publicly market "we ship code review agents" as a
  product capability**, alongside identity, missions, and audit — or
  is this framed purely as "here is how ethos dogfoods good local
  review, and you get it for free via `ethos seed`" with no marketing
  claim attached? This changes whether the seeded-agents README gets
  prfaq.tex treatment or stays an internal implementation note.
- **Does replacing `pr-review-toolkit` in ethos's own CLAUDE.md count
  as a competitive statement** (implicitly: our seeded agents are
  as good as or better than the community plugin) that the operator
  wants to make deliberately rather than as an incidental side effect
  of dogfooding.
