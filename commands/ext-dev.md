---
description: Manage tool-scoped extensions
argument-hint: "get|set|del|list [args]"
allowed-tools: ["mcp__plugin_ethos-dev_self__ext"]
---

# /ext-dev

Manage tool-scoped extensions via `mcp__plugin_ethos-dev_self__ext`.

## Usage

- `/ext list <persona>` — list all extension namespaces
- `/ext get <persona> <namespace> [key]` — read extension key(s)
- `/ext set <persona> <namespace> <key> <value>` — write an extension key
- `/ext del <persona> <namespace> [key]` — delete a key or namespace

Parse $ARGUMENTS to determine the method and parameters. The first word is the method.
