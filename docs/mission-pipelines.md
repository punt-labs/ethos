# Mission Pipelines

Pipelines compose typed archetype stages into workflows. A pipeline
is orchestration, not an archetype. The orchestrator (leader or
daemon) holds pipeline state. Ethos stores the declaration and
provides query surfaces; it does not execute stages.

## The Pipe Metaphor

McIlroy's pipes work because each program does one thing, the output
of one program is the input to the next, and the composition is
declared upfront. Mission pipelines apply the same principle to
typed delegation:

- Each stage does one thing (an archetype)
- The output contract of stage N matches the input contract of N+1
- Stages are independent and replaceable
- The pipeline is declared before any stage executes
- Failure at any stage is visible and recoverable

The result artifact (DES-036) is the pipe. Stage N produces a
`Result` with `verdict`, `files_changed`, and `evidence`. Stage N+1
reads those outputs as its `inputs.files` or `context`. The leader
or daemon scaffolds each stage's contract from the previous stage's
result -- the same way a shell connects stdout to stdin.

## Pipeline Declaration

A pipeline is a YAML file on the filesystem. It declares the stages
upfront. Each stage names its archetype, write set, and how it
connects to adjacent stages.

### Storage Layout

| Scope | Path | Git-tracked? |
|-------|------|-------------|
| Repo pipelines | `.punt-labs/ethos/pipelines/<name>.yaml` | Yes |
| Global pipelines | `~/.punt-labs/ethos/pipelines/<name>.yaml` | No |

Repo-local overrides global, same as every other layered store.

### YAML Schema

```yaml
# name must match the filename (without .yaml extension).
# Lowercase, hyphenated.
name: sprint

# One-line summary for help text and pipeline list output.
description: "Design, implement, and test a feature"

# Ordered list of stages. Execution proceeds left to right.
# Each stage becomes a mission when the pipeline is instantiated.
stages:
  - name: design
    archetype: design
    write_set:
      - "docs/{feature}.md"
    worker: mdm

  - name: implement
    archetype: implement
    write_set:
      - "internal/{feature}/"
    worker: bwk
    # inputs_from names the stage whose result feeds this stage's
    # inputs.files. When omitted, the stage has no automatic inputs.
    inputs_from: design

  - name: test
    archetype: test
    write_set:
      - "internal/{feature}/"
    worker: bwk
    inputs_from: implement
```

### Field Reference

| Field | Required | Description |
|-------|----------|-------------|
| `name` | yes | Pipeline name, matches filename |
| `description` | yes | One-line summary |
| `stages` | yes | Ordered list, at least 2 entries |
| `stages[].name` | yes | Stage label, unique within the pipeline |
| `stages[].archetype` | yes | Archetype name (must exist in the archetype store) |
| `stages[].write_set` | no | Write set template; `{feature}` is expanded at instantiation |
| `stages[].worker` | no | Default worker handle; leader can override at instantiation |
| `stages[].inputs_from` | no | Name of the upstream stage whose result feeds this stage |
| `stages[].evaluator` | no | Default evaluator handle; leader can override |
| `stages[].budget` | no | Override for the archetype's budget default |
| `stages[].success_criteria` | no | Stage-specific criteria; merged with archetype defaults |
| `stages[].context` | no | Stage-specific context template |

### Template Variables

Write set entries and context strings may contain `{feature}` (or
any `{key}`) placeholders. The leader supplies values at
instantiation time:

> **Planned**: `ethos mission pipeline instantiate` is not yet
> implemented. The current release provides `list` and `show`.

```bash
ethos mission pipeline instantiate sprint --var feature=walk-diff
```

This creates one mission per stage, expanding `{feature}` to
`walk-diff` in every write set entry and context string. The
missions share a `pipeline` field and carry `depends_on` edges
matching the `inputs_from` declarations.

## Pipeline Selection

The `ethos mission lint` H10 heuristic recommends a pipeline
template based on the contract's context and write set. Detection
is nature-based, with a size-based fallback.

### Decision Tree

