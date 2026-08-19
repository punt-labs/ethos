---
description: Manage tool-scoped extensions on identities — get, set, del, list
argument-hint: "get|set|del|list [args]"
allowed-tools: ["mcp__plugin_ethos_self__ext"]
---
<!-- markdownlint-disable MD041 -->

Manage tool-scoped extensions via `mcp__plugin_ethos_self__ext`.

## Usage

- `/ethos:ext list <handle>` — list all extension namespaces
- `/ethos:ext get <handle> <namespace> [key]` — read extension key(s)
- `/ethos:ext set <handle> <namespace> <key> <value>` — write an extension key
- `/ethos:ext del <handle> <namespace> [key]` — delete a key or namespace

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method.
