---
description: Manage roles — list, show, create, delete
argument-hint: "list|show|create|delete [args]"
allowed-tools: ["mcp__plugin_ethos-dev_self__role"]
---
<!-- markdownlint-disable MD041 -->

Manage roles via `mcp__plugin_ethos-dev_self__role`.

## Usage

- `/ethos-dev:role-dev` — list all roles (default)
- `/ethos-dev:role-dev list` — list all roles
- `/ethos-dev:role-dev show <name>` — show role details
- `/ethos-dev:role-dev create <name>` — create a role (prompt for responsibilities, permissions)
- `/ethos-dev:role-dev delete <name>` — delete a role

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method.

If no argument is provided, default to `list`.