```text
Contract context and write_set
  |
  +--> mentions "prfaq", "working backwards", "product validation"
  |    AND write_set non-empty?
  |      yes --> product (6 stages)
  |
  +--> mentions "z-spec", "formal spec", "model check", "invariant",
  |    "state machine"?
  |      yes --> formal (7 stages)
  |
  +--> mentions "cause of error", "recurring bug",
  |    "data corruption", "incident", "fixed before", "postmortem"?
  |      yes --> coe (5 stages)
  |
  +--> ALL write_set entries match doc patterns
  |    (*.md, *.tex, docs/*, *.pdf)?
  |      yes --> docs (2 stages)
  |
  +--> mentions "test gap"?
  |      yes --> coverage (3 stages)
  |
  +--> size fallback:
       11+ files or multi-repo  --> full (9 stages)
       4+ files or 3+ criteria  --> standard (5 stages)
       1-3 files, 1-2 criteria  --> quick (2 stages)
```

Nature-based rules are evaluated in priority order. The first match
wins. Size-based fallback fires only when no nature rule matches.

### Template Summary

| Template | Stages | When to use |
|----------|--------|-------------|
| quick | implement, review (2) | Well-understood, small changes |
| standard | design, implement, test, review, document (5) | Default feature work |
| full | prfaq, spec, design, implement, test, coverage, review, document, retro (9) | Large/cross-cutting/ambiguous |
| product | prfaq, design, implement, test, review, document (6) | New user-facing features |
| formal | spec, design, implement, test, coverage, review, document (7) | Stateful systems, protocols |
| docs | design, review (2) | Docs-only changes |
| coe | investigate, root-cause, fix, test, document (5) | Recurring bugs, incidents |
| coverage | measure, test, verify (3) | Targeted coverage improvement |

## How Stages Connect

The connection between stages is the result artifact. This is the
pipe. No special runtime machinery -- just the existing `Result`
type and the existing `inputs` field on `Contract`.

```text
Stage 1 (design)
  result: { verdict: pass, files_changed: [docs/walk-diff.md] }
      |
      | leader reads result, scaffolds next contract
      v
Stage 2 (implement)
  inputs.files: [docs/walk-diff.md]       <- from stage 1 result
  result: { verdict: pass, files_changed: [internal/walk-diff/diff.go, ...] }
      |
      v
Stage 3 (test)
  inputs.files: [internal/walk-diff/diff.go, ...]  <- from stage 2 result
```

The leader (or daemon) performs the connection:

1. Wait for stage N to reach a terminal status
2. Read stage N's result via `ethos mission show <id> --json`
3. Extract `files_changed[].path` from the result
4. Populate stage N+1's `inputs.files` with those paths
5. Advance stage N+1 to `open` status (or create it if deferred)

This is explicit. The leader sees the data flowing between stages
and can inspect, modify, or halt the pipeline at any point. No
implicit wiring, no hidden state.

## Contract Fields

Two fields on `Contract` support pipeline tracking (defined in
`docs/mission-archetypes.md`, repeated here for completeness):

```go
// Pipeline is an optional identifier grouping related missions
// into a sequence. All missions in a pipeline share the same
// pipeline value. Format is free-form (the leader picks it).
Pipeline string `yaml:"pipeline,omitempty" json:"pipeline,omitempty"`

// DependsOn is an optional list of mission IDs that must reach
// terminal status before this mission's worker should begin.
// Advisory -- the store does not block Create on dependency
// status. The leader or daemon enforces ordering.
DependsOn []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
```

`Pipeline` is a grouping key. `DependsOn` is an ordering hint. The
store validates both fields syntactically (control characters,
mission ID format for `depends_on` entries) but does not enforce
ordering at create time. The leader may pre-create the full pipeline
and block stages at execution time.

## Pipeline Lifecycle

### Instantiation

```bash
ethos mission pipeline instantiate sprint \
  --var feature=walk-diff \
  --leader claude \
  --evaluator bwk
```

This reads `sprint.yaml`, expands templates, and creates one mission
per stage. The pipeline identifier is generated from the pipeline
name and date: `sprint-walk-diff-2026-04-12`. Each mission carries
this value in its `pipeline` field.

