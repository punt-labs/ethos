# Quarry Mission Tagging

How ethos and quarry work together to capture the reasoning behind
missions. This is the third layer of the audit trail model.

## Three-Layer Audit Trail

| Layer | What | Where | Answers |
|-------|------|-------|---------|
| **Git** | Contracts, results, ADRs | `.ethos/` in repo | What was decided |
| **Local** | Event logs, session audit | `~/.punt-labs/ethos/` | What happened in detail |
| **Quarry** | Conversation transcripts | LanceDB (local) | Why was it decided |

## How It Works

Quarry's PreCompact hook already captures conversation transcripts
before context compaction. The missing piece: tagging those transcript
chunks with the active mission ID so semantic search can find the
reasoning behind specific missions.

### Current State

1. Quarry's PreCompact hook fires before context compression
2. The hook captures the conversation transcript as chunks
3. Chunks are indexed in LanceDB with metadata (session, timestamp)
4. No mission context is attached to the chunks

### Proposed Change

When quarry captures a transcript, it checks whether ethos has an
active mission in the current session:

1. Query ethos for active missions: `ethos mission list --status open --json`
2. If one or more missions are open, tag each chunk with the mission IDs
3. The tag is metadata on the chunk, not content modification

### Search Examples

After tagging, these quarry searches work:

```text
/find "why did we choose conventions over enforcement?"
# Returns: conversation chunks from the session where DES-041 was discussed,
# tagged with mission m-2026-04-10-003

/find "reasoning for write-set design"
# Returns: chunks tagged with missions that touched write-set code
```

### Implementation

**Quarry side** (changes in punt-labs/quarry):

The PreCompact hook handler needs to:

1. Check if `ethos` CLI is available (`command -v ethos`)
2. If available, run `ethos mission list --status open --json`
3. Extract mission IDs from the response
4. Add `mission_ids` to the chunk metadata when ingesting

This follows the standard integration pattern: check for ethos,
use if present, skip if not. Quarry works fine without ethos.

**Ethos side** (no code changes needed):

Ethos already provides `ethos mission list --status open --json`.
The CLI integration guide (docs/integration/cli.md) documents this
pattern. No ethos changes required.

### Verification

After implementation, verify:

```bash
# Start a session with an active mission
ethos mission create --file contract.yaml
# ... do work, let compaction happen ...

# Search for mission context
quarry find "reasoning for mission m-2026-04-10-003"
# Should return transcript chunks tagged with the mission ID
```

## Cross-Repo Coordination

This feature requires changes in the quarry repo only. The ethos
repo provides the CLI interface (already shipped) and this design
doc. File a bead in the quarry project to implement the tagging.
