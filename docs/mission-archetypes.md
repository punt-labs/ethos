# Mission Archetypes

Typed subtypes of the mission contract. An archetype constrains what
a valid mission looks like for a given kind of work: which fields
are required, what the budget defaults to, how validation tightens,
and who is authorized to create one.

## Problem

The mission contract (DES-031) treats all missions identically.
A design mission and an implementation mission carry the same
schema, pass the same validation, and accept the same budget range.
Three problems follow:

1. **No structural enforcement of mission kind.** A design mission
   should require before/after success criteria and a docs-only
   write set. An implementation mission should require test files
   adjacent to production code. Today, the `Lint` heuristics (9
   checks in `lint.go`) catch some of these post-hoc, but nothing
   prevents a design mission from claiming `internal/foo/bar.go` in
   its write set or an implementation mission from shipping without
   tests. Lint is advisory; archetypes are structural.

2. **No authorization model for mission creation.** When beadle uses
   missions as the orchestrator control plane, the question becomes:
   can this contact create this type of mission? An email from the
   CEO should be able to trigger any archetype. A CI bot should only
   trigger deploy missions. A vendor with read-only access should
   not trigger any. Today, `Store.Create` has no concept of "what
   kind of mission is this" and therefore no hook for
   authorization.

3. **No pipeline composition.** A feature lifecycle is a sequence of
   typed stages: design, implement, test, review, deploy. Each
   stage has different validation rules, different worker selection,
   and different evaluator requirements. Today, chaining missions
   is manual -- the leader reads the output of one and scaffolds
   the input to the next. Typed archetypes make the stages
   machine-readable, so a pipeline can enforce ordering and pass
   structured outputs forward.

## Archetype Definition

An archetype is a named set of constraints applied on top of the
base `Contract.Validate()` rules (13 rules as of DES-034).

Archetypes are YAML files discovered from the filesystem, following
the same layered-store pattern as identities, roles, teams, talents,
and personalities. There is no Go registration API -- tools add
archetypes by dropping YAML files into the appropriate directory.

### Storage Layout

| Scope | Path | Git-tracked? |
|-------|------|-------------|
| Repo archetypes | `.punt-labs/ethos/archetypes/<name>.yaml` | Yes |
| Global archetypes | `~/.punt-labs/ethos/archetypes/<name>.yaml` | No |

Repo-local archetypes override global archetypes with the same name,
same as every other layered store in ethos.

### YAML Schema

```yaml
# name must match the filename (without .yaml extension).
# Lowercase, hyphenated. Examples: design, implement, inbox.
name: design

# One-line summary for help text and mission show output.
description: "Design mission — output is a document, not code"

# Applied when the contract omits budget or uses zero values.
budget_default:
  rounds: 2
  reflection_after_each: true

# When true, skips the base validation rule that requires write_set
# to be non-empty (rule 10). Used by read-only archetypes (inbox,
# report) whose output is delivered via the result mechanism, not
# file writes.
allow_empty_write_set: false

# Contract fields that must be non-empty beyond the base Validate
# rules. Dot-separated paths for nested fields.
# Examples: "context", "inputs.files".
required_fields:
  - context

# Optional glob patterns the write_set must match. When present,
# every write_set entry must match at least one pattern.
# Examples: "docs/*.md", "*.md", "internal/**/*_test.go".
write_set_constraints:
  - "*.md"
  - "docs/**"
```

## Contract Schema Change

The `Contract` struct gains one field:

```go
type Contract struct {
    // ... existing fields ...

    // Type is the archetype name. Defaults to "implement" when
    // empty or absent, so all pre-existing missions are valid
    // implementation missions without migration.
    Type string `yaml:"type,omitempty" json:"type"`

    // ... existing fields ...
}
```

**Placement:** after `Repo` and before `Leader`, grouping the
mission's classification metadata together.

**Validation rule 14:** `type`, when non-empty, must match a
discovered archetype name (a YAML file in the layered store). When
empty, it is rewritten to `"implement"` by `Store.Create` (same
pattern as `CurrentRound` being rewritten to 1). `Validate()`
rejects unknown type names after the rewrite, so a hand-edited
contract with `type: foobar` fails on load.

