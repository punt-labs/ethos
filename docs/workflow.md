# Engineering Workflow: Human-Agent Team

How the Punt Labs engineering team operates with human leadership and
AI agent execution.

## Roles

| Role | Who | Responsibility |
|------|-----|----------------|
| CEO | Jim Freeman (jfreeman) | Direction, strategy, approvals |
| COO / VP Eng | Claude Agento (claude) | Decompose, spec, delegate, review, integrate, ship |
| Go Specialist | Brian K (bwk) | Implement Go code and tests per spec |

The CEO sets direction. The COO runs execution — planning work,
writing specs, delegating to specialist agents, reviewing their output,
and driving through PR cycles to merge. Specialist agents implement
code to spec and do not make architectural decisions.

## The Delegation Loop

Every implementation task follows this loop:

```text
1. Spec    → COO writes detailed spec with acceptance criteria
2. Delegate → COO launches specialist agent with the spec
3. Implement → Agent writes tests first, then code, runs make check
4. Review   → COO launches code-reviewer agent on the output
5. Fix      → If findings: COO writes fix spec, delegates back to agent
6. Repeat   → Steps 4-5 until reviewer reports clean
7. Commit   → COO verifies make check, commits, closes bead
```

### Spec Quality

The spec is the most important artifact. A good spec eliminates
review-fix cycles. A bad spec causes 3+ rounds of fixes.

A spec must include:

- **Files to read first** — the agent must understand context before writing
- **Files to modify** — explicit scope, nothing outside it
- **Tests to write** — described before the implementation
- **Acceptance criteria** — measurable conditions for "done"
- **Rules** — constraints (no cd, one command per Bash call, etc.)

### What the COO does vs delegates

| COO does directly | COO delegates to agents |
|-------------------|------------------------|
| Architecture decisions | Go/Python implementation |
| Spec writing | Test writing |
| Cross-file integration | Single-package work |
| Migration/operational work | Code review |
| Documentation | — |
| PR cycle management | — |
| Release management | — |

**Rule**: The COO does not implement Go code. If a worktree fails or
delegation breaks, fix the delegation — don't fall back to doing it
yourself.

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

Beads with dependencies form a chain:

```text
ethos-6ym → ethos-q4b → ethos-d24 → ethos-wy8 → ethos-6fy → ethos-duz
```

Each bead maps to one or more commits. The commit message references
the bead ID with `Closes ethos-XXX`.

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

## Lessons Learned (Session 2026-03-22/23)

### What worked

- Spec quality improved with each round — precise specs = fewer review cycles
- Review-fix cycles caught real bugs (voice migration to wrong layer,
  single-layer ValidateRefs, attribute stores only seeing one root)
- Role separation forced better upfront thinking about edge cases
- CEO corrections kept the workflow on track ("don't implement yourself",
  "are you going to use bwk?")

### What to improve

- **Write specs that anticipate reviewer concerns.** Include cross-layer
  validation, error handling strategy, and PII boundaries upfront.
- **Never use worktree isolation for implementation agents.** Direct
  branch work only — worktrees auto-clean and can lose uncommitted work.
- **Run architect review before starting implementation**, not after
  the design is drafted.
- **Delegate operational work too**, not just Go code.
- **Automate Copilot review requests** — configured in repo rulesets
  so it's not a manual step that can be forgotten.
