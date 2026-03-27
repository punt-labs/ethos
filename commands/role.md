---
description: Manage roles — list, show, create, delete
argument-hint: "list|show|create|delete [args]"
allowed-tools: ["mcp__plugin_ethos_self__role"]
---
<!-- markdownlint-disable MD041 -->

Manage roles via `mcp__plugin_ethos_self__role`.

## Usage

- `/ethos:role` — list all roles (default)
- `/ethos:role list` — list all roles
- `/ethos:role show <name>` — show role details
- `/ethos:role create <name>` — create a role (prompt for responsibilities, permissions)
- `/ethos:role delete <name>` — delete a role

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method.

If no argument is provided, default to `list`.
