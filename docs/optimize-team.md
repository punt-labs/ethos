# Design — the `/ethos:optimize-team` skill

## What this skill is

The `/ethos:optimize-team` skill is three files working together:

1. `plugin/commands/optimize-team.md` — the released slash command,
   `/ethos:optimize-team`, with `allowed-tools` naming the released ethos
   MCP tools (`mcp__plugin_ethos_self__team`, `...__role`,
   `...__identity`), `AskUserQuestion`, `Read`, `Glob`, and `Bash`.
2. `plugin/commands/optimize-team-dev.md` — its `-dev` twin,
   `/ethos-dev:optimize-team-dev`, identical except the MCP tool names
   carry the `-dev` plugin prefix (`mcp__plugin_ethos-dev_self__team`).
   Both files are required; `docs/development.md` mandates the
   `name.md` + `name-dev.md` pair for every plugin slash command.
3. `internal/seed/sidecar/skills/optimize-team/SKILL.md` — the seed-sidecar
   skill holding the actual procedure. `ethos seed` deploys it to
   `~/.claude/skills/optimize-team/SKILL.md`, alongside `baseline-ops`,
   `mission`, and `create-from-project`.

The command file is a thin launcher: it tells the model to run the
`optimize-team` skill, passing `$ARGUMENTS` through. The skill carries the
interview, the decision table, the budget estimate, and the write steps.
This mirrors how `create-from-project` is a sidecar skill that commands and
agents reach by name, not a CLI subcommand.

The skill is **not** a new `ethos` binary command. It drives the existing
binary and MCP surface through an interactive interview:

- `ethos setup` — the starter-team scaffold it builds on top of.
- `ethos team` / `ethos role` / `ethos identity` (CLI or the `team`,
  `role`, `identity` MCP tools) — to write team, role, and identity YAML.
- `ethos doctor` — to verify referential integrity after writing.
- `ethos vendor --team <t> --apply --prune` — to refresh the vendored
  closure in a `resolution: repo-only` repo.

### How it complements `ethos setup`

`ethos setup` scaffolds a **starter** team from a bundle: a generic roster
that gets a fresh repo running. `/ethos:optimize-team` **right-sizes** that
roster to one repo's actual tooling. Setup answers "give me a team";
optimize-team answers "give me the smallest team that covers what *this*
repo dispatches, at the lowest per-session context cost." Setup runs once at
install; optimize-team runs whenever the repo's tooling profile changes.

## The central constraint: per-session context

Every agent-kind team member produces a `.claude/agents/<handle>.md` at
SessionStart (`internal/hook/generate_agents.go`). Claude Code loads all of
them. The roster size is therefore the dominant per-session context lever,
and shrinking it is this skill's whole reason to exist.

A generated agent file is composed, in order, of: `name`, `description`
(personality first line), the `tools` list, `model`, `skills` (baseline-ops
first, then identity skills, then bundle default_skills), the persona and
writing-style prose, `responsibilities`, anti-responsibilities, `talents`,
and the `output_format` body (`buildAgentFile`, `generate_agents.go:760-848`).

**Member count is the real lever — principle 7.** The single biggest
reduction in loaded context is pruning a whole agent file, because an unneeded
seat loads *all* of the above. Within one agent file, the **persona prose plus
the `output_format` body** are the largest terms, not the `tools` list: a
specialist's `tools` list is two dozen-plus MCP entries at ~40-60 bytes each
(see `.punt-labs/ethos/roles/go-specialist.yaml` lines 26-51), roughly
0.7-1.1 KB, while the persona and writing-style prose plus the output-format
template run several KB. The `tools` list still matters — the duplicated
`quarry`/`quarry-dev` and `biff`/`biff-dev` pairs inflate every role — but it
is not the dominant per-agent term. Minimizing context is therefore, in
priority order: fewer members, then lean persona/output-format bodies, then
tighter per-role tool grants, then lean skills and the right-tier model.

## Fable-tier finding and its resolution

**Finding: ethos rejects `fable` today, but Claude Code accepts it.**
`fable` is a valid Claude Code agent `model:` frontmatter alias — the
official set, confirmed against the Claude Code subagent docs
(code.claude.com/docs/en/sub-agents), is `sonnet`, `opus`, `haiku`, `fable`,
a full model ID, or `inherit`; this environment's model set includes Fable 5.1
(`claude-fable-5-1`). Ethos emits the role's `model` string verbatim into the generated
agent file (`internal/hook/generate_agents.go`), so `model: fable` works
end-to-end at the harness — *once ethos stops rejecting it at write time.*

Ethos does not accept it today. `role.ModelAliases`
(`internal/role/role.go:41`) is exactly `{"opus", "sonnet", "haiku",
"inherit"}`, and `role.ValidateModel` (`internal/role/role.go:50-63`) accepts
a value only if it is one of those four or a `claude-*` ID; `fable` returns
`unrecognized model`.

