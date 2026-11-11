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
| 1 | — | other/none | no language implementer seat | — |
| 2 | Who reviews the implementation code? | same repo, needs a reviewer | add `go-reviewer` / `python-reviewer` (distinct handle) | opus |
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
  every edge is `worker -> reviewer` with `tier(reviewer) >
  tier(worker)`. Refuse to write a team whose review edges are not a
  strict ordering by tier.
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
   decision table. Validate the review edges — every edge is
   `worker -> reviewer` with `tier(reviewer) > tier(worker)`, strictly.
   Refuse to proceed if any edge fails this check.
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
     role (`internal/identity/identity.go`).
   - `teams/<name>.yaml` — members (identity+role) and
     `collaborations`. Each review edge is written with explicit
     direction `from: <worker-role>`, `to: <reviewer-role>` — never a
     bidirectional edge.
   - `ethos.yaml` — `agent`, `team`, and `resolution` keys.
7. **Validate the staged set, then swap.** Run `ethos doctor` against
   the staged closure. On a clean result, swap the staged files into
   place as the last step — an all-or-nothing cutover. On any
   `doctor` failure, leave the working tree untouched, restore from
   the backup if a partial write already happened, report the
   failure, and stop. The operator's existing working team survives a
   mid-write error — there is no window where the team references
   files that do not exist.
8. **Destructive reconcile — a SEPARATE, explicitly-consented step.**
   `ethos vendor --team <t> --apply --prune` removes files from the
   vendored closure. This is destructive and is NOT covered by the
   step-5 ratification. Run it **without `--yes`**, so the user sees
   and answers its own prompt, AND ask a separate `AskUserQuestion`
   first ("Reconcile now? This deletes obsolete trace and prunes
   vendored files") before invoking it. Run this only when re-shaping
   left obsolete residue, and the prune only in a
   `resolution: repo-only` repo. On "no," leave it to the user.
9. **Regenerate is not this skill's job.** Tell the user to restart
   Claude Code; SessionStart regenerates `.claude/agents/` from the
   YAML just written. Never touch the generated files directly.

## Greenfield vs optimize-in-place

**Greenfield (no team yet).** Offer to run `ethos setup` first to lay
down the starter scaffold, then run the full interview and write a
fresh roster. All seats are additions; the budget table shows the full
proposed cost against zero.

**Optimize-in-place (existing team).** Read the current team and
roles, run the interview, and produce a **diff**: seats to keep, seats
to add, seats to prune, and model-tier changes. The budget table shows
current cost, proposed cost, and the delta. Pruning removes the member
from the team YAML (and optionally the role/identity files, on an
explicit confirm); the agent file disappears on the next SessionStart
regeneration. Never delete a generated `.claude/agents/` file
directly — removing the source and regenerating is the only path.
After any prune in a `resolution: repo-only` repo, `ethos vendor
--apply --prune` reconciles the vendored closure so no orphaned role
or identity remains (step 8 above).

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
- `ethos doctor`, `ethos vendor --team <t> --apply --prune` — the
  validation and reconcile commands this flow drives.