**Wire compatibility:** `omitempty` on the YAML tag means
pre-existing mission files without a `type` field decode cleanly.
The JSON tag always emits `"type"` so consumers see a consistent
shape (same pattern as `Evaluator.Hash`).

## Archetype Registry

Seven archetypes form the initial registry: four ethos-native
archetypes and three beadle archetypes. All are YAML files on disk.

Ethos ships the four core archetypes as repo-local files in
`.punt-labs/ethos/archetypes/`. Beadle drops its three into
`~/.punt-labs/ethos/archetypes/` at install time. The split
reflects ownership: ethos defines archetypes for code-lifecycle
work; beadle defines archetypes for daemon-triggered work (email
processing, task execution, information gathering).

No Go code, no import dependency. Beadle adds archetypes the same
way any tool adds identities or talents -- by writing files.

### Ethos Archetypes

#### design

For missions whose output is a document, not code.

| Property | Value |
|----------|-------|
| Default budget | 2 rounds |
| Required fields | `context` |
| Write set constraint | Every entry must be `.md` or under `docs/` |
| Success criteria constraint | At least one criterion must contain "before" and "after", or "user-visible", or "user-facing" |
| Evaluator guidance | Domain specialist preferred over generalist |

The write-set and success-criteria constraints promote existing
`Lint` heuristics 8 (design mission has no user-visible impact
criterion) and 9 (docs write-set with a generalist evaluator) from
advisory to enforced for missions explicitly typed as `design`.

#### implement

The default archetype. For missions that produce code.

| Property | Value |
|----------|-------|
| Default budget | 3 rounds |
| Required fields | (none beyond base) |
| Write set constraint | If write set contains a `.go` file, adjacent `_test.go` must also be present (promotes lint heuristic 1) |
| Success criteria constraint | (none beyond base) |

This is the backward-compatible default. Every pre-existing mission
is an `implement` mission. The test-adjacency check promotes lint
heuristic 1 from advisory to enforced -- a mission that claims to
modify `store.go` without `store_test.go` is rejected at create
time, not warned about after the fact.

#### test

For missions that add or improve test coverage without changing
production code.

| Property | Value |
|----------|-------|
| Default budget | 2 rounds |
| Required fields | (none beyond base) |
| Write set constraint | Every entry must be a `_test.go` file, a testdata/ path, or a docs path |
| Success criteria constraint | At least one criterion must reference coverage, regression, or test |

#### review

For missions where the worker reviews existing code or a diff and
produces findings, not code changes.

| Property | Value |
|----------|-------|
| Default budget | 1 round |
| Required fields | `inputs.files` (must have something to review) |
| Write set constraint | Write set is a report path (`.md`, `.yaml`, or under `.tmp/`) |
| Success criteria constraint | (none beyond base) |

A review mission defaults to 1 round because the reviewer's job is
to read and report, not iterate. The leader can override to 2 if
they want a revision cycle.

### Beadle Archetypes

The beadle daemon creates missions triggered by email. Its work
collapses to three primitives. Beadle drops these YAML files into
`~/.punt-labs/ethos/archetypes/` at install time -- ethos discovers
them at runtime via the layered store, same as any other archetype.

#### inbox

Process unread email. Read-only: the worker reads, classifies, and
routes -- no file mutations.

| Property | Value |
|----------|-------|
| Default budget | 1 round |
| Required fields | (none beyond base) |
| Write set constraint | Must be empty |
| Success criteria constraint | (none beyond base) |

Inbox missions are inherently read-only. The empty write_set
requirement is enforced by validation, not by convention. A
contract that declares `type: inbox` with any write_set entry is
rejected at create time.

Note: the base `Contract.Validate()` requires `write_set` to be
non-empty (rule 10). Beadle's inbox archetype overrides this by
registering with `AllowEmptyWriteSet: true`, which causes the
archetype validation layer to skip the base write_set-non-empty
check. See "Per-Archetype Validation" below for the override
mechanism.

#### task