**Resolution — a concrete prerequisite ethos change, not a workaround.** The
skill's four-rung vocabulary (opus/sonnet/haiku/fable) maps 1:1 to valid
Claude Code aliases *after* a small change lands in ethos. This change is a
dependency of the skill, scoped here:

1. `internal/role/role.go:41` — add `"fable"` to `ModelAliases`. This slice
   is the single source; the schema enum reads it.
2. `internal/role/role.go:61` — update the hand-enumerated error-message
   string to list `fable`.
3. `internal/schema/registry.go:41` — update the hand-enumerated
   `Description` string to list `fable`.
4. Tests — `internal/schema/schema_test.go:244` (accepted list) and
   `internal/role/role_test.go`: add a `fable` positive case.

That change is a separate ~5-line implementation mission; this design
document does not implement it. It is named here because the skill relies on
it: until it lands, the skill must not emit `fable`.

**What the skill emits.** The model-tier ladder uses the four-rung
vocabulary, and each rung maps directly to a valid alias:

| Conceptual rung (operator vocabulary) | Emitted `model:` value |
|---|---|
| fable (top reviewer rung) | `fable` |
| opus | `opus` |
| sonnet | `sonnet` |
| haiku | `haiku` |

So "fable reviews opus" (principle 2) is honored literally: an opus worker
gets a `fable` reviewer, a sonnet worker gets an opus reviewer. The skill's
model picker only ever emits a value from this known-good set — it never
writes an unvalidated string, because the harness does not document behavior
on an invalid `model:` value. The picker runs each proposed value through the
same rule `ValidateModel` enforces (post-change) and refuses anything else.

## The interview-to-team-shape decision table

The skill interviews the user with `AskUserQuestion`, one question per axis
below. Each answer adds or removes a seat and sets that seat's role model
tier. Every question whose answer changes the team shape is asked; the skill
does not guess (principle: brief contract item 2). The table is the
encoding of all nine operator principles.

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

The reference rosters the skill reads for "what seat maps to what work" are
`.punt-labs/ethos/teams/engineering.yaml` (the full 20-seat team) and the
`gstack` bundle's role-based pipeline. These are the superset the skill
prunes *down* from, never a floor it adds *up* to.

### How each operator principle is encoded

- **Principle 1 — model tier tracks reasoning depth, not seat count.** The
  tier column is driven by domain, not by the fact that a seat exists.
  Formal methods and the security trust boundary get `opus` workers with
  `fable` reviewers (rows 5, 6); routine implementation gets `sonnet`
  (rows 1, 8); no seat gets a top tier merely for being on the roster.
- **Principle 2 — a reviewer out-tiers its work.** Every reviewer row is one
  rung above the worker it reviews: opus over sonnet (rows 2, 7, 9), fable
  over opus (rows 5, 6). The four-rung ladder (haiku < sonnet < opus < fable)
  gives a reviewer headroom even above an opus worker. Infra and crypto are
  sonnet-authored but opus-reviewed (rows 7, 9) because they are the hardest
  to review.
- **Principle 3 — the ladder is acyclic.** The decision table holds this by
  construction: it only ever adds a *dedicated* reviewer seat for a worker
  seat (rows 2, 5, 6, 7, 9), never a worker-reviews-worker edge, so a
  reciprocal pair can never arise in the first place. The one enforced
  invariant is: **every review edge is written worker -> reviewer with
  tier(reviewer) > tier(worker), strictly.** The skill validates this over
  the proposed collaboration edges *before* writing the team YAML — it refuses
  to write a team whose review edges are not a strict DAG by tier. The fable
  rung is what keeps that strict inequality satisfiable when the worker is
  already at opus. (There is no "detect a reciprocal pair and split it"
  step; the table never produces one to split.)
- **Principle 4 — one role, one model; split by function.** Each row adds a
  role with exactly one `model:`. Implement and review are always distinct
  roles on distinct handles, never one handle wearing two tiers (a role
  carries a single `model:` field — `role.go:30`).
