# Traceability Data Assets

Agent-driven development produces artifacts at every level — from
org-wide conventions to individual tool calls. Together they form
a traceability graph that answers "what happened, who did it, why,
under what authority, and what conventions governed the work" for
every line of code an agent produces.

This document inventories every data source, ordered by scope from
broadest (global conventions that shape all work) to narrowest
(individual tool calls within a single delegation).

## Scope Tiers

| Tier | Scope | What it governs | Examples |
|------|-------|----------------|----------|
| 1 | **Global / User** | Conventions that apply to all projects for a user or all users on a machine | `~/.claude/CLAUDE.md`, managed policy, user settings |
| 2 | **Project** | Conventions, identities, and config for one repository | `CLAUDE.md`, `.claude/rules/`, `.claude/settings.json`, ethos identities/roles/teams |
| 3 | **Work item** | Why a piece of work exists and what it authorized | Beads (issues), mission contracts |
| 4 | **Execution** | What an agent did under a specific delegation | Delegation records, prompts, audit logs, git commits + trailers |
| 5 | **Knowledge** | What was discussed, what was learned, what the code says | Conversation transcripts, Claude memories, source code, design docs |
| 6 | **Runtime** | Ephemeral bridges between hooks | Sidecars (active-mission, delegation-binding) |

---

## Tier 1: Global / User Conventions

### 1a. Managed Policy CLAUDE.md

**File:** `/Library/Application Support/ClaudeCode/CLAUDE.md` (macOS) or `/etc/claude-code/CLAUDE.md` (Linux)

**Question it answers:** What org-wide rules apply to every agent session on this machine?

**Scope:** All users, all projects. Cannot be excluded or overridden. Loads first in every session.

**Content:** Org-level policies — security rules, compliance requirements, banned patterns, mandatory tooling. Set by IT/DevOps, not by individual developers.

**Linkage:** Shapes every mission contract, every delegation prompt, every tool call. Not referenced by ID — it's ambient authority.

### 1b. User CLAUDE.md

**File:** `~/.claude/CLAUDE.md`

**Question it answers:** What personal conventions does this developer apply to all their projects?

**Scope:** One user, all projects. Loads after managed policy, before project CLAUDE.md.

**Content:** Personal working style, communication preferences, tool preferences, banned patterns. At Punt Labs: delegation rules, team roster, mission workflow, PR procedure, session close protocol.

**Linkage:** Shapes all work by this user. Project CLAUDE.md can narrow or override it. Not project-specific — the same file governs work across every repo.

### 1c. User Settings

**File:** `~/.claude/settings.json`

**Question it answers:** What permissions, hooks, and configuration does this user apply globally?

**Content:** Permission allowlists (`allow`, `deny`), MCP server configs, hook registrations, model preferences, output style. Merges with project settings (project wins on conflict).

---

## Tier 2: Project Conventions

### 2a. Project CLAUDE.md

**File:** `<repo>/CLAUDE.md` or `<repo>/.claude/CLAUDE.md`

**Question it answers:** What are this project's development standards, workflow rules, and architectural decisions?

**Scope:** Everyone working on this repo. Git-tracked, team-shared. Loads after user CLAUDE.md.

**Content:** Build commands (`make check`), quality gates, coding standards, team delegation rules, mission workflow, definition of done, pre-PR checklist, tool usage rules, operational constraints. Also: `CLAUDE.local.md` (gitignored) for per-developer overrides at the same scope level.

**Linkage:**

- Referenced by mission contracts (success criteria often say "make check passes" which is defined here)
- Standards in CLAUDE.md govern how agents write code, run tests, and handle reviews
- The ancestor walk loads CLAUDE.md files from filesystem root down to cwd — parent directories above the repo contribute too

### 2b. Project Rules

**File:** `<repo>/.claude/rules/*.md`

**Question it answers:** What conventions apply to specific file types or paths in this project?

**Scope:** Per-path within the project. Git-tracked. Loads lazily when matching files are opened.

**Content:** Markdown files with `paths:` YAML frontmatter (glob patterns). Rules without `paths:` load at startup like CLAUDE.md. Path-specific rules trigger when Claude reads matching files.

