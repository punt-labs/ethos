# Phase 3: Delegation Spec (2 minutes 10 seconds)

The leader writes a task-specific prompt for the worker. This is sent
via the `Agent` tool with `subagent_type: "bwk"`. The spec is
self-contained — the worker does not inherit the leader's conversation
history.

## The Actual Spec Sent to bwk

```text
## Mission: m-2026-04-10-003 — ethos-db7

### What

Eliminate the TOCTOU in checkVerifierHash by consolidating two file
reads into one.

### Why

checkVerifierHash in internal/hook/subagent_start.go currently calls
deps.Missions.Load(id) (which does os.ReadFile + yaml.Unmarshal
internally) and then calls os.ReadFile(deps.Missions.ContractPath(id))
for RawYAML. If the contract file changes between these two reads,
the parsed Contract and the raw bytes diverge.

### Spec

In checkVerifierHash (internal/hook/subagent_start.go):

1. Replace the deps.Missions.Load(id) call with a direct
   os.ReadFile(deps.Missions.ContractPath(id))
2. Call rejectSymlink before the ReadFile (preserving the ethos-jjm
   defense)
3. Unmarshal the bytes locally — use yaml.NewDecoder with
   KnownFields(true) to match Store.Load's behavior
4. Use the same raw bytes for the verifierMission.RawYAML field
5. Remove the second os.ReadFile call (currently ~line 491)

The result: one read, one unmarshal, same bytes for both hash check
and rendering.

### Write set

Only internal/hook/subagent_start.go may be modified.

### Constraints

- rejectSymlink must be called before the ReadFile
- Status and evaluator-handle filters must still work on the parsed
  contract
- Error messages must include the mission ID for diagnostics
- deps.Missions.ContractPath(id) is the API for getting the file path
- Use yaml.NewDecoder + KnownFields(true), not yaml.Unmarshal

### Acceptance criteria

- make check passes
- checkVerifierHash does exactly one os.ReadFile per contract
- The same []byte is used for both yaml decode and RawYAML
- All existing tests pass unchanged
- No new dependencies
```

## What the Worker Delivered

bwk completed in **100 seconds** (13 tool calls). The implementation:

1. Replaced `deps.Missions.Load(id)` with inline `os.Lstat` +
   `os.ReadFile` + `mission.DecodeContractStrict`
2. Added `CurrentRound` default-fill and `Validate()` to match
   Store.Load's post-decode behavior
3. Added filename-match check (`c.MissionID != id`)
4. Stored `raw` as `RawYAML` on the same struct

Result: 1 file, +50/-23 lines. `make check` passed on first try.

## Notes on Spec Writing

The spec gives **coordinates** (function name, file, line numbers) and
**invariants** (same bytes, KnownFields, symlink check ordering) — not
keystroke-level instructions. bwk is a Go specialist; they know how to
read a file and unmarshal YAML.

Bad spec patterns to avoid:

- "Fix the TOCTOU" (what TOCTOU? where?)
- "Refactor subagent_start.go" (which function? what's wrong?)
- Copy-pasting 200 lines of code (the worker can `Read` the file)
- Omitting constraints (the worker won't know about rejectSymlink)