- **Principle 5 — single-source each responsibility (a writing convention,
  not schema-enforced).** `role.Responsibilities` is a plain `[]string`
  (`role.go:31`); nothing in ethos enforces single-sourcing, so this is a
  convention the skill *follows* when authoring YAML, not a guarantee the
  schema provides. The skill writes the responsibility once, on the implement
  role, and gives the review role a reference line (e.g. "reviews Go per
  go-implementer") instead of recopying the text. The reviewer's *generated*
  file does end up carrying the implementer's responsibilities verbatim, but
  that is output, not source: `deriveAntiResponsibilities`
  (`generate_agents.go`) copies a `reports_to` target's responsibilities into
  the generated agent file, which is regenerated from source and never
  hand-edited (principle 9). If structured enforcement is ever wanted, it
  would be a new ethos field (e.g. a `responsibilities_ref` on the role) — a
  future change, not something claimed here.
- **Principle 6 — the author never grades their own work.** The QA/test
  seat (row 3) and every reviewer seat are distinct handles from the
  implementer. The skill enforces this at write time: worker handle !=
  evaluator handle, and they must not share a role — the same rule
  `ethos mission create` enforces (DES-033).
- **Principle 7 — a new seat earns its context cost.** Before adding any
  seat the skill shows its incremental context cost (next section) and asks
  the user to ratify. The default answer to every optional axis is "prune";
  a seat is added only on an explicit yes.
- **Principle 8 — the team is scoped to what the repo dispatches.** Every
  "none"/"no" answer prunes a seat. A pruned seat returns only by
  re-running the interview and answering yes — it does not linger.
- **Principle 9 — the ethos tree is source; `.claude/agents/` is output.**
  The skill writes only role/identity/team YAML and `ethos.yaml`, then lets
  SessionStart regenerate the agent files. It never writes or hand-edits any
  file under `.claude/agents/`. In a `resolution: repo-only` repo the design
  lives in the vendored tree and the skill records
  `resolution: repo-only` in `ethos.yaml`.

## The context-budget estimate

Before the user ratifies, the skill shows a concrete number so the roster's
cost is visible, not implied.

**What is measured:** the summed bytes (and an estimated token count) of the
`.claude/agents/<handle>.md` files the proposed roster *would* generate. The
skill does not need to write them to estimate — it composes the same inputs
`buildAgentFile` (`generate_agents.go:760-848`) uses and sums their serialized
size per handle:

```text
per-agent bytes ≈
    persona + writing-style prose  ← largest term per agent
  + output_format body (from the role)  ← second-largest term
  + responsibilities + anti-responsibilities
  + skills lines (baseline-ops + identity skills + bundle default_skills)
  + tools list (two dozen-plus MCP entries, doubled by -dev pairs)
  + description + talents lines
```

The dominant per-agent terms are the persona/writing-style prose and the
output-format body, not the tools list (the tools list is ~0.7-1.1 KB; the
prose bodies run several KB). The real reduction lever is **member count**:
pruning a seat removes its whole file.

**Estimating a not-yet-written seat.** A greenfield seat has no personality,
writing-style, or talent `.md` on disk yet, so its prose bytes cannot be read
from an existing file. The skill estimates them from the *template it will
write for that seat*: each seat type maps to a starter personality and
writing-style (the same templates `ethos setup` scaffolds), and the skill
measures those template bodies. The estimate is therefore the size of what the
skill is about to create, not a guess. An optimize-in-place seat that already
has its `.md` files is measured from the real files.

The skill estimates tokens as `bytes / 4` (a standard rough ratio) and
presents a table the user reads before ratifying:

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

The comparison line anchors the number against the full `engineering` team
so the user sees what the right-sizing saved. The skill shows this
immediately before the ratification `AskUserQuestion` and never writes a
file until the user picks "ratify."

## The propose-then-write flow

The skill is strictly propose-first. Writing is gated on an explicit
ratification.

1. **Detect the starting state.** Read `.punt-labs/ethos.yaml` and
   `.punt-labs/ethos/teams/`. Greenfield if no team is configured;
   optimize-in-place if one exists (see next section).
2. **Interview** via `AskUserQuestion`, one question per decision-table axis.
3. **Compose** the proposed roster from the answers, applying the decision
   table. Validate the review edges: every edge is worker -> reviewer with
   `tier(reviewer) > tier(worker)`, strictly (principle 3). Refuse to proceed
   if any edge fails.
4. **Present** the proposed team plus the context-budget table (above).
5. **Ratify the YAML writes** via a final `AskUserQuestion`: "Write this
   team?" with choices ratify / adjust / cancel. On "adjust," re-open the
   relevant interview question; on "cancel," write nothing. This ratification
   covers only the non-destructive YAML writes in step 6.
6. **Back up, then write atomically, only after ratification.** The prior
   team closure (the existing `roles/`, `identities/`, `teams/<name>.yaml`,
   and `ethos.yaml` the team references) is copied to a timestamped backup
   under `.tmp/optimize-team-backup-<ts>/` before any mutation. The skill
   then stages the full new closure to a temp directory, not in place, and
   writes each file there:
   - `roles/<name>.yaml` — one per seat, with `model:`, `responsibilities`,
     `permissions`, `tools`. A review role's `responsibilities` carry a
     reference line naming the implement role (a convention, not enforced —
     see principle 5), never a recopy.
   - `identities/<handle>.yaml` — one per agent seat: `name`, `handle`,
     `kind: agent`, `writing_style`, `personality`, `talents`, `skills`. No
     `model`, no `tools` (those live on the role —
     `internal/identity/identity.go:19`).
   - `teams/<name>.yaml` — members (identity+role) and `collaborations`. Each
     review edge is written with explicit direction `from: <worker-role>`,
     `to: <reviewer-role>`; the skill does not emit a bidirectional edge.
   - `ethos.yaml` — `agent`, `team`, and `resolution` keys.
7. **Validate the staged set, then swap.** Run `ethos doctor` against the
   staged closure. On a clean result, swap the staged files into place as the
   last step (an all-or-nothing cutover). On any `doctor` failure, leave the
   working tree untouched, restore from the backup if a partial write already
   happened, report the failure, and stop. The operator's existing working
   team survives a mid-write error — there is no window where the team
   references files that do not exist.
8. **Destructive reconcile — a SEPARATE, explicitly-consented step.**
   `ethos vendor --team <t> --apply --prune` removes
   files from the vendored closure. These are destructive and are NOT covered
   by the step-5 ratification. The skill runs them **without `--yes`**, so the
   user sees and answers each tool's own prompt, AND the skill first asks its
   own `AskUserQuestion` ("Reconcile now? This deletes obsolete trace and
   prunes vendored files") before invoking either. Repair runs only when
   re-shaping left obsolete residue; `vendor --prune` runs only in a
   `resolution: repo-only` repo. On "no," the skill leaves both to the user.
9. **Regenerate is not the skill's job.** The skill tells the user to
   restart Claude Code; SessionStart regenerates `.claude/agents/` from the
   YAML the skill wrote. The skill never touches those generated files
   (principle 9).

## Greenfield vs optimize-in-place

**Greenfield (no team yet).** The skill offers to run `ethos setup` first to
lay down the starter scaffold, then runs the full interview and writes a
fresh roster. All seats are additions; the budget table shows the full
proposed cost against zero.

**Optimize-in-place (existing team).** The skill reads the current team and
roles, runs the interview, and produces a **diff**: seats to keep, seats to
add, seats to prune, and model-tier changes. The budget table shows current
cost, proposed cost, and the delta. Pruning removes the member from the team
YAML (and optionally the role/identity files, on an explicit confirm); the
agent file disappears on the next SessionStart regeneration. The skill never
deletes a generated `.claude/agents/` file directly — removing the source
and regenerating is the only path (principle 9). After any prune in a
`resolution: repo-only` repo, `ethos vendor --apply --prune` reconciles the
vendored closure so no orphaned role or identity remains.

## How the slash command reaches the skill

`/ethos:optimize-team` is a `plugin/commands/optimize-team.md` file whose
body instructs the model to invoke the `optimize-team` skill. Claude Code
loads the skill from `~/.claude/skills/optimize-team/SKILL.md`, where
`ethos seed` placed it from `internal/seed/sidecar/skills/optimize-team/`.
Shipping the skill in the seed sidecar — rather than only inside the plugin —
is what lets it reach **any** repo: `ethos seed` deploys the sidecar skills
globally, so a repo that has never installed the ethos plugin still gets the
skill once `ethos seed` has run. The command pair is the convenience launcher;
the sidecar skill is the portable payload.

### Implementation blast radius (deployment is not automatic)

A new sidecar skill does **not** deploy just by adding its directory. Two
hardcoded enumerations gate it, and both must be edited or `ethos seed`
silently never deploys the skill and the "any repo" claim is false:

1. `internal/seed/embed.go:29` — the `//go:embed` directive for the `Skills`
   FS is an explicit, by-name list (`baseline-ops`, `mission`,
   `create-from-project` SKILL.md). Add
   `sidecar/skills/optimize-team/SKILL.md` to it, or the file is not embedded
   in the binary at all.
2. `internal/seed/seed.go:168-173` — deployment is three hardcoded
   `s.seedFile(Skills, ...)` calls, not a directory walk. Add a fourth call
   near line 174 that deploys `sidecar/skills/optimize-team/SKILL.md` to
   `filepath.Join(skillsRoot, "optimize-team", "SKILL.md")`.

These two edits (plus the `README.md` catalog row under
`internal/seed/sidecar/skills/`) are part of the implementation mission's
write-set, alongside the three skill/command files and the `fable`
`ModelAliases` prerequisite named above.

## What this skill does not do

- Does not add a CLI subcommand to the `ethos` binary.
- Does not write or edit any file under `.claude/agents/` (principle 9).
- Does not emit any value `ValidateModel` rejects; it emits `fable` only
  after the prerequisite `ModelAliases` change lands.
- Does not implement the `role.go` `ModelAliases` change itself — that is a
  separate prerequisite mission this skill depends on.
- Does not write any YAML before the user ratifies.
- Does not invent seats the interview did not justify (principle 8).
