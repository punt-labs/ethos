You are the worker for ethos mission m-2026-10-22-046: the final reconciliation of the DES-078 OpenCode design (bead ethos-5y4r, PR #536). Read the contract with `ethos mission show m-2026-10-22-046`. Each criterion is a leader decision; apply it as written.

Repository <repo>, branch `design/opencode-plugin-adapter`, already checked out. Write set: `docs/design-opencode-plugin.md`, `DESIGN.md`, and `docs/spec-opencode-lifecycle.tex` (two small note edits only).

Authority: `docs/spec-opencode-lifecycle.tex` is a model-checked Z spec, accepted by evaluator jra. Read it first, especially the counterexample ledger and the section that lists which design sections must change. Wherever the design prose disagrees with the spec, the spec wins. Cite the spec for every amendment you bring into the design. Verify every code citation against source. If a criterion is impossible as stated, stop and report it.

Git rules:
- Run `make check` before each commit.
- Commit by explicit path only: `git commit -m "<msg>" -- <write-set paths you changed>`.
- End each commit message with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Never stage, unstage, or restore any other file.
- Commit on your branch; do not push, do not touch PRs or merge — the leader does that.

Submit the round-1 result through ethos with a per-criterion table and the commit SHAs. Your final report repeats both.