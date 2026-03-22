---
description: Manage writing styles — create, list, show, delete, set on identity
argument-hint: "create|list|show|delete|set [args]"
allowed-tools: ["mcp__plugin_ethos_self__writing_style"]
---
<!-- markdownlint-disable MD041 -->

Manage writing styles via `mcp__plugin_ethos_self__writing_style`.

## Usage

- `/ethos:writing-style list` — list all writing styles
- `/ethos:writing-style show <slug>` — show writing style content
- `/ethos:writing-style create <slug>` — create a new writing style (prompt for content)
- `/ethos:writing-style delete <slug>` — delete a writing style
- `/ethos:writing-style set <handle> <slug>` — set writing style on an identity

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method.
