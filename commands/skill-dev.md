---
description: Manage skills
argument-hint: "create|list|show|delete|add|remove [args]"
allowed-tools: ["mcp__plugin_ethos-dev_self__skill"]
---

# /skill-dev

Manage skills via `mcp__plugin_ethos-dev_self__skill`.

## Usage

- `/skill list` — list all skills
- `/skill show <slug>` — show skill content
- `/skill create <slug>` — create a new skill (prompt for content)
- `/skill delete <slug>` — delete a skill
- `/skill add <handle> <slug>` — add skill to an identity
- `/skill remove <handle> <slug>` — remove skill from an identity

Parse $ARGUMENTS to determine the method and parameters. The first word is the method.
