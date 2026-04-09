# Mission Skill Design

**Status**: Phase A shipped 2026-04-09 as
`~/.claude/skills/mission/SKILL.md`, deployed by `ethos seed`. The
skill drives the Phase 3 mission primitive (DES-031 through
DES-037) — see DESIGN.md for the runtime layer. Phases B and C are
PLANNED.

How ethos bridges the gap between high-level delegation guidance
and the low-level `Agent` spawn primitive, while letting the Phase
3 runtime enforce every invariant at the store boundary.

## Problem

Three layers exist for directing agent work:

| Layer | What it provides | Gap |
|-------|------------------|-----|
| Delegation standards | "Delegate to specialists, review their work" | Too abstract — no structure for what a delegation should contain |
| **/mission skill** | Scaffolds a typed mission contract and registers it in the store | **Phase A shipped** |
| `Agent(subagent_type, prompt)` | Raw spawn with freeform prompt string | Too low-level — quality depends entirely on prompt discipline |

Before Phase 3, leaders wrote freeform delegation prompts and
quality varied. Common failures: vague scope, no success criteria,
no file ownership, no constraints, delegating understanding instead
of synthesized specs.

Phase 3 closed the runtime half of the gap. It built the typed
`mission.Contract`, write-set admission (DES-032), frozen evaluator
(DES-033), bounded rounds with mandatory reflection (DES-034),
verifier isolation (DES-035), result artifacts and close gate
(DES-036), and the append-only event log reader (DES-037). Every
primitive is enforced at the store boundary — a malformed contract,
a stolen write, a stale evaluator, or a missing result artifact is
refused before it can hurt anything.

But Phase 3 left the other half of the gap open: there was no
user-facing interface to drive the runtime. Every mission contract
was a hand-written markdown file the leader pasted into a
delegation prompt. The `/mission` skill closes that gap.

## Design

### Skill: `/mission`

A Claude Code skill that scaffolds a Phase 3 mission contract from
conversation context, runs `ethos mission create` to register it in
the store, and spawns the worker via the `Agent` tool. The skill is
pure markdown — no Go code in the skill itself. Claude reads the
file and follows the scaffolding protocol.

### Who invokes it

Leaders only. Sub-agents cannot spawn other sub-agents (Claude Code
constraint), so the skill is a no-op inside a delegation. The
SKILL.md states this explicitly at the top.

### Trigger

Invoked when the leader is about to delegate a bounded task with
clear success criteria and a known set of files to touch. Not for
exploratory research, not for work the leader intends to do
themselves, not for epics that need decomposition.

### Flow

The skill walks Claude through six steps.

**Step 1 — Resolve the worker.** Read the team roster via the
ethos team MCP tool. Match the worker to the task using the
role-to-handle mapping (Go → bwk, Python → rmh, CLI → mdm, etc.).
If the team store is not configured, ask the leader to name a
handle directly. The worker and evaluator must be distinct handles
and must not share a role — `ethos mission create` refuses the
contract otherwise (DES-033 verifier isolation).

**Step 2 — Scaffold the contract YAML.** Build the contract from
conversation context. Every field maps to the typed
`mission.Contract` schema. The strict decoder rejects unknown
fields, so typos in key names are hard errors at create time. The
SKILL.md enumerates the required fields, the optional fields, and
the server-controlled fields the store overwrites.

**Step 3 — Pick the evaluator.** The evaluator is the frozen
reviewer pinned at create time (DES-033). The hash is computed
from the evaluator's personality, writing style, talents, and role
assignments. If any of those files change after create, the
verifier hook refuses the spawn — this is the anti-drift guarantee.
Defaults: `djb` for security-sensitive code, `mdm` for CLI and
developer-experience work, `mdm` or `bwk` for Go internals, `rmh`
for Python library design.

**Step 4 — Create the mission.** Run `ethos mission create --file
<path>` and capture the returned mission ID. The CLI is silent on
success in human mode; `--json` returns the full contract. The MCP
server exposes an equivalent `mission` tool with a `create` method
that takes the YAML body as a `contract` string argument. Both
paths enforce the same trust boundary.

Common failures — read the error, fix the contract, retry:

- Write-set overlap with an open mission (DES-032)
- Unresolvable evaluator handle
- Worker and evaluator share a role
- Budget rounds out of [1, 10]

