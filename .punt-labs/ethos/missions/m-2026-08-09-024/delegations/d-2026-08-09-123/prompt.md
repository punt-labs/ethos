Review the diff of branch `task/delegation-lifecycle-attribution` against `main` in <repo> (`git diff main...task/delegation-lifecycle-attribution`).

The design doc `docs/design-delegation-lifecycle.md` (on this branch, read it first) makes several strong claims this PR is supposed to guarantee. Your job: verify each claim actually holds against the code, not just against the prose that asserts it.

Specifically check:

1. **"The existing one-shot sweep becomes complete by construction: nothing can be written to a closed mission's `delegations/` directory after this fix ships."** The design says the write path is now closed off. Verify: does the two-check-plus-exclusive-lock actually make this true, or is there ANY remaining path (an MCP tool, a CLI command, a hook OTHER than pretooluse_dispatch) that can still call `WriteDelegationSkeleton` under a closed mission? Grep for every caller of `WriteDelegationSkeleton` and confirm each is either behind the new guard or genuinely unreachable when the mission is non-open.

2. **"Terminal-states-are-final."** The design explicitly promises nothing mutates `Mission.status` after `Close` commits, and the delegation sweep only touches delegation record files. Verify: does the new exclusive-lock path introduce any `Update`, `Save`, or contract-write call between `Close`'s commit and the sweep? Is `closeDelegationSkeletons`'s existing behavior of NOT touching the contract file preserved?

3. **"The four status values (closed/failed/escalated/abandoned) all fall through to Tier A uniformly."** The test description mentions each as a separate table case. Verify the actual test file has all four states covered, and the production code branches on `c.Status != mission.StatusOpen` — a single check that catches all four — rather than a case-by-case list that could miss one.

4. **The MCP rebind claim: "a rebind over a different mission surfaces in the response's `warnings` array."** Verify (a) the warning is genuinely generated when a rebind replaces a different mission (not just when the sidecar was previously empty), (b) it's actually included in the response returned to the MCP caller, and (c) there's a test that would catch a regression where the warning gets silently dropped.

5. **"Forward-fix only, existing on-disk delegation records stay as-written."** Verify no code path in this diff reads and rewrites any existing `record.yaml` — the exclusive-lock's job is to serialize NEW writes against the sweep, not to touch existing records.

Report findings via the standard format.