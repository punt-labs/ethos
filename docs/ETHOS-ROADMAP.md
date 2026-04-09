# Ethos Roadmap

Where ethos is going. Organized into phases that build on each other.

## Current Status (2026-04-09)

Ethos is at **v3.0**. Two of five roadmap phases are complete.

| Phase | Status | Summary |
|---|---|---|
| **Phase 1 — Batteries Included** | SHIPPED | 10 starter talents, 6 starter roles, baseline-ops skill, model field on Role |
| **Phase 2 — Production-Quality Agents** | PLANNED | Anti-responsibilities, role hooks, structured output, baseline-ops injection, `/mission` skill (beads `ethos-9ai.1`–`9ai.7`) |
| **Phase 3 — Workflow Primitives** | **SHIPPED** | All 7 primitives: mission contract, write-set admission, frozen evaluator, bounded rounds, verifier isolation, result artifacts, event log reader (beads `ethos-07m.5`–`07m.11`, ADRs DES-031 through DES-037) |
| **Phase 4 — Operational Excellence** | PLANNED | SessionStart working context, role-based pre-tool safety, audit logging (beads `ethos-gcq.1`–`gcq.3`) |
| **Phase 5 — Ecosystem** | FUTURE | Starter team templates, agent marketplace |

Phase 3 shipped 2026-04-08 on PR #184, merge `c16715f`. The four
architecture rules from `~/Documents/agents-architecture.tex` are
runtime-enforced for the first time in the project's history. See
DES-037 for the Phase 3.7 ADR and the Phase 3 completion narrative.

## What's next: `ethos-9ai.5` — the `/mission` skill MVP

Phase 3 built the rails. `ethos-9ai.5` puts a train on them. The
mission primitive is in production, but there is no user-facing
interface to drive it — every mission contract today is a 281–393
line markdown file the leader writes by hand and pastes into a
delegation prompt. The `/mission` skill closes that gap: it
scaffolds mission contracts from team data, checks for write-set
conflicts with running missions, and spawns workers via the
existing primitive. Without it, Phase 3 is impressive plumbing
without a faucet.

**Why `9ai.5` specifically is the strategic unlock:**

1. **It validates the Phase 3 abstractions.** Until an agent uses
   the `/mission` skill end-to-end to drive a real workflow, we do
   not know whether the abstractions Phase 3 built are usable. The
   skill is the proof point. If the abstractions are wrong, we find
   out building `9ai.5` — not after building `9ai.1`–`9ai.4` on top
   of them.

2. **It is the smallest meaningful step.** The MVP shape is a pure
   markdown skill file at `~/.claude/skills/mission/SKILL.md` — no
   Go code required. Phase A from
   [`docs/mission-skill-design.md`](mission-skill-design.md) is
   self-contained and can ship in a single cycle.

3. **It tests our own dogfooding.** Phase 3.7's 6-round cycle
   exposed friction in the manual contract pattern every round.
   The skill is the user-facing fix for that friction.

4. **It unblocks the rest of Phase 2.** Beads `9ai.1` through
   `9ai.4` generate better agents that USE the mission primitive.
   They need the skill in place first so the workflow they
   generate has somewhere to land.

5. **It maps to the three-pillar vision.** Identity (shipped),
   workflow (shipped), integration (next). The `/mission` skill is
   the first deliverable on the integration pillar — it is what
   makes the workflow primitive accessible, not just available.

### Recommended sequence

| # | Bead | What | Phase | Effort |
|---|---|---|---|---|
| 1 | **`ethos-9ai.5`** | `/mission` skill MVP (Phase A: skill file, no Go) | 2.6 | Small |
| 2 | `ethos-9ai.4` | Baseline-ops skill injection in generated agents | 2.4 | Trivial |
| 3 | `ethos-9ai.1` | Anti-responsibility generation from team graph | 2.1 | Medium |
| 4 | `ethos-9ai.2` | Role-based hooks in generated agent frontmatter | 2.2 | Medium |
| 5 | `ethos-9ai.3` | `output_format` field on Role | 2.3 | Small |
| — | `ethos-jjm` | Symlink hardening across all 4 mission loaders (Phase 3 follow-up, parallel — security debt closure) | 3 | Small |
| — | `ethos-56a` | Investigate Agent `isolation:worktree` regression (parallel — DX tax: 6 incidents in Phase 3.7 alone) | ops | Unknown |
| — | `ethos-9ai.6`, `.7` | `GenerateAgentFiles` error-handling fixes | 2 | Cleanup |
| — | Phase 4 hooks (`ethos-gcq.1`–`.3`) | After Phase 2 ships | 4 | — |
| — | Cross-tool integration (`ethos-wb4`, `ethos-g2f`) | After Phase 2 + Phase 4 ship | Integration | High coordination |

