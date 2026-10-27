# Native OpenCode MCP identity pilot

**Opt-in only.** Default `ethos serve` ignores OpenCode session metadata,
including malformed values, and retains the existing environment/process-tree
resolution behavior for all identity and session operations. Existing native
OpenCode consumers need no configuration changes.

Start the pilot server with:

```sh
ethos serve --opencode-session-metadata
```

In the native MCP server configuration, include the flag in the command array:

```json
{"type": "local", "command": ["ethos", "serve", "--opencode-session-metadata"]}
```

For native MCP calls, OpenCode supplies the caller's session ID in
`params._meta["ai.opencode/sessionID"]`. With the flag, Ethos uses that request-local ID for
`identity whoami`, implicit `session roster`, and `session iam`, instead of
the MCP server process's environment or process ancestry.

Before using these operations, explicitly create an Ethos session roster
whose session ID is the OpenCode session ID and whose agent participant has
that exact `agent_id`. The existing CLI supports this pilot setup:

```sh
# Replace ses_EXAMPLE with the actual OpenCode session ID, and replace
# HUMAN_HANDLE and AGENT_HANDLE with existing identity handles.
ethos session create --session ses_EXAMPLE \
  --root-id HUMAN_HANDLE --root-persona HUMAN_HANDLE \
  --primary-id ses_EXAMPLE --primary-persona AGENT_HANDLE
```

**Create only an absent roster.** `session create` overwrites an existing
roster, including its participants. Check for an existing roster with
`ethos session show ses_EXAMPLE` first; do not recreate a live session.
If the roster exists but lacks the caller participant, join it instead:

```sh
ethos session join --session ses_EXAMPLE \
  --agent-id ses_EXAMPLE --persona AGENT_HANDLE
```

Metadata identifies the caller for routing; it is **not authentication** or
an authorization boundary. Only use this opt-in with trusted local callers.

Ethos does not register callers or auto-create rosters. A metadata caller
with a missing, malformed, or mismatched roster, or no exact participant,
receives an error. `whoami` loads that participant's persona through the
effective layered identity store; an empty, invalid, missing, or corrupt
identity is an error, never a reason to fall back to git or the OS user.
The `reference` option retains its usual attribute-resolution behavior.

`iam` validates the caller's current and requested identities and changes only the existing exact
caller participant. It rejects a different explicit `session_id` or
`agent_id`. The participant must still exist when the locked update runs;
a concurrent leave cannot cause `iam` to recreate it.

Metadata `whoami` and `iam` refuse while the repository store cannot be located, or while the bundle or policy configuration is unusable, as plain `whoami` does.

Explicit `session_id` remains an administrative target for `roster`, `join`,
and `leave`, including when it differs from the metadata caller. Metadata
IDs themselves must be nonempty strings containing only ASCII letters,
digits, underscores, or hyphens; invalid IDs fail rather than being normalized.

Calls without this metadata key keep the existing resolution and mutation
behavior. Other metadata keys do not activate the OpenCode caller path.
No environment variables are changed. Mission and fork behavior are unchanged.
