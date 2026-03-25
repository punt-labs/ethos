---
description: Manage session roster — roster, join, leave, iam
argument-hint: "roster|join|leave|iam [args]"
allowed-tools: ["mcp__plugin_ethos_self__session"]
---
<!-- markdownlint-disable MD041 -->

Manage session roster via `mcp__plugin_ethos_self__session`.

## Usage

- `/ethos:session` — show current session roster (default: roster)
- `/ethos:session roster` — show current session roster
- `/ethos:session join <agent_id>` — add a participant (optional: persona, parent, agent_type)
- `/ethos:session leave <agent_id>` — remove a participant
- `/ethos:session iam <persona>` — declare persona in current session

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method.

If no argument is provided, default to `roster`.
