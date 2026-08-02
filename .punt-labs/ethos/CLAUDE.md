# Ethos

How an agent drives ethos — not how to develop ethos itself. Ethos binds a
name, voice, email, GitHub handle, writing style, personality, and talents
into one identity that other tools read.

## Who am I

- `ethos whoami` — resolve your identity from the session, git config, or OS
  user.
- `ethos iam <persona>` — declare a persona for the current session.

Session hooks inject your persona into context at start and after
compaction. Ethos generates `.claude/agents/<handle>.md` from team data at
SessionStart; restart Claude Code to regenerate them after a team change.

## Delegation (missions)

- `ethos mission dispatch --worker <h> --evaluator <h> --write-set <paths> --criteria <text>`
  — write a mission contract. Dispatch writes the contract; a separate
  agent spawn does the work.
- `ethos mission show|log|results <id>` — inspect a mission.
- `ethos mission close <id>` — close a passing mission.
- `ethos mission pipeline list|show|instantiate <name>` — drive multi-stage
  work from a template.

Scope the write-set to the work's real breadth so you don't have to widen it
later. It is a spectrum, not a tight file list: a single file for a one-file
fix; a **directory prefix** (e.g. `internal/enable/`) to authorize modify+create
anywhere under it when you can't name every file up front; or a **glob** where
it fits. The set is enforced at runtime — an edit outside it fails the mission —
but it can be as broad as the work honestly needs. For new files a worker
creates while decomposing, use the **separate** `extract_into` field (its own
list of directory prefixes, decoupled from the modify write-set — DES-052), not
a `write_set` entry. Prefer declaring the right breadth up front over a tight
list you must widen via escalate→close→recreate when the work legitimately
grows. Commit one logical step at a time.

## Audit

- `ethos audit show --delegation <id>` — reconstruct a delegation's trail.
- `ethos audit seal` runs at pre-commit when ethos is enabled here; the
  sealed chunks travel in the same commit as the work.
- `ethos audit quarantine` — the recovery path for a corrupt chunk.

## Session

- `ethos session` — the current roster.
- `ethos session purge` — clear stale sessions.

## Gotchas

- Never run `make install` from inside Claude Code — the running binary
  cannot overwrite itself. Ask a human to run it from a shell.
- Agent types are discovered at SessionStart; restart after adding one.
- `ethos doctor` checks the seal hook only when ethos is enabled here — a
  dormant or never-enabled repo passes.
