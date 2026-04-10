# MCP Integration

Connect to ethos's MCP server for structured identity operations
during a Claude Code session. Full integration for tools that
already speak MCP.

## When to Use

Your tool runs as an MCP server or Claude Code plugin and wants
to query identity, sessions, teams, roles, or missions
programmatically during a session.

## MCP Server

Ethos registers as an MCP server when running as a Claude Code
plugin. The server name is `ethos` (plugin key `self`). Tool names
follow the pattern `mcp__plugin_ethos_self__<tool>`.

## Available Tools

| Tool | Methods | What it does |
|------|---------|-------------|
| `identity` | whoami, list, get, create | Query and manage identities |
| `session` | roster, iam, join, leave | Session participant management |
| `talent` | create, list, show, delete, add, remove | Domain expertise files |
| `personality` | create, list, show, delete, set | Personality files |
| `writing_style` | create, list, show, delete, set | Communication style files |
| `ext` | get, set, del, list | Tool-scoped extensions |
| `team` | list, show, create, delete, add_member, remove_member, add_collab, for_repo | Team management |
| `role` | list, show, create, delete | Role management |
| `mission` | create, show, list, close, result, results, reflect, reflections, advance, log | Mission lifecycle |
| `adr` | create, list, show | Architecture Decision Records |
| `doctor` | *(standalone)* | Installation health |

## Example: Read Identity from MCP

```text
Call mcp__plugin_ethos_self__identity with:
  method: "get"
  handle: "claude"
```

Returns JSON with all core fields, resolved attribute content
(`writing_style_content`, `personality_content`, `talent_contents`),
and the `ext` map.

## Example: Check Active Session

```text
Call mcp__plugin_ethos_self__session with:
  method: "roster"
```

Returns the session roster with all participants, their personas,
and the parent-child tree.

## Example: Query Team for This Repo

```text
Call mcp__plugin_ethos_self__team with:
  method: "for_repo"
  repo: "punt-labs/ethos"
```

Returns the team name, members, roles, and collaboration graph.

## Example: Create a Mission

```text
Call mcp__plugin_ethos_self__mission with:
  method: "create"
  contract: |
    leader: claude
    worker: bwk
    write_set:
      - internal/hook/subagent_start.go
    success_criteria:
      - make check passes
    evaluator:
      handle: djb
    budget:
      rounds: 2
      reflection_after_each: true
```

## Example: Create an ADR

```text
Call mcp__plugin_ethos_self__adr with:
  method: "create"
  title: "Conventions over enforcement for write-sets"
  context: "Write-sets need an enforcement model"
  decision: "Conventions verified in review, not runtime sandboxes"
  status: "proposed"
```

## Writing Extensions via MCP

```text
Call mcp__plugin_ethos_self__ext with:
  method: "set"
  handle: "claude"
  namespace: "my-tool"
  key: "preference"
  value: "dark-mode"
```

## Degradation

MCP tools are only available when ethos is installed as a Claude
Code plugin and the session is active. If your tool also supports
CLI or filesystem access, fall back to those when MCP is unavailable.

```python
def get_identity(handle: str) -> dict:
    # Try MCP first (if in a Claude Code session)
    try:
        result = call_mcp("mcp__plugin_ethos_self__identity",
                         method="get", handle=handle)
        return result
    except MCPNotAvailable:
        pass
    # Fall back to CLI
    result = subprocess.run(
        ["ethos", "identity", "get", handle, "--json"],
        capture_output=True, text=True
    )
    if result.returncode == 0:
        return json.loads(result.stdout)
    # Fall back to filesystem
    return load_ethos_identity(handle)  # from filesystem.md
```
