# Gstack Starter Team

A ready-made team of 6 agents ported from the gstack builder
framework. Ships with ethos as an embedded team bundle; no submodule
required.

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

### 1. Install ethos

The gstack bundle ships with ethos. Running `ethos seed` (done
automatically by the installer) deploys it to
`~/.punt-labs/ethos/bundles/gstack/`.

### 2. Activate the bundle in your repo

```bash
cd my-project
ethos team activate gstack
```

This writes `active_bundle: gstack` to `.punt-labs/ethos.yaml`.

### 3. Verify

```bash
ethos team active                # gstack
ethos team available             # shows gstack row marked *
ethos team show gstack           # shows members
```

### 4. Instantiate a pipeline

```bash
ethos mission pipeline instantiate gstack-debug \
  --leader gstack-architect \
  --worker gstack-implementer \
  --evaluator gstack-reviewer \
  --var target=internal/session/
```

This creates 3 missions (one per stage) with dependency ordering.
Use `--dry-run` to preview without creating.

### 5. Run a single mission

For one-off work without a pipeline:

```bash
ethos mission dispatch \
  --worker gstack-implementer \
  --evaluator gstack-reviewer \
  --write-set "internal/session/store.go,internal/session/store_test.go" \
  --criteria "session purge removes stale entries,test covers TTL edge case"
```

### 6. List and track

```bash
ethos mission list                    # All missions
ethos mission list --pipeline <id>    # Pipeline missions in stage order
ethos mission show <id>               # Contract, results, reflections
ethos mission log <id>                # Event audit trail
```

## Customizing

### Override personalities

The gstack agents use personalities from
`~/.punt-labs/ethos/bundles/gstack/personalities/gstack-*.md`. Treat
bundle content like vendored defaults: you can edit seeded files in
place if needed, and reseeding preserves those edits unless you pass
`--force`. For routine customization, prefer overriding in your
repo-local or global layer (the three-layer resolver picks up shadows
automatically):

```yaml
# .punt-labs/ethos/identities/gstack-implementer.yaml
personality: kernighan    # Use a different personality
```

The repo-local copy shadows the bundle identity with the same handle.

### Add agents

Create a new identity YAML and add it to the team:

```bash
ethos identity create -f my-agent.yaml
ethos team add-member gstack my-agent implementer
```

### Create custom pipelines

The gstack pipeline templates ship inside the bundle at
`~/.punt-labs/ethos/bundles/gstack/pipelines/`. Copy one into your
writable layer (`~/.punt-labs/ethos/pipelines/` or the repo-local
`.punt-labs/ethos/pipelines/`) and modify the stages:

```bash
ethos mission pipeline show gstack-debug    # See the template
```

Each stage references an archetype (implement, design, review, test,
report, task, inbox, investigate, audit, orchestrate) that defines
default constraints. See
[Archetypes and pipelines](archetypes-and-pipelines.md) for the full
reference.
