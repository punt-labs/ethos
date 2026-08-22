# GStack Review

Code review workflow with calibrated confidence scoring, distilled from
`garrytan/gstack`'s "review army" specialist-dispatch pattern (origin:
garrytan/gstack, SPDX-License-Identifier: MIT).

## Critical-pass categories

Review the diff against these, in priority order: SQL and data safety,
race conditions and concurrency, trust-boundary violations (never treat
LLM/user output as trusted input), shell injection, and enum/value
completeness (a new enum value, status, or tier must be handled
everywhere its siblings are — this requires reading code OUTSIDE the
diff, via Grep, not just the diff itself).

## Confidence calibration — every finding gets a score

| Score | Meaning | Action |
|-------|---------|--------|
| 9–10 | Verified by reading the specific code; concrete bug demonstrated | Report normally |
| 7–8 | High-confidence pattern match | Report normally |
| 5–6 | Moderate; could be a false positive | Report with an explicit "verify this" caveat |
| 3–4 | Low confidence, suspicious but may be fine | Do not put in the main report |
| 1–2 | Speculation | Only report if severity would be critical |

## Pre-emit verification gate

Before promoting any finding, quote the specific line(s) that motivate
it — file:line plus the verbatim text. "Field X doesn't exist on model
Y" requires quoting where the field would live. If you cannot quote the
motivating line, the finding is unverified: do not report it at
confidence 7+. This single gate kills the most common false-positive
class ("this field/method doesn't exist" claims made without reading the
class body).

## Finding format

`[SEVERITY] (confidence: N/10) file:line — description`

Example: `[P1] (confidence: 9/10) app/models/user.rb:42 — SQL injection
via string interpolation in where clause`

## Workflow

1. Read the diff as a whole first, not file-by-file — integration
   issues are invisible at the file level.
2. Apply the critical-pass categories above.
3. Verify each finding against the pre-emit gate before including it.
4. Group findings by severity, not by file — a reviewer scanning for
   blockers should not have to page through low-severity notes first.
5. Recommend fixes with the same search-before-recommending discipline
   as any other skill: verify the fix pattern is current best practice
   before suggesting it.
