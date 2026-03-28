# Quarry Integration

Quarry is a local semantic search engine. Ethos provides identity.
The two are independent — quarry works without ethos, ethos works
without quarry. When combined, agents gain persistent memory and
expertise scoped to their identity.

## What Each System Does Alone

**Quarry without ethos**: indexes files, answers semantic queries,
manages collections. Any user or agent can search, ingest, and
retrieve. No identity awareness — everything is project-scoped.

**Ethos without quarry**: resolves identity, emits persona and team
context at session start and compaction. Agents know who they are
and who their teammates are. No memory.

## What They Do Together

With ethos, quarry gains one capability it cannot provide alone:
**agent-scoped persistent memory**. Ethos tells quarry *who* the
agent is. Quarry gives the agent memories and expertise tied to
that identity. Memories follow the agent across repos and sessions.

## How It Works

Quarry stores its configuration in ethos's generic extension
mechanism (DES-008):

```text
~/.punt-labs/ethos/identities/<handle>.ext/quarry.yaml
```

### Current Implementation

The extension contains config keys and a `session_context` field.
At session start and before context compaction, ethos emits the
`session_context` verbatim — no parsing, no quarry-specific code.
The instructions come from quarry's extension file, not from ethos.

See DES-022 for the design rationale.

Example (`claude.ext/quarry.yaml`):

```yaml
memory_collection: memory-claude
session_context: |
  ## Memory

  You have persistent memory stored in quarry, a local semantic
  search engine. Your memories survive across sessions and machines.

  ### Working Memory

  Collection: "memory-claude"

  To recall prior knowledge:
    /find <query> — or use the quarry find tool with
    collection="memory-claude", agent_handle="claude"

  To persist something you learned:
    /remember <content> — or use the quarry remember tool with
    collection="memory-claude", agent_handle="claude",
    memory_type=fact|observation|procedure|opinion

  Memory types:
  - fact: objective, verifiable information
  - observation: neutral summary of an entity or system
  - procedure: how-to knowledge
  - opinion: subjective assessment with confidence
```

The agent receives these instructions from the first message. After
context compaction, the same instructions are re-injected so the
agent doesn't lose access to its memories.

## Extension Schema

| Key | Required | Description |
|-----|----------|-------------|
| `memory_collection` | Yes | Quarry collection name for this agent's working memory |
| `expertise_collections` | No | Comma-separated collection names for deep domain knowledge |
| `session_context` | No | Markdown instructions emitted at session start and compaction |

If `session_context` is absent, no memory instructions are emitted.
The `memory_collection` and `expertise_collections` keys are consumed
by quarry directly (via the sidecar contract) — ethos does not read
them.

## Integration Points

### Ethos Side

SessionStart and PreCompact hooks iterate over all extensions that
have a `session_context` field and emit them after the persona block.
This is generic — not quarry-specific. Any extension can provide
session context the same way (DES-022).

### Quarry Side

Quarry reads the agent's handle from ethos (via `ethos whoami` or
the ethos MCP `identity` tool) to scope memory operations:

- `/remember` tags memories with the agent's handle
- `/find` filters by the agent's handle
- PreCompact auto-captures session transcripts attributed to the agent

Quarry reads `memory_collection` from `ext/quarry.yaml` directly
(sidecar contract — no ethos API call needed).

## What Flows Where

```text
ethos                              quarry
─────                              ──────
ext/quarry.yaml session_context ──→ instructions in model context
ext/quarry.yaml memory_collection   (quarry reads directly via sidecar)
ethos whoami                    ──→ agent handle for scoping
                                ←── /find, /remember (agent actions)
                                ←── transcript capture (PreCompact)
```

## Ownership Boundaries

| Concern | Owner |
|---------|-------|
| Identity (who the agent is) | Ethos |
| Extension storage and emission | Ethos |
| Session context content (instructions) | Quarry (via ext file) |
| Collection names, memory types, slash commands | Quarry |
| Memory storage, search, retention | Quarry |

Ethos emits quarry's instructions without understanding them.
Quarry writes its own instructions without importing ethos.

## Design Decisions

**Generic extension, not consumer-specific code.** Session context
uses the extension mechanism (DES-008) and generic emission
(DES-022). No `BuildMemorySection` or quarry-specific Go code in
ethos. Adding a new consumer's session context requires zero ethos
code changes.

**One-way dependency.** Quarry depends on ethos for identity. Ethos
has zero knowledge of quarry's internals.

**Per-agent collections.** Each agent gets its own memory collection
(e.g., `memory-claude`). Memories follow the agent across repos.

**Expertise as separate collections.** Domain knowledge lives in
separate collections from working memory. Expertise doesn't decay;
working memory does.

## Design References

- DES-008: Generic extension mechanism
- DES-022: Extension-provided session context
- `internal/hook/session_start.go` — emits extension session context
- `internal/hook/pre_compact.go` — emits extension session context
- `internal/identity/store.go` — loads `ext/` directory into `Identity.Ext`
