# Build Plan: Teams, Roles, and Shared Identity Registry

Epic bead: ethos-cpi
Z Specification: [docs/teams.tex](teams.tex)

---

## Overview

Three deliverables:

1. **Go implementation** — Team and Role as first-class ethos concepts
2. **Shared identity repo** — `punt-labs/team` as git submodule
3. **Agent definition installer** — SessionStart deploys `.claude/agents/` from the submodule

---

## Phase 1: Go implementation — Role

Add Role as a first-class concept. Roles are global (reusable across teams).

### Storage

```text
.punt-labs/ethos/roles/<name>.yaml    # repo-local (via submodule)
~/.punt-labs/ethos/roles/<name>.yaml  # global fallback
```

### Schema

```yaml
name: coo
responsibilities:
  - execution quality and velocity
  - sub-agent delegation and review
permissions:
  - approve-merges
  - create-releases
```

### Changes

- `internal/role/` — new package: Role struct, Store (CRUD, YAML persistence)
- `cmd/ethos/role.go` — CLI subcommands: `ethos role create`, `list`, `show`, `delete`
- `internal/mcp/tools.go` — new `role` MCP tool with method dispatch
- Tests for all CRUD operations

### Delegate to: bwk

---

## Phase 2: Go implementation — Team

Add Team as a first-class concept. Teams bind identities to roles
for a set of repositories.

### Storage

```text
.punt-labs/ethos/teams/<name>.yaml    # repo-local (via submodule)
~/.punt-labs/ethos/teams/<name>.yaml  # global fallback
```

### Schema

```yaml
name: engineering
repositories:
  - punt-labs/ethos
  - punt-labs/biff
  - punt-labs/quarry
members:
  - identity: claude
    role: coo
  - identity: bwk
    role: go-specialist
  - identity: djb
    role: security-engineer
collaborations:
  - from: go-specialist
    to: coo
    type: reports_to
  - from: security-engineer
    to: coo
    type: reports_to
```

### Changes

- `internal/team/` — new package: Team struct, Member struct, Collaboration struct,
  Store (CRUD, YAML persistence), validation (referential integrity against
  identity and role stores)
- `cmd/ethos/team.go` — CLI subcommands: `ethos team create`, `list`, `show`,
  `add-member`, `remove-member`, `add-collab`, `delete`
- `internal/mcp/tools.go` — new `team` MCP tool with method dispatch
- Invariant enforcement per Z spec:
  - Every member references a valid identity handle and role name
  - Every team has at least one member
  - Collaboration roles must be filled by members
  - No self-collaboration
- Tests: table-driven, covering all invariants

### Delegate to: bwk

---

## Phase 3: Create `punt-labs/team` repo

The shared identity registry. All projects reference this via git submodule.

### Structure

```text
punt-labs/team/
  identities/
    jfreeman.yaml
    claude.yaml
    bwk.yaml
    mdm.yaml
    djb.yaml          # new — security (Bernstein)
    adt.yaml          # new — PM grounding (Turing)
    ghr.yaml          # new — PM building blocks (Hopper)
    edt.yaml          # new — UX/visual (Tufte)
    ach.yaml          # new — finance/ops (Hamilton)
    adb.yaml          # new — infra/platform (Lovelace)
  personalities/
    friendly-direct.md
    kernighan.md
    mcilroy.md
    bernstein.md       # new
    turing.md          # new
    hopper.md          # new
    tufte.md           # new
    hamilton.md        # new
    lovelace.md        # new
  writing-styles/
    direct-with-quips.md
    kernighan-prose.md
    mcilroy-prose.md
    bernstein-prose.md  # new
    turing-prose.md     # new
    hopper-prose.md     # new
    tufte-prose.md      # new
    hamilton-prose.md    # new
    lovelace-prose.md   # new
  talents/
    engineering.md
    management.md
    operations.md
    product-development.md
    security.md         # new
    formal-methods.md   # new
    ux-design.md        # new
    finance.md          # new
    infrastructure.md   # new
  roles/
    ceo.yaml
    coo.yaml
    go-specialist.yaml
    cli-specialist.yaml
    security-engineer.yaml   # new
    pm-grounding.yaml        # new
    pm-building-blocks.yaml  # new
    ux-designer.yaml         # new
    finance-ops.yaml         # new
    infra-engineer.yaml      # new
  teams/
    engineering.yaml
    website.yaml
  agents/
    bwk.md
    mdm.md
    djb.md              # new
    adt.md              # new
    ghr.md              # new
    edt.md              # new
    ach.md              # new
    adb.md              # new
  config.yaml           # default agent: claude
```

### Steps

1. Create `punt-labs/team` repo on GitHub
2. Move existing identities, personalities, writing styles, talents from
   ethos `.punt-labs/ethos/` into the team repo
3. Create 6 new agent identities with personalities, writing styles, talents
4. Create role definitions for all 10 team members
5. Create team definitions (engineering, website)
6. Create agent .md definitions for 6 new agents
7. Add `.claude/agents/` directory with all agent definitions

### Delegate to: bwk (Go files), me (identity content, agent definitions)

---

## Phase 4: Submodule integration

Wire the team repo into ethos and one pilot project.

### Changes — ethos repo

- Replace `.punt-labs/ethos/` contents with submodule:
  `git submodule add git@github.com:punt-labs/team.git .punt-labs/ethos`
- LayeredStore already reads from `.punt-labs/ethos/` — no code changes needed
- Verify: `ethos show claude`, `ethos team show engineering` work from submodule

### Changes — one pilot project (e.g., biff)

- Add submodule: `git submodule add git@github.com:punt-labs/team.git .punt-labs/ethos`
- Verify identity resolution works

---

## Phase 5: Agent definition installer

SessionStart hook installs agent definitions from the submodule into
`.claude/agents/` — same pattern as vox/biff command deployment.

### Changes

- `internal/hook/session_start.go` — after persona injection, scan
  `.punt-labs/ethos/agents/*.md` and copy any missing files to `.claude/agents/`
- Only copy if the file doesn't exist or differs (idempotent)
- Log deployed agents to stderr
- Do not overwrite user-modified agent definitions (check hash or mtime)

### Delegate to: bwk

---

## Phase 6: punt init integration

Update `punt init` to automatically add the team submodule.

### Changes — punt-kit

- `src/punt_kit/init.py` — if `punt-labs/team` submodule not present,
  offer to add it (or auto-add for punt-labs org repos)
- `src/punt_kit/audit.py` — check that submodule exists and is up to date

### Delegate to: separate punt-kit PR

---

## Sequence

```text
Phase 1 (Role)  ─────┐
Phase 2 (Team)  ─────┤──→ Phase 4 (Submodule) ──→ Phase 5 (Installer)
Phase 3 (Repo)  ─────┘                                    │
                                                           ▼
                                                    Phase 6 (punt init)
```

Phases 1-3 are independent and can be parallelized. Phase 4 depends on
all three. Phase 5 depends on 4. Phase 6 depends on 5.

---

## Verification

After all phases:

1. `ethos role list` — shows all 10 roles
2. `ethos team show engineering` — shows members with roles and collaborations
3. `ethos show claude` — resolves from submodule
4. New Claude Code session in any repo with submodule — persona injected,
   agent definitions installed
5. `ethos team show website` — different team composition, same identities
6. Add new identity → push to team repo → `git submodule update` in
   consuming repos → new agent available everywhere
