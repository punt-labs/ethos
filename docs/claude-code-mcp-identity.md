# MCP identity under Claude Code

Claude Code gives an MCP server no session or agent identity: the server's
environment carries no `CLAUDE_PID`, and the MCP request names no caller. Ethos
therefore learns the caller from its own PreToolUse hook (DES-074, amendment
2026-11-03).

## How it works

Before every call to an ethos MCP tool (`mcp__plugin_ethos_self__*` and
`mcp__plugin_ethos-dev_self__*`), `ethos hook pre-tool-use` returns
`updatedInput`: the original arguments plus one reserved argument, `_caller`.

```json
{"_caller": {"session_id": "<hook payload session_id>", "agent_id": "<key>"}}
```

`agent_id` is the payload's `agent_id` when a subagent is calling, so a subagent
resolves to its own persona and not its leader's. For the leader it is the
hook's own corroborated `CLAUDE_PID`, the key `SessionStart` wrote for the
primary participant. The hook overwrites any `_caller` the model supplied. It
returns no `permissionDecision`, so permission rules apply as they would
without the hook. If the payload has no `session_id`, or no agent key can be
determined, the hook declares no caller and removes any `_caller` the model
supplied, so the server sees none.

## What the server does with it

`identity whoami`, `session roster` without a `session_id`, `session iam`,
mission binding (`mission create`, `close`, `abandon`), and the Human identity
row of `doctor` resolve the caller from `_caller` through the same
exact-participant path as the OpenCode pilot (`docs/opencode-mcp-identity.md`):
the roster for `session_id` must exist and hold exactly one participant with
that `agent_id`. Otherwise the call fails with a named error. The server never
falls back to the environment, the process tree, git, or the OS user.

- `session_id` and `agent_id` must each be nonempty and contain only ASCII
  letters, digits, underscores, or hyphens. Any other key, type, or value is an
  error.
- An explicit `session_id` argument keeps its meaning as an administrative
  target for `roster`, `join`, and `leave`; `_caller` does not override it.
- `iam` changes only the declared caller's own participant, and refuses a
  different `session_id` or `agent_id`.
- Without `_caller` (a client other than Claude Code, or no hook) behavior is
  unchanged.

`_caller` identifies the caller for routing. It is not authentication: any
client that can send arguments can send it. The hook is what makes it
trustworthy under Claude Code.

## Nested sessions

A `claude -p` started from a session's shell inherits that session's
`CLAUDE_PID`, and so does its MCP server. Before this change the nested
session's `session roster` returned the outer session's roster. The hook payload
carries the nested session's own `session_id`, so the server now answers for the
nested session.

## Hook wrappers

The wrappers in `plugin/hooks/*.sh` pass stdin to the ethos binary unmodified.
They used to read one line with `read -r -t 1`, which cut a large payload (an
Edit of a large file) at 8 KiB when the writer paused, and logged `invalid JSON
input (8192 bytes)`. The ethos binary bounds its own wait.
