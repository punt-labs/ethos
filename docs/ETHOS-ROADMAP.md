# Ethos Roadmap

Where ethos is going. Organized into phases that build on each other.

## Context

Ethos v2.6.1 ships the **mechanism** for identity binding: identities,
personalities, writing styles, talents, roles, teams, sessions, persona
animation, agent file generation, extension session context. The
architecture is sound.

What's missing is **content**, **workflow**, and **delegation
discipline**. The directories are empty. The generated agents are
functional but minimal. The development workflow is manual and
inconsistent. There is no structured model for how leaders delegate
to agents.

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
file write. This is behavioral enforcement, not instruction — a hook
guarantees it happens.

**Evidence**: agent-identity-spec.tex §6 designs PostToolUse hooks for
implementation roles. claude-config-template uses `pre_tool_use.py` for
pre-execution safety checks.

**Solution**: Roles with write tools get a PostToolUse hook in generated
frontmatter:

```yaml
hooks:
  PostToolUse:
    - matcher: "Write|Edit"
      hooks:
        - type: command
          command: "make check 2>&1 | tail -20"
```

Review-only roles get no hooks (tool restrictions already prevent
writes).

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

## Phase 3: Workflow

Address the development workflow gap. Learn from feature-dev's 7-phase
lifecycle and claude-config-template's plan/implement/validate pipeline.

### 3.1 Plan-Implement-Validate Pipeline

**Problem**: Our current workflow is manual — the developer (or COO)
drives each phase. There's no structured pipeline that ensures research
happens before planning, planning before implementation, and validation
before shipping.

**Evidence**: claude-config-template's orchestrator runs:
1. Index codebase
2. Refine query (analyze codebase, improve query)
3. Fetch technical documentation
4. Research codebase with refined query
5. Create implementation plan
6. Validate plan
7. Implement plan
8. Review → fix cycles (max 3)
9. Cleanup (document learnings)

feature-dev follows a similar 7-phase lifecycle: Claim → Branch →
Implement → Document → Review → Ship → Close.

**Solution**: Ethos doesn't own the workflow — that's the plugin layer
(feature-dev, punt-kit). But ethos provides the **identity and team
context** that makes workflow agents effective. The workflow gap is:

1. **Feature-dev needs ethos integration**: Feature-dev's review agents
   should be ethos identities with personalities, not anonymous agents.
   The code-reviewer agent should have the principal-engineer
   personality. The silent-failure-hunter should have the security
   personality.

2. **Workflow agents need roles**: The plan-validate-implement pattern
   maps to roles: architect (plans), implementer (executes), reviewer
   (validates). Ethos roles provide the tool restrictions and
   anti-responsibilities.

3. **Team context in delegation**: When the COO delegates to bwk, bwk
   should know the team structure — who else is available, what each
   person does, who to escalate to. Ethos already injects this via
   BuildTeamContext. The workflow needs to leverage it.

**Delivery**: Integration work between ethos and feature-dev/punt-kit.
Not ethos core changes — identity and role definitions for workflow
agents.

### 3.2 Parallel Specialized Review

**Problem**: Code review is currently two agents (code-reviewer +
silent-failure-hunter). The claude-config-template demonstrates that
4 specialized reviewers catch more than 2 generalists.

**Evidence**: Template uses: code-quality (opus), security, best
practices, test coverage — all in parallel. Each has a narrow focus
and explicit non-overlap.

**Solution**: Define 4 review identities in the team registry:

| Identity | Personality | Talents | Role |
|----------|------------|---------|------|
| `code-reviewer` | principal-engineer | code-review, testing | reviewer |
| `security-reviewer` | bernstein | security, code-review | security-reviewer |
| `test-reviewer` | principal-engineer | testing | reviewer |
| `style-reviewer` | principal-engineer | documentation, code-review | reviewer |

Each gets generated agent definitions with anti-responsibilities
("You are NOT checking security — the security-reviewer handles that").

**Delivery**: Identity YAML files in the team registry. Role and talent
definitions from Phase 1.

### 3.3 Memory Consolidation Pattern

**Problem**: Knowledge from implementation sessions is lost. What worked,
what didn't, what constraints were discovered — none of this persists
systematically.

**Evidence**: claude-config-template uses a 4-file pattern:
- `project.md` — stable project context
- `todo.md` — active work
- `done.md` — completed work with traceability
- `decisions.md` — accumulated architectural decisions

Sprint reflection captures observations fast (`scratchpad.md`), then
consolidation integrates them deliberately into `decisions.md`.

**Solution**: This maps to quarry + beads. Quarry already provides
semantic search over accumulated knowledge. Beads track work items.
The missing piece is **deliberate reflection** — a step after each
implementation where the agent captures what it learned.

Ethos can support this by:
1. Adding a `reflect` talent that teaches agents the reflection pattern
2. Providing session context (via DES-022) that reminds agents to
   capture learnings in quarry before closing

**Delivery**: Talent file + extension session_context content.

### 3.4 Codebase Indexing Integration

**Problem**: Multiple agents need codebase understanding before doing
work. Without an index, each agent rediscovers the same information
via expensive Glob/Grep cycles.

**Evidence**: claude-config-template's indexers generate
`codebase_overview_*.md` files that agents read as their first step.
5+ agents mandate "🚨 CRITICAL FIRST STEP: Read the codebase index."
Quarry already does semantic indexing.

**Solution**: This is quarry's domain, not ethos's. But ethos's
session_context mechanism (DES-022) can inject "check quarry before
searching" as standard guidance. The `baseline-ops` skill should
include: "Before broad codebase searches, query quarry for existing
knowledge."

**Delivery**: Content in baseline-ops skill and quarry's
extension session_context.

### 3.5 Evaluation Discipline

**Problem**: Our review and implementation workflows lack formal
evaluation discipline. Review criteria can shift mid-review. There
are no mandatory reflection gates between workflow phases. Escalation
triggers are instinct-based, not signal-based.

**Evidence**: agents-architecture.tex defines three evaluation
principles: freeze the evaluator during a task, use mixed verification
tracks, and work in bounded rounds with mandatory reflection. Autostar
enforces immutable rubrics and mandatory reflection after every round
with three specific questions: worth pursuing? escalate to user? pivot?

**Solution**: Formalize evaluation discipline in the workflow layer:

1. **Frozen evaluators**: Once review criteria are set for a PR
   review cycle, they don't change until the cycle completes. No
   moving goalposts.

2. **Mixed verification tracks**: Combine deterministic (make check,
   staticcheck), external tool (Copilot, Bugbot), model-based
   (code-reviewer, security-reviewer), and human gate (user approval)
   verification in every review cycle.

3. **Bounded rounds with reflection**: After each review-fix cycle,
   the leader asks: are we converging? should we change approach?
   should we escalate? This is a gate, not a suggestion.

4. **Concrete escalation signals**: Plateau (same findings across 2
   cycles), divergence (fixing one issue introduces another), budget
   (more than N cycles on one PR). These trigger escalation, not
   "something feels off."

**Delivery**: Workflow documentation and delegation patterns. The
evaluation rules live in the workflow layer (feature-dev, CLAUDE.md),
not in ethos core. Ethos's contribution is that review agents have
stable personas with frozen evaluation criteria — the personality
doesn't drift because PreCompact re-injects it.

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

```
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
