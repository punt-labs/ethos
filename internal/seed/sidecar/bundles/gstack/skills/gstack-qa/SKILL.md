# GStack QA

Manual/exploratory QA workflow with a scored health rubric, distilled
from `garrytan/gstack`'s `qa` and `qa-only` skills (origin:
garrytan/gstack, SPDX-License-Identifier: MIT).

## Health score rubric

Score each category 0–100, starting from 100 and deducting per finding:
critical −25, high −15, medium −8, low −3 (floor 0). Weight and combine:

| Category | Weight |
|----------|--------|
| Functional | 20% |
| Accessibility | 15% |
| UX | 15% |
| Console errors | 15% |
| Links | 10% |
| Performance | 10% |
| Visual | 10% |
| Content | 5% |

`score = Σ (category_score × weight)`. Console errors: 0 → 100, 1-3 →
70, 4-10 → 40, 10+ → 10. Links: 100 minus 15 per broken link.

## Workflow

1. Establish a baseline: run the existing test suite first — QA that
   contradicts a passing test suite needs to reconcile that gap, not
   ignore it.
2. Exercise the actual user paths, not just the code paths: click
   through navigation rather than only checking routes exist.
3. Check console/network for silent errors on every page visited —
   these are invisible to a human tester without looking.
4. Score each category per the rubric above; do not skip a category
   because it "seems fine" — score it explicitly.
5. Triage findings: repro steps and a screenshot/evidence for every
   issue — no exceptions. An unreproduced finding is not yet a finding.
6. Fix loop: for report-only QA, stop after triage. For QA-with-fixes,
   fix the highest-severity issues first, then re-run the health score
   to confirm improvement.

## Framework-specific checks

- **SPA (React/Vue/Angular)**: test back/forward navigation and stale
  state after navigating away and back — client-side routing hides bugs
  that server-rendered navigation would surface.
- **Server-rendered apps**: check for hydration/mismatch warnings and
  flash-message dismissal behavior.

## Important rule

Every issue needs at least one piece of concrete evidence (screenshot,
console log, reproduction steps). A QA report with unverified claims is
worse than no report — it misdirects the next person who reads it.
