# Mission

Scaffold a Phase 3 mission contract, register it in the store, and
spawn the worker. This skill turns a conversation about "who does
what" into a typed, enforced delegation — the write set is admitted,
the evaluator is frozen, rounds are bounded, the event log records
every transition.

## Who invokes this

Leaders only. Sub-agents cannot spawn other sub-agents (Claude Code
constraint), so this skill is a no-op inside a delegation. If you
are reading this from inside an `Agent()` call, stop — report back
to your leader and let them invoke the skill themselves.

## When to use

Invoke when:

- You are about to delegate a bounded task with clear success
  criteria and a known set of files to touch.
- The work is sized for 1–3 rounds of one worker plus one evaluator.
- You want the Phase 3 runtime (write-set admission, frozen
  evaluator, bounded rounds, result artifacts, event log) to enforce
  the contract instead of trusting prompt discipline.

Do not invoke for exploratory research, work you intend to do
yourself, or epics that need decomposition first.

## Pre-flight

Before scaffolding, confirm you have a one-paragraph task
description, a rough list of files the worker will touch, at least
one verifiable success criterion, and a worker handle (or a
willingness to pick one). If anything is missing, ask the leader.
Do not invent.

## Step 1 — Resolve the worker

Read the team roster via the ethos team MCP tool. If the team store
is not configured, ask the leader to name a handle directly.

Match the worker to the task:

- Go code → `bwk`
- Python code → `rmh`
- CLI design → `mdm`
- Security review → `djb`
- ML / inference → `kpz`
- Infrastructure / CI → `adb`

The worker and evaluator must be distinct handles and must not
share a role — `ethos mission create` refuses the contract otherwise
(DES-033). Surface the choice to the leader as confirmable.

## Step 2 — Scaffold the contract YAML

Build the contract from conversation context. Every field maps to
the typed `mission.Contract` schema — no freeform prose, no invented
keys. The strict decoder rejects unknown fields, so a typo in a key
name is a hard error at create time.

Required fields:

- `leader` — your handle
- `worker` — the handle from Step 1
- `evaluator.handle` — the frozen reviewer (Step 3)
- `inputs` — a map; `bead`, `files`, and `references` are optional
- `write_set` — repo-relative paths; no absolute paths, no `..`
  segments, at least one entry
- `success_criteria` — at least one verifiable string
- `budget.rounds` — integer in [1, 10]
- `budget.reflection_after_each` — boolean

Optional: `tools` (worker allowlist), `context` (free-text design
notes), `session`, `repo`.

Do NOT populate these fields — the store overwrites them:
`mission_id`, `status`, `created_at`, `updated_at`, `closed_at`,
`evaluator.pinned_at`, `evaluator.hash`, `current_round`.

Present the scaffolded YAML to the leader in a fenced block and ask
for confirmation. The leader may edit any field. Never submit
without confirmation.

## Step 3 — Pick the evaluator

The evaluator is the frozen reviewer pinned at create time
(DES-033). The hash is computed from the evaluator's personality,
writing style, talents, and role assignments at creation. If any of
those files change after create, the verifier hook refuses the
spawn. The evaluator cannot be changed after create.

Defaults by task type:

- Security-sensitive code (auth, crypto, parsing, input handling,
  process boundaries) → `djb`
- Go internals and library design → `mdm` or `bwk`
- Python library design → `rmh`
- CLI or developer-experience work → `mdm`

Confirm the evaluator with the leader before moving on.

## Step 4 — Create the mission

Write the confirmed YAML to a scratch file and run:

```bash
ethos mission create --file .tmp/missions/contract.yaml
```

The command is silent on success in human mode. Capture the
returned mission ID (e.g. `m-2026-04-09-001`) — use `--json` on
create, or read it from `ethos mission list --json`.

The ethos MCP server exposes a `mission` tool with a `create`
method that takes the YAML body as a `contract` string argument.
Either path enforces the same trust boundary.

Common create failures — read the error, fix the contract, retry:

- Write-set overlap with an open mission (DES-032). The error names
  the blocking mission and paths. Close the other mission or narrow
  your write set.
- Unresolvable evaluator handle — the handle does not map to an
  identity with personality, writing style, talents, and role.
- Worker and evaluator share a role — pick a different evaluator.
- Budget rounds out of [1, 10].

Do not bypass.

## Step 5 — Spawn the worker

Use the `Agent` tool with `subagent_type` set to the worker handle
and `run_in_background: true`. The main session must stay responsive
— a foreground spawn blocks the leader.

The prompt's job is to point the worker at the mission, not to
restate the contract. The worker reads the contract from the store
via `ethos mission show <id>` as its first action. Phase 3
enforcement fires from there.

Prompt template:

```text
Mission <id> is yours. Read it first: `ethos mission show <id>`.
The contract names the write set, success criteria, and budget.
Your first write must land inside the write set — the store
refuses anything else. After your work for this round, submit a
result artifact: `ethos mission result <id> --file <path>`. See
`ethos mission result --help` for the YAML shape. The mission
will refuse to close until a valid result for the current round
exists. Do not commit, push, or merge — return results to me.
```

## Step 6 — Track and review

While the worker runs, monitor from the leader session:

- `ethos mission show <id>` — contract, current round, latest result.
- `ethos mission log <id>` — append-only event log. Filter with
  `--event result,close` or `--since <RFC3339>`.
- `ethos mission results <id>` — full round-by-round result log.
- `ethos mission reflections <id>` — full reflection log.

When the worker reports back, read the result artifact and decide:

