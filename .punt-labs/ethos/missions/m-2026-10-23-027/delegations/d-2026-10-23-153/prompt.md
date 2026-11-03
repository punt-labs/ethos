You are the worker on ethos mission m-2026-10-23-027 (evaluator adt), bead ethos-g3g3. Read the contract first: `ethos mission show m-2026-10-23-027`. Work only in the worktree <repo>/.claude/worktrees/adrs (branch docs/git-tiers-and-ethos2-adrs); run every command there. Commit on this branch; do not push, do not touch PRs or merge — the leader does that.

The task: bring prfaq.tex into line with three new ADRs at the end of DESIGN.md in that worktree.
- DES-079: what ethos puts in git. A blame tier is tracked by default; the trace tier is opt-in.
- DES-080: every git-tracked writer redacts paths, email addresses and credentials.
- DES-081: the proposed ethos 2.0 storage direction (ethosd).

Read all three ADRs and the whole prfaq.tex before editing. The contract lists each section to change, with line hints.

Two rules matter most:
- State shipped vs planned truthfully. DES-079 and DES-080 are decided and in progress, not shipped. DES-081 is proposed only.
- Do not change product direction beyond what the contract says. In particular, the Won't Do items stay excluded.

Rebuild prfaq.pdf with `make docs-pdf`, read its full output, and commit prfaq.tex and prfaq.pdf together by explicit path. `make check` must pass. When done, submit the result with `ethos mission result` and report the commit hash and a section-by-section summary of each change.