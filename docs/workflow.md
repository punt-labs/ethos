# Engineering Workflow: Human-Agent Team

How the Punt Labs engineering team operates with human leadership and
AI agent execution. The team, roles, and reporting structure are
formally specified in [docs/teams.tex](teams.tex) and managed via the
[punt-labs/team](https://github.com/punt-labs/team) shared identity
registry.

## The Team

| Handle | Name | Role | Responsibility |
|--------|------|------|----------------|
| jfreeman | Jim Freeman | CEO | Direction, strategy, approvals |
| claude | Claude Agento | COO / VP Eng | Decompose, spec, delegate, review, integrate, ship |
| bwk | Brian K | Go specialist | Implement Go code and tests per spec |
| mdm | Doug M | CLI specialist | CLI design, help text, composability, output formatting |
| rmh | Raymond H | Python specialist | Implement Python code and tests per spec |
| djb | Dan B | Security engineer | Threat modeling, credential audit, input validation |
| adt | Alan T | PM (grounding) | Product roadmap for Z Spec, PR/FAQ, Use Cases, Refactory |
| ghr | Grace H | PM (building blocks) | Product roadmap for Quarry, Biff, Vox, Lux, Tally |
| edt | Edward T | UX designer | Lux elements, dashboards, website, CLI output design |
| ach | Alex H | Finance & ops | Accounting, compliance, governance, billing |
| adb | Ada B | Infra engineer | CI/CD, deployment, cross-repo tooling, NATS relay |

Reporting structure:

```text
jfreeman (CEO)
  ├─ claude (COO)
  │    ├─ bwk (Go specialist)
  │    ├─ mdm (CLI specialist)
  │    ├─ rmh (Python specialist)
  │    ├─ djb (Security engineer)
  │    └─ adb (Infra engineer)
  ├─ adt (PM grounding)
  └─ ghr (PM building blocks)
       └─ edt (UX designer) [collaborates with ghr]
```

The CEO sets direction. The COO runs execution — planning work,
writing specs, delegating to specialist agents, reviewing their output,
and driving through PR cycles to merge. PMs report to the CEO on
product direction and to the COO on execution. Specialist agents
implement to spec and do not make architectural decisions.

## Persona Animation

Each agent's identity is defined in the
[team repo](https://github.com/punt-labs/team) — personality, writing
style, and talents. Ethos hooks inject this behavioral content
automatically at lifecycle events:

1. **SessionStart** — injects the primary agent's full personality and
   writing style into session context
2. **PreCompact** — re-injects a condensed persona before context
   compression (prevents behavioral drift in long sessions)
3. **SubagentStart** — injects the matched subagent's persona at spawn
   (bwk gets Kernighan personality, mdm gets McIlroy, etc.)

Agent definitions (`.claude/agents/*.md`) define *what* the agent does.
Ethos identities define *who* the agent is. The SessionStart hook also
installs agent definitions from the team submodule into `.claude/agents/`.

See [docs/persona-animation.md](persona-animation.md) for the design.

## Teams and Roles

Teams and roles are first-class ethos concepts, formally modeled in
[docs/teams.tex](teams.tex) (Z specification — type-checks with fuzz,
animates with probcli).

- **Identity** — who you are (stable: personality, writing style, talents)
- **Role** — what you do on a team (responsibilities, permissions)
- **Team** — where you work (members with roles, scoped to repositories)

The same identity can hold different roles on different teams. The
engineering team and website team have different compositions from the
same pool of identities. Team definitions live in the shared
[punt-labs/team](https://github.com/punt-labs/team) repo, referenced
via git submodule in each project.

## The Delegation Loop

Every implementation task follows this loop:

```text
0. Standards → COO consults punt-kit/standards/* for the work type
1. Spec    → COO writes detailed spec with acceptance criteria
3. Implement → Agent writes tests first, then code, runs make check
4. Review   → COO launches code-reviewer agent on the output
5. Fix      → If findings: COO writes fix spec, delegates back to agent
6. Repeat   → Steps 4-5 until reviewer reports clean
7. Commit   → COO verifies make check, commits, closes bead
```

### Spec Quality

The spec is the most important artifact. A good spec eliminates
review-fix cycles. A bad spec causes 3+ rounds of fixes.

Before writing any spec, the COO must consult the relevant
punt-kit/standards/* documents. Standards violations that reach
review are a COO failure, not a reviewer catch.

A spec must include:

- **Files to read first** — the agent must understand context before writing
- **Files to modify** — explicit scope, nothing outside it
- **Tests to write** — described before the implementation
- **Acceptance criteria** — measurable conditions for "done"
- **Rules** — constraints (no cd, one command per Bash call, etc.)

### What the COO does vs delegates

| COO does directly | COO delegates to agents |
|-------------------|------------------------|
| Architecture decisions | Go implementation (bwk) |
| Spec writing | Python implementation (rmh) |
| Cross-file integration | CLI design and formatting (mdm) |
| Z specification modeling | Security review (djb) |
| PR cycle management | Code review (feature-dev:code-reviewer) |
| Release management | Silent failure audit (pr-review-toolkit) |
| Identity/team content creation | — |
| Documentation | — |

**Rule**: The COO does not implement Go or Python code. If delegation
breaks, fix the delegation — don't fall back to doing it yourself.

## Review Protocol

Every piece of agent-produced code goes through review before commit:

1. **Code reviewer** (`feature-dev:code-reviewer`) — bugs, logic errors,
   standards compliance, missing tests
2. **Silent failure hunter** (`pr-review-toolkit:silent-failure-hunter`) —
   swallowed errors, missing propagation, inappropriate fallbacks

Both agents run in parallel. Findings are consolidated, deduplicated,
and sent back to the implementing agent as a fix spec. The review-fix
cycle repeats until both reviewers report clean.

For major features: 2 rounds minimum (implement → review → fix → review).
For the PR itself: Copilot/Bugbot reviews add additional cycles.

## Bead-Driven Work Tracking

Every non-trivial task has a bead. The lifecycle:

```text
bd create → bd update --status=in_progress → work → bd close
```

Epics have child beads with dependencies:

```text
ethos-cpi (epic)
  ├─ ethos-cpi.1 (Role)
  ├─ ethos-cpi.2 (Team)
  ├─ ethos-cpi.3 (Team repo)
  ├─ ethos-cpi.4 (Submodule) ← depends on 1-3
  ├─ ethos-cpi.5 (Installer) ← depends on 4
  └─ ethos-cpi.6 (punt init) ← depends on 5
```

Phases that can run in parallel should be parallelized. The COO
tracks dependencies and sequences work accordingly.

## Development Lifecycle

Per `~/.claude/CLAUDE.md`, every code change follows 7 phases:

1. **Claim** — find or create bead, claim it, set plan
2. **Branch** — feature branch from main
3. **Implement & Verify** — tests first, make check, end-to-end verification
4. **Document** — DESIGN.md ADRs, README, CHANGELOG
5. **Local Review** — code-reviewer + silent-failure-hunter agents
6. **Ship** — PR, Copilot review, fix cycles, merge
7. **Close** — close beads, recap email

## Root Cause Analysis

When something goes wrong — a bug in production, a failed review cycle,
a process breakdown — apply structured root cause analysis before fixing.

### Five Whys

Ask "why did this happen?" iteratively until the root cause is found.
Each answer becomes the starting point for the next question. Typically
5 iterations, but stop when you reach an actionable systemic cause.

Example from this project:

```text
Why did the two-channel display break?
→ suppress-output.sh checked old tool names

Why were the tool names wrong?
→ The MCP tools were consolidated but the hook wasn't updated

Why wasn't the hook updated?
→ No automated check verifies hook tool names match MCP definitions

Why is there no automated check?
→ The hook is shell, the tools are Go — no shared schema

Root cause: hook-tool coupling without a validation mechanism
Action: Move hook logic to Go where the tool names are defined (DES-016)
```

### Fishbone Diagram

For complex issues with multiple contributing causes, categorize by:

- **Process** — was the workflow missing a step?
- **People** — was the delegation unclear?
- **Tools** — did automation fail or not exist?
- **Environment** — was the test environment different from production?
- **Code** — was there a logic error?

### Correction of Error (COE)

For significant incidents, write a COE document:

1. **What happened** — factual timeline with data
2. **Customer impact** — who was affected and how
3. **Root cause** — five whys analysis
4. **Corrective actions** — numbered, actionable items
5. **Lessons learned** — what to do differently

COE principles:

- **Blameless** — find the "why", not the "who"
- **Action-oriented** — every finding produces a concrete action item
- **Transparent** — share across the team so everyone learns
- **Preventive** — actions improve prevention, diagnosis, or resolution
- **Not punishment** — COE is a learning mechanism, not a penalty

## Lessons Learned

### Session 2026-03-22/23

- Spec quality improved with each round — precise specs = fewer review cycles
- Review-fix cycles caught real bugs (voice migration to wrong layer,
  single-layer ValidateRefs, attribute stores only seeing one root)
- Role separation forced better upfront thinking about edge cases
- CEO corrections kept the workflow on track ("don't implement yourself",
  "are you going to use bwk?")

### Session 2026-03-25/26

- **Never present hypotheses as root causes.** "Intermittent" is not a
  root cause. State what you know, what you don't know, and what data
  you need. Labeling guesses as findings destroys credibility.
- **Binary search for debugging.** When isolating hook errors, we
  disabled all hooks and enabled one at a time — found the 2 failing
  plugins in 10 steps. Systematic elimination beats guessing.
- **Z specification before implementation.** Modeling teams/roles in Z
  first caught design issues (tuple projection limitations, cardinality
  bounds, schema-vs-tuple patterns) before any Go code was written.
- **probcli requires cardinality bounds and schema-typed records.**
  Specs that pass fuzz can fail silently in probcli. Use `\finset`
  bounds via axdef, schemas instead of tuples, and scope all quantifiers
  to state variables (not bare given types).
- **Submodules need CI configuration.** Adding a git submodule without
  `submodules: recursive` in the checkout action breaks CI silently.
- **Fail closed on integrity checks.** When verifying referential
  integrity (e.g., role deletion checking teams), abort on errors —
  don't skip the check and proceed.
- **Phase 3 content work parallelizes with code.** While bwk implements
  Go packages, the COO writes identity content (personalities, writing
  styles, talents). No blocking dependency.
- **Commit after every delegation round.** Never accumulate unstaged
  work across multiple agent calls — another agent's `git checkout`
  wiped all unstaged changes, losing hours of work.
- **Consult standards before specifying work.** Shipped team/role MCP
  tools without PostToolUse formatters (DES-018 violation). Standards
  check is now Step 0 in the delegation loop.
- **Every MCP tool needs a slash command.** CLI, MCP, and slash commands
  must ship together — never leave one channel missing.

### What to improve

- **Write specs that anticipate reviewer concerns.** Include cross-layer
  validation, error handling strategy, and PII boundaries upfront.
- **Never use worktree isolation for implementation agents.** Direct
  branch work only — worktrees auto-clean and can lose uncommitted work.
- **Delegate operational work too**, not just Go code.
- **Automate Copilot review requests** — configured in repo rulesets
  so it's not a manual step that can be forgotten.
- **Track Copilot review rounds with auto-merge** — enable auto-merge
  early and use cron jobs to monitor, rather than polling manually.
