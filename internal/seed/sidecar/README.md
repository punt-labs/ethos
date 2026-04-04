# Ethos Identity Store

This directory is managed by [ethos](https://github.com/punt-labs/ethos),
the identity binding for humans and AI agents. Other tools (Vox, Beadle,
Biff) read from this directory — they do not import ethos. The file layout
is the contract.

## Directory Structure

```text
~/.punt-labs/ethos/
  README.md                       # this file
  identities/                     # one YAML file per identity
    <handle>.yaml                 # core identity fields
    <handle>.ext/                 # tool-scoped extensions (key-value YAML per tool)
  talents/                         # shared talent definitions (markdown)
  personalities/                  # shared personality definitions (markdown)
  writing-styles/                 # shared writing style definitions (markdown)
  roles/                          # role definitions (YAML)
  skills/                         # Claude Code skills (e.g., baseline-ops)
  sessions/                       # session rosters (ephemeral, auto-managed)
```

## Identity YAML

```yaml
name: Jim Freeman
handle: jfreeman
kind: human                       # or "agent"
email: jim@punt-labs.com
github: jmf-pobox
agent: .claude/agents/jfreeman.md
writing_style: concise-quantified
personality: principal-engineer
talents:
  - executive
  - software-engineering
```

## Path Resolution

Attributes (`writing_style`, `personality`, `talents`) are slugs that
reference `.md` files in the corresponding directories. The `agent` field
is the exception — it resolves relative to the repo root because agent
`.md` files live in the project, not in the ethos store.

## Extensions

Tool-scoped key-value data in `<handle>.ext/<namespace>.yaml`. Each tool
owns its namespace. Ethos never reads or interprets extension contents.

## Attribute Files

Talents, personalities, and writing styles are plain markdown files. No
required frontmatter. Multiple identities can reference the same file.

## Sessions

Ephemeral rosters tracking participants, personas, and relationships.
Created on session start, deleted on session end.

## Consuming This Data

Any tool can read these files directly without importing ethos. The file
layout is the sidecar contract. For programmatic access: `ethos show`,
`ethos whoami`, or the MCP server (`ethos serve`).

See [DESIGN.md](https://github.com/punt-labs/ethos/blob/main/DESIGN.md)
for the full specification.