**Step 5 — Spawn the worker.** Use the `Agent` tool with
`subagent_type` set to the worker handle and `run_in_background:
true`. The main session must stay responsive. The prompt's job is
to point the worker at the mission, not to restate the contract.
The worker reads the contract from the store via `ethos mission
show <id>` as its first action, and Phase 3 enforcement fires from
there.

**Step 6 — Track and review.** The leader monitors via `ethos
mission show`, `ethos mission log`, `ethos mission results`, and
`ethos mission reflections`. When the worker reports back, the
leader closes via `ethos mission close <id>` (the close gate
refuses the transition until a valid result for the current round
exists), or continues into another round via `ethos mission
reflect` + `ethos mission advance`.

### Mission Contract Schema

The skill scaffolds a contract matching the typed `mission.Contract`
struct in `internal/mission/mission.go`. The YAML shape:

```yaml
leader: claude                 # handle of the leader
worker: bwk                    # handle of the worker
evaluator:
  handle: djb                  # frozen reviewer
inputs:
  bead: ethos-9ai.5            # optional bead binding
  files:                       # optional — files the worker MUST read
    - internal/role/role.go
  references:                  # optional — supporting docs
    - DESIGN.md
context: |                     # optional, TOP-LEVEL free-text design notes
  Add a Model field to the Role struct.
write_set:                     # required — at least one repo-relative path
  - internal/role/role.go
  - internal/role/role_test.go
success_criteria:              # required — at least one verifiable string
  - Role struct has a Model string field
  - make check passes
budget:
  rounds: 2                    # required — integer in [1, 10]
  reflection_after_each: true  # required — boolean
tools: []                      # optional — worker allowlist
```

Server-controlled fields (overwritten by `Store.ApplyServerFields`
regardless of what the YAML supplies): `mission_id`, `status`,
`created_at`, `updated_at`, `closed_at`, `evaluator.pinned_at`,
`evaluator.hash`, `current_round`.

The contract is the trust boundary. Validation rejects malformed
input at parse time; storage is flock-protected; the event log is
append-only (DES-031, DES-037).

### What the skill reads

| Data | Source | Purpose |
|------|--------|---------|
| Team roster | ethos team MCP tool | Available workers and roles |
| Role definitions | ethos role MCP tool | Tools and responsibilities |
| Conversation context | Current session | Scaffold the contract fields |

The team and role MCP tools are registered only when their stores
are configured. The skill handles their absence by asking the
leader to name a handle directly.

### What the skill does NOT do

- **Does not replace leader judgment.** Every field in the
  scaffolded contract is confirmable. The leader edits before
  submit. The skill scaffolds; the leader decides.
- **Does not manage the worker during execution.** Once spawned,
  the worker runs independently and reports back via the result
  artifact. The leader monitors via the event log and the result
  log.
- **Does not evaluate results.** The leader reads the result
  artifact and decides pass, continue, fail, or escalate.
  Evaluation is a separate concern.
- **Does not work from sub-agents.** Sub-agents cannot spawn other
  sub-agents (Claude Code constraint). The `/mission` skill is
  only valid in the primary session.
- **Does not auto-resolve write-set conflicts.** Phase 3.2
  enforces write-set admission at the store. The skill surfaces
  the conflict to the leader; resolution is the leader's call.
  File-conflict auto-resolution is Phase C.
- **Does not create or update beads.** Beads are optional inputs
  (via `inputs.bead`); mission lifecycle is independent of bead
  lifecycle. Bead integration is Phase C.

### Relationship to existing tools

| Tool | What it does | How /mission relates |
|------|--------------|----------------------|
| `Agent()` | Raw spawn primitive | /mission generates the prompt for Agent() |
| /feature-dev | Full development workflow | /mission handles one delegation within a workflow |
| TaskCreate | Work item tracking | Independent — missions track their own lifecycle |
| /plan | Set current work context | /mission sets plan before spawning |
| /who | Check team availability | /mission reads the same data |

### Worked example

The user says: "We need to add a `model` field to the Role struct
so generated agents can express a model preference (sonnet vs
opus)." The leader sizes this as a 30-minute Go job for `bwk`, with
`djb` evaluating because the change touches identity-adjacent code.
The leader invokes `/mission`.

The skill scaffolds the contract:

