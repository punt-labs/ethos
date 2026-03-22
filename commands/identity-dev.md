---
description: Manage identities — whoami, list, get, create, iam
argument-hint: "whoami|list|get|create|iam [args]"
allowed-tools: ["mcp__plugin_ethos-dev_self__identity"]
---
<!-- markdownlint-disable MD041 -->

Manage identities via `mcp__plugin_ethos-dev_self__identity`.

## Usage

- `/ethos:identity whoami` — show the caller's identity
- `/ethos:identity list` — list all identities with active session markers
- `/ethos:identity get <handle>` — show full details of an identity
- `/ethos:identity create` — create a new identity (prompt for fields)
- `/ethos:identity iam <persona>` — declare persona in current session

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method.

If no argument is provided, default to `whoami`.
