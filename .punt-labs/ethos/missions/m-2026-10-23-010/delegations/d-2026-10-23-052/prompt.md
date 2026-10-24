You are the worker for ethos mission m-2026-10-23-010. Read the contract with `ethos mission show m-2026-10-23-010`; it is authoritative.

Repository <repo>, branch `design/opencode-plugin-adapter`, already checked out. Write set: `docs/spec-opencode-lifecycle.tex`.

Copilot found a ten-step trace that violates ReconcileOnExit with all fifteen fixes on. The trace is: a resumed record meets a mission the unlocked sweep has just closed, and the call exits do-not-retry with the skeleton still open. The exhaustive slices omitted the unlocked sweep, and the full-machine run stops at depth 8, so no run reached it.

1. Reproduce the trace.
2. Apply the leader's fix (fixResumeClosed: close the resumed skeleton as aborted under CAS, remove the record, then exit).
3. Extend exhaustive coverage to include the unlocked sweep.
4. Re-run all invariants.
5. Kill a mutant for the new fix.

Reuse your harness in .tmp/lc3/.

Git rules:
- Run `make check` before each commit.
- Commit by explicit path only: `git commit -m "<msg>" -- docs/spec-opencode-lifecycle.tex`.
- End each commit message with `Co-Authored-By: Claude Opus 5.5 <[redacted-email]>`.
- Never stage, unstage, or restore any other file.
- Commit on your branch; do not push, do not touch PRs or merge — the leader does that.

Submit the round-1 result through ethos with full probcli evidence. Your final report repeats it and names the design section that must change.