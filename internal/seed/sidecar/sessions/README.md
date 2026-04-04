# Sessions

Ephemeral session rosters managed automatically by ethos hooks. Do not
edit these files manually.

Each session has:

- `<session-id>.yaml` — participant roster with personas and tree
- `<session-id>.lock` — flock for concurrent writes

A `current/` subdirectory (created on demand) maps Claude process PIDs
to session IDs for non-hook callers.

Sessions are created on `SessionStart`, updated on `SubagentStart`/`SubagentStop`,
and deleted on `SessionEnd`. Stale sessions can be cleaned up with
`ethos session purge`.
