Review the diff of branch `task/delegation-lifecycle-attribution` against `main` in <repo> (`git diff main...task/delegation-lifecycle-attribution`).

Context: this implements `docs/design-delegation-lifecycle.md` (on this branch, read first) — closing two beads (`ethos-14r7` delegation lifecycle + attribution, and `ethos-5jsf` MCP handleCreateMission rebind gap) which the design identified as the same root-cause family. Core changes:

1. `internal/mission/delegation.go` — new `AcquireMissionLockExclusive`, shared body with `AcquireMissionLock` via an unexported helper.
2. `internal/mission/store.go` — `Close` wraps `closeDelegationSkeletons` in the exclusive lock (only when the repo-tree per-mission dir exists, per the legacy-global-tree no-footprint invariant).
3. `internal/hook/pretooluse_dispatch.go` — `dispatchTierB` now re-checks mission status twice (right after Load, and again before WriteDelegationSkeleton while holding both locks) via a shared `nonOpenReason` helper with `staleBindingReason`. Non-open falls through to Tier A with stderr warning.
4. `internal/mcp/mission_tools.go` — `handleCreateMission` rebinds the active-mission sidecar mirroring the CLI's `bindDispatchedMission`.

`make check` clean, verified independently by leader (golangci-lint 0 issues, all tests pass including `-race`).

Focus your review on: (a) correctness of the two-lock ordering — does `Close` genuinely serialize against a concurrent `dispatchTierB` write, or is there a remaining TOCTOU (e.g., between Load and lock acquisition)? (b) the `nonOpenReason`/`staleBindingReason` helper unification — does it preserve all the existing warning-emission behavior (matching stderr wording) that the tests pin? (c) the MCP `handleCreateMission` rebind — does it use the same underlying `WriteActiveMissionOrigin` the CLI does, or a parallel implementation that could drift? (d) the design's explicit "only take the exclusive lock when the repo-tree per-mission dir exists" carve-out for the legacy-global-tree invariant — is that carve-out itself safe (could a repo racing between "dir doesn't exist" and "dir gets created by a concurrent dispatch write" slip through)?

Report findings via the standard format.