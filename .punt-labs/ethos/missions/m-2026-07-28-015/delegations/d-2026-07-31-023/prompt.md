You are the pinned evaluator on mission m-2026-07-31-004 (worker bwk). Adversarially verify PR #415 in the ethos repo (<repo>, branch fix/mission-lifecycle-cluster) — five mission/audit-lifecycle correctness fixes. Not a rubber stamp; build binaries and reproduce where it matters.

Get the diff: `git -C <repo> diff main...fix/mission-lifecycle-cluster`. Commits: 65bbf6e (qy7k), cbb06e4 (7vo3), 8e84585 + 249d0c0 (pobi), 04dcf12 (t2lb), 36f4243 (u4kq).

CRITICAL PRODUCT CONSTRAINT: these are the differentiator, so every fix must make a shipped feature behave as prfaq.tex intends WITHOUT loosening a settled invariant. Read prfaq.tex (feat:results, feat:writeset, human-agent parity) and DESIGN.md DES-032/DES-036/DES-054/DES-058 first. Flag ANY fix that weakens the write-set overlap refusal, the strict result decoder, or contract immutability.

Verify each, with file:line + a failing scenario for anything wrong:
1. qy7k (glob-aware containment): a path genuinely inside a declared write_set glob (docs/**) is now ACCEPTED, AND a path truly outside every declared entry is still REJECTED. Confirm it did not turn the containment check into a no-op or over-accept (e.g. a partial glob that matches too much). Exact-file and segment-prefix entries unchanged.
2. 7vo3 (delegation binding): a re-dispatch WITHOUT release now files the delegation under the mission named at DISPATCH, not the stale sticky sidecar. Confirm it touches NO contract invariant — only which mission a delegation record is filed under — and that a stale binding warns. Verify it did not silently change mission-claim semantics.
3. pobi (commit-msg committing-session): the fallback now resolves the COMMITTING session (ETHOS_SESSION / PID walk) and reads only its sidecar; a commit resolving to the older-dated session gets that session's mission or NONE, never a newer session's. Confirm the extra commit 249d0c0 surfaces a genuine trailer-lookup failure instead of swallowing it (no new silent failure, and no false trailer when the session can't be resolved).
4. t2lb (--var split): a spaced/comma --var produces N distinct write_set entries each admission-checked individually; a single-path value unchanged. Confirm the split matches dispatch --write-set semantics and doesn't mangle paths with legitimate internal characters.
5. u4kq (ambiguous identity): FindBy/Resolve now return an explicit "ambiguous identity" error naming candidates instead of an arbitrary winner; unique-match and zero-match cases unchanged. Confirm no caller now crashes on the new error path (callers handle it), and the error is deterministic.

Also: each new test must FAIL against the pre-fix commit (mutation/checkout-and-run to confirm it pins the bug, not passes incidentally). Run make check in a clean clone.

Deliver PASS or a specific must-fix list (file:line + failing scenario). Do not fix anything. Report via your return value.