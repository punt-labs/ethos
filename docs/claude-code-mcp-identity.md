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
without the hook. If the payload has no `session_id`, names an `agent_type`
but no `agent_id` (it is not the leader, and `CLAUDE_PID` would name the
leader), or no agent key can be determined, the hook declares no caller,
removes any `_caller` the model supplied so the server sees none, and logs the
cause to `hook-errors.log`.

### When the hook cannot do its work

For a call to an ethos MCP tool the hook fails closed. If the payload cannot be
read (a read error, or the 10-second deadline fired), cannot be parsed, or a
later step fails, the hook answers `permissionDecision: "deny"` with a reason
that names the cause, and writes the cause to `hook-errors.log`. It does not
exit with an error and no output, which Claude Code treats as "proceed". A
payload too broken to parse is still denied when its raw bytes name an ethos MCP
tool in `tool_name`. For any other tool the hook's failure is unchanged: an
error in `hook-errors.log`, and the call proceeds.

The hook never decides `allow`. Its output type holds `deny` or no decision, so
the word cannot be written back in.

## What the server does with it

`identity whoami`, `session roster` without a `session_id`, `session iam`,
mission binding (`mission create`, `close`, `abandon`), and the Human identity
row of `doctor` resolve the caller from `_caller` through the same
exact-participant path as the OpenCode pilot (`docs/opencode-mcp-identity.md`):
the roster for `session_id` must exist and hold exactly one participant with
that `agent_id`. Otherwise the call fails with a named error. The server never
falls back to the environment, the process tree, git, or the OS user.

### A Claude Code call with no `_caller`

When the server is running under Claude Code and a call carries neither
`_caller` nor the OpenCode `_meta` key, those methods return

```text
Claude Code call arrived without _caller: the ethos PreToolUse hook did not
declare the caller (is it installed, enabled, and not killed?)
```

The hook is missing, disabled, or killed (`~/.punt-hooks-kill`), or it declared
no caller (see above). The server then has no source for the caller that cannot
name another session's: the environment and the process tree are shared by
every session the server outlives, and Claude Code gives the server no
`CLAUDE_PID`. So it refuses, and does not read `ETHOS_SESSION`,
`ETHOS_AGENT_ID`, or the process tree for this answer.

- `doctor` reports the error on its Human identity row, status FAIL, and runs
  its other checks.
- `mission create` still creates the mission and reports the error in its
  `warnings`, because the session binding could not be written.
- `mission close` and `abandon` change the mission's status first, then return
  an error result: the session bindings were not cleared, and the closed mission
  may still stamp commit trailers. This holds for any failure to clear them, not
  only this one (an unreadable sidecar, say). The status change stands.
- `session roster`, `join` and `leave` with an explicit `session_id` still work:
  that is an administrative target, not a declaration about the caller.
- A client that is not Claude Code is unchanged: the harness-neutral chain
  (`ETHOS_SESSION`, `ETHOS_AGENT_ID`, the process tree) still answers.

A `_caller` that is present is judged on its own: a malformed one, or one naming
a participant that is not in the roster, is refused, and the environment does
not stand in for it. A well-formed `_caller` naming a real participant is
routed, whoever wrote it (see below).

- `session_id` and `agent_id` must each be nonempty and contain only ASCII
  letters, digits, underscores, or hyphens. Any other key, type, or value is an
  error.
- An explicit `session_id` argument keeps its meaning as an administrative
  target for `roster`, `join`, and `leave`; `_caller` does not override it.
- `iam` changes only the declared caller's own participant, and refuses a
  different `session_id` or `agent_id`.
- Without `_caller` (a client other than Claude Code, or no hook) behavior is
  unchanged, as for any client that is not Claude Code.

### Callers with no persona

A participant whose `persona` is empty is a built-in subagent such as
`general-purpose` or `Explore`: `SubagentStart` found no ethos identity named
for its `agent_type` (DES-007, "Null persona handling"). It is a valid caller
with no identity, not an error.

- `identity whoami` returns the text `<agent_type> has no ethos identity`, or
  `<agent_id> has no ethos identity` when the roster recorded no `agent_type`.
  It does not return the leader's, the environment's, or the OS user's identity.
- `session roster` and mission binding act on the caller's session as for any
  caller.
- `session iam` lets the caller declare a persona for its own participant; the
  persona must name an existing identity.
- `doctor` shows the same text on its Human identity row, status PASS.

The convention that maps an `agent_type` to a persona is DES-007's: an identity
whose handle equals the `agent_type`, matched when the subagent starts. Repo
config holds no other `agent_type` mapping.

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

The wrappers in `plugin/hooks/*.sh` read stdin in bash and pipe it to the ethos
binary, as DES-029 requires: Claude Code starts hooks through `/bin/sh -c`, and
Go's read of the inherited fd hung on Linux. They used to read one line with
`read -r -t 1`, which cut a large payload (an Edit of a large file) at 8 KiB
when the writer paused, and logged `invalid JSON input (8192 bytes)`. They now
run `read -r -d "" -t 10`, which takes the whole payload, newlines included,
and waits up to 10 seconds for a stalled writer. ethos still bounds its own
wait for callers that pass it an fd directly.

When that read times out (status above 128) the wrapper appends
`ethos <hook>: stdin read timed out after 10s (N bytes)` to `hook-errors.log`
and forwards what arrived, so the log says why a fragment failed to parse.
Inside ethos, `ReadInput` returns an error wrapping `ErrInputTimeout` when the
deadline fires, and an error for any read failure other than end of input. A
payload is complete only when it is exactly one JSON object, so a truncated
payload is never accepted as whole.
