# DES-054 Review — rsc (compatibility / migration)

**Verdict: ITERATE.** The schema and the audit shape are sound. The migration story has three specific gaps that will bite v3.11.0 → v3.12.0 operators on first upgrade: (a) the "old contract on disk, new contract in repo" fallback is asserted but not designed; (b) the global counter file is the only ID-allocation arbiter but the DES describes burn-an-ID and counter-rollover scenarios as no-ops without showing the contract-creation rollback; (c) the JSONL audit line is now unbounded but the atomic-write argument relies on a `PIPE_BUF` boundary that the design never names. Each is a named, bounded edit; none requires re-architecting the design.

---

## Pressure test 1: Load / Create paths against pre-DES-054 on-disk state

I walked `internal/mission/store.go` Load (line 431) and Create (line 269) end-to-end and cross-referenced the DES's migration table (line 277). The picture:

**The contract file itself is fine.** `Contract` gains `Preconditions` and `Delegations` as optional fields; both are slices that decode to nil in `omitempty` form. `DecodeContractStrict` uses `dec.KnownFields(true)`, which is the right call: a new field added to the struct will be accepted on an old file (the old file does not name it), and a new file will be accepted on an old binary only if the old binary's struct still recognizes the keys. The reverse direction — v3.11.0 reading a v3.12.0 contract that names `preconditions` — **fails**, because v3.11.0's `Contract` struct does not declare those fields and `KnownFields(true)` rejects them. The DES does not call this out. It is the standard SemVer-of-on-disk story: the format change is forward-compatible, not backward-compatible. Operators who downgrade from v3.12.0 to v3.11.0 after creating a contract with preconditions will see `field preconditions not found in type mission.Contract`. The DES should either (a) commit to never downgrading, (b) ship a `--strip-preconditions` flag on v3.11.0, or (c) move to `KnownFields(false)` for forward-compat fields and rely on Validate to catch shape errors. I recommend (a) plus a documented "v3.12.0 is a one-way door for contracts that use new fields." That is honest. The current "old lines parse with empty new fields" claim is true; the reverse is silently false.

**The storage move is where the design is incomplete.** The DES says:

> `~/.punt-labs/ethos/missions/<id>.yaml` → `<repo>/.ethos/missions/<id>/contract.yaml`. Read-fallback covers existing on-disk state.

But Store has no concept of "two roots" today. `s.contractPath(id)` is a single function returning a single path (line 204). `s.missionsDir()` returns a single directory (line 196). `Store.List` reads exactly one directory (line 734). `MatchByPrefix` reads exactly one directory (line 789). To implement read-fallback, every read path needs to:

1. Check the new repo-local path first.
2. Fall back to the global path on `os.ErrNotExist`.
3. Decide which list both directories contribute to.
4. Decide what `Update` and `Close` do when the contract lives at the old path: do they migrate it in-place during the locked section, or do they keep writing the old location until `ethos mission migrate --to-repo` runs?

The DES is silent on (4), and it is the hardest case. Five existing operators on v3.11.0 today have missions at `~/.punt-labs/ethos/missions/m-*.yaml`. They upgrade to v3.12.0. They run `ethos mission close m-2026-05-22-001`. Does that close write the trace summary to the new layout (`<repo>/.ethos/missions/<id>/log.jsonl`) while the contract still lives at the old path? If yes, the per-mission directory is inconsistent — log without contract. If the close also migrates, then `Close` silently performs an `mv` across filesystems (home and repo may sit on different mount points), which can fail mid-rename and leave neither path readable.

**Concrete edit:** name the policy explicitly. I recommend "writes always go to the new location; reads check new then old; the first write to a globally-stored mission triggers a copy-not-move, leaving the old file as a tombstone for v3.11.0 downgrade." Then `ethos mission migrate --to-repo` is a sweep that removes tombstones whose new-location twin exists and matches by content hash. That is unambiguous and round-trips cleanly. The current DES wording — "read-fallback covers existing on-disk state" — is a one-line hand-wave for a thirty-line state machine.

