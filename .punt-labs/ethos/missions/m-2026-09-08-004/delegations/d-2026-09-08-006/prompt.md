Review commit 3cf6bf4 on branch fix/spawn-binding in <repo>/.claude/worktrees/session-pid-identity. Diff against parent 34f0e33 (`git diff 34f0e33..3cf6bf4`), ignoring `.punt-labs/ethos/sessions/**` and `.punt-labs/ethos/missions/**` JSONL files — generated audit state.

This change and its ADR (DES-076 in DESIGN.md) make several claims about themselves. Your job is to verify each one actually holds in the code, not to trust the prose. The claims I want checked, verbatim from the ADR and commit message:

1. **"One dispatch, one binding, one consuming spawn — the sidecar cannot outlive the worker it was written for."** Is that exhaustively true? Enumerate the ways a dispatch-origin sidecar could persist past its intended worker and confirm each is handled or explicitly excluded. Consider: the worker never spawns at all; the worker spawns twice; the session ends mid-dispatch; two dispatches in a row before any spawn.

2. **"A spawn that gets blocked or hits an encode failure does not consume the binding, so a retry of the same `Agent()` call still finds it."** Verify against the actual ordering in `internal/hook/pretooluse_dispatch.go`, not the comment.

3. **`BindOriginClaim` "is unaffected: it stays sticky across every spawn until an explicit claim or release, exactly as before."** Confirm the claim path is genuinely untouched in behavior, and that no code path can convert one origin into the other.

4. **The declared-Worker match is the discriminator.** `spawnAgentType` reads `subagent_type` from tool input, falling back to `CLAUDE_AGENT_TYPE`. Is that the same answer `dispatchTierB` uses for its own delegation-skeleton write? The ADR claims both see the same answer for the same spawn — verify, because a divergence would mean the gate and the record disagree about who spawned.

5. **The regression test `TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_MismatchedWorkerNotCaptured`.** Does it guard the general case, or only the specific arrangement it sets up? Specifically: would it still fail if someone reintroduced the bug in a slightly different way — say, by matching on the wrong field, or by consuming on mismatch instead of match? A test that passes only because of the exact fixture it built is not a regression gate.

6. **DES-076 is marked PARTIAL and its "Non-goal" section claims a specific residual case remains open: a spawn whose agent type equals the dispatched Worker but whose task is unrelated.** Confirm that is genuinely the ONLY residual, or name others the ADR failed to enumerate. An ADR that under-states what it leaves open is the defect I most want caught here.

Report each claim as CONFIRMED or not, with file:line evidence. Where a claim is only conditionally true, say under what condition it fails.