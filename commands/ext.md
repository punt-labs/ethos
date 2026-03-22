---
description: Manage tool-scoped extensions on identities — get, set, del, list
argument-hint: "get|set|del|list [args]"
allowed-tools: ["mcp__plugin_ethos_self__ext"]
---
<!-- markdownlint-disable MD041 -->

Manage tool-scoped extensions via `mcp__plugin_ethos_self__ext`.

## Usage

- `/ext list <persona>` — list all extension namespaces
- `/ext get <persona> <namespace> [key]` — read extension key(s)
- `/ext set <persona> <namespace> <key> <value>` — write an extension key
- `/ext del <persona> <namespace> [key]` — delete a key or namespace

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method.
