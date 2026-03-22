---
description: Manage writing styles — create, list, show, delete, set on identity
argument-hint: "create|list|show|delete|set [args]"
allowed-tools: ["mcp__plugin_ethos-dev_self__writing_style"]
---
<!-- markdownlint-disable MD041 -->

# /writing-style

Manage writing styles via the `writing_style` MCP tool.

## Usage

- `/writing-style list` — list all writing styles
- `/writing-style show <slug>` — show writing style content
- `/writing-style create <slug>` — create a new writing style (prompt for content)
- `/writing-style delete <slug>` — delete a writing style
- `/writing-style set <handle> <slug>` — set writing style on an identity

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method.