**Sibling files multiply the problem.** Today's layout has `<id>.yaml`, `<id>.reflections.yaml`, `<id>.results.yaml`, `<id>.jsonl`, `<id>.lock` all in one directory (lines 220, 231, 248). The new layout puts them inside `<repo>/.ethos/missions/<id>/`. A mission whose contract is at the old location but whose result was appended after upgrade — where does the result go? `isContractFile` (line 770) was carefully maintained to filter siblings out of `List`. The new layout makes that filter obsolete in the repo (subdir per mission), but it is still load-bearing for the global fallback. The DES needs to commit: "sibling files follow their contract." If the contract is at the old path, the result, reflections, and log stay at the old paths. The migrate command moves them as a unit. Half-migration must be impossible to express on disk.

**In-flight missions across the binary upgrade.** This is the failure mode the success criteria asks about explicitly. Today, the per-mission `<id>.lock` is a stable filename never renamed or unlinked (line 209). An operator with a v3.11.0 process holding the flock while the v3.12.0 binary is installed and a second process tries to acquire the same lock — that case is fine, because the lockfile path is the same and POSIX flock is across-process. **But the new layout changes the lockfile path** (the DES says per-mission flock stays global, line 74, which is the right call, but the per-repo `.create.lock` is new). A v3.11.0 `Create` holds `~/.punt-labs/ethos/missions/.create.lock`; a v3.12.0 `Create` holds `<repo>/.ethos/missions/.create.lock`. They are different files. Two concurrent Creates — one on each binary — both pass `checkWriteSetConflicts` on stale views. The window is open for the duration of the v3.11.0 → v3.12.0 rolling upgrade. **The DES should make the global `.create.lock` persist for one release** (v3.12.0 acquires both the global and the per-repo lock; v3.13.0 drops the global). That is the migration-cost discipline I would apply to any tool: the new release pays the cost of compatibility with the previous release; the release after that drops it.

---

## Pressure test 2: Counter atomicity across 5 concurrent repos

The current per-day counter (`internal/mission/id.go`) is correct under flock. I walked it and it is good code — the temp+rename pattern is on the right file, the flock is on a stable separate lock file (line 23), the bounds check at lines 83–88 catches both exhaustion and a poisoned counter. The DES proposes keeping this counter global, adding a delegation counter to the same file under a different key (line 208). That is the right call. Five repos calling `Create` at the same instant serialize on `~/.punt-labs/ethos/missions/.counter-YYYY-MM-DD.lock`. Each gets a unique ID.

**The burn-an-ID case is real and the DES does not address it.** Today's flow:

1. `NewID` acquires the counter flock.
2. Reads counter, computes `next`, writes via temp+rename.
3. Releases the counter flock.
4. Returns `next`.
5. Caller (`Store.Create`) acquires the create lock and the per-mission lock.
6. Caller writes the contract file.

Between steps 4 and 6, anything that fails — `checkWriteSetConflicts` returns a conflict, `validateContract` rejects the contract on an archetype constraint, `writeContract` hits a full disk, the event-log append fails and the rollback succeeds — leaves the counter incremented and **no contract on disk**. The next `Create` gets the next ID. The previous ID is burned. There is no `m-2026-05-22-002` in the missions directory but `m-2026-05-22-003` exists.

Is this a problem? Today, in the per-day single-machine case, it is mostly a cosmetic gap. Operators learn to live with it. **In the new design, it is worse**, because the DES proposes the same allocation pattern for delegations (line 78: `<mission-id>-d<NN>` for delegations under a mission). A delegation that is allocated, recorded in the counter, but then fails before the delegation record file is written produces the same kind of hole. Multiply by the proposed Agent-call frequency (every subagent spawn) and the hole rate goes from "rare cosmetic" to "every long session."

