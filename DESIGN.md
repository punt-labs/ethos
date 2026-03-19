# Design Decisions

## DES-001: Sidecar architecture (SETTLED)

**Decision**: Ethos publishes identity state to a known filesystem location.
Other tools (Vox, Beadle, Biff) optionally read it. No import dependency.

**Reasoning**: If ethos were a dependency of vox/biff/beadle, upgrading ethos
would force upgrades of everything downstream. The sidecar pattern makes the
contract a file format — stable, versionable, and tools adopt it at their own
pace.

**Rejected alternatives**:

- Shared library with ethos types imported by consumers — creates tight
  coupling and version lock-step.
- Message bus (NATS) for identity propagation — over-engineered for state
  that changes rarely.

## DES-002: User-global vs repo-local storage (SETTLED)

**Decision**: Identities live in `~/.punt-labs/ethos/identities/`. Repo-local
config (active identity, agent roster) lives in `.punt-labs/ethos/config.yaml`.

**Reasoning**: Your identity doesn't change per repo. But which identities are
active in a given project, and which agents are available, is project-scoped.
This matches Git's own split: `~/.gitconfig` for identity, `.git/config` for
repo overrides.

**Rejected alternatives**:

- Everything in `~/.config/ethos/` — loses per-repo agent roster.
- Everything in `.punt-labs/ethos/` — identity files would need copying
  across repos.

## DES-003: Go over Python (SETTLED)

**Decision**: Implement ethos in Go.

**Reasoning**: Ethos will be queried on session start (via SessionStart hook),
on every Biff `/who`, and by Vox before speaking. Go's ~10ms cold start vs
Python's ~200ms+ is the difference between invisible and noticeable. The module
has a small surface area (YAML I/O, CLI, MCP tools) — Python's ecosystem
advantage doesn't apply.

**Rejected alternatives**:

- Python with lightweight entry point — still 200ms+ even with lazy imports.
  Hooks standard § 12 documents this tax.

## DES-004: Unified identity schema for humans and agents (SETTLED)

**Decision**: Humans and agents use the same YAML schema and creation flow.
The only structural difference is `kind: human` vs `kind: agent`, which
determines whether the identity can be invoked as a subagent.

**Reasoning**: An identity is an identity. It has a name, voice, email, GitHub
handle, writing style, skills, personality. Whether a human or LLM inhabits it
is a property, not a type distinction.

**Rejected alternatives**:

- Separate schemas for human profiles vs agent definitions — duplicates
  fields, creates maintenance burden, and implies a philosophical distinction
  that doesn't exist in practice.

## DES-005: Agent definition as channel binding (SETTLED)

**Decision**: The Claude Code agent `.md` file is treated as a channel binding,
like voice or email. An ethos identity *has* an agent definition the same way
it *has* an email address.

**Reasoning**: The agent `.md` defines *what tools and workflow*. The ethos
identity defines *who*. They are complementary, not competing. Ethos can
generate the `.md` or point to an existing one.

**Rejected alternatives**:

- Ethos identity replaces agent `.md` entirely — loses the system prompt
  and tool restrictions that make Claude Code agents effective.
- Ethos identity and agent `.md` as independent, unlinked artifacts —
  creates drift between "who the agent is" and "what the agent does."

## DES-006: Store type for identity persistence (SETTLED)

**Decision**: Identity CRUD operations live in `internal/identity.Store`,
a struct that takes a root directory path. `cmd/ethos/` has a thin `store()`
helper that creates a `DefaultStore` from `$HOME`. MCP handlers receive the
Store via the `Handler` struct at construction time.

**Reasoning**: The original scaffolding had identity I/O functions as
package-level functions in `cmd/ethos/identity.go` with a parallel `Identity`
struct. This made the persistence untestable (hardcoded `$HOME` paths) and
duplicated the canonical type in `internal/identity/`. The Store pattern
makes all CRUD operations testable with `t.TempDir()` and eliminates the
type duplication. Injecting the Store into MCP handlers avoids `os.Exit`
in server context.

**Rejected alternatives**:

- Package-level functions with `identityDir()` returning `string` — silent
  empty-string failure when `$HOME` is unset, untestable without env mutation.
- Passing root path to every function call — noisy signatures, repeated
  path construction.

## DES-008: Generic extension mechanism (SETTLED)

**Status**: Settled.

### Problem