Execute a specific instruction from an x-permitted contact. The
write_set varies by task -- it is required and validated normally.

| Property | Value |
|----------|-------|
| Default budget | 3 rounds |
| Required fields | `context` (the instruction to execute) |
| Write set constraint | Must be non-empty (standard base rule) |
| Success criteria constraint | (none beyond base) |

Task missions are the general-purpose "do this thing" archetype.
Budget defaults to 3 rounds but the leader can set 1-3 depending
on complexity.

#### report

Gather information and deliver a summary. Read-only: the worker
reads sources and produces a report artifact, but does not modify
project files.

| Property | Value |
|----------|-------|
| Default budget | 1 round |
| Required fields | (none beyond base) |
| Write set constraint | Must be empty |
| Success criteria constraint | (none beyond base) |

Like inbox, report missions are read-only. The report artifact is
delivered via the result mechanism (DES-036), not via file writes.

Note: same `AllowEmptyWriteSet: true` override as inbox. See
"Per-Archetype Validation" below.

### Extensibility

Tools add archetypes by dropping YAML files into the archetype
directories. No Go code, no import, no registration API.

```text
# Beadle installs its archetypes during `beadle install`:
~/.punt-labs/ethos/archetypes/inbox.yaml
~/.punt-labs/ethos/archetypes/task.yaml
~/.punt-labs/ethos/archetypes/report.yaml

# A project adds a custom archetype by committing a file:
.punt-labs/ethos/archetypes/deploy.yaml
```

This is the same pattern as identities (`identities/<handle>.yaml`),
talents (`talents/<slug>.md`), and roles (`roles/<name>.yaml`) --
filesystem discovery via a layered store, repo-local overrides
global.

Name uniqueness is enforced at load time: if two files at the same
scope have the same `name`, ethos reports an error. Repo-local
files intentionally override global files with the same name (that
is not a collision -- it is the override mechanism).

## Per-Archetype Validation

Archetype validation is a second pass after `Contract.Validate()`.
The base 13+1 rules always run first. The archetype constraints
(derived from the YAML fields) run only if the base passes.

```text
Contract.Validate()           -- 14 rules, same as today + rule 14 (type)
  |                               (rule 10 skipped if archetype.allow_empty_write_set)
  v (pass)
archetype constraints         -- per-type, derived from YAML fields
  |
  v (pass)
Store.Create proceeds
```

This layering means:

1. A contract that fails base validation is always rejected,
   regardless of archetype.
2. Archetype validation can assume the contract is well-formed
   (non-nil, valid timestamps, etc.).
3. Adding an archetype never weakens the base rules -- except for
   `allow_empty_write_set`, which is an explicit opt-out from rule
   10 for read-only archetypes. The opt-out is structural: a
   read-only archetype that requires empty write_set would
   contradict a base rule that requires non-empty write_set. The
   archetype flag resolves the contradiction at load time, not by
   silently ignoring the error.

### How YAML Fields Map to Validation

| YAML field | Validation behavior |
|------------|-------------------|
| `required_fields` | Each listed field path must be non-empty in the contract |
| `allow_empty_write_set` | When true, skips base rule 10 (write_set non-empty) |
| `write_set_constraints` | Every write_set entry must match at least one glob pattern |
| `budget_default` | Applied when the contract omits budget or uses zero values |

The Go code that interprets these fields is generic -- it does not
know about "design" or "inbox" specifically. It reads the YAML,
checks `required_fields` against the contract, matches
`write_set_constraints` globs against `write_set` entries, and
applies `budget_default` when needed. Adding a new archetype is
adding a YAML file, not writing Go.

## Authorization Model

### The x-bit: Binary Gate

The x-bit is binary: can this contact cause autonomous action, yes
or no. There are no per-archetype x variants.

| Permission | Meaning in beadle context |
|------------|--------------------------|
| r (read)   | Can read mission status and results |
| w (write)  | Can submit results and reflections to existing missions |
| x (execute)| Can create missions (any type) |

What gets done is constrained by the contract -- write_set, budget,
success criteria, and archetype validation rules. Who triggers it
is constrained by the x-bit. The two concerns are orthogonal.