**Example:** A `python-oo.md` rule with `paths: ["**/*.py"]` enforces OO patterns only when Python files are touched.

**Linkage:** Rules shape code quality for specific file types — an agent editing `.py` files gets Python-specific instructions that don't load when editing `.go` files.

### 2c. Project Settings and Hooks

**File:** `<repo>/.claude/settings.json`, `<repo>/.claude/settings.local.json`

**Question it answers:** What project-specific permissions, hooks, and configuration are in effect?

**Content:** Project-scoped permission rules, hook registrations (SessionStart, PreToolUse, PostToolUse, etc.), MCP server config, plugin enablement. `.local.json` variant is gitignored for per-developer overrides.

**Linkage:** Hooks registered here (e.g., ethos's PreToolUse and PostToolUse) are what produce the audit trail, enforce write_set contracts, and write delegation skeletons. Without these hooks, ethos has no runtime presence.

### 2d. Agent Definitions

**File:** `<repo>/.claude/agents/*.md`

**Question it answers:** What specialist personas are available for delegation, and what tools/hooks does each carry?

**Scope:** Project-scoped. Git-tracked. Generated by `ethos setup` or `GenerateAgentFiles`.

**Content:** YAML frontmatter (name, description, tools, hooks) + markdown body (personality, writing style, talents, anti-responsibilities). Each file defines one specialist agent type (e.g., `bwk.md` = Go specialist).

**Linkage:**

- `subagent_type` in Agent() dispatch → agent definition filename
- Delegation `record.yaml` `agent_type` field → this agent's handle
- Agent's PostToolUse hook configuration determines which file changes trigger `make check`

### 2e. Ethos Identity Registry

**File:** `<repo>/.punt-labs/ethos/{identities,roles,teams,personalities,writing-styles,talents}/*.{yaml,md}`

**Question it answers:** Who are the agents and humans on this project? What are their roles, expertise, and behavioral characteristics?

**Scope:** Project-scoped. Git-tracked (vendored from `punt-labs/team` or maintained locally).

**Content:** Identity YAML (name, handle, kind, personality slug, writing-style slug, talents), role YAML (responsibilities, tools), team YAML (membership, collaborations, reports_to edges), personality/writing-style/talent markdown (behavioral content).

**Linkage:**

- Identity handles appear on mission contracts (leader, worker, evaluator)
- Roles determine tool access and anti-responsibilities in generated agent files
- Team collaborations shape the delegation graph (who reports to whom)
- Evaluator hash (frozen at mission create) links to the identity content at creation time

### 2f. Repo Configuration

**File:** `<repo>/.punt-labs/ethos.yaml`

**Question it answers:** What is this project's ethos configuration?

**Content:** `agent:` (default agent handle), `team:` (active team name), `active_bundle:` (which team bundle is in use), `max_delegation_depth:` (depth limit for nested spawns).

---

## Tier 3: Work Items

### 3a. Issue Tracker (Beads)

(Previously #13 in the flat inventory.)

**File:** `<repo>/.beads/` (local Dolt database, synced to hosted DoltDB)

**Question it answers:** Why does this work exist? What was the business or technical motivation? What's the acceptance criteria?

**Content:** Each bead carries id, title, description (the full context: what's broken, why it matters), type (bug/feature/task), priority (P0–P4), status, acceptance criteria, design decisions, notes, labels, dependencies.

**Natural identifier:** The bead ID (e.g., `ethos-ozrb`). Globally unique via per-repo prefix.

**Linkage:**

- `bead ID` → mission contract `inputs.ticket` (the mission that implements this bead)
- `bead ID` → commit subject (by convention)
- `bead ID` → missions.jsonl `ticket` field (blame fallback)
- Dependencies between beads express sequencing constraints

**Why it matters:** The bead is the ORIGIN — it answers "why did anyone start working on this?" before the mission contract exists.

## Artifact Inventory

The remaining artifacts are the execution, knowledge, and runtime
layers — numbered for cross-reference with the use cases document.

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

### 13. Issue Tracker (Beads)

**Location:** `<repo>/.beads/` (local Dolt database, synced to hosted DoltDB)

**Question it answers:** Why does this work exist? What was the business or technical motivation? What's the acceptance criteria?

**Content:** Each bead (issue) carries:

- `id` — the bead identifier (e.g., `ethos-ozrb`)
- `title` — one-line summary of the problem or feature
- `description` — the full context: what's broken, why it matters, what the fix looks like
- `type` — bug, feature, task
- `priority` — P0 (critical) through P4 (backlog)
- `status` — open, in_progress, closed, deferred
- `acceptance` — the criteria that define "done"
- `design` — design decisions recorded at filing time
- `notes` — supplementary context
- `labels` — repo scoping (e.g., `repo:ethos`)
- `dependencies` — which beads block this one

**Natural identifier:** The bead ID (e.g., `ethos-ozrb`). Globally unique across all repos via per-repo prefix.

**Linkage:**

- `bead ID` → mission contract `inputs.ticket` field (the mission that implements this bead)
- `bead ID` → commit subject (by convention, every commit names its bead in parentheses)
- `bead ID` → missions.jsonl `ticket` field (the blame fallback uses this join)
- `bead ID` → the UI blame view's fallback chain (commit subject → missions.jsonl → delegation)
- Dependencies between beads express sequencing constraints across work items

**Why it matters for traceability:** The bead is the ORIGIN of the work — it answers "why did anyone start working on this?" before the mission contract, before the delegation, before the first tool call. A blame chain that stops at the mission contract still doesn't explain why the mission was created. The bead does: "Bug report from CEO: in a Python project, spawning any sub-agent caused the SessionStart hook to regenerate agent files with Go-specific patterns."

### 14. Claude Code Project Memory

**Location:** `~/.claude/projects/<project-path-hash>/memory/` (per-project, per-user, per-machine)

**Question it answers:** What operational knowledge has accumulated across sessions? What mistakes were made and corrected? What preferences does the operator have?

**Scope limitation:** Memories are **per-user, per-machine** — not git-tracked, not shareable. Claude Code explicitly prevents project-local memory paths to avoid cloned repos redirecting memory writes to sensitive locations. An investigator on a different machine cannot see another developer's memories unless they are explicitly harvested (copied into the repo and committed).

**Content:** Markdown files with YAML frontmatter, organized by type:

- `user` memories — the operator's role, expertise, preferences
- `feedback` memories — corrections the operator has given ("don't argue with my decisions", "always smoke test storage changes", "at PR convergence, merge without pausing")
- `project` memories — ongoing work context, deadlines, decisions
- `reference` memories — pointers to external systems (where bugs are tracked, which Grafana board to watch)

Each memory file has:

- `name` — kebab-case slug
- `description` — one-line summary used for relevance matching
- `metadata.type` — user, feedback, project, reference
- Body content with the actual knowledge, often including `**Why:**` and `**How to apply:**` sections

**Natural identifier:** The `name` slug (e.g., `feedback-execute-dont-argue`).

**Index:** `MEMORY.md` in the same directory — first 200 lines / 25KB loaded at session start.

**Linkage:**

- Memory files are scoped to the repo path (all sessions against the same repo share one memory directory for that user)
- `feedback` memories often reference specific incidents by session date, bead ID, or PR number
- `project` memories track what's in flight, what decisions were made, what to watch for
- The agent loads `MEMORY.md` at session start and consults relevant memories before acting — so memories influence future mission contracts, dispatch decisions, and review criteria

**Why it matters for traceability:** Memories capture LESSONS — not what happened (that's the audit log) but what was LEARNED from what happened. A feedback memory like "smoke test storage changes — after any storage/layout change, ls the destination from the real entry point" exists because the storage-activation bug shipped three releases without anyone checking the actual file paths on disk. The memory links the lesson to the incident.

**Traceability limitation:** Because memories are per-user and not in the repo, they are an input to agent behavior (they shape future decisions) but not a shared audit artifact. Two developers working on the same repo have different memories. An investigator tracing a delegation can see the mission contract, the prompt, and the audit trail — but not the memories that influenced HOW the leader wrote the prompt. Harvesting memories into the repo (as a periodic export) would close this gap but requires explicit action.

**Distinction from quarry:** Quarry captures are raw transcripts — everything that was said. Memories are curated distillations — the operator's corrections and the agent's takeaways, extracted from those transcripts and persisted as durable operational knowledge. Quarry is the record; memory is the learning.

## Join Keys

The traceability graph is navigable because every artifact shares
at least one key with its neighbors:

| Key | Appears in | Joins to |
|-----|-----------|----------|
| `mission_id` | contract.yaml, log.jsonl, results.yaml, delegation record, audit entry (`contract_id`), missions.jsonl, git trailer (`Mission:`), active-mission sidecar, delegation-binding sidecar | Everything about one mission |
| `delegation_id` | delegation record, audit entry, git trailer (`Delegation:`), delegation-binding sidecar | Every tool call and commit under one agent dispatch |
| `session_id` | audit log directory name, delegation `parent_session`, quarry capture filename, sidecar directory names | All activity in one Claude Code session, plus the conversation transcript |
| `agent_id` | audit entry | All tool calls from one subagent invocation within a session |
| `ticket` / bead ID | contract `inputs.ticket`, missions.jsonl, commit subject (by convention), bead database | Origin of the work — why this mission exists |
| `commit SHA` | git history, git trailer block, GitHub commit page | Per-line blame to delegation chain |
| `file_path` | audit entry `tool_input`, git blame, contract `write_set` | Which files were touched under which contract |
| `memory name` | memory file slug, MEMORY.md index, feedback/project body text referencing beads or PRs | Operational lessons linked to the incidents that produced them |
| `repo path` | Claude Code project memory directory, beads `metadata.json` prefix, `.punt-labs/ethos/` state root | Scopes all per-project state to one repository |

## The Blame Chain (End-to-End)

Starting from a line of code and tracing back to the full reasoning:

```text
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

### 12. Source Code and Design Documents

**Location:** The repository itself — `internal/`, `cmd/`, `docs/`, `DESIGN.md`, `CHANGELOG.md`, etc.

**Question they answer:** What does the system actually do? What design decisions were made and why?

**Content:** The code is the ground truth for behavior. Design documents (`DESIGN.md`, `docs/architecture.tex`, ADRs) record architectural decisions, rejected alternatives, and the rationale that shaped the code. `CHANGELOG.md` records what shipped in each release and why.

**Natural identifier:** File path (for code), DES-NNN identifiers (for design decisions).

**Linkage:**

- `git blame <file>` → commit → trailer → mission → delegation (the blame chain)
- `contract.write_set` names the files the mission was authorized to touch
- `audit.tool_input.file_path` names the files the agent actually read/edited
- Quarry indexes all of these for semantic search — "why did we choose X over Y" resolves to the design doc section, the conversation where it was discussed, and the mission that implemented it

**Quarry indexing:** The working directory is auto-indexed at session start. Design docs, code, and conversation captures are all searchable by meaning via `quarry find`. A query like "why does the delegation record use a sidecar instead of env vars" returns hits across code comments, design docs, and conversation transcripts — three perspectives on the same decision.

## What's Not Yet Captured

| Gap | What's missing | Impact |
|-----|---------------|--------|
| Model reasoning | The model's chain-of-thought (why bwk chose THAT specific edit, not an alternative) | The prompt says what to do; the audit shows what was done; but the internal reasoning is in the Claude conversation, not the audit log. Quarry captures fill this partially. |
| Tier A delegation records | Ad-hoc Agent() calls without a mission produce no `record.yaml` — only audit-log tagging by `delegation_id` | Tier A spawns are traceable via the audit log but have no on-disk record to browse in the UI |
| Tool output | The audit log captures `tool_input` but not `tool_result` — you can see WHAT was asked but not what came back | Adding tool_result would roughly double audit log size |
| Cross-session continuity | A leader may discuss a problem in session A, then dispatch the fix in session B. The delegation links to session B only. | Quarry's semantic search can bridge sessions by content; the structural link (session_id) is single-session |
| Pre-trailer commits | Commits made before the commit-msg.sh sidecar fix have no `Mission:`/`Delegation:` trailers | The UI blame view falls back to bead-ID parsing from commit subjects; works for most commits but isn't structural |
