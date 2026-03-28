# Agent Teams

Agent Teams is a Claude Code experimental feature (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`) that spawns multiple Claude Code processes as a coordinated team within a single repo.

## Architecture

Each teammate is a **separate Claude Code process** with its own:

- Session ID (from Claude Code)
- PID and process tree
- Context window
- MCP server instances (all plugins load independently)
- Skills (100+ available, same as lead)
- Hooks (SessionStart, PreCompact, etc. — all fire independently)

Teammates do NOT inherit the lead's conversation history. They start fresh with CLAUDE.md, MCP servers, and the task prompt.

## Process Model

```text
Lead (claude PID 12438, launched via ~/.local/bin/claude symlink)
├── alpha  (claude PID 63379, spawned as ~/.local/share/claude/versions/2.1.86)
├── bravo  (claude PID 63618, spawned as ~/.local/share/claude/versions/2.1.86)
└── charlie (claude PID 63685, spawned as ~/.local/share/claude/versions/2.1.86)
```

The lead is launched via the `claude` symlink. Teammates are spawned directly by the lead's binary. This means the teammate's process name is the version number (e.g., `2.1.86`), not `claude`. See "PID Discovery" below.

## Communication

### SendMessage (intra-team)

The only way for teammates to communicate. Text output is NOT visible to other agents.

```text
Human → types in REPL → lead receives as conversation turn
Lead  → SendMessage(to: "alpha") → alpha receives as conversation turn
Alpha → SendMessage(to: "team-lead") → lead receives as conversation turn
Alpha → SendMessage(to: "bravo") → bravo receives as conversation turn
Lead  → SendMessage(to: "*") → broadcast to all teammates (plain text only)
```

The human has no direct access to teammates — everything goes through the lead.

### Biff (cross-repo)

Each teammate's biff MCP server connects to the NATS relay independently. All teammates appear in `/who` as separate `@claude-puntlabs:ttyN` entries. They are not distinguishable by name — all show as `claude-puntlabs`.

Verified: teammates see each other and the lead in `/who`. The lead sees teammates after NATS presence propagation (not instant — may take a few seconds).

## Task List

Shared filesystem-based task list at `~/.claude/tasks/<team-name>/`.

Each task is a JSON file named by sequential ID:

```json
// ~/.claude/tasks/my-team/1.json
{
  "id": "1",
  "subject": "Implement feature X",
  "description": "What needs to be done",
  "activeForm": "Implementing feature X",
  "status": "pending",
  "blocks": ["2"],
  "blockedBy": [],
  "owner": "alpha"
}
```

Fields:

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Sequential numeric ID |
| `subject` | string | Brief title, imperative form |
| `description` | string | What needs to be done |
| `activeForm` | string | Present continuous form for spinner display |
| `status` | enum | `pending` → `in_progress` → `completed` (or `deleted`) |
| `blocks` | string[] | Task IDs that cannot start until this completes |
| `blockedBy` | string[] | Task IDs that must complete before this can start |
| `owner` | string | Teammate name (e.g., `"alpha"`) |

## Team Config

Created by `TeamCreate` at `~/.claude/teams/<team-name>/config.json`:

```json
{
  "name": "my-team",
  "description": "Working on feature X",
  "createdAt": 1774672635443,
  "leadAgentId": "team-lead@my-team",
  "leadSessionId": "e992be6e-f8f6-4096-836f-17869ff095e6",
  "members": [
    {
      "agentId": "team-lead@my-team",
      "name": "team-lead",
      "agentType": "team-lead",
      "model": "claude-opus-4-6[1m]",
      "joinedAt": 1774672635443,
      "tmuxPaneId": "",
      "cwd": "/Users/jfreeman/Coding/punt-labs/ethos"
    },
    {
      "agentId": "alpha@my-team",
      "name": "alpha",
      "agentType": "general-purpose",
      "model": "claude-opus-4-6",
      "prompt": "...",
      "color": "blue",
      "joinedAt": 1774672645782,
      "tmuxPaneId": "35B524F3-...",
      "cwd": "/Users/jfreeman/Coding/punt-labs/ethos",
      "backendType": "iterm2",
      "isActive": true
    }
  ]
}
```

Key fields: `leadSessionId` links to the lead's Claude session. Each member has `name` (used for SendMessage routing), `agentType` (matches `.claude/agents/<name>.md` if custom), `tmuxPaneId`, and `backendType`.

## Environment Variables

Teammates inherit these from the lead's environment:

```bash
CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1
CLAUDECODE=1
CLAUDE_CODE_ENTRYPOINT=cli
GIT_AUTHOR_NAME=Claude Agento
GIT_AUTHOR_EMAIL=claude@punt-labs.com
GIT_COMMITTER_NAME=Claude Agento
GIT_COMMITTER_EMAIL=claude@punt-labs.com
```

No team-specific env vars (no `CLAUDE_TEAM_NAME` or similar). The teammate discovers its team by reading `~/.claude/teams/*/config.json`.

## Hook Behavior

All plugin hooks fire for teammates, same as the lead:

| Hook | Fires? | Verified |
|------|--------|----------|
| SessionStart (ethos) | Yes | Agent definitions deployed, session created |
| SessionStart (quarry) | Yes | CWD auto-registered as collection |
| SessionStart (beads) | Yes | `bd prime` returns context |
| PreCompact | Yes | Persona re-injected (not yet tested for teammates) |

The SessionStart hook payload for teammates includes a valid `session_id`:

```json
{
  "session_id": "92438591-dcc9-4766-b68c-879981e75330",
  "transcript_path": "...",
  "cwd": "/Users/jfreeman/Coding/punt-labs/ethos",
  "hook_event_name": "SessionStart",
  "source": "startup",
  "model": "claude-opus-4-6"
}
```

## PID Discovery

Ethos uses `FindClaudePID()` to walk the process tree and find the topmost `claude` ancestor. This is used to key session PID files at `~/.punt-labs/ethos/sessions/current/<pid>`.

**Problem (fixed in v2.2.2):** The lead's claude process is launched via the `~/.local/bin/claude` symlink, so `ps -o comm` shows `claude`. Teammates are spawned directly by the binary at `~/.local/share/claude/versions/2.1.86`, so `ps -o comm` shows `2.1.86`. The `isClaudeComm` function only matched `claude`, causing `FindClaudePID` to fail for teammates.

**Fix:** On macOS, `readProc` checks if the executable path (from `kern.procargs2`) contains `/claude/versions/` and normalizes the comm to `claude`. On Linux, `readProc` inspects `/proc/<pid>/exe` for the same pattern. This allows `FindClaudePID` to find the teammate's claude ancestor regardless of how it was launched.

## Ethos Session Behavior

Each teammate gets its own independent ethos session:

```text
Lead session:     e992be6e-...  (jfreeman + claude PID 12438)
Alpha session:    927ac858-...  (jfreeman + claude PID 63379)
Bravo session:    4c3463d1-...  (jfreeman + claude PID 63618)
Charlie session:  4d9bfa60-...  (jfreeman + claude PID 63685)
```

Sessions are separate — the lead's `ethos session roster` does not show teammates. Teammate identity resolves as `Claude Agento (claude)` via `ethos whoami`.

## Lifecycle

```text
TeamCreate          → creates ~/.claude/teams/<name>/config.json
                      creates ~/.claude/tasks/<name>/

Agent(name, team)   → spawns teammate as separate Claude Code process
                      teammate loads CLAUDE.md, plugins, MCP servers
                      SessionStart hooks fire independently
                      teammate receives task prompt

TaskCreate          → creates ~/.claude/tasks/<name>/<id>.json
TaskUpdate(owner)   → assigns task to teammate by name

SendMessage         → routes message to teammate's inbox
                      delivered as new conversation turn

shutdown_request    → teammate approves and terminates

TeamDelete          → removes ~/.claude/teams/<name>/
                      removes ~/.claude/tasks/<name>/
```

## Workflow

```python
# 1. Create team
TeamCreate(team_name="feature-x")

# 2. Create tasks
TaskCreate(subject="Implement API endpoint", description="...")
TaskCreate(subject="Write tests", description="...", blockedBy=["1"])

# 3. Spawn teammates
Agent(name="impl", subagent_type="bwk", prompt="Implement the API...")
Agent(name="reviewer", subagent_type="djb", prompt="Review the code...")

# 4. Assign tasks
TaskUpdate(taskId="1", owner="impl")

# 5. Teammates work, complete tasks, go idle
# 6. Lead coordinates via SendMessage

# 7. Shutdown
SendMessage(to="impl", message={"type": "shutdown_request"})
SendMessage(to="reviewer", message={"type": "shutdown_request"})
TeamDelete()
```