```yaml
# ~/.punt-labs/beadle/contacts/ceo.yaml (example)
permissions:
  read: true
  write: true
  execute: true    # can trigger any mission type
```

```yaml
# ~/.punt-labs/beadle/contacts/ci-bot.yaml (example)
permissions:
  read: true
  write: false
  execute: true    # can trigger missions; contract constrains scope
```

```yaml
# ~/.punt-labs/beadle/contacts/vendor.yaml (example)
permissions:
  read: true
  write: false
  execute: false   # cannot trigger missions
```

A CI bot with `execute: true` can only do what the contract allows.
If the contract says `type: task` with `write_set: [deploy/]`, the
archetype and write_set rules enforce the scope -- not the x-bit.
The x-bit answers one question: should beadle act on this message
at all?

### Authorization Check Flow

```text
beadle receives inbound message (email, webhook, etc.)
  |
  v
resolve contact identity
  |
  v
contact.permissions.execute == true?
  |
  +-- YES: classify message → select archetype → build contract
  |         ethos mission create --file <contract.yaml>
  |         (archetype validation enforces structural constraints)
  |
  +-- NO:  reject with:
            "beadle: contact <handle> not authorized to trigger missions"
```

The authorization check happens in beadle, before the `ethos
mission create` call. Ethos does not enforce authorization -- it
validates the contract. Beadle enforces who can create missions.
This separation keeps ethos's trust boundary clean: ethos answers
"is this contract well-formed?" and beadle answers "is this contact
allowed to create it?"

### Why Authorization Lives in Beadle, Not Ethos

Ethos is an identity service. It knows who people are and what
roles they have. It does not know who is allowed to do what in a
given operational context -- that is the orchestrator's job.

If ethos enforced authorization, it would need to know about
contacts, permission records, and inbound message routing -- all
beadle concepts. The boundary would leak. Instead:

- **Ethos** validates the `type` field against the archetype
  registry (structural correctness).
- **Beadle** checks the contact's `execute` bit before creating a
  mission (operational authorization). The contract's archetype,
  write_set, and budget constrain what gets done.
- **Biff** routes archetype-aware notifications when missions
  create, advance, or close (collaboration protocol from Layer 3).

Three tools, three concerns. The x-bit gates entry. The contract
constrains scope.

## Pipeline Composition

Pipeline is an orchestration pattern on top of archetypes, not a
separate archetype. A pipeline chains missions of different types
into a sequence. Beadle's "scheduled" pattern (run this task at
09:00 daily) is likewise orchestration -- a cron trigger that
creates a mission of an existing archetype.

### Typed Stages

A feature lifecycle is a sequence of missions piped together:

```text
design | implement | test | review
```

Each stage is an archetype. The output of one stage becomes the
input to the next. The pipeline is not a new runtime primitive --
it is a convention enforced by the leader (or by beadle when it
orchestrates sprints).

### How Stages Connect

The connection is the result artifact (DES-036). Each mission
produces a `Result` with `files_changed`, `evidence`, and a
`verdict`. The next stage reads the previous stage's result as
input.

```text
Stage 1: design mission
  write_set: [docs/feature-x.md]
  result: { verdict: pass, files_changed: [docs/feature-x.md] }
      |
      v (leader reads result, scaffolds next contract)
Stage 2: implement mission
  inputs.files: [docs/feature-x.md]   # <-- output of stage 1
  write_set: [internal/foo.go, internal/foo_test.go]
  result: { verdict: pass, files_changed: [...] }
      |
      v
Stage 3: test mission
  inputs.files: [internal/foo.go, internal/foo_test.go]
  write_set: [internal/foo_integration_test.go]
      |
      v
Stage 4: review mission
  inputs.files: [internal/foo.go, internal/foo_test.go, ...]
  write_set: [.tmp/missions/results/review-feature-x.yaml]
```

### Pipeline Contract Fields

Two optional fields on `Contract` support pipeline tracking without
breaking the existing schema:

