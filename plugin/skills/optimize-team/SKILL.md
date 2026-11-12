---
name: optimize-team
description: Right-size an ethos team roster to one repo's actual tooling, at the lowest per-session .claude/agents context cost. Use when asked to optimize, shrink, right-size, or trim a team, compose a minimal team for a repo, or reduce agent/session context cost. Interviews the repo, proposes the smallest covering team with a context-budget estimate, and writes team YAML only after ratification.
---

# Optimize Team

Right-size an ethos team roster to one repo's actual tooling, at the
lowest per-session context cost. `ethos setup` scaffolds a **starter**
team — a generic roster that gets a fresh repo running. This skill
**right-sizes** that roster: it interviews the user about what the repo
actually dispatches, proposes the smallest team that covers it, shows
the context cost before writing anything, and only writes after
explicit ratification.

## When to use

Run this when:

- A repo has just scaffolded a team via `ethos setup` and the roster
  needs trimming to the repo's real tooling profile (greenfield).
- An existing team has grown seats that no longer earn their context
  cost, or is missing a seat the repo now needs (optimize-in-place).

Not: a one-off single-identity creation (`create-from-project` handles
that), and not a CLI subcommand — this skill drives the existing
`ethos` binary and MCP surface, never adds one.

## The central constraint: per-session context

Every agent-kind team member produces a `.claude/agents/<handle>.md`
at SessionStart. Claude Code loads all of them every session. Roster
**size** is therefore the dominant per-session context lever — pruning
a whole agent file removes everything that file would have loaded:
persona and writing-style prose, the output-format body,
responsibilities, anti-responsibilities, talents, skills, and the
tools list. Within one agent file, persona/writing-style prose plus
the output-format body are the largest terms (several KB), not the
tools list (roughly 0.7-1.1 KB even for two dozen-plus MCP entries,
since `quarry`/`quarry-dev` and `biff`/`biff-dev` pairs duplicate).

Minimizing context is therefore, in priority order:

1. Fewer members (the dominant lever).
2. Lean persona / output-format bodies.
3. Tighter per-role tool grants.
4. Lean skills and the right-tier model.

## The model-tier ladder

Four rungs, strictly ordered: `haiku` < `sonnet` < `opus` < `fable`.
Every value this skill ever emits is one of these four, `inherit`, or
a full `claude-*` model ID — never an unvalidated string. Run every
proposed value through the same check `ValidateModel`
(`internal/role/role.go`) enforces, and refuse to propose anything it
would reject.

A reviewer role always out-tiers the worker role it reviews, strictly:
opus over sonnet, fable over opus. This is what keeps the review-edge
inequality satisfiable even when the worker is already at opus — the
fable rung exists for exactly that case (a worker at opus needs
headroom above it).

## The interview-to-team-shape decision table

Interview the user with `AskUserQuestion`, one question per row below.
Every answer adds or removes a seat and sets that seat's model tier.
Ask every question whose answer changes the team shape — never guess a
"none" answer to save a question.

