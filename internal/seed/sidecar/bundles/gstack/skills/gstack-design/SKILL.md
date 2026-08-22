# GStack Design

Design-quality workflow: a designer's eye for a plan, mockup, or shipped
UI. Distilled from `garrytan/gstack`'s `design-consultation`,
`design-shotgun`, `design-html`, and `design-review` skills (origin:
garrytan/gstack, SPDX-License-Identifier: MIT).

## Scope gate

Before reviewing, confirm the target: a design system built from
scratch, a specific plan/mockup, or a shipped page/flow. Do not guess
which mode applies.

## Design principles

- Prefer consistency over novelty: reuse existing tokens, spacing, and
  component patterns before inventing new ones.
- Every visual decision should map to a user outcome — clarity, trust,
  reduced friction — not taste alone.
- Accessibility is not a separate pass; check contrast, focus order, and
  label text as part of the same review.
- Empty states, loading states, and error states are part of the design,
  not an afterthought — review all three, not just the happy path.

## Cognitive patterns

- **How users actually behave**: they scan, they don't read; they act on
  the first plausible affordance, not the best one. Design for scanning.
- **Priority under pressure**: when compressing a review, keep the
  scope gate and the opinionated recommendation; drop exhaustive
  per-pixel notes first.
- **0–10 rating**: score a design's readiness (0 = not started, 10 =
  ship-ready) and say why, not just a verdict — the score should track a
  concrete list of what's missing.

## Workflow

1. Confirm scope (gate above).
2. If reviewing a plan/mockup with no shipped UI yet, produce or review
   visual mockups before code — catching a layout problem at the mockup
   stage is far cheaper than after implementation.
3. Walk the review by section: information architecture, visual design,
   interaction/motion, accessibility, content/copy.
4. For every finding, name the concrete user-facing consequence and an
   opinionated fix — not just "this looks off."
5. Close with a readiness score and the specific gaps that would raise
   it.

## Output

State findings plainly, one issue per line where possible, and always
pair a finding with a recommendation. Ask before assuming a direction on
anything that trades off scope, timeline, or established patterns.
