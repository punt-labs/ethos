---
description: Manage personalities
argument-hint: "create|list|show|delete|set [args]"
allowed-tools: ["mcp__plugin_ethos_self__personality"]
---

# /personality

Manage personalities via `mcp__plugin_ethos_self__personality`.

## Usage

- `/personality list` — list all personalities
- `/personality show <slug>` — show personality content
- `/personality create <slug>` — create a new personality (prompt for content)
- `/personality delete <slug>` — delete a personality
- `/personality set <handle> <slug>` — set personality on an identity

Parse $ARGUMENTS to determine the method and parameters. The first word is the method.
