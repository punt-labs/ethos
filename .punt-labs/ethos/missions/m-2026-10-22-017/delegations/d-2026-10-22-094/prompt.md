You are the worker for ethos mission m-2026-10-22-017: round 5 of the OpenCode V2 plugin/adapter design (bead ethos-5y4r, PR #536). Read the full contract first with `ethos mission show m-2026-10-22-017`. Its success criteria are authoritative.

Repository: <repo>, branch `design/opencode-plugin-adapter`. It is already checked out. Do not create or switch branches, and do not touch `main`.

Write set: `docs/design-opencode-plugin.md` and `DESIGN.md`.

Steps:
1. Fetch the open PR #536 review threads read-only, using the `gh api graphql` query in the contract context.
2. Read the design doc and DES-078 in full before editing.
3. Verify each of the 11 design findings against real source before you amend the design or rebut the finding. A rebuttal needs a file:line citation.
4. Apply the operator's enablement ruling exactly as the contract states it:
   - With no harness named and an interactive terminal, `ethos enable` prompts for the harnesses, with both claude and opencode preselected.
   - The explicit forms skip the prompt.
   - With no harness named and no terminal, it exits with a usage error naming both harnesses.
   - `ethos disable` mirrors this.

Git rules:
- Run `make check` before each commit and record each result.
- Commit by explicit path only: `git commit -m "<msg>" -- docs/design-opencode-plugin.md DESIGN.md`.
- Other files in this checkout are staged or modified. Never stage, unstage, restore, or commit them.
- End each commit message with `Co-Authored-By: Claude Opus 5.5 <[redacted-email]>`.
- Commit on your branch; do not push, do not touch PRs or merge — the leader does that. Do not reply to or resolve review threads.

If a finding forces a genuine design choice the contract does not settle, stop and report it. Do not decide it yourself.

When done, submit the round-1 result through ethos. Its evidence must include:
- a per-thread disposition table: thread ID, fixed or rebutted, design section or citation;
- commit SHAs;
- `make check` outcomes.

Final report: the same table, the SHAs, and any unresolved question.