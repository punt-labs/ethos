# Team Setup Guide

How to create and structure an ethos team from scratch. This guide is
for any organization using ethos, not specific to Punt Labs.

**Prerequisite:** Install ethos first -- see [Quick Start](../README.md#quick-start).

## Overview

Ethos resolves identity data through three layers, in order:
repo-local `.punt-labs/ethos/` (override), the active bundle, and
global `~/.punt-labs/ethos/`. Each layer holds the same subdirectories
— identities, personalities, writing styles, talents, roles, and teams.
Three distribution options fit three user profiles:

1. **Bundle (preferred for starter teams)** — a self-contained team
   directory activated per repo. Gstack ships embedded in ethos and
   deploys on `ethos seed`. Activate with
   `ethos team activate gstack`. Add your own with
   `ethos team add-bundle <git-url>`.
2. **Repo-local files (for bespoke teams)** — author YAML directly
   under `.punt-labs/ethos/` in the repo. Best when the team lives
   alongside the code and nowhere else.
3. **Submodule (legacy)** — share `.punt-labs/ethos/` across repos as
   a git submodule of a team registry. Still supported. New users
   should prefer bundles; `ethos team migrate` converts a legacy
   submodule to the bundles layout.

## Directory Structure

```text
.punt-labs/ethos/
  identities/
    alice.yaml
    bob.yaml
    code-reviewer.yaml      # agent identity
  personalities/
    principal-engineer.md
    friendly-direct.md
  writing-styles/
    concise-quantified.md
    direct-with-quips.md
  talents/
    engineering.md
    security.md
    product-management.md
  roles/
    tech-lead.yaml
    backend-engineer.yaml
    security-reviewer.yaml
  teams/
    engineering.yaml
  agents/
    code-reviewer.md         # optional: static agent definitions
```

All files are git-tracked. The directory can live directly in each repo
or be shared as a submodule (see [Sharing Across Repos](#sharing-across-repos)).

## Step 1: Create Identities

Each identity is a YAML file in `identities/`. One file per person or
agent.

**Human identity:**

```yaml
# identities/alice.yaml
name: Alice Chen
handle: alice
kind: human
email: alice@example.com
github: alicechen
personality: principal-engineer
writing_style: concise-quantified
talents:
  - engineering
  - security
```

**Agent identity:**

```yaml
# identities/code-reviewer.yaml
name: Code Reviewer
handle: code-reviewer
kind: agent
personality: principal-engineer
writing_style: concise-quantified
talents:
  - engineering
  - security
```

The handle must start and end with a lowercase letter or digit, and may
contain lowercase letters, digits, and hyphens in the middle
(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`).
Agent handles must exactly match the `subagent_type` string in Claude
Code for auto-matching to work (case-sensitive).

Fields reference attributes by slug — the filename without extension.
`personality: principal-engineer` resolves to
`personalities/principal-engineer.md`.

## Step 2: Create Attributes

Attributes are plain markdown files. No required frontmatter.

**Personality** — how the agent thinks and decides:

```markdown
# Principal Engineer

Direct, accountable, evidence-driven. Root causes are provable —
present facts, data, and tests, not "likely" theories.

## Decision-making

- Replace adjectives with data: "much faster" → "3x faster"
- Every statement must pass the "so what" test
- Say "I don't know" when uncertain
```

**Writing style** — how the agent communicates:

```markdown
# Concise and Quantified

Short sentences, under 30 words. Lead with the answer, not the
reasoning. No performative validation, no weasel words.
```

**Talent** — what the agent knows or can do:

```markdown
# Security

Code review for OWASP top 10, dependency auditing, threat modeling.
Emphasis on input validation at system boundaries.
```

Multiple identities can share the same attribute files.

## Step 3: Create Roles

Roles define responsibilities and tool permissions. Used by teams
and agent file generation.

```yaml
# roles/backend-engineer.yaml
name: backend-engineer
model: sonnet
responsibilities:
  - Implement backend services and APIs
  - Write tests with full coverage
tools:
  - Read
  - Write
  - Edit
  - Bash
  - Grep
  - Glob
```

The `tools` field determines which Claude Code tools the agent can
use when ethos generates agent definition files (DES-026).

To create a role with all fields including `tools`, write the YAML file
and use `ethos role create -f roles/backend-engineer.yaml`. The
interactive `ethos role create` prompts for `responsibilities` and
`permissions` only.

## Step 4: Create Teams

Teams bind identities to roles for specific repositories.

```yaml
# teams/engineering.yaml
name: engineering
repositories:
  - myorg/backend
  - myorg/frontend
members:
  - identity: alice
    role: tech-lead
  - identity: bob
    role: backend-engineer
  - identity: code-reviewer
    role: security-reviewer
collaborations:
  - from: backend-engineer
    to: tech-lead
    type: reports_to
  - from: security-reviewer
    to: tech-lead
    type: reports_to
```

Referential integrity is enforced: every member must reference a valid
identity handle and role name. Every team must have at least one member.
Collaboration roles must be filled by team members.

## Step 5: Configure the Repo

Create `.punt-labs/ethos.yaml` at the repo root:

```yaml
agent: code-reviewer     # handle of the primary agent identity
team: engineering        # team name for hook context
```

This tells ethos which agent persona to use for the primary Claude
Code session and which team context to inject.

## Sharing Across Repos

For teams that work across multiple repositories, two options exist.

### Option A: Bundle (recommended)

Package the team as a bundle — a directory with a `bundle.yaml`
manifest and the standard subdirectories (`identities/`, `teams/`,
etc.). Publish it as a git repo and add it to consumers:

```bash
ethos team add-bundle git@github.com:myorg/team.git --name myorg --apply
ethos team activate myorg
```

`add-bundle` submodules the bundle under
`.punt-labs/ethos-bundles/<name>/` by default; `--global` clones it
to `~/.punt-labs/ethos/bundles/<name>/` instead. Activation is
per-repo via `active_bundle` in `.punt-labs/ethos.yaml`.

### Option B: Legacy submodule at `.punt-labs/ethos/`

The original sharing mechanism. Still supported. Extract the
`.punt-labs/ethos/` directory into its own git repo and add it as a
submodule in each project:

```bash
# Create the team repo
mkdir my-team
cd my-team
git init
# ... add identities, personalities, writing-styles, talents, roles, teams
git add .
git commit -m "Initial team setup"
git remote add origin git@github.com:myorg/team.git
git push -u origin main

# Add to each project
cd /path/to/project
git submodule add git@github.com:myorg/team.git .punt-labs/ethos
git add .punt-labs/ethos .gitmodules
git commit -m "chore: add team submodule"
```

When cloning a repo with the submodule:

```bash
git clone --recurse-submodules git@github.com:myorg/project.git
```

If already cloned without submodules:

```bash
git submodule init
git submodule update
```

To pull latest team data:

```bash
git -C .punt-labs/ethos fetch origin
git -C .punt-labs/ethos checkout origin/main
git add .punt-labs/ethos
git commit -m "chore: update team submodule"
```

The `.punt-labs/ethos.yaml` config file lives in each project (not in
the submodule) because `agent` and `team` bindings are repo-specific.

To move a legacy submodule to the bundles layout, run
`ethos team migrate` (dry-run by default; `--apply` to execute).

## What Happens Automatically

Once configured, ethos hooks handle the rest:

1. **SessionStart** — resolves the human (from git config) and agent
   (from ethos.yaml), creates a session roster, injects the full
   persona block (personality, writing style, talents, team context)
   into the session, and generates `.claude/agents/<handle>.md` files
   from identity data.

2. **PreCompact** — re-injects the persona block before context
   compression so behavioral instructions survive long sessions.

3. **SubagentStart** — auto-matches subagent types to identity handles,
   injects the matched persona and any extension session contexts for
   that identity.

## Extension Session Context

Any tool can provide session-scoped instructions by setting a
`session_context` key in its extension YAML. Ethos emits all
session contexts at session start and before compaction.

```bash
ethos ext set my-agent my-tool session_context "Your session context instructions here"
```

This is how tools like quarry (memory), beadle (email), and biff
(messaging) inject their own behavioral context without requiring
ethos-side code changes. See DES-022 in
[DESIGN.md](../DESIGN.md#des-022-extension-provided-session-context-settled).

## Verification

After setup, verify everything works:

```bash
ethos doctor                       # Check installation health
ethos whoami                       # Should resolve your identity
ethos identity list                # Should show all team identities
ethos team show engineering        # Should show members and roles
ethos role show backend-engineer   # Should show responsibilities and tools
```

Start a Claude Code session — the persona block should appear in the
session context, and `.claude/agents/` files should be generated.
