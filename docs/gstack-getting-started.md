# Gstack Starter Team

A ready-made team of 6 agents ported from the gstack builder
framework. Ship with ethos v3.6.0.

## Philosophy

Three principles from gstack's ETHOS.md:

- **Boil the Lake** -- understand the full problem before proposing
  solutions. No partial fixes.
- **Search Before Building** -- exhaust existing solutions before
  writing new code.
- **User Sovereignty** -- the user decides. AI recommends, user
  approves.

## Agents

| Handle | Role | Description |
|--------|------|-------------|
| `gstack-architect` | architect | System design, architecture lock, component diagrams |
| `gstack-implementer` | implementer | Code generation, bug fixes, feature implementation |
| `gstack-reviewer` | reviewer | Code review with severity levels, pre-landing checks |
| `gstack-qa` | qa-engineer | QA testing, golden path verification, regression tests |
| `gstack-security` | security-reviewer | OWASP Top 10, STRIDE analysis, secrets audit |
| `gstack-product` | product-lead | Scope review, 10-star vision, opportunity cost analysis |

The architect is the hub -- implementer, reviewer, qa, and security
report to architect. Product collaborates with architect.

## Pipelines

5 pipeline templates compose the agents into multi-stage workflows:

| Pipeline | Stages | Purpose |
|----------|--------|---------|
| `gstack-plan` | validate, scope, architecture, design-review | Idea validation to architecture lock |
| `gstack-ship` | review, test, security, ship, document | Code review to released and documented |
| `gstack-design` | system, explore, audit, implement | Design system to production HTML/CSS |
| `gstack-debug` | investigate, fix, verify | Root cause investigation to verified fix |
| `gstack-review` | ceo-review, design-audit, eng-review, devex-review | Multi-perspective review pipeline |

## Quick Start

### 1. Configure your repo

```yaml
# .punt-labs/ethos.yaml
agent: gstack-architect
team: gstack
```

### 2. Instantiate a pipeline

```bash
ethos mission pipeline instantiate gstack-debug \
  --var target=internal/session/ \
  --leader gstack-architect \
  --worker gstack-implementer \
  --evaluator gstack-reviewer
```

This creates 3 missions (one per stage) with dependency ordering.
Use `--dry-run` to preview without creating.

### 3. Run a single mission

For one-off work without a pipeline:

```bash
ethos mission dispatch \
  --worker gstack-implementer \
  --evaluator gstack-reviewer \
  --write-set "internal/session/store.go,internal/session/store_test.go" \
  --criteria "session purge removes stale entries,test covers TTL edge case"
```

### 4. List and track

```bash
ethos mission list                    # All missions
ethos mission list --pipeline <id>    # Pipeline missions in stage order
ethos mission show <id>               # Contract, results, reflections
ethos mission log <id>                # Event audit trail
```

## Customizing

### Override personalities

The gstack agents use personalities from
`.punt-labs/ethos/personalities/gstack-*.md`. To change an agent's
personality, edit its identity YAML:

```yaml
# .punt-labs/ethos/identities/gstack-implementer.yaml
personality: kernighan    # Use a different personality
```

### Add agents

Create a new identity YAML and add it to the team:

```bash
ethos identity create -f my-agent.yaml
ethos team add-member gstack --identity my-agent --role implementer
```

### Create custom pipelines

Pipeline templates are YAML files in
`internal/seed/sidecar/pipelines/`. Copy an existing template and
modify the stages:

```bash
ethos mission pipeline show gstack-debug    # See the template
```

Each stage references an archetype (implement, design, review, test,
report, task, inbox, investigate, audit, orchestrate) that defines
default constraints. See
[Archetypes and pipelines](archetypes-and-pipelines.md) for the full
reference.
