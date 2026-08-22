# GStack Debug

Systematic root-cause debugging workflow, distilled from
`garrytan/gstack`'s `investigate` skill (origin: garrytan/gstack,
SPDX-License-Identifier: MIT).

## Iron law

No fixes without root-cause investigation first. Fixing symptoms
creates whack-a-mole debugging: every fix that doesn't address the root
cause makes the next bug harder to find.

## Phase 1: root-cause investigation

1. Collect symptoms — error messages, stack traces, reproduction steps.
   Ask one clarifying question at a time if the report is incomplete.
2. Read the code — trace the path from symptom back to potential
   causes; do not guess from the error message alone.
3. Check recent changes — `git log --oneline -20 -- <affected-files>`.
   A regression means the root cause is in the diff, not in old code.
4. Reproduce deterministically before forming a hypothesis. If you
   can't reproduce it, gather more evidence first.
5. Check for recurrence — a bug recurring in the same area across
   sessions is an architectural smell, not a one-off.

State a specific, testable root-cause hypothesis before touching any
code: "Root cause hypothesis: ..." — not "might be related to X."

## Scope lock

After forming the hypothesis, restrict edits to the narrowest directory
containing the affected files. This prevents an investigation from
drifting into unrelated refactoring while chasing a bug.

## Phase 2–4: pattern, hypothesis, implementation

- Check whether the symptom matches a known class of bug (off-by-one,
  race condition, stale cache, missing null check) before assuming this
  case is novel.
- Test the hypothesis with the smallest possible probe before writing
  the fix — a hypothesis that survives a cheap test is worth fixing;
  one that doesn't sends you back to phase 1.
- Implement the fix at the root cause, not at the symptom's call site,
  unless the root cause is genuinely out of scope (say so explicitly if
  so).

## Phase 5: verification and report

Verify the original symptom no longer reproduces, not just that the
code compiles or a related test passes. Report using one of: DONE (with
evidence), DONE_WITH_CONCERNS (list them), BLOCKED (state the blocker),
or NEEDS_CONTEXT (state exactly what's missing).
