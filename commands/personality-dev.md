---
description: Manage personalities
argument-hint: "create|list|show|delete|set [args]"
allowed-tools: ["mcp__plugin_ethos-dev_self__personality"]
---

# /personality-dev

Manage personalities via `mcp__plugin_ethos-dev_self__personality`.

## Usage

- `/personality-dev list` — list all personalities
- `/personality-dev show <slug>` — show personality content
- `/personality-dev create <slug>` — create a new personality (prompt for content)
- `/personality-dev delete <slug>` — delete a personality
- `/personality-dev set <handle> <slug>` — set personality on an identity

Parse $ARGUMENTS to determine the method and parameters. The first word is the method.
