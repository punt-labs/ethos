You are the worker on ethos mission m-2026-07-21-001 (implement archetype). Working directory: <repo>. Start with `ethos mission show m-2026-07-21-001` — the contract is authoritative for scope, write set, and the 12 success criteria.

Task: implement DES-058 phase 1 — the live audit write path split from the sealed committed record. The merged design docs/audit-seal.md is the complete specification (read it end-to-end first, plus the DES-058 entry at the tail of DESIGN.md); the contract's criteria restate its load-bearing requirements. Phase 2 (lock relocation) is explicitly out of scope.

Working rules:
- Branch off main: feat/des-058-audit-seal-impl.
- TDD: failing tests first per component, then the code.
- Commit per logical step; every commit passes make check; never more than 30 minutes uncommitted.
- Never run make install; build to .tmp/ethos and dogfood the real flow (criterion 10 requires pasted expected-vs-actual output).
- Do not commit the stale working-tree artifacts (July-11 mission dirs, .lock files, quarry captures) — your commits touch only your write set.
- If the write set is missing a file you need, stop and submit verdict=escalate with the specific path — do not write outside the envelope.
- No lint/type suppressions without citing a pre-authorized standard class.

When done (or at a round boundary), submit your result: ethos mission result m-2026-07-21-001 --file <result.yaml> (round: 1, author: bwk, verdict, confidence, files_changed with added/removed counts, evidence naming each success criterion with pass/fail plus the make check and dogfood outputs, prose). Consider --verify --base main to cross-check your files_changed accounting.

The message relay drops long final replies: write your full completion report to .tmp/missions/results/m-2026-07-21-001-r1-report.md and make your final message just "written" plus the branch name and result status.