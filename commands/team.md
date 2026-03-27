---
description: Manage teams — list, show, create, delete, members, collaborations
argument-hint: "list|show|create|delete|add-member|remove-member|add-collab|for-repo [args]"
allowed-tools: ["mcp__plugin_ethos_self__team"]
---
<!-- markdownlint-disable MD041 -->

Manage teams via `mcp__plugin_ethos_self__team`.

## Usage

- `/ethos:team` — list all teams (default)
- `/ethos:team list` — list all teams
- `/ethos:team show <name>` — show team details
- `/ethos:team create <name>` — create a team (prompt for members, repositories)
- `/ethos:team delete <name>` — delete a team
- `/ethos:team add-member <team> <identity> <role>` — add a member to a team
- `/ethos:team remove-member <team> <identity> <role>` — remove a member from a team
- `/ethos:team add-collab <team> <from> <to> <type>` — add a collaboration (reports_to, collaborates_with, delegates_to)
- `/ethos:team for-repo [repo]` — show team(s) for a repository (defaults to current repo)

Parse $ARGUMENTS to determine the `method` and remaining parameters. The first word is the method. Map hyphenated methods to underscored MCP method names (e.g. `add-member` becomes `add_member`, `for-repo` becomes `for_repo`).

If no argument is provided, default to `list`.