```go
type Contract struct {
    // ... existing fields ...

    // Type is the archetype name.
    Type string `yaml:"type,omitempty" json:"type"`

    // Pipeline is an optional identifier grouping related missions
    // into a sequence. All missions in a pipeline share the same
    // pipeline value. Format is free-form (the leader picks it).
    // Example: "feature-x-2026-04-12".
    Pipeline string `yaml:"pipeline,omitempty" json:"pipeline,omitempty"`

    // DependsOn is an optional list of mission IDs that must be in
    // a terminal status (closed, failed, escalated) before this
    // mission's worker should begin. Advisory -- the store does not
    // block Create on dependency status. The leader or beadle
    // enforces ordering.
    DependsOn []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`

    // ... existing fields ...
}
```

**Pipeline field** is a grouping key. `ethos mission list --pipeline
feature-x` filters to all missions in the pipeline. No enforcement
-- the field is metadata for the leader and for beadle's sprint
view.

**DependsOn field** is advisory. `Store.Create` does not refuse a
mission whose dependencies are still open -- the leader may want to
pre-create the full pipeline and let each stage block on its
predecessor at execution time. Beadle can enforce ordering at
orchestration time by checking dependency status before sending the
worker's trigger message.

### Pipeline Query

```bash
# List all missions in a pipeline
ethos mission list --pipeline feature-x-2026-04-12

# Show pipeline DAG (future -- not in initial implementation)
ethos mission pipeline feature-x-2026-04-12
```

### Before/After: Pipeline in Practice

**Before (untyped missions):**

The leader manually tracks which missions form a sequence. Stage
outputs are passed by convention ("read the result of
m-2026-04-12-001"). No way to query "show me all missions for
feature X" or "which stages are done."

```bash
ethos mission create --file design.yaml
# ... wait for completion ...
ethos mission create --file implement.yaml
# Leader manually ensures implement reads design output
```

**After (typed archetype pipeline):**

```bash
# Create the full pipeline upfront
ethos mission create --file design.yaml
# contract contains: type: design, pipeline: feature-x

ethos mission create --file implement.yaml
# contract contains: type: implement, pipeline: feature-x,
#   depends_on: [m-2026-04-12-001]

ethos mission create --file test.yaml
# contract contains: type: test, pipeline: feature-x,
#   depends_on: [m-2026-04-12-002]

# Query pipeline status
ethos mission list --pipeline feature-x --json
# Returns all 3 missions with their status, type, and dependencies

# Beadle triggers each stage when dependencies close
```

## Before/After: Mission Create

### Before (no archetypes)

```yaml
# All missions look the same structurally.
leader: claude
worker: mdm
evaluator:
  handle: bwk
write_set:
  - docs/mission-archetypes.md
success_criteria:
  - Design doc defines what a mission archetype is
budget:
  rounds: 2
  reflection_after_each: true
```

```bash
$ ethos mission create --file contract.yaml
# Succeeds. Lint warns about missing before/after criterion
# and generalist evaluator, but these are advisory.
```

### After (with archetypes)

```yaml
# Archetype constrains what's valid.
type: design
leader: claude
worker: mdm
evaluator:
  handle: bwk
context: |
  Mission archetypes are typed subtypes that constrain valid state
  for missions. Motivated by beadle's orchestrator control plane
  and sprint pipeline composition.
write_set:
  - docs/mission-archetypes.md
success_criteria:
  - Design doc defines what a mission archetype is and why typed subtypes matter
  - Before/after for mission create showing what changes with archetypes
budget:
  rounds: 2
  reflection_after_each: true
```

```bash
$ ethos mission create --file contract.yaml
# Succeeds. The design archetype enforces:
#   - context field is non-empty (checked)
#   - write_set is docs-only (checked)
#   - at least one before/after criterion (checked)
```

```bash
# What happens when archetype validation fails:
$ cat bad-design.yaml
type: design
leader: claude
worker: bwk
evaluator:
  handle: djb
write_set:
  - internal/mission/archetype.go    # NOT docs-only
success_criteria:
  - Archetype struct exists           # no before/after
budget:
  rounds: 2
  reflection_after_each: true

