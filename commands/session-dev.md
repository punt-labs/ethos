---
description: Manage session roster — roster, join, leave
argument-hint: "roster|join|leave [args]"
allowed-tools: ["mcp__plugin_ethos-dev_self__session"]
---
<!-- markdownlint-disable MD041 -->

Manage session roster via `mcp__plugin_ethos-dev_self__session`.

## Usage

- `/ethos:session` — show current session roster (default: roster)
- `/ethos:session roster` — show current session roster

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method.

If no argument is provided, default to `roster`.