Cross-tool integration (agents with team messaging via biff, agents
with email via beadle) is deliberately sequenced last because it
requires coordinated changes across multiple repos and should wait
until ethos itself is solidly anchored. Call this Phase 5 work in
everything but name.

## Context

Ethos v2.6.1 shipped the **mechanism** for identity binding:
identities, personalities, writing styles, talents, roles, teams,
sessions, persona animation, agent file generation, extension
session context. Phase 3 (v2.7–v3.0) added the **workflow primitive
layer** on top: typed mission contracts, runtime-enforced
write-set admission, frozen evaluators, bounded rounds, verifier
isolation, typed result artifacts, append-only event log.

What is missing is **content integration** (Phase 2), **operational
hooks** (Phase 4), and **cross-tool identity binding**
(ethos+biff, ethos+beadle). Generated agents are functional but
minimal. The development workflow is manual in the seams between
CLAUDE.md guidance and the Phase 3 primitives. Agents are not yet
first-class participants in team messaging or email.

The sections below describe each phase in detail, preserved as the
source of truth for phase definitions, evidence, and delivery
notes.

### Three-Layer Model

An effective agent has three layers of context:

| Layer | What It Provides | Lifecycle | Where It Lives |
|-------|-----------------|-----------|---------------|
| **Persona** | Identity, judgment, taste, communication style | Durable -- survives across tasks and sessions | Ethos identity (personality + writing style + talents) |
| **Role** | Tools, responsibilities, anti-responsibilities, team position | Semi-durable -- changes when the team changes | Ethos role YAML |
| **Mission** | Typed I/O contract, files owned, success criteria, constraints | Ephemeral -- one task, then done | Delegation prompt from leader |

Ethos owns persona and role. The leader writes the mission. The persona
gives judgment. The role gives boundaries. The mission gives precision.

### Sources

This roadmap was informed by studying:

- **claude-config-template** — 16 pre-built agents, 18 slash commands,
  80+ security rules, plan/implement/validate pipeline, 4-reviewer PR
  system, codebase indexing, memory consolidation, hook-based automation
- **feature-dev plugin** — 7-phase development lifecycle, parallel
  specialized review, gated delegation model
- **agents-architecture.tex** — Claude Code's official agent and team
  architecture: identity via AsyncLocalStorage, 3-layer tool resolution,
  mailbox + task list coordination, operational invariants
- **channels-architecture.tex** — Claude Code's inbound MCP messaging:
  7-layer gate model, permission relay, identity attribution
  opportunities
- **autostar** — soft RLVR skill: structured optimization loops, frozen
  evaluators, bounded rounds with mandatory reflection, disposition-based
  long-term memory
- **DSPy** — programmatic prompt optimization: roles as typed interfaces,
  offline optimization with evaluation traces, separation of
  orchestration from compilation

All demonstrate that shipping pre-built content — not just mechanism —
is what makes a tool useful out of the box. The architecture docs
further show that effective agents need runtime discipline, evaluation
discipline, and structured delegation — not just good definitions.

---

## Phase 1: Batteries Included

Ship content that makes ethos useful without requiring every team to
write their own personalities, talents, and roles from scratch.

### 1.1 Baseline Operational Skill

**Problem**: Sub-agents lose Claude Code's default system prompt. They
don't know to use Read instead of cat, to run make check, or to never
commit. Every team reinvents this in agent body text.

**Solution**: Ship `~/.claude/skills/baseline-ops/SKILL.md` containing
the operational discipline subset: dedicated tool usage, verification
after changes, no commits, scope discipline, security basics, progress
tracking, concise output.

**Delivery**: Claude Code skill file, referenced in generated agent
frontmatter via `skills: [baseline-ops]`. Not an ethos Go change — a
content file deployed by the installer.

**Evidence**: claude-config-template has 5+ agents that each
independently embed "🚨 CRITICAL FIRST STEP: Read the codebase index"
and tool usage patterns. This duplication is eliminated by a shared
skill.