Consumers of ethos (Beadle, Biff, Vox, Lux, and future tools) need to
store tool-specific attributes on identities. A GPG key ID for email
signing, a preferred TTY name, a voice mood default — these are real
needs, but they do not belong in the ethos identity schema. Ethos is a
low-level identity facility. It must not know about its consumers.

Adding consumer-specific fields (e.g., `gpg_key_id` for Beadle) couples
a low-level facility to a high-level consumer. Every new consumer would
require an ethos schema change, a new release, and coordination across
repos. This does not scale.

### Design

Ethos provides a generic, namespaced key-value extension mechanism at
two scopes:

1. **Persona-level** — durable attributes stored on the filesystem
2. **Session-participant-level** — ephemeral attributes in the session
   roster (DES-007's `ext` map)

Any software can read and write arbitrary key-value pairs scoped to a
namespace (the tool's name) without ethos knowing what the keys mean.

### Persona-Level Extensions

Extensions live in a directory alongside the identity YAML file:

```text
~/.punt-labs/ethos/identities/
  mal.yaml                    # ethos owns — core identity fields
  mal.ext/                    # extension directory
    beadle.yaml                    # beadle owns — beadle-specific attributes
    biff.yaml                      # biff owns — biff-specific attributes
    vox.yaml                       # vox owns — vox-specific attributes
```

Each file is a flat YAML map of key-value pairs owned by the named tool:

```yaml
# mal.ext/beadle.yaml
gpg_key_id: 3AA5C34371567BD2
imap_server: mail.punt-labs.com
trust_default: verify
```

```yaml
# mal.ext/biff.yaml
preferred_tty: tty1
```

**Ownership rules:**

- Ethos creates the `<persona>.ext/` directory when the identity is
  created.
- Each tool manages its own `<namespace>.yaml` file inside the directory.
- Tools may read/write their namespace file directly (sidecar contract)
  or through the ethos CLI/MCP interface.
- Tools must not write to another tool's namespace file.
- Ethos never reads or interprets extension contents except to assemble
  the merged view.

### Merged View

When any consumer asks "who is mal?", ethos returns the complete
picture — core identity fields plus all extensions:

```yaml
name: Mal Reynolds
handle: mal
kind: human
email: mal@serenity.ship
github: mal
voice:
  provider: elevenlabs
  voice_id: abc123
agent: .claude/agents/mal.md
writing_style: |
  Direct. Short sentences. Data over adjectives.
personality: |
  Principal engineer. Formal methods, accountability.
skills:
  - formal-methods
  - product-strategy
ext:
  beadle:
    gpg_key_id: 3AA5C34371567BD2
    imap_server: mail.punt-labs.com
  biff:
    preferred_tty: tty1
  vox:
    default_mood: calm
```

This applies to all read interfaces: `ethos show`, `ethos show --json`,
and the `get_identity` MCP tool. The `ext` map is always present in the
output (empty map `{}` when no extensions exist).

`ethos list` and `ethos list --json` return summary data only (no
extensions). Extensions are returned only when loading a specific
identity.

### Session-Participant-Level Extensions

The session roster (DES-007) already defines an `ext` map per
participant. This serves the same purpose at session scope — Biff
writes `ext.biff.tty: s004`, Vox writes `ext.vox.voice_active: true`.

Session extensions are ephemeral (deleted with the roster on session
end). Persona extensions are durable (persist across sessions).

The two levels are independent. A tool may store durable defaults at the
persona level and ephemeral session state at the participant level.

### CLI Commands

```text
ethos ext get <persona> <namespace> [key]     Read one key or all keys
ethos ext set <persona> <namespace> <key> <value>   Write a key
ethos ext del <persona> <namespace> [key]     Delete one key or entire namespace
ethos ext list <persona>                       List all namespaces
```

### MCP Tools

```text
ext_get      Read extension key(s) for a persona
ext_set      Write extension key for a persona
ext_del      Delete extension key or namespace
ext_list     List namespaces for a persona
```

### Validation Constraints

Ethos enforces structural constraints without interpreting values:

| Field | Rule |
|-------|------|
| Namespace | `^[a-z][a-z0-9-]*$`, max 32 characters |
| Key | `^[a-z][a-z0-9_]*$`, max 64 characters |
| Value | Any valid YAML scalar, max 4096 bytes |
| Keys per namespace | Max 64 |
| Namespaces per persona | Max 32 |

Keys and namespaces are validated on write. Values are stored as-is
(ethos does not parse or interpret them). Reads return raw values.

### Why Files-Per-Namespace

| Alternative | Rejected Because |
|-------------|-----------------|
| Add fields to identity YAML | Couples ethos to consumers; every consumer needs an ethos release |
| Single `ext.yaml` with all namespaces | Tools can corrupt each other's data; merge conflicts on concurrent writes |
| Database (SQLite, etc.) | Violates sidecar contract — tools must be able to read files directly |
| Generic `ext` map inside identity YAML | Ethos would need to parse/merge on every identity write; risk of data loss |

Files-per-namespace means:

- Tools can read their data directly without ethos (sidecar contract)
- No merge conflicts between tools writing concurrently
- Identity YAML stays clean — only ethos-owned fields
- File permissions can differ per namespace if needed
- Adding a new consumer requires zero ethos changes

### Interaction with DES-001 (Sidecar Contract)

The sidecar contract extends to extensions. The file format is the
contract:

- Path: `~/.punt-labs/ethos/identities/<persona>.ext/<namespace>.yaml`
- Format: flat YAML map (string keys, scalar values)
- Any tool can read any namespace file directly without importing ethos

The CLI/MCP interface is a convenience layer for tools that don't want
to manage file I/O or that operate in environments without direct
filesystem access (e.g., MCP-only agents).

---

## DES-007: Session roster — multi-participant identity awareness (SETTLED)

**Status**: Settled. All open questions resolved.

### Problem

Ethos currently tracks one "active" identity. In practice, a session has
multiple participants — a human, a primary agent, and subagents. Any
participant needs to answer:

1. **Who am I?** — my own identity (persona)
2. **Who is everyone else?** — all participants, their personas, and relationships

### Design Principles

**No human/agent distinction in the session model.** The registry has a
`kind` field on each persona (human vs agent), but the session treats all
participants uniformly. The structural difference is captured by the
parent-child tree, not by a role or kind field.

**Initiator/delegate is implicit in the tree.** The root participant (no
parent) is the session initiator. Any participant is an initiator relative
to its children and a delegate relative to its parent. No explicit role
field is needed.

**Extensible participant records.** Other tools (Biff, Vox, Beadle) need
to decorate participants with their own metadata. Each participant has an
`ext` map keyed by tool name. Ethos does not validate or constrain `ext`
contents — the sidecar contract extends to the session file. Values are
`map[string]any`; consumers are responsible for type assertions.

### Data Model

Session roster stored at `~/.punt-labs/ethos/sessions/<session-id>.yaml`:

```yaml
session: ba3bb20f
started: 2026-03-18T14:30:00Z
participants:
  - agent_id: mal          # OS login ($USER)
    persona: mal            # ethos identity lookup key
    parent: ~
    ext:
      biff:
        tty: s001

  - agent_id: "19147"           # topmost claude ancestor PID (process tree walk)
    persona: archie
    parent: mal
    ext:
      biff:
        tty: s004

  - agent_id: a5734dd           # Claude Code subagent ID (from hook input)
    persona: code-reviewer       # auto-matched from agent_type → ethos persona
    parent: "19147"
    ext: {}

  - agent_id: a93c2be
    persona: silent-failure-hunter
    parent: "19147"
    ext: {}

  - agent_id: b8823ff
    persona: ~                   # built-in subagent, no ethos persona
    agent_type: Explore          # raw type preserved for tools that need it
    parent: "19147"
    ext: {}
```

**Fields per participant:**

| Field | Source | Purpose |
|-------|--------|---------|
| `agent_id` | Runtime — OS login, Claude PID, or subagent ID | Unique instance identifier |
| `persona` | Ethos identity registry | Links to full identity profile; `~` when no persona exists |
| `agent_type` | Claude Code hook input | Raw type (e.g., `Explore`, `code-reviewer`); preserved for tools |
| `parent` | Session structure | Who initiated this participant; `~` for root |
| `ext` | Other tools (Biff, Vox, etc.) | Tool-scoped metadata; open `map[string]any` |

**Null persona handling:** When `persona` is `~`, the participant has no
ethos identity. Query tools return the `agent_type` as a display name.
A default persona can be configured in repo config for common agent types
(e.g., map all `code-reviewer` subagents to a shared persona with a
defined writing style and personality).

**Primary agent identification:** The SessionStart hook resolves the
primary agent's `agent_id` by walking the process tree to the topmost
`claude` ancestor PID, using the same `ps -eo pid=,ppid=,comm=` approach
proven in Biff (see `find_session_key()` in biff DES-011/DES-011a). This
PID is stable across all hook invocations in the same session. Falls back
to `$PPID` when no `claude` ancestor is found.

**Tree relationships:**

```text
mal (root — no parent)
  └─ 19147 (archie)
       ├─ a5734dd (code-reviewer)
       ├─ a93c2be (silent-failure-hunter)
       └─ b8823ff (Explore, no persona)
```

Any participant can derive from the tree:

- **Root / session initiator**: walk up to `parent: ~`
- **My initiator**: my `parent`
- **My delegates**: anyone whose `parent` is my `agent_id`
- **Siblings**: same `parent`
- **Full chain of authority**: walk from me to root

### Lifecycle via Hooks

Claude Code provides hook events that map directly to roster operations:

| Hook Event | Roster Operation | Data Available |
|------------|-----------------|----------------|
| `SessionStart` | Create roster, join root + primary agent | `session_id`, `$USER`, Claude PID (tree walk) |
| `SubagentStart` | Join subagent | `session_id`, `agent_id`, `agent_type` |
| `SubagentStop` | Leave subagent | `session_id`, `agent_id` |
| `SessionEnd` | Tear down roster | `session_id` |

**Concurrency:** Multiple SubagentStart hooks may fire in parallel.
Roster writes use `flock(LOCK_EX)` on a lock file
(`sessions/<session-id>.lock`) before read-modify-write. This matches
the `O_EXCL` pattern in `Store.Save` and requires no daemon.

**Cleanup:** The `SessionEnd` hook deletes the roster file. If the
session crashes without `SessionEnd` firing, `ethos session purge`
cleans up stale rosters by checking whether the primary agent's PID
is still alive. The ethos CLI and MCP tools own this — no external
mechanism.

### Commands

| Command | What it does |
|---------|-------------|
| `ethos iam <persona>` | Set my persona for this session (used at session start) |
| `ethos session` | List all participants in the current session |
| `ethos session join` | Add a participant (called by hooks) |
| `ethos session leave` | Remove a participant (called by hooks) |
| `ethos session purge` | Clean up stale session rosters |

`ethos iam <persona>` is the session-aware identity command. It differs
from `ethos whoami` (which manages the global active identity in the
registry). `iam` declares "I am this persona in this session." The
caller's `agent_id` is determined automatically (OS login, Claude PID
walk, or subagent ID from context).

### MCP Tools

| Tool | Purpose |
|------|---------|
| `session_iam` | Declare persona for current participant |
| `session_roster` | Return full participant list with tree |
| `session_join` | Register a new participant |
| `session_leave` | Deregister a participant |

### Resolved: Persona Default by Agent Type

Each agent type has a default persona — the identity in the registry with
the same name as the `agent_type`. When SubagentStart fires with
`agent_type: code-reviewer`, the hook looks up `persona[agent_type]` in
the registry. If an identity named `code-reviewer` exists, it becomes the
participant's persona. If not, `persona: ~`.

No mapping configuration. The convention is: create an ethos identity with
the same name as the agent type.

```bash
ethos create -f code-reviewer.yaml   # persona for all code-reviewer subagents
ethos create -f explore.yaml         # persona for all Explore subagents
```

A specific subagent instance can override the default via `ethos iam`
to declare a different persona explicitly.

### Resolved: Session ID Propagation to Non-Hook Callers

Hooks receive `session_id` in stdin JSON, but non-hook callers (Biff, Vox)
need the session ID too. The SessionStart hook writes the session ID to a
PID-keyed file:

```text
~/.punt-labs/ethos/sessions/current/<claude-pid>
```

Contents: the session ID (plain text). Any descendant process walks the
process tree to the topmost `claude` ancestor PID, reads that file, and
gets the session ID. This is the same pattern Biff uses for unread count
files (see Biff DES-011). The SessionEnd hook deletes the file.

### Constraints

- Must work without a daemon — ethos is a CLI tool, not a server.
- Must survive subagent spawning — new processes need access.
- Must not require ethos as a dependency — the sidecar contract (DES-001)
  holds. Other tools read known paths.
- Must handle concurrent sessions on the same machine.
- Participant records must be extensible by other tools without ethos
  changes.

---

## DES-009: Hook compatibility with Claude Code (SETTLED)

**Status**: Settled after 5 broken releases (v0.3.0–v0.3.3).

### Problem

Ethos hooks crashed on every session start with `SessionStart:startup hook error`. The error persisted across 5 release cycles, each claiming to fix it.

### Root Causes (3 independent bugs, all required)

**1. `INPUT=$(cat)` blocks indefinitely.**

Claude Code pipes JSON to hook subprocesses via stdin but does not always close the pipe promptly for SessionStart events. Bash `cat` is equivalent to Python's `sys.stdin.read()` — it blocks until EOF. Biff discovered and documented this same bug (see biff DES-025). Every ethos hook used `INPUT=$(cat)` and was vulnerable.

**Fix**: Use `read -r -t 1` with a 1-second timeout for hooks that need stdin data. SessionStart hook doesn't need stdin at all — removed the read entirely.

**2. `"matcher": ""` in hooks.json.**

Every ethos hook entry in hooks.json had `"matcher": ""` (empty string). Every working plugin either omits the matcher key entirely (catch-all) or uses a specific regex pattern. The empty string matcher may be treated differently by Claude Code — either matching nothing or causing a configuration error.

**Fix**: Remove the `"matcher"` key from all non-PostToolUse hooks, matching biff/vox/quarry pattern.

**3. Missing patterns from working plugins.**

Compared to biff (the most mature plugin), ethos hooks were missing:

- Kill switch: `[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0`
- `exit 0` at the end of every hook
- `hookEventName` field in the JSON output
- JSON output via heredoc (not `printf` with manual escaping)
- `PLUGIN_ROOT` derived from `dirname "$0"` (not `CLAUDE_PLUGIN_ROOT` env var)

**Fix**: Rewrote all 5 hooks to match biff's proven patterns exactly.

### Why It Took 5 Releases

1. **Never compared to working code.** The hooks were written from scratch without reading biff's working implementation. Every fix was based on theory (bash version, `set -u`, array syntax) rather than diffing against a known-good reference.

2. **Point fixes without pattern search.** `set -u` was removed from session-start.sh but not the other 4 hooks. `INPUT=$(cat)` was the actual bug but was never identified because no one grepped for `cat)` across all hooks.

3. **No end-to-end testing.** Every fix was verified by running `make check` (Go tests) and piping JSON to the hook manually. Neither reproduces the actual Claude Code execution environment where stdin pipes aren't closed promptly.

4. **No comparison to working plugins.** The user had to ask "did you look at all the other ones which work?" after 5 failed attempts. Reading biff's DESIGN.md (DES-025) would have identified the `stdin.read()` bug immediately — it was documented with root cause analysis, rejected alternatives, and test cases.

### Cross-Project Pattern

Any Claude Code plugin hook that reads stdin with `cat`, `sys.stdin.read()`, or any blocking read is vulnerable. The safe patterns are:

- **Don't read stdin** if you don't need the data (session-start)
- **`read -r -t <seconds>`** in bash for hooks that need stdin data
- **`select` + `os.read(fd, N)`** in Python (see biff DES-025)
- **`INPUT=$(cat)` is safe for PostToolUse** — Claude Code closes the pipe for these events

### Final Hook Patterns

All ethos hooks now follow these rules:

| Pattern | Source | Required |
|---------|--------|----------|
| `[[ -f "$HOME/.punt-hooks-kill" ]] && exit 0` | biff | Yes — emergency kill switch |
| `set -euo pipefail` | biff, vox | Yes |
| `PLUGIN_ROOT="$(cd "$(dirname "$0")/.." && pwd)"` | biff, vox | Yes — not env var |
| `read -r -t 1` instead of `INPUT=$(cat)` | biff DES-025 | Yes for hooks needing stdin |
| No stdin read at all | biff session-start | Yes for SessionStart |
| No `"matcher"` key in hooks.json | biff, vox | Yes for catch-all hooks |
| `exit 0` at end | biff | Yes |
| JSON via heredoc with `hookEventName` | biff | Yes for hooks returning context |

### Rejected Alternatives

- **`set -eo pipefail` (drop `-u`)** — unnecessary. The actual bug was `cat` blocking, not unbound variables. Biff uses `set -euo pipefail` and works fine.
- **Downloading release binary via `go install`** — `go install` doesn't support `-ldflags`, producing `ethos dev`. Fix: download pre-built binary from GitHub releases. This was a separate installer bug discovered during the same cycle.
- **`mktemp` in `/tmp`** — not atomic for `settings.json` updates. Use `mktemp "${SETTINGS}.tmp.XXXXXX"` on the same filesystem.
