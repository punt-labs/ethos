Read your mission contract: `ethos mission show m-2026-08-09-024` in <repo>, on branch `task/delegation-lifecycle-attribution`. Also read the design doc it implements in full: `docs/design-delegation-lifecycle.md` (already committed on this branch).

This closes two beads at once: ethos-14r7 and ethos-5jsf — the design explicitly identifies 5jsf as the same root-cause family and required for full facet-2 coverage on the MCP surface. Both are in your write-set.

Strict TDD: write the failing tests first (each of the four state values — closed, failed, escalated, abandoned — as separate table cases for the case-1 status re-check; the concurrency test racing dispatchTierB against Close needs a synchronization harness; the MCP test asserts handleCreateMission writes the active-mission sidecar just like the CLI does). Confirm each test fails for the right reason before writing the fix, then make them pass. Run `make check` before every commit — it must be green at each commit, not just at the end. Commit incrementally per the mission criteria.

When done, submit your result with `ethos mission result m-2026-08-09-024 --file <path> --verify` and report back.