Review commit 3cf6bf4 on branch fix/spawn-binding in <repo>/.claude/worktrees/session-pid-identity. Diff it against its parent 34f0e33 (`git diff 34f0e33..3cf6bf4`), ignoring the `.punt-labs/ethos/sessions/**` and `.punt-labs/ethos/missions/**` JSONL files — those are generated audit state, not authored code.

What the change does: `ethos mission dispatch` writes an "active-mission" sidecar that previously bound the NEXT `Agent()` spawn in the session to that mission, regardless of whether it was the intended worker. That silently mis-attributed unrelated agents as delegations of a mission they had nothing to do with. The fix makes the dispatch-origin binding single-use and scoped to the contract's declared `Worker`: only a spawn whose agent type matches consumes it; a mismatched spawn proceeds unbound and leaves the sidecar for the real worker. `ethos mission claim` bindings are deliberately unaffected and stay sticky.

The decision record is DES-076 in DESIGN.md — read it, and specifically check whether the code does what the ADR claims it does. On the PR that just merged in this repo, the single most expensive class of defect was prose asserting behavior the code did not have.

Focus areas, in priority order:

1. **Correctness of the match-and-consume logic.** `readActiveMissionForDispatch` and `consumeDispatchBinding` in `internal/hook/pretooluse_dispatch.go`. Is consumption correctly ordered relative to dispatch success? The ADR claims a spawn that gets blocked or hits an encode failure does NOT consume the binding, so retrying the same call still finds it — verify that's true in the code.
2. **The claim-vs-dispatch asymmetry.** `BindOriginClaim` must stay sticky; `BindOriginDispatch` must be single-use. Confirm the two paths cannot be confused, and that the origin file's ordering discipline described in `internal/mission/active.go` is preserved.
3. **CLAUDE.md compliance** for this repo — see the repo's CLAUDE.md and docs/development.md.
4. **Whether any comment or CLI output string now describes behavior the code no longer has.** The commit claims to fix one such string in `cmd/ethos/mission.go`. Check for others it missed.

Report concrete findings with file:line. If you find nothing in a category, say so explicitly rather than staying silent on it.