**Concrete edit:** make the counter rollback the caller's responsibility, named in the API. The current `NewID` returns `(id, error)` and forgets. The new shape should be:

```go
func NewID(root string, now time.Time) (id string, release func(commit bool), err error)
```

The caller defers `release(false)`, calls `release(true)` after the contract has hit disk. `release(false)` decrements the counter under the same flock; `release(true)` is a no-op. The bounds check still catches counter exhaustion; rollback unwinds back to the previous value. The DES should also specify whether the counter rolls over at midnight UTC (today it does, per `counterDateFormat`) and how the delegation counter handles the day boundary — a delegation born at 23:59:59 UTC under mission `m-2026-05-21-005` gets `m-2026-05-21-005-d01`; one born at 00:00:01 UTC the next day gets... what? `m-2026-05-21-005-d02` (same mission day) or `m-2026-05-22-005-d01` (today's wall clock)? The DES is silent. The honest answer is "same mission day" — the delegation belongs to its parent mission, not to the wall clock — but it is a clarification.

**Cross-machine concurrency is not addressed.** The DES says "globally unique across all repos on **one machine**" (line 245). A team that runs ethos on two laptops will generate the same `m-2026-05-22-003` on both, with no detection until the contracts collide in a shared repo via git. This is the current behavior — DES-054 inherits it — but the DES introduces the framing "globally unique" without scoping the global. Either rename to "machine-unique" or add a host-id suffix. I prefer the former: today's audience is single-machine, and adding the suffix is a future DES when teams start sharing missions across machines.

**The five-concurrent-repos test passes for mission IDs, fails for the directory-create lock.** The per-repo `.create.lock` (DES line 76) is repo-relative. That is the right shape for write_set conflict checks (write_set paths are repo-relative), and the global counter handles ID uniqueness. The two interact only at the counter step. As long as counter acquisition is fast (sub-millisecond under contention; the lock is held for one ReadFile and one Rename), five repos serialize and proceed.

---

## Pressure test 3: Atomic appends and the unbounded JSONL line

This is the area I was most worried about and the one that needs the most precise fix.

**POSIX guarantees atomicity of `write(2)` only for sizes ≤ `PIPE_BUF`.** On Linux, `PIPE_BUF` is 4096 bytes for regular files when `O_APPEND` is set. On macOS, it is 512. The `man 2 write` page from POSIX.1-2017:

> If the O_APPEND flag of the file status flags is set, the file offset shall be set to the end of the file prior to each write and no intervening file modification operation shall occur between changing the file offset and the write operation.

That guarantees the file-offset → write sequence is atomic against concurrent appenders (no interleaved bytes). It does **not** guarantee that the write of a single `write(2)` call is delivered as a single contiguous chunk if the buffer exceeds the kernel's atomic-write boundary. A 30KB write to an `O_APPEND` file from two concurrent processes can interleave at the chunk boundary, producing a corrupt JSONL line. The DES claims "atomic append" (implicit at line 65, explicit nowhere) and goes from a 200-byte preview to an unbounded `tool_input`. That is a real regression.

In practice today, the audit log writes `~200 bytes` per line (line 86 of `audit_log.go`) — well below `PIPE_BUF` on every relevant OS. A single concurrent appender to the same audit file is rare (one Claude Code process per session). But the DES makes the per-delegation audit log live under `<repo>/.ethos/missions/<id>/delegations/<delegation-id>/audit.jsonl` (DES line 70) — a path that, by construction, is touched by exactly one delegation. Two writers to one delegation's audit log would be a bug elsewhere.

**The concrete risk is different.** It is not interleaving between two writers; it is **partial write under signal or panic mid-line**. A 30KB JSONL line is one `write(2)` syscall, which on Linux is generally delivered as one buffer-cache write (the page-cache write is a single page-aligned operation; the syscall returns short on partial). But if the process receives `SIGKILL` after the syscall returns and before fsync, the line on disk may be truncated at a page boundary (4KB on Linux). The audit log will contain a partial JSON line, which makes the file unparseable from that point forward unless the reader is line-tolerant.

**Concrete edit:** the DES should mandate that the audit-log reader be line-tolerant: scan to the next `\n`, skip lines that fail to parse, emit a count of skipped lines. That is cheap. The writer should also explicitly call `f.Sync()` after each line — not for performance, but to bound the truncation window. The current code (line 67 of `audit_log.go`) does not sync. Adding a sync on each write adds a few hundred microseconds per Agent call, which is negligible in the hot path (the Agent call itself is seconds). The DES's open question 4 estimates 1000 calls × 5KB = 5MB per session and calls it "negligible." With sync, 1000 calls × few-hundred-microseconds ≈ 0.5–1 second of additional latency over the life of a session. Also negligible. Worth committing.

**Rotation.** The DES does not mention rotation. The per-delegation audit log is bounded by the delegation lifetime (open at spawn, closed at return), so it does not grow unboundedly. The per-session audit log (today's `<session-id>.audit.jsonl`) does grow unboundedly — a long-running Claude Code session can write for hours. The DES does not say what to do when an in-flight delegation's audit log file is rotated by an external process (logrotate). The honest answer is "we do not rotate; operators who care archive at session end." That should be stated.

**Trimming is the real failure mode and the DES enables it.** The DES says the new `tool_input` is logged in full. An operator who runs `ethos audit migrate` to backfill historical entries cannot do so atomically: the rewrite has to read the file, transform every line, and rewrite. The DES does not specify whether the rewrite is via temp+rename (correct) or in-place (corrupt under crash). It should specify temp+rename, and `ethos audit migrate` must hold the per-delegation flock for the duration of each delegation it rewrites. **mdm's review** of the migrate UX should cover the "what happens when migrate is killed mid-file" case.

---

## Pressure test 4: Dangling session references in the in-repo audit log

The DES partitions storage: sessions stay global (line 74), audit logs move to the repo. The audit log carries the session id (`auditEntry.Session`, today's line 14; the DES does not propose dropping this field, only adding `parent_session`, `agent_id`, `agent_type`). Cross-checking the schema:

`auditEntry.Session` is a Claude Code-supplied session id (`session_id` from the hook payload, line 30 of `audit_log.go`). The session file at `~/.punt-labs/ethos/sessions/<session-id>.yaml` holds the roster: who joined, when, what persona. The DES adds `parent_session` and resolves `agent_id`, `agent_type` from the roster "at hook time" (DES line 100). Those resolutions happen **once, at write**. The resolved values are baked into the JSONL line. The audit log does not need to chase the session file later to render itself.

**This is the right design.** The audit log is self-contained from the moment of write. If the session file is later GC'd by `ethos session purge`, the audit log retains the agent handle (`rsc`), the agent type (`general-purpose`), and the parent session id (a string token, even if the session is gone). Nothing dangles.

**The one place it could dangle is the precondition predicate.** The DES says preconditions evaluate "the calling delegation's audit log" (line 122). The delegation record references `parent` = session_id (DES line 38). If a precondition needs to resolve `parent` to "who am I delegating from" — to walk up the parent chain — it has to read the session file. After GC, the chain breaks.

But the DES's predicate language is closed and small. None of the four predicates (`audit_contains_tool`, `audit_contains_path`, `audit_contains_path` with template, the implied fourth at line 303) reach across delegations. The scope invariant at line 268 — `scope(p) ⊆ delegation_of(p)` — explicitly forbids cross-delegation reference. So the predicate evaluator does not need the session file. Confirmed.

**One missed surface.** The commit-msg trailer (DES line 173) appends `Mission:` and `Delegation:` lines, not `Session:`. Good. But `git log --grep="Delegation: m-..."` to reconstruct history (DES line 183) relies on the delegation id being self-describing. The current proposed shape `<mission-id>-d<NN>` is. If a later DES introduces an alternative scheme — `d-YYYY-MM-DD-NNN` for ad-hoc delegations (DES line 78) — the `git log --grep` pattern bifurcates. Worth one sentence: "delegation ids are self-describing; the grep pattern covers both forms."

**`TraceSummary.Session`** (`trace.go` line 33) is already optional. Good. After session GC, the missions.jsonl line retains the session id as a string token; no resolution is required.

---

## The cross-tool compatibility check the DES missed

**Beadle and Biff both consume the session roster, not the audit log.** Neither tool reads the per-delegation audit JSONL. So neither breaks under DES-054.

**But Vox reads the audit log to extract recent tool calls for spoken summaries** (this is a feature in the vox mcp). If the audit log format changes — `tool_input` goes from preview to full, and the new fields appear — Vox's parser must tolerate the new shape. The DES does not name Vox. The fix is "Vox parses with `tolerate unknown fields`" (the JSON equivalent of `KnownFields(false)`). If Vox uses Go's `encoding/json` defaults, it already tolerates unknown fields. If Vox uses a strict schema, it breaks. I do not know Vox's parser. **mdm or rop should check this** — it falls slightly outside my migration lens but is the kind of cross-tool compatibility surface the DES table at line 277 should enumerate.

**A second surface: `gh pr` integrations.** The commit-msg trailer adds `Mission:` and `Delegation:` lines. Today's `prfaq-dev` and `feature-dev` plugins parse commit-msg trailers. The DES should run one PR through the chain to confirm nothing trips on the new trailers. Not a blocker, a verification step.

**A third: the workspace `.beads/` tracker.** Beads reads `mission_id` indirectly via commit-msg `Refs:` lines (today's pattern). The DES adds `Mission:` and `Delegation:` lines. Beads should ignore lines it doesn't recognize, which is the standard behavior, but worth confirming.

---

## Summary of named edits

The DES is sound in shape. The migration story needs five named edits:

1. State the on-disk format change is forward-compatible, not backward-compatible. Document v3.11.0 → v3.12.0 as a one-way door for any contract that uses new optional fields, or ship a `--strip-preconditions` flag on v3.11.0.
2. Specify the read-fallback / dual-write state machine for the storage move. "Writes always go to the new location; reads check new then old; first write to a globally-stored mission copies it; `mission migrate --to-repo` is a sweep that removes tombstones whose new-location twin exists." Not a one-liner; a thirty-line state diagram in the DES.
3. Hold both the global and per-repo `.create.lock` during v3.12.0's lifetime. Drop the global lock in v3.13.0. Document the discipline.
4. Change `NewID`'s signature to `(id string, release func(commit bool), err error)` so a failed Create can roll back the counter. Specify the day-boundary behavior of the delegation counter (parent mission's day, not wall clock).
5. State the JSONL atomic-write contract: writer calls `f.Sync()` after each line; reader is line-tolerant (skip-to-newline on parse failure); `ethos audit migrate` rewrites via temp+rename per delegation file, holding the per-delegation flock.

With those five edits, the migration story holds. Without them, every operator running v3.11.0 today hits an unpapered edge on first upgrade.

The audit-log schema is good, the delegation primitive earns its keep, the predicate language is rightly small. Ship after the migration edits land.

**One sentence on the dogfooding:** I appreciate that this review's `write_set` lists `.tmp/missions/results/` (the legacy path) and the leader has asked me to also write to `.ethos/missions/<id>/artifacts/` (the proposed DES-054 path). The fact that the proposed path is not in the contract's `write_set` is exactly the kind of gap a contract enforcer should catch — and exactly the kind of gap the audited-delegation work is designed to make impossible. Today, the write happens because the leader operates outside the contract; under DES-054, the leader would have to amend the contract or accept a precondition failure. That is the design working as intended.

— rsc, 2026-05-22
