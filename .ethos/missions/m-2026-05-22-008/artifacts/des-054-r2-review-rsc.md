# DES-054 v3 Round-2 Review — rsc (compatibility / migration)

**Verdict: APPROVE WITH NAMED EDITS.** The CEO pivot removed the part of v2 I most worried about — the synthesizer was a compatibility hazard whose payoff did not justify the surface — and what remains is a tractable migration. The five named edits from round 1 are addressed in substance (storage state machine, dual `.create.lock`, `NewID` rollback, JSONL `f.Sync` + line-tolerant reader, counter `schema_version`). The smaller v3 surface introduces one genuinely new migration question — the **session audit log location move from `~/.punt-labs/ethos/sessions/` to `<repo>/.ethos/sessions/`** — and the draft addresses it in one sentence at line 202. That sentence is the load-bearing one. It needs a state machine, not a sentence. Everything else is paint.

The classification grid below tags each finding `[REQ]` (changes WHAT the system does — surface contract or operator-visible behaviour) or `[IMPL]` (changes HOW without changing surface contract or behaviour — can be applied autonomously).

---

## Pressure test 1: Session audit log at the moment of binary upgrade

This is the new compatibility surface in v3, and it is the one the draft addresses least concretely. Three things happen on disk during the upgrade window:

1. A v3.11.0 process is mid-session. The session has a live audit log at `~/.punt-labs/ethos/sessions/<session-id>.audit.jsonl`, opened with `os.O_APPEND|os.O_CREATE|os.O_WRONLY` (today's `audit_log.go:60`). The file descriptor is held by the v3.11.0 binary for the lifetime of the session — except it is not held; `HandleAuditLog` opens, writes, and closes per call (line 65). So at any quiescent moment, the file is closed.
2. The operator installs v3.12.0. The old binary is overwritten on disk.
3. The next tool call in the still-running Claude Code session fires the PostToolUse hook, which now resolves to the v3.12.0 binary. It computes the path as `<repo>/.ethos/sessions/<session-id>.audit.jsonl` and writes there.

**The audit log is now split across two files for one logical session.** The first N entries live at `~/.punt-labs/ethos/sessions/<session-id>.audit.jsonl`; the next M entries live at `<repo>/.ethos/sessions/<session-id>.audit.jsonl`. A downstream consumer (vox, an auditor running `ethos audit show`, a future reconciler) that reads "the audit log for session X" sees one or the other, not the union.

The draft's mitigation, line 202: "Files moved from `~/.punt-labs/ethos/sessions/*.audit.jsonl` to `<repo>/.ethos/sessions/*.audit.jsonl` on next session start; old global files remain readable as fallback."

This is two distinct claims and they do not compose:

- "Moved on next session start" — but the session in question did not restart; it crossed the binary boundary. The move trigger is wrong. The right trigger is *the first write to a session whose audit log exists in the legacy location*.
- "Old global files remain readable as fallback" — readable by what? By `ethos audit show`, yes. By the precondition evaluator, also yes (it walks the audit log file path). But the evaluator now has to read **both** paths and concatenate by timestamp to reconstruct the actual sequence, because Tier B preconditions evaluate against "the calling delegation's audit log" and a delegation that spans the upgrade has entries in both files. The draft does not say it reads both.

**[REQ] Edit 1.** Name the policy explicitly. The state machine I propose:

| Trigger | Behaviour |
|---|---|
| Write to session whose legacy audit log exists | Read legacy file, append to new repo-local file, fsync, rename legacy file to `<session-id>.audit.jsonl.migrated` as a tombstone |
| Read for `ethos audit show <session-id>` | Read repo-local first; if absent, read legacy `.migrated` tombstone; if neither, read legacy `.audit.jsonl` |
| `ethos audit migrate` | Sweep all `*.audit.jsonl` in legacy location, apply the write-time migration, leave `.migrated` tombstones |
| Tombstone GC | `ethos audit migrate --gc` removes tombstones older than 30 days when a repo-local twin exists |

The draft's current single sentence at line 202 covers neither the split-session case nor the precondition-evaluator-reads-both case. The state-machine version is unambiguous and round-trips through downgrade — a v3.11.0 binary started after the v3.12.0 upgrade reads the `.migrated` tombstone as a regular audit file (the extension is irrelevant if the operator renames it back).

**[IMPL] Edit 2.** The dual-read in the precondition evaluator. When a Tier B precondition evaluates against `audit_of(d)`, the evaluator must check the repo-local path first and, if the session has any legacy entries, concatenate them. This is an implementation choice that follows from edit 1; it changes no surface.

**[REQ] Edit 3.** State that **session rosters never move**. The draft does say this (line 70: "rosters stay global"), but the audit-log-move announcement at line 202 is colocated with roster discussion and a casual reader can conflate them. One explicit invariant in the migration section: "Session **roster** files (`<session-id>.yaml`, `.lock`, `current/<pid>`) are always read from `~/.punt-labs/ethos/sessions/`. Session **audit** files (`<session-id>.audit.jsonl`) are read from `<repo>/.ethos/sessions/`, with legacy fallback." Two sentences. Cures the ambiguity for every future reader.

---

## Pressure test 2: KnownFields one-way door on the smaller v3 schema

The v3 delta is `Preconditions []ToolPrecondition` and `Delegations []DelegationTemplate` on `Contract`. The `synthetic` flag is gone. The `auditEntry` struct gains five fields, all `omitempty`.

Is the `KnownFields(true)` one-way door still warranted? **Yes, for contracts; partially no, for audit entries.**

**Contracts: the one-way door holds.** A v3.11.0 binary that reads a v3.12.0 contract with `preconditions: [...]` fails with `field preconditions not found in type mission.Contract`. The draft commits to this at line 196 ("No strip flag"). That is correct under v2 and correct under v3. The pivot did not shrink the contract schema enough to remove the door — `preconditions` and `delegations` are still net-new and still load-bearing for Tier B semantics. A v3.11.0 binary that silently accepts a Tier B contract by dropping its preconditions has bypassed the contract. That is worse than refusing to read.

**Audit entries: a strict decoder would break the migration.** The v2 audit reader was implicit because `auditEntry` decodes with `encoding/json` defaults, which already tolerate unknown fields. The draft does not propose tightening to `KnownFields(true)` for audit entries, and it should not. The audit log is append-only and replayed by multiple consumers (vox, `ethos audit show`, future tools). A strict decoder there would convert today's "we added a field" into "every old binary fails on a new entry," which is the wrong direction for an append-only log.

**[IMPL] Edit 4.** State the asymmetry in the draft. One sentence: "Contracts decode with `KnownFields(true)` to refuse silent feature loss. Audit entries decode with default permissive JSON to keep older readers (vox, audit show) compatible with new optional fields." This is documenting current behaviour, not changing it. The current code already has the asymmetry; the design should name it.

**Does v3 reduce or expand the version skew exposure?** It reduces it materially. The synthesizer surface (`synthetic` flag, meta-evaluator identity, rule-6 relaxation) was the part most exposed to skew — a v3.11.0 binary reading a synthesized v3.12.0 contract would have had to either treat it as a normal contract (silently violating rule-6) or refuse it (breaking ad-hoc spawns from new binaries on old reviewer machines). The pivot makes Tier A contractless on the wire; nothing to skew. The only remaining skew is `preconditions`/`delegations`, and the one-way door is the right call.

---

## Pressure test 3: Tier A vs Tier B audit write paths — atomicity under both branches

The draft splits audit writes by tier:

- **Tier A** writes to `<repo>/.ethos/sessions/<session-id>.audit.jsonl`, serialized through `~/.punt-labs/ethos/sessions/<session-id>.lock` (line 222).
- **Tier B** writes to `<repo>/.ethos/missions/<id>/delegations/<delegation-id>/audit.jsonl`, serialized through per-delegation flock (line 211).

Both branches need the same atomic-write contract from round 1: `f.Sync()` after each line, line-tolerant reader, temp+rename for `ethos audit migrate`. The draft says this at line 172 for Tier B ("JSONL atomic-write contract: `f.Sync()` after every line, line-tolerant reader, `audit migrate` rewrites via temp+rename under per-delegation flock (Tier B) or per-session flock (Tier A)"). Good.

But there is a Tier A specific concern. The per-session flock at `~/.punt-labs/ethos/sessions/<session-id>.lock` is in the **global** filesystem. The audit log it gates is in the **repo** filesystem. These can be on different mount points. The lock and the file are not on the same volume.

This is not a correctness problem under POSIX — flock on lockfile A is perfectly capable of serializing writes to file B on a different volume, because the locking happens in kernel memory keyed by inode. It is a **diagnosability** problem. An operator running `ls ~/.punt-labs/ethos/sessions/<session-id>.lock` and `ls <repo>/.ethos/sessions/<session-id>.audit.jsonl` separately, in two terminals, sees them as unrelated files. When the lock is held but the audit log is empty (or vice versa), the operator has no signal that they belong together.

**[IMPL] Edit 5.** Document the lock-file/data-file split for Tier A: comment in code, mention in the concurrency table at line 207. The table already lists "Session audit JSONL" at `<repo>/.ethos/sessions/` and "Session roster flock" at `~/.punt-labs/ethos/sessions/`, but does not name the **per-session audit-log flock** explicitly as a separate row. Add a row: "Per-session audit-log flock | 5 | `~/.punt-labs/ethos/sessions/<session-id>.lock` | flock-held."

**[REQ] Edit 6.** State the multi-process atomicity guarantee in invariant form. A new invariant or a clause in I3:

```text
-- Audit append atomicity. For any session s with concurrent writers
-- w1, w2 to the Tier A audit log:
I9: forall e1, e2 in audit_entries(s): no_interleave(line(e1), line(e2))
```

The mechanism — per-session flock + `f.Sync()` + bounded line length — is in the draft. The invariant that ties them together is not. mdm's evaluator lens on migration UX should also touch this: an operator who sees a corrupt audit JSONL line has no way to know whether the corruption came from a missing flock, a missing sync, or a `kill -9` mid-write. Make the invariant explicit so the failure mode has a name.

**Cross-tier audit visibility.** A Tier B delegation that spawns a Tier A child is plausible (a governed worker spawns a bare Agent for an ad-hoc query). The Tier B parent's audit log lives at `<repo>/.ethos/missions/<id>/delegations/<delegation-id>/audit.jsonl`; the Tier A child's audit lives at `<repo>/.ethos/sessions/<session-id>.audit.jsonl`. They are linked only by `delegation_id` and `parent_delegation` in the entry payload. The draft acknowledges this at line 72 ("the `delegation_id` field on every audit entry makes the two views queryable together"). Good. But there is no `ethos audit show --delegation <id>` command described that does the join. **[IMPL] Edit 7.** Add the join command to the recommended-next-step phase 3 list. Implementation detail, but the operator's mental model of "show me everything this delegation did, including bare-Agent children" needs a single command. Without it, the cross-tier link is theoretical.

---

## Pressure test 4: NewID rollback and counter schema_version on the smaller scope

**Still necessary.** The pivot reduced the breadth of the Create surface — no synthesizer means no contract generated server-side on every Agent call — but it did not reduce the rate of ID allocation. Every Tier B delegation still allocates a delegation ID. Every mission still allocates a mission ID. The same burn-an-ID failure mode applies: counter advances under flock, Create fails post-counter, counter does not roll back, the next Create gets the next ID, the missing ID is observable in `git log --grep="Mission: m-2026-05-22-"`.

Two reasons it remains necessary on the smaller scope:

1. **Delegation IDs are now Tier-A allocated too** (line 233: `d-YYYY-MM-DD-NNN` for Tier A). Every bare `Agent(...)` call gets a delegation ID from the same counter. Tier A spawn frequency is higher than mission creation frequency by an order of magnitude in real sessions. The burn-an-ID hole rate scales with that frequency.
2. **`Counter.yaml schema_version`** at line 200 is the only piece of v3.12.0 state a v3.11.0 binary will see if a user downgrades. The draft says v1 readers use "permissive YAML decode and ignore unknown top-level keys" — that depends on the v3.11.0 binary's decode mode. I checked: the current counter reader is on `internal/mission/id.go` (the prior review walked it) and uses standard YAML unmarshal, which does ignore unknown keys by default. So this is "permissive by accident, by virtue of which library function we call." That is fine, but the draft should commit to it: a future code change to `id.go` that switches to `yaml.Decoder.KnownFields(true)` would silently break downgrade. **[REQ] Edit 8.** Add a one-line invariant: "`counter.yaml` is read with permissive YAML decoding. Future schema additions append top-level keys; existing keys never change meaning."

**Day-boundary clarification.** My round-1 review asked: a delegation born at 00:00:01 UTC the day after its parent mission was created — `m-2026-05-21-005-d02` (same mission day) or `m-2026-05-22-005-d01` (today's wall clock)? The v3 draft does not answer this. The honest answer remains "same mission day — the delegation belongs to its parent mission, not to the wall clock." **[REQ] Edit 9.** State this in the migration section.

**Tier A counter shape.** Tier A delegations use `d-YYYY-MM-DD-NNN` (line 233), which is wall-clock indexed. Tier B uses `<mission-id>-d<NN>`, which is mission-indexed. The two share the global counter file but namespace differently. The draft does not say which key in `counter.yaml` they use. **[IMPL] Edit 10.** Specify the counter file shape:

```yaml
schema_version: 2
date: 2026-05-22
mission_counter: 7
tier_a_delegation_counter: 23
mission_delegation_counters:
  m-2026-05-22-001: 4
  m-2026-05-22-003: 1
```

The format is the draft's call; the requirement is that it be specified rather than implicit.

---

## Pressure test 5: `ethos audit migrate` edge cases

The draft proposes `ethos audit migrate` for backfilling `parent_session` and `agent_type` from contemporaneous session rosters (line 202) and as the migration vehicle for the storage move (mentioned at the round-1 close). Three edge cases that the draft does not name:

**No global state to migrate.** A fresh install. The user runs `ethos audit migrate`. The legacy directory `~/.punt-labs/ethos/sessions/` does not contain any `*.audit.jsonl` files. The command should be a no-op that exits 0 with the message "no legacy audit logs found." The draft does not state this; without it, the command is a candidate for "exited 1 because the directory is empty" surprises. **[IMPL] Edit 11.** Specify exit-0-on-no-op.

**Repeated runs.** The user runs `ethos audit migrate` twice in a row. The second run sees that every legacy file already has a `.migrated` tombstone (or a repo-local twin). It should be idempotent: do nothing, exit 0, message "all sessions already migrated." The draft does not state this. **[IMPL] Edit 12.** Specify idempotency.

**Partial migration mid-failure.** The user runs `ethos audit migrate` against 50 sessions. The 23rd one fails (full disk, permission denied on the repo path). The command exits non-zero. What is the state on disk? If sessions 1–22 are migrated (tombstones in place, twins in repo) and session 23 is partially written (truncated twin in repo, no tombstone), the next run needs to detect partial state and recover. The draft does not name this. **[REQ] Edit 13.** Specify the recovery contract: "Each session's migration is atomic via temp+rename. Partial state on session N is detected by the absence of a tombstone for N; the next run resumes from N." This is a real operator-visible behaviour — when the command says it migrated, it has to be true even after a crash.

**Source filesystem read-only.** A user with a corporate-managed `~/.punt-labs/` mount where the home directory is read-only at runtime (some MDM setups). `ethos audit migrate` cannot write tombstones. **[REQ] Edit 14.** Specify the read-only fallback: "If the legacy directory is read-only, migration copies entries to the repo path and the legacy file becomes the implicit tombstone (will never have a `.migrated` extension applied). Future reads check for the repo-local twin first; legacy file is treated as the tombstone iff a twin exists."

**Repo not initialized.** The user runs `ethos audit migrate` in a directory that has no `.ethos/`. The command needs `.ethos/sessions/` to exist or be creatable. **[IMPL] Edit 15.** Specify: "Creates `<repo>/.ethos/sessions/` with `0o700` if absent; refuses if `<repo>/.ethos/` exists but is not writable."

**Cross-repo migration.** An operator working across five repos has audit logs from sessions that touched multiple repos. The legacy global path stored one file per session regardless of which repo it touched. The new path is per-repo. Which repo gets the migrated file? **[REQ] Edit 16.** State the policy. The honest answer: "ethos audit migrate runs in one repo and migrates only sessions whose roster lists that repo as the working directory. Sessions whose working directory cannot be resolved stay in the legacy location and are not migrated." This is the only policy that does not require ethos to guess.

---

## Cross-tool surface check (carried from round 1, re-applied to v3)

- **Vox** — still consumes the audit log shape. The v3 schema is a strict superset of v2's (all new fields `omitempty`). Vox's parser must tolerate unknown fields. If it uses Go `encoding/json` defaults, it already does. **[IMPL] Verification.** mdm to confirm.
- **Beadle, Biff** — still consume only session rosters, not the audit log. The roster format is unchanged. No action.
- **`prfaq-dev`, `feature-dev`, beads** — still consume commit-msg trailers. The new trailers `Mission:` and `Delegation:` are additive; existing tools should ignore unknown trailers. **[IMPL] Verification.** Run one PR through to confirm; not a blocker.
- **`gh pr` integrations** — unchanged from round 1.

---

## What I would not change

The smaller v3 surface deserves explicit credit. The synthesizer was a compatibility hazard whose payoff did not justify the surface; dropping it removes:

- An entire decode path on read that v3.11.0 binaries would have to negotiate.
- The `synthetic` flag on `Contract`, which would have been a new required field with semantic-versioning implications.
- The `claude-meta` evaluator identity, which would have had to ship in the global identity store via `ethos seed` — a new on-disk surface to manage.
- Three invariants (I8/I9/I10 about synthetic contracts) that added formal-method surface without reducing operator-visible failure modes.

The advice hook is the right kind of governance. It is information, not enforcement, which means it can ship in v3.12.0 and be tuned in v3.12.1 without breaking anything that depends on the prior tone. That is a small but real compatibility win: the advice surface is not a contract.

The decision to keep the storage move in v3 — over the alternative of "move audit logs in a later release" — is the right call. The schema change to `auditEntry` (the five new fields) is going to bake in v3.12.0 regardless; piggy-backing the location move on the same release means there is one upgrade story, not two. Splitting them would have created a v3.12.0 → v3.13.0 second migration window that everyone would have to walk through.

---

## Summary table — REQ vs IMPL classification

| # | Finding | Class | Owner |
|---|---|---|---|
| 1 | Specify the session audit log move state machine (read-fallback, tombstone, first-write trigger) | REQ | bwk (impl) + rsc (eval) |
| 2 | Precondition evaluator reads both legacy and new-location audit logs during transition | IMPL | bwk |
| 3 | State invariant: session rosters never move; audit logs do | REQ | claude (DES edit) |
| 4 | Document KnownFields asymmetry: strict for contracts, permissive for audit entries | IMPL | claude (DES edit) |
| 5 | Concurrency table adds Tier A per-session audit-log flock row | IMPL | claude (DES edit) |
| 6 | New invariant I9 for audit append atomicity (no interleave) | REQ | jra (formal) |
| 7 | `ethos audit show --delegation <id>` joins Tier A + Tier B audit views | IMPL | mdm (CLI) |
| 8 | Counter.yaml read with permissive YAML decoding; future keys append-only | REQ | claude (DES edit) |
| 9 | Day-boundary policy for delegation IDs: mission's day, not wall clock | REQ | claude (DES edit) |
| 10 | Specify counter.yaml shape (mission_counter, tier_a_delegation_counter, mission_delegation_counters map) | IMPL | bwk |
| 11 | `ethos audit migrate` exits 0 on no legacy state | IMPL | mdm |
| 12 | `ethos audit migrate` idempotent across repeated runs | IMPL | mdm |
| 13 | `ethos audit migrate` recovery after partial failure: temp+rename per session, resume by tombstone | REQ | bwk (impl) + rsc (eval) |
| 14 | Read-only legacy filesystem fallback policy | REQ | claude (DES edit) |
| 15 | `ethos audit migrate` creates `<repo>/.ethos/sessions/` with 0o700 if absent | IMPL | mdm |
| 16 | Cross-repo audit migration: only migrate sessions whose roster names this repo | REQ | claude (DES edit) |

Eight REQs, eight IMPLs. The REQs need CEO consent because they change operator-visible behaviour (the migration command's exit codes, the storage state machine, the partial-recovery contract). The IMPLs can be applied autonomously by the implementing specialist; they are documentation refinements or implementation details that follow from already-stated semantics.

---

## Recommended next step

Approve v3 for implementation with the 16 named edits applied to the DES before phase 1 begins. The REQs go into a single DES edit pass owned by claude (the leader); the IMPLs are distributed to the implementing specialists (bwk for impl-side, mdm for CLI surface, jra for the formal invariant).

The migration story is now tractable. The audit log location move is the load-bearing surface for v3, and the state machine in edit 1 is what makes the difference between "one-line hand-wave" and "thirty-line spec." The other 15 edits are paint by comparison, but the paint matters — every operator running v3.11.0 today walks through these surfaces on first upgrade, and each named edge case is one fewer surprise.

The pivot was correct. Ship after the edits land.

— rsc, 2026-05-22
