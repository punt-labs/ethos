# Build Plan: Quarry Memory Integration (Phase 3)

Bead: ethos-c3v
Branch: feat/quarry-memory-hooks
Design doc: ~/.claude/plans/generic-booping-bengio.md

## Overview

Add memory awareness to ethos hooks. When an agent's identity has
`ext/quarry` configured, SessionStart and PreCompact inject instructions
explaining what quarry is, where the agent's memories live, and how to
use them.

## What exists

- `ext/quarry` config: `ethos ext set claude quarry memory_collection "memory-claude"`
- Quarry MCP tools accept `agent_handle`, `memory_type`, `collection` params
- SessionStart emits persona block via `BuildPersonaBlock(id)`
- PreCompact emits persona block + team context via `BuildPersonaBlock(id)` + `buildTeamSection(deps)`
- Identity `Ext` map is populated at load time from `~/.punt-labs/ethos/identities/<handle>.ext/*.yaml`

## Changes

### 1. New file: `internal/hook/memory.go`

New function `BuildMemorySection(ext map[string]map[string]string, handle string) string`.

Reads the `quarry` namespace from the identity's ext map. If
`memory_collection` key exists, builds and returns a memory section.
If no quarry ext, returns empty string.

Expected ext keys:

- `memory_collection` (required) — e.g., "memory-claude"
- `expertise_collections` (optional) — comma-separated, e.g., "claude-books,claude-blogs"

Output format:

```text
## Memory

You have persistent memory stored in quarry, a local semantic search
engine. Your memories survive across sessions and machines.

### Working Memory

Collection: "memory-claude"

To recall prior knowledge:
  /find <query> — or use the quarry find tool with collection="memory-claude", agent_handle="claude"

To persist something you learned:
  /remember <content> — or use the quarry remember tool with collection="memory-claude", agent_handle="claude", memory_type=fact|observation|procedure|opinion

Memory types:
- fact: objective, verifiable information ("the API rate limit is 100 req/s")
- observation: neutral summary of an entity or system
- procedure: how-to knowledge ("when deploying, run migrations first")
- opinion: subjective assessment with confidence

### Expertise

Your expertise corpus is in collections: claude-books, claude-blogs.
Search these for deep domain knowledge. Expertise does not decay over time.
```

The agent handle is derived from the identity's `Handle` field, not
hardcoded.

### 2. Modify `internal/hook/session_start.go`

After `BuildPersonaBlock(agentID)` (line 131), call
`BuildMemorySection(agentID.Ext)` passing the agent identity's handle.
Append the result to `msg` if non-empty.

The identity is already loaded with extensions at line 101:
`agentID, agentLoadErr := store.Load(agentPersona)` — `store.Load`
populates `agentID.Ext` from the ext directory.

### 3. Modify `internal/hook/pre_compact.go`

After `BuildPersonaBlock(id)` (line 79), call `BuildMemorySection(id.Ext)`
passing the identity's handle. Append the result to `sections` if non-empty.

The identity is already loaded with extensions at line 68:
`id, err := deps.Identities.Load(agentPersona)` — the LayeredStore's
Load populates `id.Ext` from global (extensions always come from global).

### 4. Tests: `internal/hook/memory_test.go`

Table-driven tests for `BuildMemorySection`:

- Empty ext map → empty string
- No quarry namespace → empty string
- quarry namespace with memory_collection only → memory section with working memory, no expertise
- quarry namespace with memory_collection + expertise_collections → full section with both
- quarry namespace with expertise_collections but no memory_collection → empty string (memory_collection is required)

### 5. Update existing tests

- `TestHandleSessionStart_*` — verify memory section appears in output when ext/quarry is configured
- `TestHandlePreCompact_*` — verify memory section appears in output when ext/quarry is configured

## Acceptance criteria

1. `BuildMemorySection` with ext/quarry returns non-empty markdown section
2. `BuildMemorySection` without ext/quarry returns empty string
3. SessionStart output includes memory section when ext/quarry configured
4. PreCompact output includes memory section when ext/quarry configured
5. Agent handle in instructions matches the identity's handle, not hardcoded
6. `make check` passes
7. No changes to files outside the spec

## Delegate to: bwk
