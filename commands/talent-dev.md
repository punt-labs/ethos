---
description: Manage talents — create, list, show, delete, add to identity, remove from identity
argument-hint: "create|list|show|delete|add|remove [args]"
allowed-tools: ["mcp__plugin_ethos-dev_self__talent"]
---
<!-- markdownlint-disable MD041 -->

Manage talents via `mcp__plugin_ethos-dev_self__talent`.

## Usage

- `/ethos-dev:talent list` — list all talents
- `/ethos-dev:talent show <slug>` — show talent content
- `/ethos-dev:talent create <slug>` — create a new talent (prompt for content)
- `/ethos-dev:talent delete <slug>` — delete a talent
- `/ethos-dev:talent add <handle> <slug>` — add talent to an identity
- `/ethos-dev:talent remove <handle> <slug>` — remove talent from an identity

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method.
