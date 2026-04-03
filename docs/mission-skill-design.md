# Mission Skill Design

How ethos bridges the gap between high-level delegation guidance
(CLAUDE.md) and low-level agent primitives (Agent tool).

## Problem

Three layers exist for directing agent work:

| Layer | What It Provides | Gap |
|-------|-----------------|-----|
| CLAUDE.md | "Delegate to specialists, review their work" | Too abstract — no structure for what a delegation should contain |
| **??? (mission skill)** | Structured task contract with typed fields | **Missing** |
| Agent(subagent_type, prompt) | Raw spawn with freeform prompt string | Too low-level — quality depends entirely on prompt discipline |

Leaders write freeform delegation prompts. Quality varies. Common
failures: vague scope, no success criteria, no file ownership, no
constraints, delegating understanding instead of synthesized specs.

The agent-definitions guide documents the mission template, but
documentation is passive. A skill is active — it scaffolds the
prompt, validates the contract, and enforces structure at delegation
time.

## Design

### Skill: `/mission`

A Claude Code skill that scaffolds and executes mission-shaped
delegations using ethos team and role data.

### Trigger

Invoked when the leader wants to delegate a bounded task:

- `/mission` — interactive mission builder
- `/mission @bwk "Implement BuildExtensionContext"` — shorthand
- Natural language: "send bwk to implement the extension context function"

### Flow

**Step 1: Resolve the agent.**

Read the team roster via ethos. Show available agents with their
roles, tools, and current status (idle/busy via session roster).
If the user named an agent, confirm. If not, suggest based on the
task description and role match.

```
Available agents for this task:

  bwk  — Go specialist (implementer)
         Tools: Read, Write, Edit, Bash, Grep, Glob
         In session: no

  rmh  — Python specialist (implementer)
         Tools: Read, Write, Edit, Bash, Grep, Glob
         In session: no

  djb  — Security reviewer (security-reviewer)
         Tools: Read, Grep, Glob, Bash
         In session: no

Recommended: bwk (task involves Go code)
```

Note: the session roster tracks presence (joined/left), not activity
state. Idle/busy detection would require a future extension.

**Step 2: Build the mission contract.**