| # | Interview question | Answer | Seat effect | Model tier |
|---|---|---|---|---|
| 1 | Primary language(s)? | Go | add `go-implementer` (worker) | sonnet |
| 1 | — | Python | add `python-implementer` (worker) | sonnet |
| 1 | — | both | add both implementers | sonnet each |
| 1 | — | other language (e.g. Swift, Rust, TypeScript) | add `<lang>-implementer` (worker) | sonnet |
| 1 | — | none (no implementation surface) | no language implementer seat | — |
| 2 | Who reviews the implementation code? | same repo, needs a reviewer | add `<lang>-reviewer` (distinct handle from the implementer, for whichever language(s) row 1 added) | opus |
| 2 | — | no second-party review wanted | implementer only; note the author-grades-own-work risk | — |
| 3 | Testing approach? | TDD / dedicated test+QA | add `qa` seat distinct from the implementer | sonnet |
| 3 | — | implementer writes own tests | no QA seat; implementer carries tests | — |
| 4 | Backlog / product management needed? | yes | add `product` seat | sonnet |
| 4 | — | no | prune the product seat | — |
| 5 | Security / trust-boundary surface? | credentials, parsing, auth, multi-tenant | add `security` seat (worker) | opus |
| 5 | — | — | its reviewer | fable (out-tiers opus) |
| 5 | — | none | prune the security seat | — |
| 6 | Formal-methods need? | Z / B specs, model-checking | add `formal` seat (worker) | opus |
| 6 | — | — | its reviewer | fable (out-tiers opus) |
| 6 | — | none | prune the formal seat | — |
| 7 | Infra / CI surface? | CI workflows, release, cross-repo rollout | add `infra` seat | sonnet (authored) |
| 7 | — | — | its reviewer | opus (hard-to-review domain) |
| 7 | — | none | prune the infra seat | — |
| 8 | CLI / command surface? | user-facing CLI, help text, man pages | add `cli` seat | sonnet |
| 8 | — | none | prune the cli seat | — |
| 9 | Crypto primitives? | key handling, signing | add `crypto-implementer` | sonnet (authored) |
| 9 | — | — | its reviewer | opus (hard-to-review domain) |
| 9 | — | none | prune | — |

Row 1/2 are not Go/Python-only: any repo whose primary language is
something else (Swift, Rust, TypeScript, ...) still gets an
implementer+distinct-reviewer pair proposed for that language — "none"
is reserved for a repo with no implementation surface to speak of
(pure docs, pure config), not for "a language other than Go or
Python." Pruning a real specialist pair because the table only listed
two languages is exactly the gap this row exists to close.

The reference rosters to read for "what seat maps to what work" are
`.punt-labs/ethos/teams/engineering.yaml` (the full 20-seat team) and
the `gstack` bundle's role-based pipeline. These are the superset this
skill prunes **down** from, never a floor it adds **up** to.

### Invariants the table encodes

- **Model tier tracks reasoning depth, not seat count.** No seat gets a
  top tier merely for existing on the roster — formal methods and the
  security trust boundary get opus workers with fable reviewers (rows
  5, 6); routine implementation gets sonnet (rows 1, 8).
- **A reviewer out-tiers its work, strictly.** Every reviewer row is
  one rung above the worker it reviews. Validate this over the
  proposed collaboration edges **before** writing the team YAML:
  every edge is `from: <worker-role>`, `to: <reviewer-role>`, `type:
  reports_to`, with `tier(reviewer) > tier(worker)`. A `Collaboration`
  has no valid default for `type` — `internal/team/team.go:22-25`
  requires it on every edge, and `validCollabType` (team.go:44,
  `CollaborationTypes`) accepts only `reports_to`,
  `collaborates_with`, or `delegates_to`; an edge missing `type`, or
  carrying any other string, fails team load. Refuse to write a team
  whose review edges are not a strict ordering by tier, or whose edges
  omit `type`.
- **The ladder is acyclic by construction.** The table only ever adds
  a *dedicated* reviewer seat for a worker seat, never a
  worker-reviews-worker edge — a reciprocal pair never arises to
  split in the first place.
- **One role, one model.** Each row adds a role with exactly one
  `model:`. Implement and review are always distinct roles on
  distinct handles, never one handle wearing two tiers.
- **Single-source each responsibility, as a writing convention.**
  Write the responsibility once, on the implement role; give the
  review role a reference line ("reviews Go per go-implementer")
  instead of recopying the text. Nothing in ethos enforces this
  mechanically — it is a convention this skill follows when authoring
  YAML, not a schema guarantee.
- **The author never grades their own work.** The QA/test seat and
  every reviewer seat are distinct handles from the implementer.
  Enforce this at write time: worker handle != evaluator handle, and
  they must not share a role (the same rule `ethos mission create`
  enforces).
- **A new seat earns its context cost.** Before adding any seat, show
  its incremental context cost (below) and ask the user to ratify.
  The default answer to every optional axis is "prune" — a seat is
  added only on an explicit yes.
- **The team is scoped to what the repo dispatches.** Every "none"/"no"
  answer prunes a seat. A pruned seat returns only by re-running the
  interview and answering yes — it does not linger.
