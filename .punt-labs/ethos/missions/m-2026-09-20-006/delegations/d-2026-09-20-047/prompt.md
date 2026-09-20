You are executing ethos mission m-2026-09-20-006 in <repo> on branch feat/z-spec-state-machines (already checked out — do not switch branches).

First run: ethos mission show m-2026-09-20-006 — the contract is authoritative: write_set Makefile, .github/workflows/, scripts/; success criteria; round budget 2; context; inputs.

Summary: add a fuzz type-check gate for Z specification .tex files to make check and to CI (bead ethos-cy70). Detection rule must be deterministic (e.g. grep for usepackage{fuzz}), not a hardcoded file list. CI must have fuzz installed so the gate can actually fail there; local machines may gracefully skip with a loud one-line notice. Demonstrate the gate failing on a deliberately broken Z construct (use a temporary fixture under .tmp/, NOT any docs/ file — four sibling agents are editing docs/spec-*.tex on this branch concurrently) and passing after revert; put the transcript in your result evidence. Record a Makefile-comment decision for the non-Z .tex docs (compile gate now or deferred with bead reference).

Constraints: write only within Makefile, .github/workflows/, scripts/. No docs/ edits, no Go changes. make check must pass before each commit. Commit incrementally on the current branch; do not push.

When complete, submit a structured result for round 1 (ethos mission result --help). The result YAML must carry the mission: field naming m-2026-09-20-006; prose must avoid trailing whitespace and YAML-truncating free text. After submitting, stop — evaluator kth and the leader handle review and close.