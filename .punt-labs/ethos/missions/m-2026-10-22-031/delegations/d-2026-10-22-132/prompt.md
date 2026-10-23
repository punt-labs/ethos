You are the worker for ethos mission m-2026-10-22-031. Read the contract with `ethos mission show m-2026-10-22-031`; it is authoritative.

Repository <repo>, branch `design/opencode-plugin-adapter`, already checked out. Write set: `docs/spec-opencode-integration.tex` only.

Task: extend the existing Z specification with a model of the session lifecycle and of `claim-forked`, as specified in prose in `docs/design-opencode-plugin.md` (§2.1.1, §2.3, §2.3.1, §2.6, §5 points 1–2). Then type-check it with fuzz and model-check the four contract invariants with ProB. You have the z-spec MCP tools (check, model_check, animate, test).

Three review rounds have already found counterexamples in prose; the contract lists them. Your model must reproduce each one against the operations as currently written, then amend the operations until all four invariants hold. Record every counterexample and the amendment that removes it. The amended operations become the authority the design document will be brought into line with.

Keep schemas flat. Bound the integers, prefix the free types, and avoid B keywords.

Git rules:
- Run `make check` before each commit; it runs fuzz on docs/*.tex.
- Commit by explicit path only: `git commit -m "<msg>" -- docs/spec-opencode-integration.tex`.
- End each commit message with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Never stage, unstage, or restore any other file.
- Commit on your branch; do not push, do not touch PRs or merge — the leader does that.

Submit the round-1 result through ethos. The evidence must include:
- the exact probcli commands, bounds, and states explored;
- the outcome for each invariant;
- each counterexample and the amendment that removed it;
- the commit SHAs.

Your final report repeats all of this and lists any operation that could not be made to satisfy an invariant.