Stage ordering maps to `depends_on`:

```text
design    -> (no dependency)
implement -> depends_on: [m-2026-04-12-030]   (the design mission)
test      -> depends_on: [m-2026-04-12-031]   (the implement mission)
```

### Query

```bash
# List all missions in a pipeline
ethos mission list --pipeline sprint-walk-diff-2026-04-12

# JSON output for scripting
ethos mission list --pipeline sprint-walk-diff-2026-04-12 --json
```

The `--pipeline` filter returns missions ordered by their
dependency chain, with status, type, and current round.

### Advancement

The leader advances the pipeline by connecting results to inputs.
When stage N closes:

1. Leader reads `ethos mission show <stage-N-id> --json`
2. Extracts `results[].files_changed` paths
3. Updates stage N+1's inputs (or creates stage N+1 if deferred)
4. Worker begins stage N+1

Beadle automates this for daemon-driven pipelines: it watches for
mission close events and triggers the next stage's worker.

## Error Handling

Failure at any stage halts the pipeline at that stage. The leader
decides the recovery path.

### Stage Failure Modes

| Failure | Pipeline effect | Recovery |
|---------|----------------|----------|
| `verdict: fail` | Stage stays open or moves to `failed` | Leader re-runs the stage (new round or new mission) |
| `verdict: escalate` | Stage moves to `escalated` | Leader intervenes, then re-runs or skips |
| Budget exhausted | Stage cannot advance | Leader closes as `failed` or increases budget |
| Evaluator rejects | Stage stays at current round | Worker revises; leader may reassign |

### Pipeline Status Derivation

A pipeline's status is derived from its stages, not stored
separately:

| Condition | Pipeline status |
|-----------|----------------|
| All stages `closed` | Complete |
| Any stage `failed` or `escalated` | Blocked (at that stage) |
| Any stage `open`, none failed | In progress |
| All stages not yet created | Pending |

```bash
# See pipeline status at a glance
ethos mission list --pipeline sprint-walk-diff-2026-04-12
```

```text
Pipeline: sprint-walk-diff-2026-04-12  (in progress)
  1. design     m-2026-04-12-030  closed   round 2/2  pass
  2. implement  m-2026-04-12-031  open     round 1/3  ---
  3. test       m-2026-04-12-032  open     round 0/2  ---
```

### No Automatic Retry

Pipelines do not retry failed stages automatically. The leader must
make an explicit decision: retry the stage, skip it, or abort the
pipeline. Automatic retry hides failures.

## Pipeline Events in Mission Logs

Pipeline lifecycle events appear in the per-mission JSONL event log.
Each event includes a `pipeline` field in `details` when the mission
belongs to a pipeline.

```json
{"ts":"2026-04-12T09:00:00Z","event":"create","actor":"claude","details":{"pipeline":"sprint-walk-diff-2026-04-12","stage":"design","depends_on":[]}}
{"ts":"2026-04-12T09:30:00Z","event":"close","actor":"claude","details":{"pipeline":"sprint-walk-diff-2026-04-12","stage":"design","verdict":"pass"}}
```

