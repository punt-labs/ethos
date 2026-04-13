# Archetypes and Pipelines Guide

How to use mission archetypes and pipelines, and how to create your own.
This guide covers the workflow layer of ethos — the part that turns
prose delegation into typed contracts with file-level boundaries, bounded
rounds, and audit trails.

**Prerequisite:** Install ethos first — see
[Quick Start](../README.md#quick-start). Identity must resolve. Run
`ethos doctor` to confirm.

## Why

When you hand work to an agent as prose — "please fix the bug in the
validator and add a regression test" — two things go wrong regularly:

- **The agent drifts.** It modifies files you didn't expect. It revises
  its own success criteria mid-round. The reviewer's standards drift
  between rounds. You rediscover the intent later from chat history.
- **You lose the decision context.** Three rounds later nobody can
  answer "why did we choose approach A over B?" because nothing captured
  the reasoning at the time.

Mission contracts fix both. A contract declares who the worker is, who
the evaluator is (with a content hash pinned at launch so the criteria
cannot drift), which files are in-bounds, what success looks like, and
how many rounds the work gets. Results and reflections are append-only.
The audit log is JSONL; one corrupt line does not erase the tail.

## Your first mission

Write a contract. Here is a minimal one:

```yaml
# .tmp/missions/fix-validator.yaml
leader: claude
worker: bwk
evaluator:
  handle: djb
write_set:
  - internal/mission/validate.go
success_criteria:
  - make check passes
  - every new error message names the field that failed validation
budget:
  rounds: 2
  reflection_after_each: true
```

Create the mission:

```bash
ethos mission create --file .tmp/missions/fix-validator.yaml
```

Ethos assigns an ID, pins the evaluator's content hash, and creates an
event log at `~/.punt-labs/ethos/missions/<id>.log.jsonl`. The worker
opens the mission, implements the change, submits a structured result,
and the leader reflects. Full walkthrough:
[docs/example/](example/).

The contract is the trust boundary. The store refuses:

- Overlapping write-sets between open missions
- Self-review (evaluator is the same as worker or leader)
- Close without a result for the current round
- Advance without a reflection

## Archetypes: typed mission conventions

An archetype is a named set of constraints applied on top of the base
contract validator. Declaring `type: design` on a mission means the
contract must have a non-empty `context` field and the write-set can
only contain markdown and docs paths. Declaring `type: report` means
the write-set may be empty (the output is a report, not code).

### Built-in archetypes

Seven archetypes ship in the box. All live as YAML at
`~/.punt-labs/ethos/archetypes/`.

| Archetype | Purpose | Budget default | Write-set constraints | Required fields |
|-----------|---------|----------------|-----------------------|-----------------|
| `implement` | Code change with a specific outcome | 3 rounds | Any path | — |
| `design` | Produce a design document | 2 rounds | `*.md`, `docs/**` | `context` |
| `test` | Add or improve tests | 2 rounds | `*_test.go`, `testdata/**`, `docs/**`, `*.md` | — |
| `review` | Read and report findings | 1 round | `*.md`, `*.yaml`, `.tmp/**` | `inputs.files` |
| `report` | Gather info and summarize (read-only) | 1 round | empty allowed | — |
| `task` | Execute a specific instruction | 3 rounds | Any path | `context` |
| `inbox` | Process unread email (read-only) | 1 round | empty allowed | — |

List them:

```bash
ethos mission pipeline list    # pipelines
ls ~/.punt-labs/ethos/archetypes/   # archetypes live here
```

Read a specific one:

```bash
cat ~/.punt-labs/ethos/archetypes/design.yaml
```

### Declaring an archetype

Set the `type` field on your contract:

```yaml
type: design
leader: claude
worker: mdm
evaluator:
  handle: claude
write_set:
  - docs/pipeline-walkthrough.md
success_criteria:
  - document covers problem, options considered, decision, migration
context: |
  Users don't understand when to use quick vs standard vs full
  pipelines. The guide should show three worked examples with
  context.
```

Ethos looks up `archetypes/design.yaml`, applies its constraints on top
of the base validation, and rejects the contract if something doesn't
fit — e.g. if `context` is empty or if the write-set contains
`internal/foo.go` (not a markdown or docs path).

### Creating your own archetype

An archetype is a YAML file. Put it in
`~/.punt-labs/ethos/archetypes/<name>.yaml` (global) or
`.punt-labs/ethos/archetypes/<name>.yaml` (repo-local; overrides
global):

```yaml
# .punt-labs/ethos/archetypes/security-review.yaml
name: security-review
description: Security review — read-only threat analysis with numbered findings
budget_default:
  rounds: 1
  reflection_after_each: true
allow_empty_write_set: false
required_fields:
  - inputs.files
  - context
write_set_constraints:
  - "*.md"
  - "docs/**"
```

Field reference:

| Field | Purpose |
|-------|---------|
| `name` | Archetype slug (must match filename) |
| `description` | One-line description |
| `budget_default.rounds` | Default round cap applied at create time |
| `budget_default.reflection_after_each` | Whether reflection is required between rounds |
| `allow_empty_write_set` | If true, contracts of this type may omit `write_set` |
| `required_fields` | Fields that must be non-empty beyond base validation |
| `write_set_constraints` | Glob patterns every write-set entry must match |

Rules:

- The filename without `.yaml` is the archetype name.
- Required fields use dotted paths: `inputs.files`, `inputs.bead`,
  `context`, `success_criteria`.
- Write-set constraints use `filepath.Match` glob syntax. Entries must
  match at least one pattern.
- Archetypes are extensible without modifying ethos — add a YAML file
  and it shows up in `ethos mission pipeline list` and is usable from
  any contract's `type:` field.

Design reference: [mission-archetypes.md](mission-archetypes.md).

## Pipelines: archetypes in sequence

A pipeline is a declaration of how archetype-typed stages compose. It
is orchestration, not an archetype. The `design → implement → test →
review` sequence becomes one YAML file; you instantiate it and get
four mission contracts wired together with `depends_on` edges.

### Built-in pipelines

Eight pipelines ship in the box. Pick based on the nature of the work,
not just its size:

| Pipeline | Stages | Use when |
|----------|--------|----------|
| `quick` | implement → review | Small, well-understood change |
| `standard` | design → implement → test → review → document | Default feature work |
| `full` | prfaq → spec → design → implement → test → coverage → review → document → retro | Large or cross-cutting work |
| `product` | prfaq → design → implement → test → review → document | New user-facing feature (PR/FAQ first) |
| `formal` | spec → design → implement → test → coverage → review → document | Stateful system, protocol, data model |
| `docs` | design → review | Documentation-only change |
| `coe` | investigate → root-cause → fix → test → document | Cause-of-error investigation |
| `coverage` | measure → test → verify | Targeted test coverage improvement |

Inspect one:

```bash
ethos mission pipeline show standard
```

### Running a pipeline

`ethos mission pipeline instantiate` reads a pipeline YAML, expands
template variables, and creates one mission per stage. Each mission
carries the pipeline ID and a `depends_on` edge pointing at the
upstream stage.

> **Built-in pipelines are skeletons.** The shipped `quick`, `standard`,
> `full`, `product`, `formal`, `docs`, `coe`, and `coverage` pipelines
> declare stage structure but leave `write_set` empty for stages that
> expect user-supplied paths. Running `instantiate` on a built-in
> directly fails validation for archetypes that require a non-empty
> write-set (`implement`, `test`, `design`). Copy the built-in to
> `.punt-labs/ethos/pipelines/<name>.yaml` and add `write_set` entries
> (with optional `{key}` placeholders) before running. See
> [Creating your own pipeline](#creating-your-own-pipeline).

```bash
ethos mission pipeline instantiate standard \
  --leader claude \
  --evaluator bwk \
  --var feature=walk-diff
```

Output:

```text
Pipeline: standard-2026-04-13-a3f901
  1. design     m-2026-04-13-030  open  (no dependencies)
  2. implement  m-2026-04-13-031  open  depends_on: [m-2026-04-13-030]
  3. test       m-2026-04-13-032  open  depends_on: [m-2026-04-13-031]
  4. review     m-2026-04-13-033  open  depends_on: [m-2026-04-13-032]
  5. document   m-2026-04-13-034  open  depends_on: [m-2026-04-13-033]
```

The worker picks up stage 1 (design), produces a doc, submits a result.
The leader reads the result and populates stage 2's `inputs.files` with
the doc path. Stage 2's worker begins. And so on.

Query all missions in a pipeline:

```bash
ethos mission list --pipeline standard-2026-04-13-a3f901
```

Use `--dry-run` to preview generated contracts without creating them:

```bash
ethos mission pipeline instantiate standard --leader claude --evaluator bwk \
  --var feature=walk-diff --dry-run
```

### Template variables

Pipelines can contain `{key}` placeholders in stage `write_set`,
`context`, and `success_criteria`. Pass values with `--var key=value`
(repeatable):

```yaml
# .punt-labs/ethos/pipelines/feature.yaml
name: feature
stages:
  - name: design
    archetype: design
    write_set:
      - "docs/{feature}.md"
    context: "Write a design doc for {feature}"
  - name: implement
    archetype: implement
    write_set:
      - "internal/{feature}/"
    inputs_from: design
```

Instantiate:

```bash
ethos mission pipeline instantiate feature \
  --leader claude --evaluator bwk \
  --var feature=walk-diff
```

The write-set expands to `docs/walk-diff.md` and `internal/walk-diff/`.
An unknown variable is a hard error — the command fails and names the
missing token.

### Pipeline selection heuristic

The linter (`ethos mission lint`) suggests a pipeline when you create a
standalone contract. It checks the context and write-set for nature
signals first, then falls back to size:

1. Context mentions `prfaq`, `pr/faq`, `working backwards`, or
   `product validation` → suggest **product**
2. Context mentions `z-spec`, `formal spec`, `model check`,
   `invariant`, `state machine` → suggest **formal**
3. Context mentions `cause of error`, `recurring bug`,
   `data corruption`, `fixed before`, `postmortem` → suggest **coe**
4. Every write-set entry is markdown or under `docs/` → suggest **docs**
5. Context mentions `test gap` → suggest **coverage**
6. 11+ files or multi-repo context → suggest **full**
7. 4+ files or 3+ success criteria → suggest **standard**
8. Otherwise → suggest **quick**

The suggestion is advisory (severity: info). The leader picks what to
use.

### Creating your own pipeline

A pipeline is a YAML file. Put it in
`~/.punt-labs/ethos/pipelines/<name>.yaml` or
`.punt-labs/ethos/pipelines/<name>.yaml`:

```yaml
# .punt-labs/ethos/pipelines/release.yaml
name: release
description: Release process — changelog, tag, announce
stages:
  - name: changelog
    archetype: design
    description: Finalize CHANGELOG for this release
    write_set:
      - CHANGELOG.md
    context: "Freeze the [Unreleased] section as version {version}"
    success_criteria:
      - version header stamped with today's date
      - every change is attributed to a PR or commit
  - name: tag
    archetype: task
    description: Cut the tag and push
    inputs_from: changelog
    context: "Create and push v{version} tag"
    success_criteria:
      - git tag v{version} exists on origin
      - GitHub release created with the CHANGELOG excerpt
  - name: announce
    archetype: task
    description: Post to team
    inputs_from: tag
    context: "Announce v{version} release via /wall"
    success_criteria:
      - wall message posted
      - recap email sent to the founder
```

Instantiate:

```bash
ethos mission pipeline instantiate release \
  --leader claude --evaluator bwk \
  --var version=3.4.0
```

Field reference:

| Field | Purpose |
|-------|---------|
| `name` | Pipeline slug (must match filename) |
| `description` | One-line description |
| `stages[].name` | Stage slug; unique within the pipeline |
| `stages[].archetype` | Archetype name (must exist) |
| `stages[].description` | One-line human-readable label |
| `stages[].worker` | Default worker for this stage (override with `--worker`) |
| `stages[].evaluator` | Default evaluator for this stage (override with `--evaluator`) |
| `stages[].inputs_from` | Upstream stage name; populates `depends_on` and flows results |
| `stages[].write_set` | Template write-set (supports `{key}` expansion) |
| `stages[].context` | Template context (supports `{key}` expansion) |
| `stages[].success_criteria` | Template criteria (supports `{key}` expansion) |
| `stages[].budget` | Override for the archetype's budget default |

Rules:

- Stage order in the YAML is the execution order. Each stage can only
  reference upstream stages via `inputs_from`.
- `depends_on` is advisory — the store does not block `create` on
  dependency status. The leader enforces ordering.
- Every stage is an independent mission. Re-run, skip, or replace one
  stage without affecting the rest.
- Pipelines are extensible without modifying ethos — same pattern as
  archetypes.

Design reference: [mission-pipelines.md](mission-pipelines.md).

## When to use what

| Situation | Approach |
|-----------|----------|
| One-off code change, scope is clear | Write a contract directly, archetype `implement` |
| Need a design doc first | Archetype `design`, or pipeline `docs` / `standard` |
| Bug that's been fixed before | Pipeline `coe` |
| New user-facing feature | Pipeline `product` (PR/FAQ first) |
| Protocol or state machine change | Pipeline `formal` (Z-Spec first) |
| Large refactor across many files | Pipeline `full` |
| Test coverage improvement | Pipeline `coverage` |
| Documentation PR | Pipeline `docs` |

## See also

- [Live mission example](example/) — real walkthrough with every
  command and output
- [Team setup guide](team-setup.md) — roles, teams, agent generation
- [Mission archetypes design](mission-archetypes.md) — deeper design
  discussion and rationale
- [Mission pipelines design](mission-pipelines.md) — pipeline
  semantics, error handling, events
- [Agent guide](../AGENTS.md) — CLI, MCP tools, hooks
