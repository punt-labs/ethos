You are the pinned evaluator on ethos mission m-2026-07-21-010 (implement archetype). Working directory: <repo>. Your lens: compatibility, upgrade paths, shell portability.

Evaluate branch fix/hook-chain-seal (3 commits: a1349f9 installer chaining, 72fd8d9 doctor check + fixtures, 2d0b478 docs). Contract: ethos mission show m-2026-07-21-010. Worker report: .tmp/missions/results/m-2026-07-21-010-r1-report.md. The bug: install.sh's no-clobber skip meant the DES-058 pre-commit seal hook never installed on machines where beads owns pre-commit (all org machines).

Verify empirically, not just by reading:
1. Chaining correctness: run the installer's hook-install path against throwaway git repos covering: no existing hook (installs standalone); foreign hook present (appends marker section ONCE; re-run idempotent; host content byte-untouched above the markers); our standalone hook present (recognized, updated in place); the hand-appended interim section shape from THIS repo's real .git/hooks/pre-commit (recognized and upgraded, not duplicated — this machine is the live case).
2. The fall-through hazard: a host hook that ends with unconditional `exit` bypasses the appended section — does the installer detect/warn as the contract requires (or is the limitation documented)?
3. Doctor check: present/missing/stale cases; and that the check FIRES on a repo whose hook lacks the seal section (the exact production gap).
4. Shell portability: POSIX sh, no bashisms, shellcheck clean (run it).
5. make check green (run it). The three out-of-envelope files (cmd/ethos tests, internal/mcp test, docs/audit-seal.md) were leader-authorized — verify they're consistent, don't re-report the envelope question.
6. commit-msg: worker fixed the same flaw there preemptively; verify the fix didn't change behavior for the common empty-slot case.

Write your review to .tmp/missions/results/m-2026-07-21-010-eval-rsc.md — verdict first (APPROVE / APPROVE WITH NAMED EDITS / ITERATE), numbered findings with severity and file:line, concrete fixes. Reply exactly "written".