You are the worker for ethos mission m-2026-10-22-042, the successor to your m-2026-10-22-031. Read the contract with `ethos mission show m-2026-10-22-042`; it is authoritative.

Repository <repo>, branch `design/opencode-plugin-adapter`, already checked out. Write set: `docs/spec-opencode-integration.tex` and `docs/spec-opencode-lifecycle.tex`, which is a new file.

Evaluator jra rejected round 1 for two blocking reasons:
- **B1:** the `leaked` ghost is set only in the fix=zfalse branches, so ReconcileOnExit holds by construction. jra demonstrated this with mutants.
- **B2:** the embedded model regressed the Registry validation from a certified-complete 3862 transitions to 14692 and left uncovered operations.

The fixes:
- Split the model into its own file.
- Make the ghost uniform in both branches and prove it with mutants.
- Add the N2–N9 text.

jra's reproduction scripts are in `.tmp/jra-eval/` and your earlier scratch is in `.tmp/lc/`. Reuse both.

One leader ruling: C7 is accepted. A roster with a live numeric participant is never purged.

Git rules:
- Run `make check` before each commit.
- Commit by explicit path only: `git commit -m "<msg>" -- docs/spec-opencode-integration.tex docs/spec-opencode-lifecycle.tex`.
- End each commit message with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Never stage, unstage, or restore any other file.
- Commit on your branch; do not push, do not touch PRs or merge — the leader does that.

Submit the round-1 result through ethos with full probcli evidence: commands, bounds, states, depth, outcomes, and mutant results. Your final report repeats it.