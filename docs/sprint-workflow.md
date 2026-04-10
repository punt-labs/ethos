# Sprint Workflow

How to run a structured development sprint using ethos missions.
Each phase is a mission with the appropriate
[archetype](templates/README.md). The team template provides 6
personas with distinct expertise and role-based tool restrictions.

## Setup

```bash
ethos seed --force    # deploys sprint team + roles + personalities
```

Configure your repo:

```yaml
# .punt-labs/ethos.yaml
agent: claude
team: sprint
```

## The Sprint Sequence

### 1. Think (product-thinker, design archetype)

Before any code, the product thinker forces six questions:
who is the user, what is the job to be done, what does success
look like, what are we not building, what is the riskiest
assumption, and how will we know we're wrong.

Mission: broad write-set (`docs/`), open criteria. The output
is a problem statement and success criteria, not a spec.

### 2. Plan (sprint-architect, design archetype)

The architect translates the product thinker's output into
technical structure: component boundaries, data flow, edge cases,
dependencies. The output is a spec with testable assertions.

Mission: broad write-set (`docs/`), criteria include "produce a
spec with at least 3 testable assertions per component."

### 3. Build (sprint-implementer, implement archetype)

The implementer writes code within the architect's spec. Tight
write-set (specific files from the spec). Specific criteria
(make check passes, tests cover the spec's assertions).

Mission: tight write-set, 1-2 rounds. If the spec is wrong,
the implementer submits `verdict: escalate` with `open_questions`
explaining the gap.

### 4. Review (sprint-reviewer, review archetype)

The reviewer reads the implementer's diff and reports findings.
Empty write-set — read-only. Each finding has severity, location,
confidence, and a suggested fix.

Mission: empty write-set, 1 round. Findings feed back to the
implementer as a fix spec.

### 5. Test (sprint-qa, implement archetype on test files)

The QA engineer writes regression tests, runs the full suite,
and verifies edge cases from the spec. Write-set limited to
test files only.

Mission: write-set = test files, criteria = all spec assertions
have corresponding tests, make check passes.

### 6. Security (sprint-security, review archetype)

The security auditor reviews the final code for vulnerabilities.
OWASP top 10, trust boundaries, credential handling, dependency
audit. Read-only.

Mission: empty write-set, 1 round. Critical findings block merge.

### 7. Ship

The leader reviews all mission results, merges the PR, and closes
the beads. The mission event logs provide the full audit trail.

## Mission Event Log

After the sprint, `ethos mission log <id>` for each mission
reconstructs what happened:

```text
2026-04-10 08:00 PDT  create   by claude  worker=sprint-architect
2026-04-10 08:15 PDT  result   by sprint-architect  round=1 verdict=pass
2026-04-10 08:16 PDT  reflect  by claude  round=1 rec=continue
2026-04-10 08:16 PDT  close    by claude  status=closed
```

## Adapting the Sequence

Not every sprint needs all 6 phases. A bug fix might skip Think
and Plan, going straight to Build → Review → Ship. A design
exploration might be Think → Plan only, with no implementation.
The archetypes and team are building blocks, not a rigid pipeline.
