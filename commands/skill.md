---
description: Manage skills — create, list, show, delete, add to identity, remove from identity
argument-hint: "create|list|show|delete|add|remove [args]"
allowed-tools: ["mcp__plugin_ethos_self__skill"]
---
<!-- markdownlint-disable MD041 -->

# /skill

Manage skills via the `skill` MCP tool.

## Usage

- `/skill list` — list all skills
- `/skill show <slug>` — show skill content
- `/skill create <slug>` — create a new skill (prompt for content)
- `/skill delete <slug>` — delete a skill
- `/skill add <handle> <slug>` — add skill to an identity
- `/skill remove <handle> <slug>` — remove skill from an identity

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method.