- **The ethos tree is source; `.claude/agents/` is output.** This skill
  writes only role/identity/team YAML and `ethos.yaml`, then lets
  SessionStart regenerate the agent files. It never writes or
  hand-edits any file under `.claude/agents/`.

## The context-budget estimate

Before the user ratifies, show a concrete number so the roster's cost
is visible, not implied.

**What is measured:** the summed bytes (and an estimated token count)
of the `.claude/agents/<handle>.md` files the proposed roster *would*
generate. Compose the same inputs the agent-file generator
(`internal/hook/generate_agents.go`) uses and sum their serialized
size per handle — persona + writing-style prose, the output-format
body, responsibilities + anti-responsibilities, skills lines
(baseline-ops + identity skills + bundle default_skills), the tools
list, description + talents lines. The dominant terms are the
persona/writing-style prose and the output-format body, not the tools
list.

Two corrections the estimate must apply, both verified against
`internal/hook/generate_agents.go`:

- **Exclude the configured main agent.** The agent planner skips
  `cfg.Agent` (the repo's main-session identity) entirely — it never
  generates a `.claude/agents/<handle>.md` for it (the `m.Identity ==
  mainAgent` continue). Counting the main agent's seat in the budget
  overstates both the current-cost baseline and the savings from
  pruning it; it was never going to produce a file either way.
- **Include the per-seat generated hook/scaffolding block.** Any role
  whose `tools:` includes `Write` or `Edit` (`hasWriteTool`) gets an
  extra generated block appended to its agent file — a `hooks:`
  section wiring `PostToolUse` on the `Write|Edit` matcher. This is
  bytes the generator adds on top of the persona/writing-style/output
  terms above, not covered by reading only the source `.md` files.
  Add it to every write-enabled seat's estimate, or state plainly that
  the number is a lower bound that omits it.

**Estimating a not-yet-written seat.** A greenfield seat has no
personality, writing-style, or talent `.md` on disk yet, so its prose
bytes cannot be read from an existing file. Estimate them from the
*template it will write for that seat* — each seat type maps to a
starter personality and writing-style (the same templates `ethos
setup` scaffolds), and the estimate measures those template bodies.
The estimate is therefore the size of what this skill is about to
create, not a guess. An optimize-in-place seat that already has its
`.md` files is measured from the real files.

Estimate tokens as `bytes / 4` (a standard rough ratio) and present a
table before the ratification question:

```text
Proposed roster — context budget
  handle          model    est. bytes   est. tokens
  go-implementer  sonnet       3,100          775
  go-reviewer     opus         2,600          650
  security        opus         2,700          675
  ─────────────────────────────────────────────────
  TOTAL (3 seats)              8,400        2,100

  For comparison: full engineering roster (20 seats) ≈ 52 KB ≈ 13,000 tokens
```

The comparison line anchors the number against the full `engineering`
team so the user sees what right-sizing saved. Show this immediately
before the ratification `AskUserQuestion`, and never write a file
until the user picks "ratify."

## The propose-then-ratify-then-write flow

Strictly propose-first. Writing is gated on an explicit ratification.

1. **Detect the starting state.** Read `.punt-labs/ethos.yaml` and
   `.punt-labs/ethos/teams/`. No team configured is greenfield; an
   existing team is optimize-in-place (see below).
2. **Interview** via `AskUserQuestion`, one question per decision-table
   axis.
3. **Compose** the proposed roster from the answers, applying the
   decision table. Validate the review edges — every edge is `from:
   <worker-role>`, `to: <reviewer-role>`, `type: reports_to`, with
   `tier(reviewer) > tier(worker)`, strictly. Refuse to proceed if any
   edge fails this check or omits `type`.
4. **Present** the proposed team plus the context-budget table above.
5. **Ratify the YAML writes** via a final `AskUserQuestion`: "Write
   this team?" with choices ratify / adjust / cancel. On "adjust,"
   re-open the relevant interview question. On "cancel," write
   nothing. This ratification covers only the non-destructive YAML
   writes in step 6 — not step 8's reconcile.
6. **Back up, then write atomically, only after ratification.** Copy
   the prior team closure (the existing `roles/`, `identities/`,
   `teams/<name>.yaml`, and the `ethos.yaml` the team references) to a
   timestamped backup under `.tmp/optimize-team-backup-<ts>/` before
   any mutation. Stage the full new closure to a temp directory, not
   in place, and write each file there:
   - `roles/<name>.yaml` — one per seat, with `model:`,
     `responsibilities`, `permissions`, `tools`. A review role's
     `responsibilities` carry a reference line naming the implement
     role (the convention above), never a recopy.
   - `identities/<handle>.yaml` — one per agent seat: `name`,
     `handle`, `kind: agent`, `writing_style`, `personality`,
     `talents`, `skills`. No `model`, no `tools` — those live on the
     role (`internal/identity/identity.go`). Writing a `personality:`
     or `writing_style:` slug does not create that content — it is a
     reference the agent-file generator resolves against
     `personalities/<slug>.md` / `writing-styles/<slug>.md`
     (`internal/attribute/`). In a `resolution: repo-only` repo — this
     skill's usual target — a slug that does not resolve is a hard
     error at identity load (`ErrIncompleteRepoSet`,
     `internal/identity/layered.go`), and agent generation then reports
     the member as a failure by name rather than quietly producing
     fewer files than the team has (`generate_agents.go`, DES-057).
     (In a layered repo with a global tail the same miss is only a
     warning and the member is soft-skipped.) Either way the slug must
     resolve, so every slug used here must either already resolve (an
     existing repo/bundle/global attribute) or be staged in the
     same closure: write the matching
     `personalities/<slug>.md` / `writing-styles/<slug>.md` file
     alongside the identity, using one of the starter templates `ethos
     setup` scaffolds as the base.
   - `teams/<name>.yaml` — members (identity+role) and
     `collaborations`. Every edge is a 3-field `Collaboration` —
     `from`, `to`, and `type` — never direction alone; `type` is
     mandatory (`internal/team/team.go:22-25`) and must be one of
     `reports_to` / `collaborates_with` / `delegates_to`
     (`CollaborationTypes`, team.go:44). A review edge is written as
     the worker reporting to its reviewer, e.g.:

     ```yaml
     collaborations:
       - from: go-implementer
         to: go-reviewer
         type: reports_to
     ```

     Never a bidirectional edge, and never an edge with `from`/`to`
     only.
   - `ethos.yaml` — `agent`, `team`, and `resolution` keys.
7. **Validate the staged set with the checks this skill can run, then
   swap, then confirm with `ethos doctor`.** `ethos doctor` checks the
   *live* repo — it takes no staged-root argument (`cmd/ethos/main.go`
   `doctorCmd`/`runDoctor`) — so it cannot be pointed at the temp
   staging directory from step 6. Before swapping, validate the staged
   closure with the checks this skill owns: every `model:` value
   passes `ValidateModel` (`internal/role/role.go`); every
   collaboration edge has a `type` from `CollaborationTypes` and a
   strict tier ordering (above); every worker/evaluator pair is a
   distinct handle; and every proposed member's `personality` and
   `writing_style` slug resolves against a staged or existing
   `.md` file (the gap named in step 6 above) — in a repo-only repo an
   unresolved slug makes agent generation fail and name the member, so
   catch it here to fail fast before the write rather than after. On a clean result, swap the staged files into
   place as the last step — an all-or-nothing cutover — then run
   `ethos doctor` against the now-live result to confirm it resolves
   end-to-end (identity/role/team cross-references, orphaned files,
   hook currency). If `doctor` reports a FAIL on the live result,
   restore the backup from step 6 immediately, report the failure, and
   stop. The backup-and-restore is the real safety net here, not a
   pre-write doctor run: the operator's existing working team survives
   a bad write because the backup is restored the moment `doctor`
   catches it, not because `doctor` ever saw the staged files first.
8. **Destructive reconcile — a SEPARATE, explicitly-consented step.**
   `ethos vendor` has no `--yes` flag and prompts for nothing itself
   (`cmd/ethos/vendor.go`); its own safety model is `--dry-run`
   (the default — plan only, no write) versus `--apply` (write the
   snapshot). Drive that model explicitly instead of relying on a
   vendor-provided prompt that does not exist:
   1. Run `ethos vendor --team <t> --prune` with no `--apply` — this
      plans only and prints what would be removed from the vendored
      closure.
   2. Present that plan verbatim in a separate `AskUserQuestion`
      ("Reconcile now? This deletes obsolete trace and prunes these
      vendored files: `<plan output>`") before invoking anything
      destructive.
   3. On explicit consent, run `ethos vendor --team <t> --apply
      --prune` to write the deletions. On "no," leave it to the user
      and stop here.

   This reconcile is destructive and is NOT covered by the step-5
   ratification — that one covers only the non-destructive YAML writes
   in step 6. Run it only when re-shaping left obsolete residue, and
   the prune only in a `resolution: repo-only` repo.
9. **Regenerate is not this skill's job.** Tell the user to restart
   Claude Code; SessionStart regenerates `.claude/agents/` from the
   YAML just written. Never touch the generated files directly.

## Greenfield vs optimize-in-place

**Greenfield (no team yet).** `ethos setup` persists immediately —
`store.Save` runs as each identity is created and the repo config is
written to disk in the same pass (`cmd/ethos/setup.go`), with no
dry-run or staging step of its own. Running it "first" would therefore
write starter identities and a starter team before this skill's
ratification gate ever fires, breaking the "canceling writes nothing"
promise this skill makes everywhere else. Gather the full proposal
WITHOUT running `ethos setup`: run the interview, compose the roster
from the decision table, and present the budget table exactly as in
optimize-in-place, but against a starting state of zero existing
seats. Only after the user ratifies, write the full closure directly
(roles, identities, team, `ethos.yaml`) in one step 6 — there is no
separate "run setup, then trim" pass. All seats are additions; the
budget table shows the full proposed cost against zero.

**Optimize-in-place (existing team).** Read the current team and
roles, run the interview, and produce a **diff**: seats to keep, seats
to add, seats to prune, and model-tier changes. The budget table shows
current cost, proposed cost, and the delta. Pruning removes the member
from the team YAML (and optionally the role/identity files, on an
explicit confirm). This does NOT delete the pruned member's generated
`.claude/agents/<handle>.md` file — the agent-file generator
(`internal/hook/agent_plan.go`) only ever writes or renames into that
directory, never removes a file for a member no longer on the team.
The file is left behind as an orphan; `ethos doctor`'s "Orphaned agent
files" check (`internal/doctor/doctor.go`) flags it, and the user
removes it by hand (or leaves it, inert, since nothing regenerates or
reads it once its identity is off the team). Never delete a generated
`.claude/agents/` file directly as part of *writing* the new roster —
removing the source YAML is this skill's job; removing the resulting
orphaned file is the user's, flagged by `doctor`, not automatic.
After any prune in a `resolution: repo-only` repo, step 8's plan-then-
consent-then-apply reconcile (`ethos vendor --team <t> --prune`, then
`--apply --prune` on consent) cleans the vendored closure so no
orphaned role or identity remains there.

## What this skill does NOT do

- Does not add a CLI subcommand to the `ethos` binary.
- Does not write or edit any file under `.claude/agents/`.
- Does not emit any value `ValidateModel` rejects.
- Does not write any YAML before the user ratifies.
- Does not invent seats the interview did not justify.

## Reference

- `docs/optimize-team.md` (ethos repo) — the full design this skill
  implements.
- `internal/role/role.go` — `ModelAliases`, `ValidateModel`.
- `internal/hook/generate_agents.go` — `buildAgentFile`, the generated
  agent-file composition this skill's budget estimate mirrors.
- `ethos doctor` — the live-repo confirmation check this flow runs
  after cutover (step 7).
- `cmd/ethos/vendor.go` — `ethos vendor --team <t> --prune` (plan) and
  `ethos vendor --team <t> --apply --prune` (write) — the reconcile
  commands step 8 drives, in that order.