- Pass → `ethos mission close <id>`. The close gate refuses the
  transition until a valid result for the current round exists.
- Continue → submit a reflection via `ethos mission reflect <id>
  --file <path>`, then `ethos mission advance <id>` to bump the
  round counter. The advance gate refuses after a `stop` or
  `escalate` recommendation.
- Fail or escalate → `ethos mission close <id> --status failed` or
  `--status escalated`.

## Worked example

The user says: "We need to add a `model` field to the Role struct
so generated agents can express a model preference (sonnet vs
opus)." The leader sizes this as a 30-minute Go job for `bwk`, with
`djb` evaluating because the change touches identity-adjacent code.

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

The leader confirms. The skill writes it to
`.tmp/missions/role-model.yaml` and runs:

```bash
ethos mission create --file .tmp/missions/role-model.yaml
```

The store returns `m-2026-04-09-001`. The skill spawns `bwk`:

```text
Agent(
  subagent_type="bwk",
  prompt="Mission m-2026-04-09-001 is yours. Read it first via
          `ethos mission show m-2026-04-09-001`. After your work,
          submit a result via `ethos mission result
          m-2026-04-09-001 --file <path>`. Do not commit — return
          results to me.",
  run_in_background=true,
)
```

While `bwk` works, the leader checks progress:

```bash
ethos mission log m-2026-04-09-001 --event create,result
```

`bwk` reports back `pass`. The leader reads the result and closes:

```bash
ethos mission results m-2026-04-09-001
ethos mission close m-2026-04-09-001
```

The log now records `create`, `result`, `close`. The leader writes
the commit, pushes, and opens the PR.

## Design mission archetype

Some missions produce a document rather than code — an integration
design, an architecture decision, a protocol specification. These
are design missions. The contract shape and workflow differ from
code missions in three ways: the write set targets docs, the success
criteria require before/after user-visible impact, and the worker
often needs to talk to agents in other repos before writing anything.

### Contract shape

```yaml
leader: claude
worker: mdm                           # or the domain specialist
evaluator:
  handle: bwk                          # someone who reads the design
inputs:
  bead: ethos-abc
  references:
    - docs/existing-design.md          # prior art the worker should read
    - .punt-labs/ethos/teams/core.yaml # team graph for cross-repo scope
context: |
  One paragraph: what the design must decide, what repos are
  involved, what constraints exist. Name the agents who own the
  other repos — the worker will contact them.
write_set:
  - docs/my-design.md                  # docs, not code
success_criteria:
  - Design covers <topic> with concrete API contracts
  - Each integration point has a before/after showing user-visible change
  - No code changes required in this repo (or: minimal code changes listed)
  - make check passes
budget:
  rounds: 2
  reflection_after_each: true
```

The write set is typically one or two files under `docs/`. If the
design requires code changes, list those files too — but a design
mission that grows a large code write set is probably two missions.

Every success criterion that describes an integration point should
include a "before/after" requirement: what the user or operator sees
today versus what they will see after the design is implemented.
This forces the worker to ground the design in observable behavior
rather than internal abstractions.

### Cross-repo collaboration

Design missions that span repositories need information from agents
who own the other side. The worker should talk to those agents
before reading their code — the agent has context (plans, pending
changes, constraints) that the code does not.

**When biff is available:**

1. Run `/who` to see which agents are active across repos.
2. `/finger @<agent>` to check what they are working on.
3. `/write @<agent>` with a specific question: what the integration
   point looks like from their side, what constraints they have,
   what they plan to change. Include enough context that they can
   answer without reading your repo.
4. Wait for the response via `/read`. Incorporate their answers
   into the design. Attribute cross-repo decisions to the
   conversation ("agreed with @bwk via biff").

**Detecting biff availability:**

Biff is an MCP server, not a guaranteed dependency. Before using
biff tools, check whether the `tty` MCP server is loaded in the
current session. If it is not loaded, or if no agent is active in
the target repo, fall back to reading code directly.

The fallback chain:

1. Biff available, agent active → talk to the agent.
2. Biff available, no agent active → `/write` them anyway (they
   will see it when they start), then read their code and docs
   for immediate answers.
3. Biff not available → read the other repo's code, docs, and
   CLAUDE.md. Note in the design that cross-repo decisions are
   based on code reading, not agent conversation, and flag them
   for confirmation.

**Do not block on biff.** If the agent does not respond within the
round's working time, proceed with code-reading and mark the
decision as unconfirmed. A design with an unconfirmed decision is
better than no design.

### Worker selection

Design missions default to `mdm` for docs and CLI-facing designs,
or the domain specialist when the design is deep in their area
(e.g. `djb` for a security protocol, `kpz` for an inference
pipeline). The evaluator should be someone who will consume the
design — the implementer, not another writer.

## What this skill does NOT do

- Does not edit Phase 3 primitives. The runtime is frozen; this
  skill only drives it.
- Does not replace leader judgment. Every field is confirmable.
- Does not auto-resolve write-set conflicts. Surface the error to
  the leader.
- Does not manage the worker during execution.
- Does not evaluate results. The leader decides pass, continue,
  fail, or escalate.
- Does not create or update beads. Bead integration is via
  `inputs.bead` only.

## Reference

- `ethos mission create --help`, `show --help`, `log --help` — CLI
  surfaces with JSON payload shapes documented
- `DESIGN.md` DES-031 through DES-037 — runtime design
- `internal/mission/mission.go` — the `Contract` struct and YAML
  tags (schema source of truth)
