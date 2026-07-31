Root-cause investigation ONLY — do NOT change any code, do NOT commit, do NOT open a PR. Diagnose and report back with a written analysis. This is bead ethos-q6e2 in the ethos repo (/Users/jfreeman/Coding/punt-labs/ethos, branch main).

SYMPTOM: Every commit in this repo runs the ethos pre-commit audit-seal step, and it prints ~48 warnings of this form (one per affected mission), covering ~45 distinct missions:

  "session c7e50ab0-10d9-4b09-9402-db32cfe8f2da wrote mission-log lines for mission m-2026-07-20-013 but its mission live log is gone; unsealed mission-log lines were lost"

For a system whose entire thesis is an immutable, trustworthy audit trail, a message on every commit saying audit lines are "lost" is alarming and must be explained precisely.

YOUR JOB — answer these, with file:line citations and evidence:
1. WHERE the warning is emitted (grep the exact string; likely internal/hook/audit_seal.go or the audit seal path) and the exact code condition that triggers it. Quote the code.
2. WHAT "mission live log" means vs "mission-log lines," and WHY the live log is "gone" for these ~45 missions. Trace the lifecycle: where the live log is written, where it's expected at seal time, and what removes/relocates/rotates it. Is this a path mismatch, a cleanup that runs too early, a relocation (e.g. the DES-054 state move to .punt-labs/ethos, or the self-standing-repos change #410), or genuine deletion?
3. WHETHER data is actually lost or the warning is misleading. Concretely: are the "unsealed mission-log lines" recoverable/preserved elsewhere (already-sealed chunks, the mission's own directory, git history, the missions.jsonl trace), or are they truly discarded? Prove it — show where the lines exist or show that they're dropped on the floor.
4. WHY it's ~45 missions specifically — what do those missions have in common (age, a schema/layout migration boundary, closed-status, a particular directory)? Look at the actual on-disk state under .punt-labs/ethos/missions/ and cross-reference the warned mission IDs.
5. Is this a REGRESSION (a recent change orphaned old live logs) or has it always been this way? Check git log/blame on the seal code and the state-layout changes (DES-054 #334, self-standing repos #410, redaction #411).

DELIVERABLE: a tight root-cause report — the mechanism, whether data is genuinely lost (yes/no with proof), the trigger population, regression-or-not, and a RECOMMENDED fix direction (do not implement it) with the tradeoffs. If the honest answer is "the warning is cosmetic and no data is lost," say that plainly with proof; if it's "real audit lines are being discarded," say that with equal clarity and flag severity. Cite the ADRs (DESIGN.md) governing audit seal and mission-log lifecycle where relevant. Report via your return value.