Stage advancement (when the leader connects stage N's output to
stage N+1's input) is logged as an `advance` event on the
downstream mission:

```json
{"ts":"2026-04-12T09:31:00Z","event":"advance","actor":"claude","details":{"pipeline":"sprint-walk-diff-2026-04-12","stage":"implement","inputs_from":"design","inputs_files":["docs/walk-diff.md"]}}
```

These events allow reconstruction of the full pipeline execution
history from the individual mission logs.

## Worked Example: Sprint Pipeline

A feature lifecycle: design the doc, implement the code, test it.

### Pipeline Declaration

```yaml
# .punt-labs/ethos/pipelines/sprint.yaml
name: sprint
description: "Design, implement, and test a feature"

stages:
  - name: design
    archetype: design
    write_set:
      - "docs/{feature}.md"
    worker: mdm
    success_criteria:
      - "Design doc covers problem, decision, and migration path"
      - "Before/after showing user-visible change"

  - name: implement
    archetype: implement
    write_set:
      - "internal/{feature}/"
    worker: bwk
    inputs_from: design
    success_criteria:
      - "All design decisions from the doc are implemented"
      - "make check passes"

  - name: test
    archetype: test
    write_set:
      - "internal/{feature}/"
    worker: bwk
    inputs_from: implement
    success_criteria:
      - "Integration tests cover the new behavior"
      - "Coverage does not decrease"
```

### Instantiation

> **Planned**: `ethos mission pipeline instantiate` is not yet
> implemented. This example shows the intended API.

```bash
$ ethos mission pipeline instantiate sprint \
    --var feature=walk-diff \
    --leader claude \
    --evaluator bwk

Created pipeline sprint-walk-diff-2026-04-12:
  design     m-2026-04-12-030  open  (no dependencies)
  implement  m-2026-04-12-031  open  depends_on: [m-2026-04-12-030]
  test       m-2026-04-12-032  open  depends_on: [m-2026-04-12-031]
```

### Generated Contracts

Stage 1 (design):

```yaml
mission_id: m-2026-04-12-030
type: design
pipeline: sprint-walk-diff-2026-04-12
leader: claude
worker: mdm
evaluator:
  handle: bwk
write_set:
  - docs/walk-diff.md
success_criteria:
  - "Design doc covers problem, decision, and migration path"
  - "Before/after showing user-visible change"
budget:
  rounds: 2
  reflection_after_each: true
```

Stage 2 (implement):

```yaml
mission_id: m-2026-04-12-031
type: implement
pipeline: sprint-walk-diff-2026-04-12
depends_on:
  - m-2026-04-12-030
leader: claude
worker: bwk
evaluator:
  handle: bwk
inputs:
  files:
    # populated when design stage closes:
    # - docs/walk-diff.md
write_set:
  - internal/walk-diff/
success_criteria:
  - "All design decisions from the doc are implemented"
  - "make check passes"
budget:
  rounds: 3
  reflection_after_each: true
```

Stage 3 (test):

```yaml
mission_id: m-2026-04-12-032
type: test
pipeline: sprint-walk-diff-2026-04-12
depends_on:
  - m-2026-04-12-031
leader: claude
worker: bwk
evaluator:
  handle: bwk
inputs:
  files:
    # populated when implement stage closes
write_set:
  - internal/walk-diff/
success_criteria:
  - "Integration tests cover the new behavior"
  - "Coverage does not decrease"
budget:
  rounds: 2
  reflection_after_each: true
```

### Execution Flow

```text
1. Leader creates pipeline. Three missions exist, stages 2-3 blocked.

2. Worker mdm executes design stage.
   Result: { verdict: pass, files_changed: [docs/walk-diff.md] }

3. Leader reads design result, populates implement inputs:
   inputs.files: [docs/walk-diff.md]
   Worker bwk begins implement stage.

4. Worker bwk produces code.
   Result: { verdict: pass, files_changed: [internal/walk-diff/diff.go,
             internal/walk-diff/diff_test.go] }

5. Leader reads implement result, populates test inputs:
   inputs.files: [internal/walk-diff/diff.go, internal/walk-diff/diff_test.go]
   Worker bwk begins test stage.

6. Worker bwk writes integration tests.
   Result: { verdict: pass, files_changed: [internal/walk-diff/diff_integration_test.go] }

7. All stages closed. Pipeline complete.
```

## Worked Example: Beadle Email Pipeline

Beadle schedules a meeting by email: check the calendar, find
a free slot, reserve it, notify participants. Four stages, each
a different archetype.

### Pipeline Declaration

```yaml
# ~/.punt-labs/ethos/pipelines/schedule-meeting.yaml
name: schedule-meeting
description: "Check calendar, find slot, reserve, and notify"

stages:
  - name: check-schedule
    archetype: report
    worker: beadle
    context: "Read {organizer}'s calendar for {date_range}"
    success_criteria:
      - "Calendar availability retrieved for the date range"

  - name: find-slot
    archetype: report
    worker: beadle
    inputs_from: check-schedule
    context: "Find a {duration} slot when all participants are free"
    success_criteria:
      - "At least one viable slot identified"
      - "All participant conflicts checked"

  - name: reserve
    archetype: task
    worker: beadle
    inputs_from: find-slot
    write_set:
      - ".tmp/calendar/{meeting_id}.ics"
    context: "Reserve the chosen slot on {organizer}'s calendar"
    success_criteria:
      - "Calendar event created"
      - "ICS file written with correct attendees and time"

  - name: notify
    archetype: task
    worker: beadle
    inputs_from: reserve
    write_set:
      - ".tmp/email/{meeting_id}-invite.eml"
    context: "Send meeting invitation to all participants"
    success_criteria:
      - "Invitation email sent to each participant"
      - "Email contains correct time, location, and agenda"
```

### Instantiation

> **Planned**: `ethos mission pipeline instantiate` is not yet
> implemented. This example shows the intended API.

```bash
$ ethos mission pipeline instantiate schedule-meeting \
    --var organizer=jim \
    --var date_range=2026-04-14..2026-04-18 \
    --var duration=30m \
    --var meeting_id=standup-q2 \
    --leader beadle \
    --evaluator claude

Created pipeline schedule-meeting-standup-q2-2026-04-12:
  check-schedule  m-2026-04-12-040  open  (no dependencies)
  find-slot       m-2026-04-12-041  open  depends_on: [m-2026-04-12-040]
  reserve         m-2026-04-12-042  open  depends_on: [m-2026-04-12-041]
  notify          m-2026-04-12-043  open  depends_on: [m-2026-04-12-042]
```

### Generated Contracts

Stage 1 (check-schedule):

```yaml
mission_id: m-2026-04-12-040
type: report
pipeline: schedule-meeting-standup-q2-2026-04-12
leader: beadle
worker: beadle
evaluator:
  handle: claude
context: "Read jim's calendar for 2026-04-14..2026-04-18"
success_criteria:
  - "Calendar availability retrieved for the date range"
budget:
  rounds: 1
  reflection_after_each: true
```

Stage 2 (find-slot):

```yaml
mission_id: m-2026-04-12-041
type: report
pipeline: schedule-meeting-standup-q2-2026-04-12
depends_on:
  - m-2026-04-12-040
leader: beadle
worker: beadle
evaluator:
  handle: claude
context: "Find a 30m slot when all participants are free"
success_criteria:
  - "At least one viable slot identified"
  - "All participant conflicts checked"
budget:
  rounds: 1
  reflection_after_each: true
```

Stage 3 (reserve):

```yaml
mission_id: m-2026-04-12-042
type: task
pipeline: schedule-meeting-standup-q2-2026-04-12
depends_on:
  - m-2026-04-12-041
leader: beadle
worker: beadle
evaluator:
  handle: claude
write_set:
  - ".tmp/calendar/standup-q2.ics"
context: "Reserve the chosen slot on jim's calendar"
success_criteria:
  - "Calendar event created"
  - "ICS file written with correct attendees and time"
budget:
  rounds: 3
  reflection_after_each: true
```

Stage 4 (notify):

```yaml
mission_id: m-2026-04-12-043
type: task
pipeline: schedule-meeting-standup-q2-2026-04-12
depends_on:
  - m-2026-04-12-042
leader: beadle
worker: beadle
evaluator:
  handle: claude
write_set:
  - ".tmp/email/standup-q2-invite.eml"
context: "Send meeting invitation to all participants"
success_criteria:
  - "Invitation email sent to each participant"
  - "Email contains correct time, location, and agenda"
budget:
  rounds: 3
  reflection_after_each: true
```

### Execution Flow

```text
1. Beadle daemon creates pipeline from inbound email trigger.

2. check-schedule executes (report archetype, read-only).
   Result: { verdict: pass, evidence: [{name: "calendar-api", status: "pass"}] }
   Result artifact contains availability data.

3. Beadle reads result, populates find-slot inputs.
   find-slot executes (report archetype, read-only).
   Result: { verdict: pass, evidence: [{name: "slot-search", status: "pass"}] }
   Result artifact identifies Tuesday 10:00-10:30.

4. Beadle reads result, populates reserve inputs.
   reserve executes (task archetype, writes ICS file).
   Result: { verdict: pass, files_changed: [.tmp/calendar/standup-q2.ics] }

5. Beadle reads result, populates notify inputs.
   notify executes (task archetype, sends email).
   Result: { verdict: pass, files_changed: [.tmp/email/standup-q2-invite.eml] }

6. All stages closed. Pipeline complete.
   Beadle replies to the original email: "Meeting scheduled."
```

## Worked Example: Product Pipeline

A new user-facing feature: Working Backwards before engineering.
The PR/FAQ forces product thinking before a line of code is written.

### Pipeline Declaration

```yaml
# .punt-labs/ethos/pipelines/product.yaml
name: product
description: "New feature with product validation — Working Backwards before engineering"

stages:
  - name: prfaq
    archetype: design
    description: "Working Backwards — press release, FAQs, meeting personas"
    worker: claude
    success_criteria:
      - "Press release is one page, customer-facing"
      - "External FAQ answers 'why now' and 'what if it fails'"
      - "Internal FAQ covers cost, timeline, and dependencies"

  - name: design
    archetype: design
    description: "Produce the technical design document"
    worker: mdm
    inputs_from: prfaq
    success_criteria:
      - "Design doc covers problem, decision, and migration path"
      - "Before/after showing user-visible change"

  - name: implement
    archetype: implement
    write_set:
      - "internal/{feature}/"
    worker: bwk
    inputs_from: design
    success_criteria:
      - "All design decisions from the doc are implemented"
      - "make check passes"

  - name: test
    archetype: test
    write_set:
      - "internal/{feature}/"
    worker: bwk
    inputs_from: implement
    success_criteria:
      - "Integration tests cover the new behavior"
      - "Coverage does not decrease"

  - name: review
    archetype: review
    worker: djb
    inputs_from: test
    success_criteria:
      - "No HIGH or MEDIUM findings"

  - name: document
    archetype: design
    write_set:
      - "DESIGN.md"
      - "README.md"
      - "CHANGELOG.md"
    worker: mdm
    inputs_from: review
    success_criteria:
      - "DESIGN.md has ADR for key decisions"
      - "README documents new user-facing behavior"
```

### Instantiation

> **Planned**: `ethos mission pipeline instantiate` is not yet
> implemented. This example shows the intended API.

```bash
$ ethos mission pipeline instantiate product \
    --var feature=auto-suggest \
    --leader claude \
    --evaluator bwk

Created pipeline product-auto-suggest-2026-04-12:
  prfaq      m-2026-04-12-050  open  (no dependencies)
  design     m-2026-04-12-051  open  depends_on: [m-2026-04-12-050]
  implement  m-2026-04-12-052  open  depends_on: [m-2026-04-12-051]
  test       m-2026-04-12-053  open  depends_on: [m-2026-04-12-052]
  review     m-2026-04-12-054  open  depends_on: [m-2026-04-12-053]
  document   m-2026-04-12-055  open  depends_on: [m-2026-04-12-054]
```

### Generated Contracts

Stage 1 (prfaq):

```yaml
mission_id: m-2026-04-12-050
type: design
pipeline: product-auto-suggest-2026-04-12
leader: claude
worker: claude
evaluator:
  handle: bwk
success_criteria:
  - "Press release is one page, customer-facing"
  - "External FAQ answers 'why now' and 'what if it fails'"
  - "Internal FAQ covers cost, timeline, and dependencies"
budget:
  rounds: 2
  reflection_after_each: true
```

Stage 2 (design):

```yaml
mission_id: m-2026-04-12-051
type: design
pipeline: product-auto-suggest-2026-04-12
depends_on:
  - m-2026-04-12-050
leader: claude
worker: mdm
evaluator:
  handle: bwk
inputs:
  files:
    # populated when prfaq stage closes
write_set:
  - docs/auto-suggest.md
success_criteria:
  - "Design doc covers problem, decision, and migration path"
  - "Before/after showing user-visible change"
budget:
  rounds: 2
  reflection_after_each: true
```

### Execution Flow

```text
1. Leader creates pipeline. Six missions exist, stages 2-6 blocked.

2. Worker claude executes prfaq stage (Working Backwards).
   Result: { verdict: pass, files_changed: [docs/auto-suggest-prfaq.md] }

3. Leader reads prfaq result, populates design inputs:
   inputs.files: [docs/auto-suggest-prfaq.md]
   Worker mdm begins design stage.

4. Worker mdm produces design doc.
   Result: { verdict: pass, files_changed: [docs/auto-suggest.md] }

5. Leader reads design result, populates implement inputs.
   Worker bwk writes the code.

6. Worker bwk writes tests. Worker djb reviews. Worker mdm documents.

7. All stages closed. Pipeline complete.
```

## Worked Example: COE Pipeline

A recurring bug: the same class of failure has appeared before.
The COE pipeline forces investigation before fixing, and
documentation before closing.

### Pipeline Declaration

```yaml
# .punt-labs/ethos/pipelines/coe.yaml
name: coe
description: "Cause of Error investigation — recurring bugs, data corruption, incidents"

stages:
  - name: investigate
    archetype: report
    description: "Reproduce the issue, gather data, build a timeline"
    worker: bwk
    success_criteria:
      - "Issue reproduced with steps"
      - "Timeline of when the bug first appeared"
      - "Data gathered: logs, stack traces, git blame"

  - name: root-cause
    archetype: report
    description: "Analyze data, determine root cause with evidence"
    worker: bwk
    inputs_from: investigate
    success_criteria:
      - "Root cause identified with evidence, not theory"
      - "Equivalence class enumerated — all siblings found"

  - name: fix
    archetype: implement
    write_set:
      - "internal/{feature}/"
    worker: bwk
    inputs_from: root-cause
    success_criteria:
      - "Fix addresses root cause, not symptom"
      - "Regression test reproduces original failure"
      - "All siblings in equivalence class fixed"

  - name: test
    archetype: test
    write_set:
      - "internal/{feature}/"
    worker: bwk
    inputs_from: fix
    success_criteria:
      - "Regression test fails without fix, passes with fix"
      - "No sibling regressions"

  - name: document
    archetype: design
    write_set:
      - "docs/coe/{feature}.md"
    worker: mdm
    inputs_from: test
    success_criteria:
      - "COE doc has: timeline, root cause, fix, prevention"
      - "Linked to the PR that ships the fix"
```

### Instantiation

> **Planned**: `ethos mission pipeline instantiate` is not yet
> implemented. This example shows the intended API.

```bash
$ ethos mission pipeline instantiate coe \
    --var feature=stale-cache \
    --leader claude \
    --evaluator djb

Created pipeline coe-stale-cache-2026-04-12:
  investigate  m-2026-04-12-060  open  (no dependencies)
  root-cause   m-2026-04-12-061  open  depends_on: [m-2026-04-12-060]
  fix          m-2026-04-12-062  open  depends_on: [m-2026-04-12-061]
  test         m-2026-04-12-063  open  depends_on: [m-2026-04-12-062]
  document     m-2026-04-12-064  open  depends_on: [m-2026-04-12-063]
```

### Generated Contracts

Stage 1 (investigate):

```yaml
mission_id: m-2026-04-12-060
type: report
pipeline: coe-stale-cache-2026-04-12
leader: claude
worker: bwk
evaluator:
  handle: djb
success_criteria:
  - "Issue reproduced with steps"
  - "Timeline of when the bug first appeared"
  - "Data gathered: logs, stack traces, git blame"
budget:
  rounds: 2
  reflection_after_each: true
```

Stage 2 (root-cause):

```yaml
mission_id: m-2026-04-12-061
type: report
pipeline: coe-stale-cache-2026-04-12
depends_on:
  - m-2026-04-12-060
leader: claude
worker: bwk
evaluator:
  handle: djb
inputs:
  files:
    # populated when investigate stage closes
success_criteria:
  - "Root cause identified with evidence, not theory"
  - "Equivalence class enumerated — all siblings found"
budget:
  rounds: 2
  reflection_after_each: true
```

### Execution Flow

```text
1. Leader creates pipeline. Five missions exist, stages 2-5 blocked.

2. Worker bwk executes investigate stage (report archetype, read-only).
   Result: { verdict: pass, evidence: [{name: "reproduction", status: "pass"},
             {name: "timeline", status: "pass"}] }

3. Leader reads investigation result, populates root-cause inputs.
   Worker bwk analyzes the data.
   Result: { verdict: pass, evidence: [{name: "root-cause-proof", status: "pass"},
             {name: "sibling-scan", status: "pass"}] }

4. Leader reads root-cause result, populates fix inputs.
   Worker bwk writes the fix and regression test.
   Result: { verdict: pass, files_changed: [internal/stale-cache/cache.go,
             internal/stale-cache/cache_test.go] }

5. Worker bwk verifies fix. Worker mdm writes COE document.

6. All stages closed. Pipeline complete.
   COE document at docs/coe/stale-cache.md.
```

## Design Invariants

1. **Pipeline is orchestration, not an archetype.** There is no
   `type: pipeline` in the archetype registry. A pipeline is a
   declaration of how archetype-typed stages compose. Adding a
   pipeline archetype would conflate the thing being composed with
   the composition mechanism.

2. **The orchestrator is the leader, not ethos.** Ethos stores the
   pipeline declaration, creates missions, and provides query
   surfaces. It does not execute stages, advance the pipeline, or
   connect outputs to inputs. The leader (or beadle daemon) does
   that. Ethos is the filesystem and the validation layer; the
   orchestrator is the shell.

3. **Stages are independent missions.** Each stage has its own
   mission ID, write set, evaluator, budget, and result. A stage
   can be re-run, skipped, or replaced without affecting the
   pipeline declaration. The pipeline groups them; it does not own
   them.

4. **The result artifact is the pipe.** No new inter-stage data
   format. The existing `Result` type (DES-036) carries
   `files_changed` and `evidence` from stage N to stage N+1. The
   leader extracts what the next stage needs and populates its
   `inputs`.

5. **Dependencies are advisory.** `depends_on` tells the leader
   (and readers) the intended ordering. The store does not enforce
   it -- a leader may create all stages upfront and block execution
   at orchestration time. This matches the existing `depends_on`
   semantics from `docs/mission-archetypes.md`.

6. **No implicit wiring.** The leader explicitly reads stage N's
   result and populates stage N+1's inputs. There is no runtime
   that automatically connects outputs to inputs. Explicit wiring
   means the leader can inspect, transform, or augment the data
   between stages.

## Interaction with Existing Primitives

| Primitive | Interaction |
|-----------|-------------|
| Archetypes (mission-archetypes.md) | Each stage declares an archetype; archetype validation applies per-stage |
| DES-031 (contract) | Pipeline adds `pipeline` and `depends_on` fields; base validation unchanged |
| DES-032 (conflict) | Write-set conflict check is cross-pipeline: stages in different pipelines can conflict on the same file |
| DES-036 (result artifacts) | Results are the inter-stage data channel; no schema change |
| DES-037 (event log) | Pipeline and stage name appear in event details |
| `mission list` | `--pipeline` filter returns all missions in a pipeline |
| `mission show` | `Pipeline:` and `Depends on:` lines appear when non-empty |

## What This Is Not

- **Not a DAG executor.** Pipelines are linear sequences with
  advisory dependencies. Fan-out (stage N feeds stages N+1a and
  N+1b in parallel) is a future extension, not in this design.

- **Not a workflow engine.** No conditionals, no loops, no
  branching on verdict. The leader makes those decisions. The
  pipeline declares the happy path.

- **Not stored in the mission store.** The pipeline YAML file is a
  declaration template. Instantiated pipelines are a set of
  missions sharing a `pipeline` field value. There is no separate
  pipeline state file -- the state is derived from the missions.
