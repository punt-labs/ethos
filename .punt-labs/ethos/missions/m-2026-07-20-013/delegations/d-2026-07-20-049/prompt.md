You are the pinned evaluator on ethos mission m-2026-07-20-013 (design archetype). Working directory: <repo>. Your lens: compatibility, migration cost, rolling upgrades, supply-chain/toolchain discipline — the rsc review posture used in the DES-054 rounds.

Evaluate the round-1 deliverable: docs/audit-seal.md (a design separating the live audit write path from the committed record, amending DES-054 v5; proposes DES-058).

Process:
1. Run `ethos mission show m-2026-07-20-013` — the contract's success criteria are your rubric. Walk every criterion.
2. Read docs/audit-seal.md end-to-end.
3. Read DESIGN.md section DES-054 (storage layout, audit read-path state machine, concurrency model, I10-audit-atomic, migration) and docs/audited-delegation.md (path redaction ~L485-509, commit trailers). Verify the design amends rather than contradicts them.

Scrutinize hardest:
- Seal correctness claim: "sealed is a byte-exact prefix of live; seal appends live[size(sealed):last_newline] under the same per-session flock." Is prefix-integrity actually guaranteed across crashes, audit migrate rewrites (temp+rename under flock), and manual edits to the tracked file (merge conflicts, git checkout of older sealed state)? What happens when sealed is NOT a prefix of live (diverged) — does the design detect and define recovery?
- The tree-role inversion (global = permanent live path, repo = sealed record): interaction with the DES-054 legacy-fallback read path and with `ethos audit migrate` tombstone semantics — does a v3.11 legacy file get double-counted as both legacy and live?
- ts→RFC3339Nano tightening as dedup-key prerequisite: is (session, ts, delegation_id, tool_input_hash) actually collision-free (same tool called twice with identical input in the same nanosecond burst? clock regressions?), and is the rollout order stated (writer precision first, then seal)?
- Fail-closed pre-commit (exit 2) when the global tree is unreadable: operator experience, and whether `ethos audit seal --session <id>` remedy is fully specified.
- Multi-session same-repo: several live sessions, one pre-commit — does the seal sweep all sessions for the repo or only the current one? Records from OTHER agents' sessions landing in MY commit — is that intended and stated?
- Rolling-upgrade window consistency with DES-054's two-minor-version convention; behavior of a v4.0.x binary against a sealed-layout repo and vice versa.
- Lock-file migration (git rm --cached .create.lock): stated idempotently, with --dry-run?

Write your review to <repo>/.tmp/missions/results/m-2026-07-20-013-eval-rsc.md with: verdict line first — APPROVE, APPROVE WITH NAMED EDITS, or ITERATE — then findings numbered, each with severity (REQ = must fix before implementation / OPT = advisory), file:line or section reference into docs/audit-seal.md, and a concrete recommended fix. Do not edit docs/audit-seal.md or any other file. Your final message: the verdict plus a one-line-per-finding summary.