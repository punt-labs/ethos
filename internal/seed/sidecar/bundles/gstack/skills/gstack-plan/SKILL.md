# GStack Plan

Discovery-and-planning workflow: turn a rough idea into a reviewed plan
before any code changes. Distilled from `garrytan/gstack`'s
`plan-eng-review`, `plan-design-review`, `autoplan`, and `plan-tune`
skills (origin: garrytan/gstack, SPDX-License-Identifier: MIT).

## Scope gate — ask before reviewing

Before reading any code or running any review, confirm what to review:
the current branch diff, a plan or design doc, or a specific path. Do
not guess. If a design doc already exists for this branch, read it
first and treat it as the source of truth for problem statement,
constraints, and chosen approach.

## Step 0: scope challenge

Before deep review, answer:

1. What existing code already solves each sub-problem? Reuse before
   building parallel paths.
2. What is the minimum change that achieves the goal? Flag deferrable
   work; be ruthless about scope creep.
3. Complexity smell: more than ~8 files or 2+ new classes/services in
   one plan is a signal to challenge the shape, not a hard limit.
4. Search before building: does the framework/runtime already have a
   built-in for this pattern? Is this current best practice? Are there
   known pitfalls? Prefer proven mechanisms over custom ones.
5. Completeness check: with AI-assisted implementation, full test
   coverage and edge-case handling is cheap. A plan proposing a
   shortcut that saves only minutes of AI time should usually do the
   complete version instead.

## Cognitive patterns for review

Apply these instincts, not as a checklist but as a lens:

- **State diagnosis** — is the team falling behind, treading water,
  repaying debt, or innovating? Each state wants a different fix.
- **Blast radius** — what's the worst case, and how many systems or
  people does it touch?
- **Boring by default** — spend novelty budget on the one thing that
  actually needs it; everything else should be proven technology.
- **Incremental over revolutionary** — strangler fig over big-bang
  rewrite; feature flags and canaries over global rollout.
- **Systems over heroes** — design for a tired engineer at 3am, not
  your best person on their best day.
- **Reversibility** — prefer decisions that are cheap to undo.
- **Essential vs. accidental complexity** — does this solve a real
  problem, or one the plan just created?

## Priorities under pressure

If context is tight, do not skip: Step 0 (scope challenge) and an
opinionated recommendation for every open question. Everything else can
compress before those two.

## Output

Work through review sections one at a time (architecture, code quality,
tests, performance where relevant), name concrete tradeoffs for every
issue, give an opinionated recommendation, and ask before assuming a
direction on anything non-obvious. Once the user accepts or rejects a
scope call, commit to it — do not re-litigate scope later in the same
review.
