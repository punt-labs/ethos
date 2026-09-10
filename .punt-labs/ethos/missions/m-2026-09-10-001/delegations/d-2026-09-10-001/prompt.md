You are the worker on ethos mission m-2026-09-10-001. Read the full contract first:

    ethos mission show m-2026-09-10-001

Work in <repo> on branch `fix/doctor-health-checks` (already created, clean, based on main at the v4.18.0 release). Do NOT create or switch worktrees or branches.

The contract's success criteria are binding. Four things worth restating because they are the ones most likely to be skipped:

1. **Read the triage notes on the beads before their original text.** `bd show ethos-bfml ethos-hy40 ethos-kcbv ethos-e05k ethos-jw1z`. Each carries a note dated 2026-09-10 written after I verified it against 4.18.0 source. **Two filings are corrected there.** `ethos-hy40` says "doctor checks only the seal hook" — that is FALSE now; `trailerHookSpec` exists and `CheckHookCurrency` runs over both. Building to the filed text would produce the wrong fix.

2. **`bfml` and `hy40` are one defect.** Three PASSes on a repo with no hooks, and the commit-msg side of the same gap. Fix together.

3. **Do not make `CheckHookCurrency` fail on absence.** That PASS is deliberate and documented in its own doc comment, and correct for a *currency* check — a dormant repo must not be told its absent hooks are stale. The gap is PRESENCE: seal presence when enabled is checked, trailer presence is not, and nothing composes "enabled AND missing" into a failure.

4. **Convert `hasActiveSealCall` to execution-based verification first (`ethos-kcbv`), then build presence on the converted detector.** It is still lexical — `SplitKeepEnds` + `HeredocMask` + `stripInlineComment` + regex — with four documented patch rounds visible in the code, and a stop-loss already declared. Adding a fifth consumer to it is building on the thing that keeps breaking. If you conclude execution-based verification is genuinely infeasible here, STOP and report why with evidence rather than adding a fifth lexical patch.

**On tests, specifically for this bead.** Every new or changed check needs a test confirmed FAILING against pre-fix code, with the pre-fix output pasted. That is not boilerplate here: the defect you are fixing *is* a check that reports success without verifying, and the immediately preceding branch had five tests caught guarding their own fixture rather than their stated property — including a drift guard added to prevent exactly that, and a regression test that could not detect its own fix being deleted. A test for a health check that has never been observed failing is the same bug one level up.

**Before you commit, state what your change makes worse, if anything.** If the honest answer is "nothing," say what you checked to conclude that. A limitation disclosed in your report but not in the code comment is functionally undisclosed — that exact miss cost a round on the last branch.

Report when done. Do not push — I push and drive the PR.