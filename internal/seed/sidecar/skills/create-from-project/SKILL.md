# Create Identity from Project Context

Create an ethos identity by fine-tuning starter content from your
existing project files. 90% batteries-included defaults, 10%
LLM-inferred personalization from what you already have.

## When to Use

Run this when you've just installed ethos and want to create your
first identity without starting from scratch. The skill reads your
project context (CLAUDE.md, .claude/agents/, git config) and
proposes identity attributes that match your existing preferences.

## Workflow

### Step 1: Gather Context

Read the following files if they exist:

1. **CLAUDE.md** in the project root — communication preferences,
   coding conventions, tool usage rules
2. **.claude/agents/*.md** — existing agent definitions with tool
   restrictions and behavioral instructions
3. **git config** — `user.name` and `user.email` via
   `git config user.name` and `git config user.email`

If none of these exist, fall back to the interactive wizard:
`ethos create`.

### Step 2: Infer Identity Attributes

From the gathered context, propose:

- **Name**: from git config `user.name`
- **Handle**: slugified from name, or git config username
- **Email**: from git config `user.email`
- **Kind**: "human" (this skill creates the user's identity, not
  an agent's)
- **Writing style**: infer from CLAUDE.md communication rules.
  Match against available starter styles:
  - Terse/direct/no-sycophancy → `concise-quantified`
  - Technical/precise → `precise-writer`
  - Measured/balanced → `measured-prose`
  - If no clear match, default to `concise-quantified`
- **Personality**: infer from CLAUDE.md engineering principles.
  Default to `principal-engineer` for most developers.
- **Talents**: infer from project language and tools.
  Go project → include `engineering`
  Python project → include `engineering`
  Multiple languages → include `engineering`

### Step 3: Present Proposals

Show the proposed identity to the user:

```text
Based on your project context, I'd create this identity:

  Name:          Jim Freeman
  Handle:        jfreeman
  Email:         jim@example.com
  Kind:          human
  Writing style: concise-quantified
                 (inferred from: "Keep sentences under 30 words"
                  in CLAUDE.md)
  Personality:   principal-engineer
                 (inferred from: "You are a principal engineer"
                  in CLAUDE.md)
  Talents:       engineering

Accept this identity, or adjust any field?
```

Use AskUserQuestion to confirm. The user can accept as-is or
adjust individual fields.

### Step 4: Create the Identity

Call `ethos create -f <temp-yaml>` with the confirmed attributes.
Or use the MCP `identity` tool with `method: create`.

Report what was created and suggest next steps:

```text
Created identity: jfreeman (human)
  Writing style: concise-quantified
  Personality: principal-engineer

Next steps:
  - Create agent personas: ethos create (with kind: agent)
  - Configure your repo: add agent: <handle> to .punt-labs/ethos.yaml
  - Restart Claude Code to activate
```

## What This Does NOT Do

- Does NOT extract a full identity from CLAUDE.md — most CLAUDE.md
  files don't have enough personal content for that
- Does NOT modify CLAUDE.md — it reads it as input only
- Does NOT create agent identities — use `ethos create` or
  `ethos import --from soulspec` for agents
- Does NOT require CLAUDE.md to exist — falls back to the
  interactive wizard
