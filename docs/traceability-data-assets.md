# Traceability Data Assets

Every agent delegation in ethos produces artifacts across five
stores. Each artifact answers a distinct forensic question, carries
a natural identifier, and links to artifacts in other stores via
shared keys. Together they form the traceability graph — the
evidence chain that answers "what happened, who did it, why, and
under what authority" for every line of code an agent produces.

## The Five Stores

| Store | Location | Lifecycle | Git-tracked |
|-------|----------|-----------|-------------|
| Mission store | `<repo>/.punt-labs/ethos/missions/<mission-id>/` | Created at `mission create`, closed at `mission close` | Yes |
| Delegation store | `<repo>/.punt-labs/ethos/missions/<mission-id>/delegations/<delegation-id>/` | Created at PreToolUse Tier B dispatch, closed at mission close | Yes |
| Audit store | `<repo>/.punt-labs/ethos/sessions/<date>-<session-id>/audit.jsonl` | Appended on every tool call (PostToolUse) | Yes |
| Git store | `.git/` (commits, trailers) | Created at `git commit` | Yes |
| Conversation store | `<repo>/.punt-labs/quarry/captures/session-<session-id>.md` | Captured at context compaction by quarry | Yes |

## Artifact Inventory

### 1. Mission Contract

**File:** `missions/<mission-id>/contract.yaml`

**Question it answers:** What was authorized? Who was responsible?

**Content:**
- `mission_id` — the natural identifier (e.g., `m-2026-05-25-002`)
- `leader` — who scoped the work
- `worker` — who executed it
- `evaluator.handle` + `evaluator.hash` — who reviewed, frozen at creation so the evaluator can't be swapped mid-mission
- `write_set` — which files the worker was authorized to touch
- `extract_into` — which directories authorized for new-file creation
- `success_criteria` — the acceptance bar, in the leader's words
- `context` — the reasoning the leader provided: why this work matters, what constraints apply, what the CEO said
- `status` — open → closed/failed/escalated
- `created_at`, `closed_at`, `updated_at` — timestamps
- `budget.rounds` — how many review cycles were budgeted
- `type` — implement, investigate, etc.
- `inputs.ticket` — the bead/issue tracker ID that initiated the work

**Linkage:**
- `mission_id` → delegation records (one-to-many: a mission can have multiple delegations)
- `mission_id` → mission event log (`log.jsonl` sibling)
- `mission_id` → results (`results.yaml` sibling)
- `inputs.ticket` → external issue tracker (beads)
- `mission_id` appears as `contract_id` on audit entries

### 2. Mission Event Log

**File:** `missions/<mission-id>/log.jsonl`

**Question it answers:** What state transitions occurred, in what order?

**Content:** One JSONL line per event:
- `create` — mission opened, names worker + evaluator
- `result` — worker submitted a structured result (verdict, confidence, evidence)
- `close` — mission reached terminal state
- `round_advanced` — leader approved advancement to next review round
- `reflect` — leader recorded a reflection on the round

Each line carries `ts`, `event`, `actor`, and a `details` map specific to the event type.

**Linkage:**
- Lives alongside `contract.yaml` — same `mission_id` directory
- `actor` field → identity handles (leader or worker)

### 3. Mission Results

**File:** `missions/<mission-id>/results.yaml`

**Question it answers:** What was the worker's structured output? What evidence did they provide?

**Content:**
- `round` — which review round this result covers
- `author` — the worker who produced it
- `verdict` — pass, fail, or escalate
- `confidence` — 0.0 to 1.0
- `files_changed` — list of paths + line counts (added/removed)
- `evidence` — list of named checks with pass/fail/skip status
- `prose` — free-form summary from the worker

**Linkage:**
- `mission` field → contract
- `files_changed` paths → git blame → back to this mission

### 4. Mission Trace Index

**File:** `missions.jsonl` (at the `<repo>/.punt-labs/ethos/` root)

**Question it answers:** What missions have been completed in this repo? (Flat query surface for `ethos find missions`.)

**Content:** One JSONL line per closed mission — a denormalized summary containing mission_id, status, leader, worker, evaluator, ticket, write_set, success_criteria, rounds_used, verdict, files_changed, created_at, closed_at.

**Natural identifier:** `id` field = mission_id

**Linkage:**
- `id` → per-mission directory
- `ticket` → bead/issue tracker
- Used by `ethos find missions` and the UI dashboard
- Used by the blame fallback (commit subject contains ticket → missions.jsonl lookup → mission → delegation)

### 5. Delegation Record

**File:** `missions/<mission-id>/delegations/<delegation-id>/record.yaml`

**Question it answers:** Who was spawned, under what tier, and what was the outcome?

