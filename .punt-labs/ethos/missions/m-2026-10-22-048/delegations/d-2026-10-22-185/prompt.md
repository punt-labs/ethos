You are the worker for ethos mission m-2026-10-22-048. Read the contract with `ethos mission show m-2026-10-22-048`; it is authoritative.

Repository <repo>, branch `design/opencode-plugin-adapter`, already checked out. Write set: `docs/spec-opencode-lifecycle.tex` only.

Local review of the round-8 design found interactions with existing Claude Code paths that your lifecycle model does not include:
- **F1:** a Tier B dispatch binding would be aborted by deleteFiles.
- **F3:** SessionStart on resume or compact replaces a fork's roster.
- **F10:** the unlocked Store.Close fallback can overwrite a skeleton's verdict.
- **F2/B1:** the scope of fixSupersede is wrong.
- **F9:** the reuse path does not respect lock order.

Model each one. Reproduce it as a counterexample with its fix switched off, then show it holds with the fix on. Add the two new invariants, NoForeignAbort and NoVerdictOverwrite. Re-run everything, and kill a mutant per new fix.

Verify every code citation against source. Reuse your harness in .tmp/lc2/ and jra's in .tmp/jra-eval/.

Git rules:
- Run `make check` before each commit.
- Commit by explicit path only: `git commit -m "<msg>" -- docs/spec-opencode-lifecycle.tex`.
- End each commit message with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Never stage, unstage, or restore any other file.
- Commit on your branch; do not push, do not touch PRs or merge — the leader does that.

Submit the round-1 result through ethos with full probcli evidence. Your final report repeats it, and lists the design sections that must change as a result.