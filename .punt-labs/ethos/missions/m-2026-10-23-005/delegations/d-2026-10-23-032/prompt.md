You are the worker for ethos mission m-2026-10-23-005: the final design-text pass for DES-078, the OpenCode plugin (bead ethos-5y4r, PR #536). Read the contract with `ethos mission show m-2026-10-23-005`. Each criterion is a leader decision; apply it as written.

Repository <repo>, branch `design/opencode-plugin-adapter`, already checked out. Write set: `docs/design-opencode-plugin.md` and `DESIGN.md`.

Authority: `docs/spec-opencode-lifecycle.tex`, model-checked and accepted by the evaluator jra. Read its ledger, its consequences list (the design sections that must change), and its limits, including N16–N18. Where the design text and the spec disagree, the spec wins. Cite the spec for every amendment. Verify every code citation against source.

The contract also carries review findings that the spec does not cover:
- B2: the mission store root is recorded in the pending record;
- the code-reviewer items;
- F4/F5/F7/F8/F12;
- the rsc wording items;
- a grep for superseded claims.

Apply all of them. If a criterion is impossible as stated, stop and report it.

Git rules:
- Run `make check` before each commit.
- Commit by explicit path only: `git commit -m "<msg>" -- docs/design-opencode-plugin.md DESIGN.md`.
- End each commit message with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Never stage, unstage, or restore any other file.
- Commit on your branch; do not push, do not touch PRs or merge — the leader does that.

Submit the round-1 result through ethos with a per-criterion table and the commit SHAs. Your final report repeats both.