---
description: Manage writing styles
argument-hint: "create|list|show|delete|set [args]"
allowed-tools: ["mcp__plugin_ethos-dev_self__writing_style"]
---

# /writing-style-dev

Manage writing styles via `mcp__plugin_ethos-dev_self__writing_style`.

## Usage

- `/writing-style-dev list` — list all writing styles
- `/writing-style-dev show <slug>` — show writing style content
- `/writing-style-dev create <slug>` — create a new writing style (prompt for content)
- `/writing-style-dev delete <slug>` — delete a writing style
- `/writing-style-dev set <handle> <slug>` — set writing style on an identity

Parse $ARGUMENTS to determine the method and parameters. The first word is the method.