Scaffold the mission from context. Pre-populate fields from what
the leader has discussed in the conversation. Present as confirmable
options (autostar pattern — don't silently infer).

```
Mission for bwk:

  Task: Implement BuildExtensionContext in internal/hook/

  Inputs:
    - DES-022 design (extension-provided session context)
    - Current HandleSessionStart as reference pattern
    ↳ Add or modify? [confirm]

  Outputs:
    - internal/hook/extension_context.go (new file)
    - internal/hook/extension_context_test.go (new file)
    ↳ Add or modify? [confirm]

  Success criteria:
    - All extension namespaces iterated
    - session_context key collected from each
    - Empty string when no extensions
    - Tests: nil, empty, single, multiple namespaces
    - make check passes
    ↳ Add or modify? [confirm]

  files_owned:
    - internal/hook/extension_context.go
    - internal/hook/extension_context_test.go
    ↳ Confirm? [y]

  Constraints:
    - No ethos consumer knowledge (DES-008)
    - Function signature matches BuildPersonaBlock pattern
    ↳ Add or modify? [confirm]
```

**Step 3: Check for conflicts.**

Before spawning:
- Check session roster for active agents
- Check if any files_owned overlap with another agent's claimed files
- If overlap: warn and offer options (queue, worktree, abort)

**Step 4: Confirm and spawn.**

Show the complete mission. Leader confirms. Skill spawns the agent
with the structured prompt.

The spawn uses `Agent(subagent_type=handle, prompt=mission_text, run_in_background=true)`.
The mission_text is the formatted contract -- not freeform prose.
In this workflow, run_in_background=true keeps the mission asynchronous so the leader can continue tracking and later review the results against the contract.

**Step 5: Track.**

Optionally create or update a bead for the mission. Record the
mission contract, agent handle, and spawn time. When the agent
completes, the leader reviews results against the success criteria.

### Mission Contract Format

The skill generates a prompt in this structure:

```
Task: [one-sentence description]

Inputs:
  - [specific files, data, or synthesized findings]

Outputs:
  - [files to create or modify]
  - [format for structured results if applicable]

Success criteria:
  - [concrete, verifiable conditions]
  - [tests that must pass]

files_owned:
  - [paths this agent may create or modify -- nothing else]

Constraints:
  - [design decisions already made]
  - [what NOT to do]
```

This is the prompt the agent receives. Combined with:
- The agent definition (persona + role from ethos)
- SubagentStart hook injection (personality, writing style, team context)
- CLAUDE.md (project rules)
- baseline-ops skill (operational discipline)

The agent has everything: who it is, what it can do, what it should
do right now, how to work, and what rules to follow.

### What the Skill Reads

| Data | Source | Purpose |
|------|--------|---------|
| Team roster | ethos team MCP tool | Available agents and roles |
| Session roster | ethos session MCP tool | Active/idle status |
| Role definitions | ethos role MCP tool | Tools and responsibilities |
| Conversation context | Current session | Pre-populate mission fields |

The role and team MCP tools are registered only when their stores are
configured. The skill should handle their absence gracefully.
| Bead state | beads CLI | Track mission in work tracker |

### What the Skill Does NOT Do

- Does not replace the leader's judgment. The leader writes the task
  description, defines success criteria, and confirms the contract.
  The skill scaffolds; the leader decides.
- Does not manage the agent during execution. Once spawned, the agent
  works independently. The leader monitors via task notifications.
- Does not evaluate results. The leader reviews agent output against
  the success criteria. Evaluation is a separate concern.
- Does not work from sub-agents. Sub-agents cannot spawn other
  sub-agents (Claude Code constraint). The /mission skill is only
  valid in the primary session.

### Relationship to Existing Tools

| Tool | What It Does | How /mission Relates |
|------|-------------|---------------------|
| Agent() | Raw spawn primitive | /mission generates the prompt for Agent() |
| /feature-dev | Full development workflow (7 phases) | /mission handles one delegation within a workflow |
| TaskCreate | Work item tracking | /mission optionally creates tasks for tracking |
| /plan | Set current work context | /mission sets plan before spawning |
| /who | Check team availability | /mission reads the same data to show available agents |

### Phased Delivery

**Phase A (MVP — skill file only):**

Ship as `~/.claude/skills/mission/SKILL.md`. The skill instructions
tell Claude how to scaffold missions. No Go code, no MCP tools —
pure prompt engineering. Claude reads ethos team/role data via
existing MCP tools and follows the scaffolding protocol.

**Phase B (structured — slash command):**

Ship as `/ethos:mission` slash command. Reads team data, pre-populates
fields, presents confirmable options. Still uses Agent() for spawn.

**Phase C (integrated — with conflict detection):**

Add file ownership tracking. Check for overlapping claims before
spawn. Integrate with beads for mission tracking. This requires
coordination-layer support (roadmap 4.3 write-set admission).

### Example Session

```
User: I need bwk to add a model field to the Role struct

Claude: Let me build that mission.

/mission @bwk

  Task: Add model field to Role struct and wire through GenerateAgentFiles

  Inputs:
    - internal/role/role.go (current Role struct)
    - internal/hook/generate_agents.go (current generation logic)
    - ETHOS-ROADMAP.md §1.4 (requirements)

  Outputs:
    - Modified internal/role/role.go (model field added)
    - Modified internal/hook/generate_agents.go (model in frontmatter)
    - New/modified tests for both files

  Success criteria:
    - Role struct has Model string field with yaml/json tags
    - GenerateAgentFiles includes model in frontmatter when non-empty
    - Default: "inherit" when model is empty
    - make check passes
    - Tests cover: model set, model empty, model inherit

  files_owned:
    - internal/role/role.go
    - internal/role/role_test.go
    - internal/hook/generate_agents.go
    - internal/hook/generate_agents_test.go

  Constraints:
    - No changes to Role Store or LayeredStore interfaces
    - Field name: Model (not ModelHint, not PreferredModel)
    - YAML key: model (lowercase)

Confirm and send? [y]

→ Spawning bwk with mission...
→ Mission tracked as bead ethos-xyz
```