$ ethos mission create --file bad-design.yaml
ethos: mission create: design mission write_set must contain only .md files or docs/ paths
```

### mission show Output Change

```text
Mission:    m-2026-04-12-020
Type:       design
Status:     open
...
Pipeline:   feature-x-2026-04-12
Depends on: m-2026-04-12-019
```

The `Type:` line appears after `Mission:` and before `Status:`.
`Pipeline:` and `Depends on:` appear after `Budget:` when
non-empty. Omitted when empty (no noise for missions outside a
pipeline).

## Migration Path

### No Breaking Changes

1. **Empty `type` defaults to `"implement"`.** `Store.Create`
   rewrites empty type to `"implement"` before validation, same
   pattern as `CurrentRound` rewritten to 1. Pre-existing mission
   files without a `type` field decode cleanly via `omitempty`.

2. **`implement` archetype has no new constraints beyond lint
   promotion.** The test-adjacency check is the only new
   enforcement, and it was already a lint warning. Leaders who
   intentionally omit test files can add the directory entry
   (`internal/mission/`) to satisfy the check.

3. **`pipeline` and `depends_on` are optional.** Pre-existing
   missions have neither field. They load, validate, and operate
   identically to today.

4. **New validation rule 14 accepts all discovered archetype names
   plus empty.** A hand-edited contract with `type: foobar` fails,
   but that contract would have had to be manually created -- no
   existing tool produces it.

5. **Lint heuristics that become archetype rules remain as lint for
   untyped missions.** If a mission has `type: implement`, the
   test-adjacency check is enforced. If a pre-existing mission has
   no type, the lint heuristic still fires as a warning. No
   behavior regression.

### Rollout Order

1. Add `Type` field to `Contract` struct with `omitempty`.
2. Add validation rule 14 to `Validate()`.
3. Add archetype layered store with filesystem discovery.
4. Ship four core archetype YAML files; wire `Store.Create` to
   load archetype and run constraints after base validation.
5. Update `mission show` and `mission list` to display `Type`.
6. Add `Pipeline` and `DependsOn` fields.
7. Add `--pipeline` filter to `mission list`.
8. Update MCP tool to surface `type`, `pipeline`, `depends_on`.

Steps 1-5 are the minimum viable archetype system. Steps 6-8 add
pipeline support. Each step is independently shippable.

## Archetype Defaults Summary

| Archetype | Default Rounds | Required Fields | Write Set Constraint | Success Criteria Constraint | Shipped By |
|-----------|---------------|-----------------|---------------------|----------------------------|------------|
| design | 2 | context | .md or docs/ only | before/after or user-visible | ethos |
| implement | 3 | (base only) | .go requires _test.go | (base only) | ethos |
| test | 2 | (base only) | _test.go, testdata/, or docs only | coverage/regression/test ref | ethos |
| review | 1 | inputs.files | .md, .yaml, or .tmp/ only | (base only) | ethos |
| inbox | 1 | (base only) | must be empty | (base only) | beadle |
| task | 3 | context | (standard base rule) | (base only) | beadle |
| report | 1 | (base only) | must be empty | (base only) | beadle |

## Interaction with Existing Primitives

| Primitive | Interaction |
|-----------|-------------|
| DES-031 (contract) | Archetype adds `Type` field; base validation unchanged |
| DES-032 (conflict) | Write-set conflict check is archetype-agnostic. Two design missions can conflict on the same .md file. |
| DES-033 (frozen evaluator) | Hash computation is archetype-agnostic. The evaluator is frozen regardless of mission type. |
| DES-034 (bounded rounds) | Default budget comes from the archetype. Leader can override. Budget enforcement is unchanged. |
| DES-035 (verifier isolation) | Role-overlap check is archetype-agnostic. Verifier isolation block includes the type field. |
| DES-036 (result artifacts) | Close gate is archetype-agnostic. Every archetype requires a result before close. |
| DES-037 (event log) | Log events include the type field for filtering. |
| Lint (lint.go) | Heuristics 1, 8, 9 are promoted to enforced rules for their respective archetypes. Remaining heuristics stay advisory. |