```yaml
leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  bead: ethos-9ai.x
context: |
  Add a Model field to internal/role/role.go and wire it through
  GenerateAgentFiles. Default to "inherit" when empty so
  pre-existing roles round-trip without loss.
write_set:
  - internal/role/role.go
  - internal/role/role_test.go
  - internal/hook/generate_agents.go
  - internal/hook/generate_agents_test.go
success_criteria:
  - Role struct has a Model string field with yaml and json tags
  - GenerateAgentFiles emits model in frontmatter when non-empty
  - Default is "inherit" when Model is empty
  - make check passes
  - tests cover model set, empty, and "inherit" cases
budget:
  rounds: 2
  reflection_after_each: true
```

The leader confirms. The skill writes the YAML to
`.tmp/missions/role-model.yaml`, runs `ethos mission create --file
.tmp/missions/role-model.yaml`, and captures the returned ID
(e.g. `m-2026-04-09-001`). The skill then spawns `bwk` via
`Agent(subagent_type="bwk", prompt="Mission m-2026-04-09-001 is
yours. Read it first via 'ethos mission show m-2026-04-09-001'.
After your work, submit a result via 'ethos mission result
m-2026-04-09-001 --file <path>'. Do not commit — return results
to me.", run_in_background=true)`.

While `bwk` works, the leader checks progress via `ethos mission
log m-2026-04-09-001 --event create,result`. `bwk` reports back
with a `pass` verdict. The leader reads the result via `ethos
mission results m-2026-04-09-001`, then closes the mission with
`ethos mission close m-2026-04-09-001`. The event log now carries
`create`, `result`, `close`.

## Phased delivery

**Phase A — skill file (SHIPPED 2026-04-09):** The skill ships as
`~/.claude/skills/mission/SKILL.md`, deployed by `ethos seed`. The
skill instructions tell Claude how to scaffold a Phase 3 contract,
run `ethos mission create`, spawn the worker via `Agent` with
`run_in_background: true`, and track the mission via the existing
CLI and MCP surfaces. No slash command, no MCP method, no bead
integration. Pure prompt engineering over the existing runtime.

**Phase B — slash command (PLANNED):** Ship as `/ethos:mission`
slash command. Reads team data eagerly, pre-populates fields,
presents confirmable options as interactive prompts. Still uses
`Agent()` for the spawn. The goal is to collapse the multi-turn
interactive flow of Phase A into a single command invocation.

**Phase C — conflict detection and bead integration (PLANNED):**
Surface write-set conflicts before the YAML is submitted, not
after. Add bead integration so a mission create optionally updates
the linked bead status. This requires either extending the store
with a dry-run create mode or reading the open-mission write sets
at scaffold time.

## Invariants

1. **The skill uses the actual Phase 3 primitive.** No reinventing
   the contract format. Every field in the scaffolded YAML maps to
   a real `mission.Contract` field. The skill runs `ethos mission
   create` to register the contract in the store — it does not
   just spawn an agent with a prompt that contains the "contract."
2. **The skill is markdown only.** No Go code in the skill itself.
   The skill teaches Claude what to do; Claude executes it.
3. **The skill is invoked by the leader, not sub-agents.**
4. **The skill does not replace leader judgment.** The contract
   YAML is scaffolded, not auto-generated. Every field is
   confirmable.
5. **The skill respects the actual CLI and MCP surface.** Use the
   real command names, the real flags, the real YAML field names.
6. **The skill is self-contained.** It does not require
   pre-existing missions, beads, or session state.
7. **The skill handles the no-team / no-roles case gracefully.**
   If the team or role MCP tools are unavailable, fall back to
   asking the leader to name the worker handle directly.

## References

- DES-031 — Mission contract (typed delegation artifact)
- DES-032 — Cross-mission write-set admission control
- DES-033 — Frozen evaluator (content hash pinning)
- DES-034 — Bounded rounds with mandatory reflection
- DES-035 — Verifier isolation
- DES-036 — Result artifacts and close gate
- DES-037 — Append-only mission event log reader API
- `internal/mission/mission.go` — Contract schema (source of truth)
- `cmd/ethos/mission.go` — CLI handlers
- `internal/mcp/mission_tools.go` — MCP tool dispatch
- `internal/seed/sidecar/skills/mission/SKILL.md` — the skill body