**Content:**
- `id` — the delegation identifier (e.g., `d-2026-05-25-011`)
- `tier` — `B` (contract-bound) or `A` (ad-hoc, though Tier A doesn't produce a record today)
- `mission` — the parent mission this delegation belongs to
- `agent_type` — the specialist who was spawned (e.g., `bwk`)
- `parent_session` — the session that dispatched this delegation
- `parent_delegation` — for nested spawns, the parent delegation in the chain
- `created_at` — when the spawn fired
- `closed_at` — when the mission closed (stamps the delegation)
- `verdict` — pass, fail, error, or aborted

**Linkage:**
- `id` (delegation_id) → audit entries (`delegation_id` field on every tool call)
- `mission` → parent contract
- `parent_session` → session audit log AND quarry conversation capture
- `id` appears as git trailer `Delegation: <id>` on commits made during this delegation
- Prompt sibling links the delegation to its instructions

### 6. Delegation Prompt

**File:** `missions/<mission-id>/delegations/<delegation-id>/prompt.md`

**Question it answers:** What exactly was the agent told to do? What reasoning did the leader provide?

**Content:** The verbatim prompt the leader sent to the worker via the Agent() dispatch. Contains the task description, reading order, constraints, acceptance criteria, and often the CEO's directives or product context that motivated the work.

**Natural identifier:** Same directory as `record.yaml` — identified by delegation-id.

**Linkage:**
- Lives alongside `record.yaml`
- The prompt text is also captured in the audit log (the `Agent` tool call's `tool_input.prompt` field) — the file is the authoritative copy

### 7. Session Audit Log

**File:** `sessions/<date>-<session-id>/audit.jsonl`

**Question it answers:** What did every agent do, tool call by tool call?

**Content:** One JSONL line per tool invocation. Fields:
- `ts` — RFC3339 timestamp
- `session` — the Claude Code session ID (shared between parent and all subagents)
- `agent_id` — unique per-subagent invocation (absent for the main session thread)
- `agent_type` — the specialist name (e.g., `bwk`)
- `delegation_id` — which delegation this call falls under (set via the delegation-binding sidecar)
- `contract_id` — which mission contract governs this call (= mission_id)
- `parent_session` — the session that spawned the subagent
- `tool` — Read, Edit, Write, Bash, Grep, Glob, Agent, etc.
- `tool_input` — the full tool input map, with paths redacted (`$HOME` → `~`, `repoRoot` → `<repo>`)
- `tool_input_hash` — sha256 of the redacted canonical-JSON input (machine-independent, for dedup/collision detection)
- `tool_input_preview` — 200-char human-readable preview

**Natural identifier:** `session` + `ts` + `tool` (composite). The `delegation_id` field is the join key to the delegation store.

**Linkage:**
- `delegation_id` → delegation record + prompt
- `contract_id` → mission contract
- `session` → session directory name → quarry capture filename
- `agent_id` → groups all calls from one subagent invocation
- `tool_input.file_path` (for Read/Edit/Write) → git blame on the same file

### 8. Git Commit Trailers

**Location:** Git commit messages (trailer block)

**Question they answer:** Under what contract and delegation was this commit made?

**Content:**
- `Mission: <mission-id>` — the governing contract
- `Delegation: <delegation-id>` — the specific agent dispatch

**Natural identifier:** The commit SHA.

**Linkage:**
- `Mission` trailer → mission contract directory
- `Delegation` trailer → delegation record + prompt + audit trail
- Commit SHA → `git blame` (per-line attribution)
- Commit SHA → GitHub commit page

**How they land:** The `hooks/commit-msg.sh` git hook reads the delegation-binding sidecar (`~/.punt-labs/ethos/sessions/<session-id>/delegation-binding`) and appends trailers when a Tier B delegation is active. Commits made before the sidecar fix (v3.12.0) don't have trailers; the UI blame view falls back to parsing the bead/ticket ID from the commit subject and looking it up in `missions.jsonl`.

### 9. Conversation Transcript (Quarry Capture)

**File:** `<repo>/.punt-labs/quarry/captures/session-<session-id>.md`

**Question it answers:** What was the full conversation context? What did the leader discuss with the operator before dispatching? What reasoning led to the approach?

**Content:** Full markdown transcript of the Claude Code session — user messages, assistant responses, tool calls and their outputs, system reminders. Contains the pre-dispatch reasoning, the operator's directives, the design discussions, and the dispatch decisions.

**Natural identifier:** The session ID (same as the audit log's `session` field and the delegation record's `parent_session` field).

**Linkage:**
- Session ID = audit log session directory name = delegation `parent_session`
- Searchable by mission ID, delegation ID, bead ID, agent name, or any keyword
- Quarry indexes captures for semantic search (`quarry find`)
- Captures are snapshots at context-compaction time — a long session may have multiple captures, or the most recent work may not yet be captured

### 10. Delegation-Binding Sidecar

**File:** `~/.punt-labs/ethos/sessions/<session-id>/delegation-binding`

**Question it answers:** What delegation is currently active for this session? (Runtime bridge, not a forensic artifact.)

**Content:** Three newline-separated values:
- Line 1: delegation_id
- Line 2: mission_id
- Line 3: parent_session

**Purpose:** Bridges the gap between PreToolUse (where the delegation is allocated) and PostToolUse (where audit entries need the delegation_id). Claude Code's `additional_env` from PreToolUse does not persist into hook script subprocesses, so this sidecar is the transport mechanism.

**Lifecycle:** Written at Tier B dispatch time, read by the PostToolUse audit writer and the commit-msg git hook, overwritten by the next Tier B dispatch, implicitly stale when the session ends.

### 11. Active-Mission Sidecar

**File:** `~/.punt-labs/ethos/sessions/<session-id>/active-mission`

**Question it answers:** What mission has the leader claimed for this session?

**Content:** A single line — the mission_id.

**Purpose:** Bridges the gap between `ethos mission claim` and the next Agent() dispatch. The leader's Claude Code session cannot export `MISSION_ID` into its own process env, so the sidecar is how the PreToolUse hook discovers the active mission.

**Lifecycle:** Written by `ethos mission claim`, read by the PreToolUse dispatch, cleared by `ethos mission release` or naturally stale at session end.

## Join Keys

The traceability graph is navigable because every artifact shares
at least one key with its neighbors:

| Key | Appears in | Joins to |
|-----|-----------|----------|
| `mission_id` | contract.yaml, log.jsonl, results.yaml, delegation record, audit entry (`contract_id`), missions.jsonl, git trailer (`Mission:`), active-mission sidecar, delegation-binding sidecar | Everything about one mission |
| `delegation_id` | delegation record, audit entry, git trailer (`Delegation:`), delegation-binding sidecar | Every tool call and commit under one agent dispatch |
| `session_id` | audit log directory name, delegation `parent_session`, quarry capture filename, sidecar directory names | All activity in one Claude Code session, plus the conversation transcript |
| `agent_id` | audit entry | All tool calls from one subagent invocation within a session |
| `ticket` / bead ID | contract `inputs.ticket`, missions.jsonl, commit subject (by convention) | External issue tracker, commit-to-mission fallback |
| `commit SHA` | git history, git trailer block, GitHub commit page | Per-line blame to delegation chain |
| `file_path` | audit entry `tool_input`, git blame, contract `write_set` | Which files were touched under which contract |

## The Blame Chain (End-to-End)

Starting from a line of code and tracing back to the full reasoning:

```
git blame file.go:42
  → commit 2b851cc (Claude Agento, 2026-05-25)

git log --format='%(trailers)' 2b851cc
  → Mission: m-2026-05-25-002
  → Delegation: d-2026-05-25-011

missions/m-2026-05-25-002/contract.yaml
  → leader: claude, worker: bwk, evaluator: rsc
  → write_set: [internal/hook/generate_agents.go, ...]
  → success_criteria: [detect project type at generation time, ...]
  → context: "Bug report from CEO: in a Python project..."

missions/m-2026-05-25-002/delegations/d-2026-05-25-011/record.yaml
  → agent_type: bwk, tier: B, verdict: pass

missions/m-2026-05-25-002/delegations/d-2026-05-25-011/prompt.md
  → "Fix the hardcoded Go file-extension patterns..."
  → (the exact instructions that explain WHY the code looks this way)

ethos audit show --delegation d-2026-05-25-011
  → 35 tool calls: Read generate_agents.go, Edit ×5, go test ×3,
    staticcheck, make check, git commit
  → (the exact sequence of actions that produced the code)

quarry find "extension matcher generate_agents"
  → session-f8f75233.md — the conversation where the leader discussed
    the bug with the CEO, decided on the approach, and dispatched bwk
  → (the reasoning context that preceded the instructions)
```

## What's Not Yet Captured

| Gap | What's missing | Impact |
|-----|---------------|--------|
| Model reasoning | The model's chain-of-thought (why bwk chose THAT specific edit, not an alternative) | The prompt says what to do; the audit shows what was done; but the internal reasoning is in the Claude conversation, not the audit log. Quarry captures fill this partially. |
| Tier A delegation records | Ad-hoc Agent() calls without a mission produce no `record.yaml` — only audit-log tagging by `delegation_id` | Tier A spawns are traceable via the audit log but have no on-disk record to browse in the UI |
| Tool output | The audit log captures `tool_input` but not `tool_result` — you can see WHAT was asked but not what came back | Adding tool_result would roughly double audit log size |
| Cross-session continuity | A leader may discuss a problem in session A, then dispatch the fix in session B. The delegation links to session B only. | Quarry's semantic search can bridge sessions by content; the structural link (session_id) is single-session |
| Pre-trailer commits | Commits made before the commit-msg.sh sidecar fix have no `Mission:`/`Delegation:` trailers | The UI blame view falls back to bead-ID parsing from commit subjects; works for most commits but isn't structural |
