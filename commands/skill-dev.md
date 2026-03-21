---
description: Manage skills
argument-hint: "create|list|show|delete|add|remove [args]"
allowed-tools: ["mcp__plugin_ethos-dev_self__skill"]
---

# /skill-dev

Manage skills via `mcp__plugin_ethos-dev_self__skill`.

## Usage

- `/skill-dev list` — list all skills
- `/skill-dev show <slug>` — show skill content
- `/skill-dev create <slug>` — create a new skill (prompt for content)
- `/skill-dev delete <slug>` — delete a skill
- `/skill-dev add <handle> <slug>` — add skill to an identity
- `/skill-dev remove <handle> <slug>` — remove skill from an identity

Parse $ARGUMENTS to determine the method and parameters. The first word is the method.