**Bead**: ethos-l9d (closed, shipped PR #162)

### 1.2 Starter Roles

**Problem**: Roles drive tool restrictions and agent generation. Without
roles, GenerateAgentFiles has no `tools` field for frontmatter. Teams
must define roles before they can use agent generation.

**Solution**: Ship 6 role archetypes covering the common delegation
patterns:

| Role | Tools | Key Responsibilities |
|------|-------|---------------------|
| `implementer` | Read, Write, Edit, Bash, Grep, Glob | Write code, write tests, run quality gates |
| `reviewer` | Read, Grep, Glob, Bash | Review code, report findings, never fix |
| `researcher` | Read, Grep, Glob, WebFetch, WebSearch | Find information, return findings, never write code |
| `architect` | Read, Grep, Glob | Design systems, evaluate tradeoffs, produce specs |
| `security-reviewer` | Read, Grep, Glob, Bash | Vulnerability hunting, dependency audit, threat modeling |
| `test-engineer` | Read, Write, Edit, Bash, Grep, Glob | Write tests, improve coverage, test infrastructure |

**Delivery**: YAML files in `internal/seed/sidecar/roles/`, deployed by installer to
`~/.punt-labs/ethos/roles/`. Teams override or extend.

### 1.3 Starter Talents

**Problem**: Talents are domain expertise files that make agents
effective. Without them, agents have personality and writing style but
no domain knowledge.

**Solution**: Ship 10 starter talents:

| Talent | Domain |
|--------|--------|
| `code-review` | Review methodology, common bug patterns, OWASP top 10 |
| `testing` | Test pyramid, coverage strategy, table-driven tests, mocking boundaries |
| `go` | Idiomatic Go, error handling, concurrency, stdlib-first |
| `python` | PEP 8, type hints, pytest, dependency management |
| `typescript` | Strict mode, type safety, React patterns |
| `security` | Input validation, dependency auditing, threat modeling, secrets handling |
| `cli-design` | Unix philosophy, composability, help text, exit codes |
| `api-design` | REST conventions, error responses, versioning |
| `documentation` | Technical writing, ADRs, changelogs, README structure |
| `devops` | CI/CD, containerization, infrastructure as code |

**Evidence**: claude-config-template ships 80+ security rule files in
`memories/security_rules/`. Rich domain content is what makes agents
effective — not just role boundaries.

**Delivery**: Markdown files in `internal/seed/sidecar/talents/`, deployed by
installer. These are starting points — teams extend with
project-specific expertise.

### 1.4 Model Field on Role

**Problem**: Complex analysis (architecture review, plan validation)
benefits from opus. Routine implementation works well with sonnet.
Currently no way to express this preference.

**Evidence**: claude-config-template assigns `model: opus` to
plan-validator and code-quality reviewer, `model: sonnet` to locators
and implementers.

**Solution**: Add `model` field to the Role struct. GenerateAgentFiles
includes it in frontmatter. Default: `inherit` (use whatever the parent
session uses).

**Delivery**: Go code change in `internal/role/` and
`internal/hook/generate_agents.go`.

---

## Phase 2: Production-Quality Agents

Make generated agent definitions as effective as hand-crafted ones.
Introduce structured delegation so the mission layer is well-defined.

### 2.1 Anti-Responsibility Generation

**Problem**: Generated agents define what they do (from role
responsibilities) but not what they don't do. Without explicit
boundaries, agents drift into adjacent domains.

**Evidence**: claude-config-template's 5 PR review agents each include:
"IMPORTANT: You are NOT checking X. Other agents handle those." This
prevents scope creep. The agents-architecture.tex recommends "assign
ownership" and "bound fan-out" — anti-responsibilities enforce this.

**Solution**: Derive anti-responsibilities from the team collaboration
graph. If `implementer` reports_to `coo`, generate: "Don't make
architectural decisions — the COO handles that." If the team has a
`reviewer` and an `implementer`, the reviewer gets: "Don't fix code —
report findings for the implementer."

**Delivery**: Logic in `GenerateAgentFiles()` that reads the
collaboration graph and emits a "What You Don't Do" section.

### 2.2 Role-Based Hooks in Generated Frontmatter

**Problem**: Implementation agents should run `make check` after every
file write. This is visible enforcement, not instruction — a hook
surfaces the output at the point of the write so the agent sees it
without having to remember to run the command.

**Evidence**: agent-identity-spec.tex §6 designs PostToolUse hooks for
implementation roles. claude-config-template uses `pre_tool_use.py` for
pre-execution safety checks.

**Solution**: Roles with write tools get a PostToolUse hook in generated
frontmatter. The command pins cwd to `$CLAUDE_PROJECT_DIR` so `make
check` resolves against the repo Makefile even if the sub-agent has
cd'd into a subdirectory, and pipes the first 60 lines of output
through `head -n 60` so the first failure is always visible:

```yaml
hooks:
  PostToolUse:
    - matcher: "Write|Edit"
      hooks:
        - type: command
          command: "(cd \"$CLAUDE_PROJECT_DIR\" && make check) 2>&1 | head -n 60"
```

The 60-line window fits this repo's `make check` shape: the target is
a sequence of quiet-on-success stages (`go vet`, `staticcheck`,
`shellcheck`, `markdownlint`, then non-verbose
`go test -race -count=1 ./...` — no `-v` flag), so the first failure
always lands near the top. A clean run is about 18 lines; a failing
run is tens to low hundreds, well inside the window. Go compile
errors short-circuit the whole sequence in 5-30 lines and land at the
top. A failing lint or test stage is equally visible because every
preceding stage was silent on success. Non-verbose `go test` prints
one line per package on success and a single `--- FAIL:` block for
the first failing package on failure. `tail -20` would lose the first
`FAIL:` to the trailing `make: *** [check] Error 1` summary; `head -n
60` keeps it visible. The hook is advisory, not blocking — the pipe
to `head` masks the exit code, so Claude Code does not gate the next
Write on a broken build. The command runs under `/bin/sh`; use
POSIX-sh syntax only (the `-n N` form of `head` is POSIX-canonical;
the BSD `-N` shortcut is not). Review-only roles get no hooks (tool
restrictions already prevent writes).

**Delivery**: Hook templates per role category in `GenerateAgentFiles()`.

### 2.3 Structured Output for Agent Handoff

**Problem**: When one agent's result feeds another, free-form prose
makes consolidation hard and loses structured data.

**Evidence**: agents-architecture.tex: "prefer structured outputs for
handoff — machine-readable fields such as files changed, verdicts,
confidence, and open questions." claude-config-template defines strict
output templates per agent type.

**Solution**: Add `output_format` field to Role (optional markdown
template). GenerateAgentFiles includes it in the agent body as an
"Output Format" section. Output formats should include structured
fields (files changed, verdict, confidence, open questions) alongside
human-readable prose.

**Delivery**: Field on Role struct, templates in starter roles.

### 2.4 Baseline-Ops Skill Injection

**Problem**: The baseline-ops skill (Phase 1.1) exists but generated
agents don't reference it.

**Solution**: GenerateAgentFiles always includes
`skills: [baseline-ops]` in frontmatter for every generated agent.

**Delivery**: One-line change in `GenerateAgentFiles()`.

### 2.5 Mission-Shaped Delegation Guide

**Problem**: Ethos defines the persona (who) and role (what they can
do), but says nothing about the mission (what they should do right
now). Leaders write delegation prompts ad hoc with inconsistent
quality.

**Evidence**: agents-architecture.tex: "Make every worker prompt
self-contained. Workers do not share the coordinator's full
conversational state. The best prompts name files, expected outputs,
constraints, and completion criteria explicitly." Autostar's 7-
checkpoint onboarding demonstrates mission confirmation as a gate.

**Solution**: The mission is the leader's responsibility, not an
ethos-generated artifact. But ethos can provide guidance and
structure. `docs/agent-definitions.md` now includes a "Writing
Effective Delegation Prompts" section with the mission template:
inputs, outputs, success criteria, files owned, constraints.

Future: a `/delegate` slash command that scaffolds a mission-shaped
prompt from the team's role definitions, ensuring every delegation
has typed inputs, outputs, success criteria, and file ownership.

**Delivery**: Documentation (done). Slash command (future).

### 2.6 Mission Skill (`/mission`)

**Problem**: The three-layer model (persona/role/mission) identifies
the mission as the leader's responsibility, but provides no
structured tooling for it. Leaders write freeform delegation prompts
with inconsistent quality. The gap between CLAUDE.md guidance
("delegate to specialists") and the raw Agent() primitive is too
wide.

**Evidence**: autostar demonstrates that structured onboarding with
confirmable checkpoints produces better outcomes than freeform
instructions. agents-architecture.tex says "make every worker prompt
self-contained" with "files, expected outputs, constraints, and
completion criteria." Our own agent-definitions guide documents the
mission template but documentation is passive.

**Solution**: A `/mission` skill that scaffolds mission-shaped
delegations:

1. Resolve the agent from the team roster (show available agents,
   roles, status)
2. Build the mission contract: task, inputs, outputs, success
   criteria, files_owned, constraints — pre-populated from
   conversation context, presented as confirmable options
3. Check for file ownership conflicts with running agents
4. Spawn the agent with the structured prompt
5. Optionally track the mission in beads

The skill reads ethos team/role data via MCP, scaffolds the prompt,
and uses Claude Code's Agent() to execute. It sits at the structured
layer between CLAUDE.md and agent primitives.

**Phased delivery**:

- **Phase A (MVP)**: Skill file at `~/.claude/skills/mission/SKILL.md`
  — pure prompt engineering, no Go code
- **Phase B**: `/ethos:mission` slash command with team data
  pre-population
- **Phase C**: File ownership tracking and conflict detection
  (depends on roadmap 4.3)

**Design**: See `docs/mission-skill-design.md` for the full
specification including flow, contract format, and examples.

---

## Phase 3: Workflow Primitives (SHIPPED 2026-04-08)

**Status**: Complete. All 7 primitives shipped across PRs #176–#184
between 2026-04-07 and 2026-04-08. Settled ADRs: DES-031 through
DES-037. Beads `ethos-07m.5` through `ethos-07m.11` all closed.

Build the structured workflow primitives that turn ethos identities and
roles into runtime contracts. The source material is `agents-architecture.tex`
and `instructions-memory-architecture.tex`. Phase 3 takes the rules in
those documents and gives them executable form.

### Design rules from the architecture documents

Two rules from `agents-architecture.tex` shape every primitive in
Phase 3:

1. **Roles are interfaces, not personas.** A role is specified by its
   inputs, outputs, tools, and success criteria. Stylistic descriptions
   ("be a careful researcher") are not contracts.
2. **Centralize understanding, decentralize execution.** The leader
   synthesizes findings and writes the next prompt. Workers do not
   inherit the leader's reasoning state.

Two rules from `instructions-memory-architecture.tex` shape where the
primitives live:

1. **Documentation is guidance, hooks and policies are enforcement.**
   Anything that sounds like "every time," "before," or "after" belongs
   in a hook or policy, not in a personality file.
2. **Subagents do not inherit ambient context.** Load-bearing
   constraints must be restated in the delegated task, not assumed.

These four rules force a single conclusion: discipline must be a
runtime contract, not text in a personality file. Phase 3 builds the
contracts.

### 3.1 Mission Contract

**Problem**: Today the leader writes a free-form prompt and hopes the
worker has enough context. Success criteria, write-set, evaluator, and
budget are implicit. There is no artifact the worker can verify against
and no artifact the leader can audit afterward.

**Solution**: A typed mission contract. Ethos owns the schema and the
storage. The leader fills in the contract before launching a worker.
The worker reads the contract as its first action and emits a result
artifact when done.

```yaml
mission_id: m-2026-04-07-001
status: open
leader: claude
worker: bwk
created_at: 2026-04-07T15:00:00Z
updated_at: 2026-04-07T15:00:00Z
inputs:
  bead: ethos-13j
  files: [internal/hook/stdin.go]
write_set:
  - internal/hook/stdin.go
  - internal/hook/stdin_test.go
tools: [Read, Write, Edit, Bash, Grep, Glob]
success_criteria:
  - make check passes
  - new test reproduces the bug without the fix
  - new test passes with the fix
evaluator:
  handle: djb
  pinned_at: 2026-04-07T15:00:00Z
budget:
  rounds: 3
  reflection_after_each: true
```

**Delivery**: New `internal/mission/` package. CLI: `ethos mission
create`, `ethos mission show`, `ethos mission close`. MCP: `mission`
tool with create/show/list/close methods. Storage:
`~/.punt-labs/ethos/missions/<id>.yaml`.

### 3.2 Write-Set Admission Control

**Problem**: When two missions claim overlapping files, the second one
silently corrupts the first one's work or merges into a half-applied
state. There is no runtime gate that prevents this.

**Solution**: The mission contract declares its `write_set`. Ethos
records active mission write sets in a session-scoped registry. A new
mission whose write set overlaps an active mission must either wait or
isolate in a worktree. The check happens at `mission create` time, not
at first edit.

```text
ethos mission create --bead ethos-13j --write-set internal/hook/
ERROR: write-set conflict with mission m-2026-04-07-002 (worker: rmh)
  overlapping paths: internal/hook/
  options:
    --wait              wait for m-2026-04-07-002 to close
    --isolate           launch in a worktree (no shared writes)
```

**Delivery**: Write-set tracking in `internal/mission/`. Conflict
detection on create. Worktree integration via existing `cmd.Stdin`
isolation patterns from `subprocess_test.go`.

### 3.3 Frozen Evaluator

**Problem**: Today's review agents can drift mid-cycle. The
code-reviewer's personality file may change between PR review rounds
because it's loaded from disk on each invocation. The success criteria
the leader had in mind at round 1 may not be the criteria the verifier
applies at round 3.

**Solution**: The mission contract names its evaluator at launch time.
The evaluator is identified by ethos handle plus content hash of the
evaluator's personality, talents, and success criteria. Any drift
between rounds is detected and surfaced.

```yaml
evaluator:
  handle: djb
  pinned_at: 2026-04-07T15:00:00Z
  hash: sha256:abc123...
```

When the evaluator subagent spawns for round N+1, ethos verifies the
content hash still matches. If the personality file changed, the
mission must be explicitly re-launched — no silent goalpost moving.

**Delivery**: Evaluator pinning in `internal/mission/`. Content hash
computed from personality + writing_style + talents + success_criteria.
SubagentStart hook validates the hash before injecting persona.

### 3.4 Bounded Rounds with Reflection

**Problem**: Long-running fix cycles drift indefinitely. The leader has
no structural gate that says "stop and reconsider after N rounds." The
agent fixes round 5's bug while introducing round 6's bug, and nobody
notices.

**Solution**: Mission contracts declare a round budget. After each
round, ethos forces a reflection step before the next round can start.
The reflection is a structured artifact, not free prose:

```yaml
reflection:
  round: 3
  converging: false
  signals:
    - plateau: code-reviewer reports same finding as round 2
    - divergence: silent-failure-hunter caught new issue introduced this round
  recommendation: pivot | escalate | continue | stop
  reason: |
    Two consecutive rounds of plateau on the same finding indicates the
    current approach won't converge. Recommend pivot to alternative
    implementation.
```

After the budget is exhausted without convergence, the mission must
either be re-scoped (new contract, new budget) or closed. No quiet
seventh round.

**Delivery**: Reflection schema in `internal/mission/`. Round counter
on the mission. CLI: `ethos mission reflect <id>`. Block on
`ethos mission round <id>` if reflection missing.

### 3.5 Independent Verification

**Problem**: When the same agent implements and verifies, the verifier
is too invested in its own implementation. The verifier reads the
implementer's scratch state and rationalizes it.

**Solution**: The verifier subagent receives only the mission contract
and the deltas (files changed, test output). It cannot read the
implementer's scratch files, prior reasoning, or personality
adjustments. Ethos enforces this by spawning the verifier in a separate
subagent context with a restricted file allowlist derived from the
mission write set.

The verifier cannot be the implementer. Ethos checks that the
mission's `worker` and `evaluator` handles differ, and that the
evaluator's role does not overlap the worker's role.

**Delivery**: Verifier isolation in `internal/mission/`. SubagentStart
hook injects the mission contract and deltas, strips parent transcript.
Role overlap check on mission create.

### 3.6 Structured Handoff Artifacts

**Problem**: Today, worker output is prose. The leader reads it,
interprets it, decides what's important. This works for one worker but
breaks down at fan-out: synthesizing five prose reports into a single
decision is the leader's hardest job.

**Solution**: Every worker emits a typed result artifact. The schema is
fixed:

```yaml
result:
  mission: m-2026-04-07-001
  verdict: pass | fail | escalate
  confidence: 0.0-1.0
  files_changed:
    - path: internal/hook/stdin.go
      lines_added: 23
      lines_removed: 4
  evidence:
    - test: TestShellScript_SessionStart
      status: pass
      duration_ms: 12
  open_questions:
    - "Should we backport this to v2.7.x?"
  prose: |
    Optional human-facing summary, not the coordination substrate.
```

The leader's synthesis step reads structured fields, not prose. Prose
is the human-facing layer.

**Delivery**: Result schema in `internal/mission/`. Validation on
`ethos mission close`. Refuse to close a mission without a valid
result artifact.

### 3.7 Append-Only Mission Log Reader (SHIPPED 2026-04-08)

**Problem**: When something goes wrong, the leader cannot reconstruct
what happened. Memory files are not authoritative — they're recall.
Beads track work items, not decisions. Phase 3.1 already writes the
audit trail (JSONL event log per mission) via a private `appendEvent`
helper, but there is no public reader — the events are on disk and
invisible to a post-mortem.

**Solution**: A public `Store.LoadEvents(missionID)` method plus
`ethos mission log <id>` CLI and `mission log` MCP method read the
append-only log that Phase 3.1 has been writing since launch. Read-
only — the writer is frozen. Partial-damage resilient: one corrupt
line does not erase the log, one oversized line does not truncate
the tail, one attacker-planted ESC sequence does not reach the
operator terminal. Per-line warnings for any unparseable line,
sanitized at source. See `DES-037` for the full design.

**Delivery**: `LoadEvents` in `internal/mission/log.go` (reader path;
the existing `appendEvent` writer path is unchanged). CLI and MCP
surfaces with `--event <type,list>` and `--since <RFC3339>` filters.
DES-020 formatter `formatMissionLog` for MCP consumers. 28 test
classes covering the reader's failure-mode equivalence class.

**Phase 3 complete.** After 3.7 lands, the four architecture rules
from `~/Documents/agents-architecture.tex` are runtime-enforced.

### What Phase 3 is not

- **Not integration with anything.** Ethos owns the primitives. They
  are usable from any workflow tool that wants discipline, but Phase 3
  ships them standalone with no upstream or downstream dependency.
- **Not personality changes.** Personalities stay advisory. Mission
  contracts are the enforcement layer.
- **Not new identities.** The 4 specialized review identities idea
  from earlier drafts of this section is moved to Phase 4 — they are
  one example of a team that could use the Phase 3 primitives, not a
  prerequisite for building them.
- **Not memory rework.** Memory remains advisory recall. Mission logs
  are a separate substrate with different rules (append-only,
  authoritative, not subject to truncation).

### Sequencing

Mission contract first (3.1) — every other primitive depends on its
schema. Then write-set admission (3.2) and frozen evaluator (3.3) in
parallel — both are independent extensions to the contract. Then
bounded rounds (3.4) and independent verification (3.5) — both depend
on the mission lifecycle. Result artifacts (3.6) and the event log
(3.7) are consumed by everything else but can be built last because
they don't block the others.

---

## Phase 4: Operational Excellence

Hook enhancements, security, and observability.

### 4.1 Hook-Based Context Loading

**Problem**: SessionStart currently injects persona, team context, and
extension session_context. It could also inject working context — git
branch, uncommitted changes, recent issues.

**Evidence**: claude-config-template's `session_start.py` loads git
branch, uncommitted changes, and recent GitHub issues into session
context.

**Solution**: Extend SessionStart to emit working context alongside
persona context. This is additive — doesn't change existing behavior.

**Delivery**: Addition to `HandleSessionStart` in `internal/hook/`.

### 4.2 Pre-Tool Safety Hooks

**Problem**: No safety layer for dangerous tool invocations. An agent
can run destructive commands without pre-execution checks.

**Evidence**: claude-config-template implements 3-layer defense in
`pre_tool_use.py`:

1. Regex patterns (~0ms) — catches rm -rf, fork bombs
2. settings.json deny list (~1ms) — enforced even with --dangerously-skip-permissions
3. LLM intent checking (~1-2s) — catches sophisticated threats

**Solution**: Ethos doesn't own tool safety — that's Claude Code's
domain and the pre_tool_use hook. But ethos roles could define
**role-specific safety constraints** that feed into pre-tool hooks.
A reviewer role should never write files; a researcher should never
execute destructive commands.

**Delivery**: Safety constraint field on Role, consumed by hooks.

### 4.3 Write-Set Admission Control

**Problem**: Multiple agents editing the same files creates merge
conflicts, duplicate work, and overwritten changes. Currently the
only protection is worktree isolation, which is opt-in.

**Evidence**: agents-architecture.tex recommends: "Before launching an
implementation worker, declare the expected file set and refuse
concurrent writers with overlapping claims unless they are isolated
in worktrees." The effective agents principles identify "broad write
access" and "overlapping writes" as top anti-patterns.

**Solution**: Missions declare `files_owned`. The leader (or a
coordination hook) checks for overlapping claims before spawning
a second writer. Options when overlap is detected: queue the second
task, isolate in a worktree, or reject with explanation.

**Delivery**: This requires coordination-layer support (feature-dev
or agent teams), not ethos core. Ethos's contribution: the role
defines tool restrictions (can this agent write at all?), and the
mission template includes `files_owned` as a standard field.

### 4.4 Session Audit Logging

**Problem**: No audit trail for what happened during a session — what
tools were used, what decisions were made, what the agent did.

**Evidence**: claude-config-template logs all pre-tool-use decisions to
`memories/logs/hook-audit.jsonl` with timestamp, session ID, tool,
decision, and reason.

**Solution**: PostToolUse hook (FormatOutput) already sees all tool
invocations. Add optional audit logging to a session log file.

**Delivery**: Addition to `HandleFormatOutput` in `internal/hook/`.

### 4.5 Audio/Notification Hooks

**Problem**: No feedback when the agent finishes work or needs input.
Long-running sessions require manual monitoring.

**Evidence**: claude-config-template ships audio notifications for
completion, alerts, and session end. Different sounds for different
events.

**Solution**: This is vox's domain. Ethos's contribution is ensuring
the session lifecycle hooks (SessionEnd, Stop) fire reliably so vox
can play appropriate sounds. Already working.

**Delivery**: No ethos changes needed. Vox handles this via extension
session_context.

---

## Phase 5: Ecosystem

Long-term investments in the ethos ecosystem.

### 5.1 Starter Team Templates

**Problem**: Setting up a team from scratch requires creating
identities, personalities, writing styles, talents, roles, and teams.
The team-setup guide explains how, but it's still manual.

**Solution**: Ship team templates for common setups:

- **Solo developer** — 1 human + 3 agents (implementer, reviewer,
  researcher)
- **Small team** — 2-3 humans + 5 agents (Go/Python implementers,
  reviewer, security reviewer, architect)
- **Full team** — punt-labs/team as the reference implementation

Templates are directories that `ethos init` can scaffold from.

### 5.2 Agent Marketplace

**Problem**: Teams create effective agent definitions that could benefit
others. No mechanism to share.

**Solution**: A curated registry of agent identity packages (identity +
personality + writing style + talents + role). Teams publish and
discover compositions that work.

**Delivery**: Future — requires community adoption first.

### 5.3 Cross-Tool Workflow Orchestration

**Problem**: Complex workflows span multiple tools — ethos (identity),
quarry (knowledge), beads (tracking), biff (coordination), feature-dev
(development). No orchestrator ties them together.

**Evidence**: claude-config-template's `orchestrator.py` and
`sprint_runner.py` demonstrate multi-phase orchestration with
reflection checkpoints.

**Solution**: A workflow orchestrator that leverages ethos identity
context, quarry knowledge, beads tracking, and feature-dev phases.
This is above the ethos layer — likely a punt-kit or feature-dev
concern.

**Delivery**: Future — requires Phase 1-3 content to be shipped first.

---

## Priority and Sequencing

```text
Phase 1 (Batteries Included)
├── 1.1 baseline-ops skill ←── DONE
├── 1.2 Starter roles (6) ←── DONE
├── 1.3 Starter talents (10) ←── DONE
└── 1.4 Model field on Role ←── DONE

Phase 2 (Production Agents)     ← depends on Phase 1
├── 2.1 Anti-responsibility generation
├── 2.2 Role-based hooks
├── 2.3 Structured output for handoff
├── 2.4 Baseline-ops injection
├── 2.5 Mission-shaped delegation guide ←── DONE (docs)
└── 2.6 Mission skill (/mission) ←── BRIDGES THE GAP

Phase 3 (Workflow)              ← depends on Phase 1-2
├── 3.1 Feature-dev integration
├── 3.2 Specialized review identities
├── 3.3 Memory consolidation pattern
├── 3.4 Codebase indexing integration
└── 3.5 Evaluation discipline

Phase 4 (Operational)           ← independent, can parallel Phase 2-3
├── 4.1 Context loading hooks
├── 4.2 Role-based safety constraints ← coordinate with Phase 2 Role changes
├── 4.3 Write-set admission control ← depends on 2.6 Phase C
├── 4.4 Session audit logging
└── 4.5 Audio/notification (vox)

Phase 5 (Ecosystem)             ← future
├── 5.1 Starter team templates
├── 5.2 Agent marketplace
└── 5.3 Cross-tool workflow orchestration
```

Phase 1 shipped in PR #162. Phase 2 is the immediate priority.

Phase 2 requires Go changes to `GenerateAgentFiles()` and the Role
struct. Depends on Phase 1 roles and baseline-ops being shipped.
Item 2.5 (mission-shaped delegation guide) is already documented in
`docs/agent-definitions.md`.

Phase 3 is integration work across ethos, feature-dev, and punt-kit.
Depends on Phase 1-2 providing the identity and role content that
workflow agents need. Item 3.5 (evaluation discipline) formalizes
review-cycle rules that currently live as ad hoc leader judgment.

Phase 4 is independent and can run in parallel with Phase 2-3.
Item 4.3 (write-set admission) requires coordination-layer support.

Phase 5 is future work that depends on community adoption.
