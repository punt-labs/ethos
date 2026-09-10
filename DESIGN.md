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
handle, writing style, talents, personality. Whether a human or LLM inhabits it
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

- Ethos creates the `<handle>.ext/` directory when the identity is
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
# voice config moved to ext/vox (DES-019)
agent: .claude/agents/mal.md
writing_style: |
  Direct. Short sentences. Data over adjectives.
personality: |
  Principal engineer. Formal methods, accountability.
talents:
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
ethos ext get <handle> <namespace> [key]     Read one key or all keys
ethos ext set <handle> <namespace> <key> <value>   Write a key
ethos ext delete <handle> <namespace> [key]   Delete one key or entire namespace
ethos ext list <handle>                       List all namespaces
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

- Path: `~/.punt-labs/ethos/identities/<handle>.ext/<namespace>.yaml`
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

`ethos iam <persona>` declares "I am this persona in this session."
`ethos whoami` reads it back via the resolution chain (DES-011). The
caller's `agent_id` is determined automatically (OS login, Claude PID
walk, or subagent ID from context).

### MCP Tools

| Tool | Method | Purpose |
|------|--------|---------|
| `session` | `iam` | Declare persona for current participant |
| `session` | `roster` | Return full participant list with tree |
| `session` | `join` | Register a new participant |
| `session` | `leave` | Deregister a participant |

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

---

## DES-010: Rich identity attributes — markdown references (SETTLED)

**Status**: Settled. Implemented in PR #47. Build plan at `docs/build-plan.md`.

### Problem

Identity attributes (`writing_style`, `personality`, `talents`) are inline
strings — labels with no actionable content. A consumer reading the identity
gets `"software engineer"` but not what that means: no standards, no
anti-patterns, no tools. There is no reuse — if two identities share a
talent, the description is duplicated or absent.

### Decision

Convert all three attribute fields from inline strings to relative paths
pointing to markdown files. Each attribute type gets its own directory
under the ethos root:

```text
~/.punt-labs/ethos/
  talents/                        # shared talent definitions
  personalities/                  # shared personality definitions
  writing-styles/                 # shared writing style definitions
```

An identity becomes a unique combination of reusable `.md` files plus
core identity fields (name, handle, kind, email, github, voice, agent).

```yaml
writing_style: writing-styles/concise-quantified.md
personality: personalities/principal-engineer.md
talents:
  - talents/executive.md
  - talents/software-engineering.md
```

Paths are relative to the ethos root (`~/.punt-labs/ethos/`). The `agent`
field is the exception — it resolves relative to the repo root, not the
ethos root, because agent `.md` files live in the project.

### Resolution model

`Load()` resolves all markdown references and returns content inline by
default. This is the common case — most callers need the content.

Callers that only need paths (performance optimization for display-only
use cases like biff `/who`) pass `Reference(true)` to skip file reads.
This follows the JSON API `include` convention: full content is the
default, lightweight references are opt-in.

The Identity struct carries both:

- **Path fields** (`WritingStyle`, `Personality`, `Talents`) — always
  populated from YAML, present in both modes
- **Content fields** (`WritingStyleContent`, `PersonalityContent`,
  `TalentContents`) — populated by default, empty when `reference: true`

`List()` always passes `Reference(true)` — listing all identities should
not read every attribute file. Content resolution is for single-identity
reads.

MCP tools (`get_identity`, `whoami`) return full content by default.
An optional `reference` boolean parameter returns paths only.

### Missing file handling

When `Load()` resolves an attribute and the `.md` file is missing, the
content field is set to an empty string and a warning is added to
`Identity.Warnings []string`. This matches the existing `ListResult.Warnings`
pattern. Consumers can check `Warnings` to detect broken references.
`Save()` validates that all referenced files exist and rejects the save
if any are missing.

### Path containment

Attribute paths must resolve within the ethos root. Containment is
verified by computing absolute, cleaned paths for both the ethos root
and the candidate path, then using `filepath.Rel` to ensure the
resulting relative path does not escape the root (rejects `..`,
`../` prefixes, and absolute results). Naive `strings.HasPrefix` is
not used — it is unsafe (e.g., `/ethos2` matches prefix `/ethos`).
Symlinks are allowed — users may symlink attributes from a dotfiles
repo. The containment check runs on the logical path before following
symlinks.

### Sidecar README deployment

The repo `internal/seed/sidecar/` directory contains README.md files for each
subdirectory of `~/.punt-labs/ethos/`. The installer copies these during
installation so users and consuming tools have documentation of the file
layout and sidecar contract. READMEs are deployed with `cp -n` (no
clobber) to avoid overwriting user modifications.

### Uniform for humans and agents

A human's talent file describes their expertise and standards. An agent's
talent file describes its capabilities and tools. Same format, same
resolution, same reuse model. The `kind` field distinguishes human from
agent — the attribute system does not.

### Rejected alternatives

- **Inline strings with optional file override** — two sources of truth,
  unclear which wins. Clean break is simpler.
- **Load returns paths, caller resolves** — pushes complexity onto every
  consumer. Most callers need content, not paths.
- **Cap resolved content at 64KB** — silent truncation is worse than
  returning the full file. If a file is too large, the author splits it.
- **Frontmatter in `.md` files for metadata** — unnecessary complexity.
  The filename is the identifier, the content is the value. If metadata
  is needed later, frontmatter can be added without breaking existing
  files.

---

## DES-011: Identity resolution — humans from git/OS, agents from repo config (SETTLED)

### Problem

The session start hook assigns personas to the human root and the primary
agent. Both currently get the same persona — the "global active identity"
read from `~/.punt-labs/ethos/active`. This has three problems:

1. **Repos are multi-user.** A tracked `active:` field in repo config
   makes no sense — the repo belongs to the whole team, not one person.
   The human identity must come from an external source specific to the
   current user.

2. **Human and agent get the same persona.** The session roster shows the
   same identity for both root and primary agent. They are different
   participants and should have different identities.

3. **The "global active identity" concept has no clear purpose.** If the
   human is whoever git/OS says they are, and the agent is configured per
   repo, there is nothing left for a global active file to do.

### Design

#### `whoami` resolution chain

`ethos whoami` answers "who am I?" for any caller — human in a shell,
primary agent in a Claude Code session, or sub-agent. Resolution tries
each source in order, stopping at the first match:

| Step | Source | Match field |
|------|--------|-------------|
| 1 | `iam` declaration (PID-keyed file) | Explicit persona set via `ethos iam` |
| 2 | `git config user.name` | Identity `github` field |
| 3 | `git config user.email` | Identity `email` field |
| 4 | `$USER` (OS login) | Identity `handle` field |

Step 1 checks for an explicit `iam` declaration. This uses the same
PID-keyed file mechanism as Claude Code sessions
(`~/.punt-labs/ethos/sessions/current/<PID>`). `ethos whoami` walks the
process tree upward, checking for a `current/<PID>` file at each
ancestor. This works identically for:

- **Interactive shell**: `ethos iam jfreeman` writes to `current/$$`.
  `ethos whoami` in the same shell (or a child process) finds it.
- **Claude Code session**: the SessionStart hook writes to
  `current/<claude-pid>`. MCP tools find it via process tree walk.

The `iam` declaration lives as long as the process it's keyed to. When
the shell exits or Claude Code terminates, the PID becomes stale.
`ethos session purge` cleans up stale PID files by checking whether the
process is alive.

Steps 2–4 are the automatic resolution chain. Each step queries the
identity store for an identity whose field value matches the source. If
no identity matches any step, the caller has no ethos persona — the raw
`$USER` value is used as a display name.

**Rationale for field mapping:**

- `git config user.name` is commonly set to a GitHub username (e.g.,
  `jmf-pobox`), which matches the identity's `github` field.
- `git config user.email` matches the identity's `email` field directly.
- `$USER` is the OS login (e.g., `jfreeman`), which by convention matches
  the identity's `handle`.

These are three different fields on the identity schema, queried against
three different environment sources. No new schema fields are needed.

#### Agent resolution

The primary agent identity is configured per repo in
`.punt-labs/ethos/config.yaml`:

```yaml
agent: claude
```

This file is tracked in git — the whole team shares the same agent
configuration. When `agent:` is unset, the primary agent has no ethos
persona (undefined). It does **not** fall back to the human's persona —
that would conflate two distinct session participants.

#### Session binding

Resolution feeds into the session lifecycle (DES-007):

- **SessionStart hook**: resolves human via steps 2–4, resolves agent
  from repo config, calls `iam` for both. After `iam`, subsequent
  `whoami` calls hit step 1 (PID-keyed file) and return immediately.
- **SubagentStart hook**: resolves sub-agent persona by `agent_type`
  convention (DES-007 § Persona Default by Agent Type), calls `iam`.
- **Interactive shell**: user calls `ethos iam <persona>` explicitly.
  `ethos whoami` returns it for the life of that shell.
- **`ethos whoami`**: read-only query. Runs the resolution chain
  (steps 1–4). No write path.

### Removed concepts

- **`~/.punt-labs/ethos/active` file** — no longer needed. Human identity
  comes from git/OS, not a manually-set pointer.
- **`ethos whoami <handle>` (write path)** — no "set active" operation.
  The human is whoever git/OS says they are.
- **`active:` field in repo config** — repos are multi-user. The repo
  does not configure who the human is.
- **`resolve.Resolve()` repo-local-to-global chain for humans** — dead
  code that was never wired in; replaced by the multi-field lookup.

### Commands affected

| Command | Before | After |
|---------|--------|-------|
| `ethos whoami` | Reads `~/.punt-labs/ethos/active` | Runs human resolution chain (git/OS → identity store) |
| `ethos whoami <handle>` | Sets global active file | Removed |
| `ethos doctor` | Checks global active file | Checks that git/OS resolves to a valid identity |

### Identity store query

The resolution chain requires looking up identities by non-handle fields
(`github`, `email`). The identity store gains a `FindBy(field, value)`
method that scans all identities and returns the first match. This is a
linear scan over YAML files — acceptable given the small number of
identities (typically < 20).

### Rejected alternatives

- **Global active file as the sole source** — ignores multi-user reality.
  A repo checked out by two developers would show the same human persona
  for both.
- **Repo config `active:` field for humans** — same problem. Tracked
  config is shared; human identity is per-user.
- **Match `git config user.name` against identity `handle`** — fails when
  git username differs from ethos handle (e.g., `jmf-pobox` vs
  `jfreeman`). The `github` field is the correct match target.
- **Agent falls back to human persona** — conflates two distinct session
  participants. An unnamed agent is more honest than a mislabeled one.
- **Require git for human resolution** — too rigid. `$USER` fallback
  handles environments without git (containers, CI, non-repo dirs).

---

## DES-012: Namespaced slash commands — no top-level deployment (SETTLED)

### Decision

All ethos slash commands use the plugin namespace (`/ethos:*`). No
commands are deployed to `~/.claude/commands/`. Every MCP tool has a
corresponding slash command.

### Commands

| Command | MCP Tool | Description |
|---------|----------|-------------|
| `/ethos:identity` | `identity` | Manage identities (whoami, list, get, create, iam) |
| `/ethos:talent` | `talent` | Manage talents (create, list, show, delete, add, remove) |
| `/ethos:personality` | `personality` | Manage personalities (create, list, show, delete, set) |
| `/ethos:writing-style` | `writing_style` | Manage writing styles (create, list, show, delete, set) |
| `/ethos:ext` | `ext` | Manage extensions (get, set, del, list) |
| `/ethos:session` | `session` | Manage session roster (roster, join, leave) |

Dev variants use `/ethos-dev:*` automatically via the plugin name in
`plugin.json`.

### Rationale

Top-level commands like `/skill`, `/session`, `/ext` occupy generic
names that will conflict with Claude Code built-ins or other plugins.
Plugin-namespaced commands (`/ethos:skill`) are collision-free and
clearly attributed.

The session-start hook previously copied command `.md` files to
`~/.claude/commands/` for top-level access. This is removed — the
plugin namespace is sufficient.

### Rejected alternatives

- **Top-level deployment** (`/skill`, `/personality`) — generic names
  will conflict. Claude Code or another plugin will claim `/skill`.
- **Prefix without namespace** (`/ethos-skill`) — inconsistent with
  the plugin namespace convention (`plugin:command`).
- **Selective top-level** (only deploy unique names) — still fragile.
  Any new plugin or Claude Code feature could claim the name.

## DES-013: Session-start hook must not write to shared directories (SETTLED)

### Incident — 2026-03-21 (8 hours lost across 2 engineers + 1 agent)

The ethos v0.7.0 session-start hook (`hooks/session-start.sh`) caused a
complete failure of top-level slash command discovery on every machine
where ethos was installed. All `/read`, `/who`, `/vox`, `/write`,
`/find`, `/lux`, and other top-level commands from `~/.claude/commands/`
disappeared. Two machines were affected. A third machine that never had
ethos v0.7.0 was unaffected.

### Root cause

The v0.7.0 session-start hook performed two destructive operations on
shared global state:

**1. Copied command files to `~/.claude/commands/`.**

```bash
for cmd_file in "$PLUGIN_ROOT/commands/"*.md; do
    dest="$COMMANDS_DIR/$name"
    cp "$cmd_file" "$dest"
done
```

This deployed 7 files (`ext.md`, `iam.md`, `personality.md`,
`session.md`, `skill.md`, `whoami.md`, `writing-style.md`) into the
shared `~/.claude/commands/` directory. Each file contained an
`allowed-tools` frontmatter entry referencing `mcp__plugin_ethos_self__*`
MCP tools.

When the ethos MCP server was not running (ethos not installed, or
installed but not active in the current repo), Claude Code's command
parser encountered `allowed-tools` entries referencing non-existent MCP
tools. Instead of skipping those individual files, the parser failed for
the **entire** `~/.claude/commands/` directory — killing discovery of
every top-level command from every plugin (biff, vox, quarry, beadle,
lux, dungeon).

This failure was **silent**. No error message. Commands simply
disappeared from the skill list.

The damage was **persistent**. Uninstalling the ethos plugin removed the
registry entry and cache but left the 7 copied files in
`~/.claude/commands/`. Every subsequent Claude Code session continued to
fail on those orphaned files. The only fix was manually deleting the 7
ethos files from `~/.claude/commands/`.

**2. Mutated `~/.claude/settings.json` via `jq`.**

```bash
jq --arg g "$PROD_GLOB" \
  '.permissions.allow = (.permissions.allow // []) + [$g]' \
  "$SETTINGS" > "$TMPFILE"
mv "$TMPFILE" "$SETTINGS"
```

This re-serialized the entire settings.json through `jq` to add a
single permission entry. The `jq` round-trip is a secondary risk — it
could re-order keys, normalize Unicode, or strip encoding details that
Claude Code's parser depends on. In this incident, `jq` mutation was
confirmed harmless (rewriting settings.json with identical content did
not fix the issue), but it remains a fragile pattern.

### Why diagnosis took 8 hours

1. **Silent failure.** Claude Code gave no error — commands just
   vanished. The failure mode (entire directory disabled) was
   disproportionate to the cause (7 bad files out of 35).

2. **Wrong initial hypothesis.** The first 4 hours were spent comparing
   plugin directory structures, command frontmatter, MCP server
   behavior, and plugin.json format between biff (working) and ethos
   (not working). This was the wrong layer — the ethos agent was trying
   to get `/ethos:whoami` to work when the actual damage was to
   `~/.claude/commands/`.

3. **Red herring: `jq` mutation.** 2 hours were spent investigating
   whether `jq`'s re-serialization of settings.json was the corruption
   vector. It was not.

4. **Persistent damage after uninstall.** Uninstalling the ethos plugin
   did not remove the copied command files, so the failure persisted
   across restarts and reinstalls, making it appear version-independent.

5. **Coincidence masking.** The ethos plugin installation, the
   `~/.claude/commands/` breakage, and the ethos agent's separate
   problem (plugin commands not appearing as namespaced skills) were
   three different issues that occurred simultaneously, creating
   confusion about which symptom belonged to which cause.

### Decision

**Deploy commands from an install script, not a session-start hook.**

Biff, Vox, Lux, and Quarry all deploy top-level slash commands to
`~/.claude/commands/` — and it works. The difference is **when** and
**how**:

| Project | Deploys commands via | Runs when | Guarantees |
|---------|---------------------|-----------|------------|
| Biff | `biff install` | Once, explicitly | Plugin installed first, MCP server registered |
| Vox | `vox install` | Once, explicitly | Plugin installed first, MCP server registered |
| Lux | `lux install` | Once, explicitly | Plugin installed first, MCP server registered |
| Quarry | `quarry install` | Once, explicitly | Plugin installed first, MCP server registered |
| **Ethos** | **session-start hook** | **Every session** | **None** |

The install scripts ensure the plugin and MCP server are registered
before deploying commands. By the time command files land in
`~/.claude/commands/`, the `allowed-tools` entries they reference
(`mcp__plugin_biff_tty__*`, etc.) resolve to real MCP tools.

The ethos session-start hook had no such guarantee. It copied command
files on every session start regardless of whether the ethos MCP server
was active. When the MCP tools didn't resolve, Claude Code's command
parser failed for the entire `~/.claude/commands/` directory.

Specifically:

1. **Command deployment belongs in `ethos install`, not in the
   session-start hook.** Follow the established pattern from biff, vox,
   lux, quarry: install script registers the plugin, deploys commands,
   and sets permissions — once.

2. **Do not mutate `~/.claude/settings.json` from a hook.** Permission
   entries for plugin MCP tools should be set by the install script. A
   hook that re-runs `jq` on settings.json every session is fragile
   and unnecessary.

3. **Session-start hooks may read global state but not write it.** A
   plugin's session-start hook may read `settings.json`, read identity
   files, set environment variables, and emit `hookSpecificOutput`. It
   must not create, modify, or delete files outside `$PLUGIN_ROOT` or
   the plugin's own data directory (`~/.punt-labs/ethos/`).

### Fix applied

1. Removed the 7 ethos command files from `~/.claude/commands/` on
   affected machines.
2. Removed the `jq` settings mutation from the session-start hook
   (refactor/namespaced-commands branch).
3. Removed the command-copy logic from the session-start hook
   (refactor/namespaced-commands branch, DES-012).
4. Ethos must implement `ethos install` following the biff/vox/lux/quarry
   pattern before deploying top-level commands again.

### Rules for session-start hooks

| Allowed | Forbidden |
|---------|-----------|
| Read `~/.claude/settings.json` | Write `~/.claude/settings.json` |
| Read `~/.claude/commands/*.md` | Create/delete files in `~/.claude/commands/` |
| Write to `~/.punt-labs/ethos/` | Write to `~/.claude/` (any path) |
| Emit `hookSpecificOutput` JSON | Run `jq` on shared config files |
| Set environment variables | Modify `installed_plugins.json` |
| Call `ethos` CLI subcommands | Call `claude plugin install/uninstall` |

---

## DES-014: Rename `skill` → `talent` system-wide (SETTLED)

### Problem

`skill` is a reserved name in Claude Code. When a plugin command file
is named `skill.md`, Claude Code's command parser fails for the
**entire** `commands/` directory — silently breaking all plugin commands
from that plugin. This was discovered during DES-012 when
`/ethos:skill` poisoned the autocomplete for all 10 ethos commands.

### Decision

Rename `skill` to `talent` everywhere:

- MCP tool: `skill` → `talent`
- CLI subcommand: `ethos skill` → `ethos talent`
- Command file: `skill.md` → `talent.md`
- Identity YAML field: `talents:` → `talents:`
- Identity struct fields: `Skills` → `Talents`, `SkillContents` → `TalentContents`
- Attribute Kind: `attribute.Skills` → `attribute.Talents`
- Storage directory: `~/.punt-labs/ethos/skills/` → `~/.punt-labs/ethos/talents/` (attribute directory, not Claude Code skills)
- Sidecar: `internal/seed/sidecar/skills/` → `internal/seed/sidecar/talents/`

### Breaking change

The identity YAML schema changes from `talents:` to `talents:`.
Existing identity files must be updated manually. No external users
exist — this is acceptable.

### Rejected alternatives

- **Keep `skill` internally, only rename the command file** — creates a
  confusing split where the command is `/ethos:talent` but the MCP tool
  is `skill`, the CLI is `ethos skill`, and the storage is `skills/`.
- **Use `skills` (plural) for the command file** — might avoid the
  reserved name conflict, but doesn't address the root issue. Claude
  Code could reserve `skills` next.
- **Use a different word only for the command file** — same split
  problem as option 1.

## DES-015: Plugin development via cache symlink (SETTLED)

### Problem

Claude Code plugins are loaded from a versioned cache directory at
`~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/`. During
development, the cached snapshot is stale — changes to commands, hooks,
skills, and agents in the working tree are not reflected until the
plugin is re-published and re-fetched.

The binary (Go/Python) can be rebuilt and installed independently
(`make install`), but plugin prompt files (`.md` commands, hook shell
scripts, skill definitions) are only read from the cache. This creates
a two-speed problem: MCP tool changes take effect after `make install`
and restart, but prompt changes require manually copying files into
the cache or re-publishing.

### Decision

Add `make dev` and `make undev` targets to the Makefile:

- `make dev` — builds and installs the binary, then replaces the
  plugin cache version directory with a symlink to the working tree.
  The original cache is preserved as `<version>.bak`.
- `make undev` — removes the symlink and restores the original cache
  from backup.

```bash
# Enter dev mode: binary installed, plugin cache → working tree
make dev

# Exit dev mode: restore original cache
make undev
```

This makes all prompt files (commands, hooks, skills, agents) live-
editable during development. Combined with `make install` for binary
changes, the full development loop is:

1. Edit Go code → `make dev` (rebuilds binary + ensures symlink)
2. Edit prompt files → restart Claude (no build needed, symlink is live)
3. Edit MCP tools → `make dev` + restart Claude

### Scope

This pattern applies to any Claude Code plugin that has a compiled
binary alongside prompt files. It is not ethos-specific — biff, vox,
quarry, lux, and z-spec all have the same two-speed problem.

### Version resolution

The symlink uses the latest version directory found in the cache
(`ls -1 | sort -V | tail -1`). This matches the version Claude Code
resolved from the marketplace registry. No synthetic "dev" version is
used — Claude Code would not look for a version the registry doesn't
advertise.

### Rejected alternatives

- **`--plugin-dir .`** — ephemeral (one session), must be passed every
  time. `make dev` persists until `make undev`.
- **Version bump to `n+1`** — Claude Code resolves versions from the
  marketplace registry. A version the registry doesn't know about would
  not be loaded.
- **Copy files into cache on every build** — fragile, easy to forget,
  and creates drift between source and cache. Symlink is atomic.
- **Use `dev` as the version string** — same problem as `n+1`: Claude
  Code won't look for it.

## DES-016: Hook business logic in Go, not shell (SETTLED)

### Problem

Ethos hook shell scripts contained 387 lines of business logic:
identity resolution, session roster management, JSON parsing via
grep/cut, and per-tool output formatting. This violated
punt-kit/standards/hooks.md §3 ("shell scripts are thin gates") and
created 4 additional problems:

1. `suppress-output.sh` (205 lines) checked old tool names after the
   MCP tool consolidation — two-channel display was broken for all
   consolidated tools.
2. `session-start.sh` forked the ethos binary 3-4 times per session
   start, adding cold-start latency.
3. JSON extraction used brittle `grep -o | cut -d'"'` instead of
   structured parsing.
4. No per-tool sentinel check — hooks ran even when ethos was not
   configured for the project.

### Decision

Move all hook business logic into Go (`internal/hook/` package) and
reduce shell scripts to 6-line thin gates that check preconditions
and delegate to `ethos hook <event>`.

The Go handlers read JSON from stdin using a non-blocking reader with
deadline-based timeout (avoiding the open-pipe-no-EOF hang), call
identity/session/resolve packages directly (no binary forks), and
emit structured JSON to stdout.

### Result

- Shell: 387 lines → 30 lines (5 scripts × 6 lines)
- Binary forks per session-start: 4 → 0
- Two-channel display: fixed for all 6 consolidated tools × 28 methods
- Open-pipe regression test: included

### Rejected alternatives

- **Keep logic in shell, fix tool names only** — leaves grep/cut
  parsing, multi-fork cold start, and missing sentinel check unfixed.
- **Use jq for all JSON parsing in shell** — better than grep/cut but
  still violates the "thin gate" standard. jq is also a runtime
  dependency that may not be installed.
- **Use a lightweight Go binary (`ethos-hook`)** — Go compiles to a
  single binary. Adding a separate entry point creates two binaries
  to install and version. The `ethos hook` subcommand achieves the
  same isolation without the operational cost.

## DES-017: Session PID keying via ancestor walk, not PPID (SETTLED)

### Problem

Session roster files are keyed by PID: `sessions/current/{pid}` maps
a Claude Code process to its active session ID. The MCP server
discovers its session by calling `process.FindClaudePID()`, which
walks the process tree to find the topmost `claude` ancestor.

The original shell hooks and the initial Go port both used
`os.Getppid()` (the immediate parent PID) to key the current session
file. This produces a different PID than `FindClaudePID()` because
Claude Code interposes intermediate processes between the main
process and hook/MCP subprocesses:

```text
Claude Code (PID 19147)        ← FindClaudePID() returns this
├── shell (hook runner)
│   └── ethos hook session-start   ← os.Getppid() returns shell PID
└── claude (MCP manager)
    └── ethos serve                ← FindClaudePID() returns 19147
```

The hook writes `sessions/current/{shell-PID}`, but the MCP server
looks up `sessions/current/{19147}` — file not found, session tools
fail with "no active session."

This is the same issue biff documented in DES-011a: `os.getppid()` is
not a stable session identifier when Claude Code's process tree has
intermediate layers.

### Decision

Use `process.FindClaudePID()` in all hook handlers that read or write
PID-keyed session state. This matches the MCP server's discovery
mechanism and produces a consistent key regardless of how many
intermediate processes exist.

### Stale PID files

The `sessions/current/` directory accumulates PID files from previous
sessions that were not cleaned up (crashes, forced exits, sessions
where the SessionEnd hook did not fire). Filed as ethos-dl9.
`ethos session purge` can clean these up manually.

## DES-018: Repo-scoped identity configuration (SETTLED)

### Problem

All identity data lived in `~/.punt-labs/ethos/` — user-global and
untracked. Team identities, talents, personalities, and writing styles
were invisible to other team members, not version-controlled, and lost
on reinstall. Different repos couldn't have different teams.

### Decision

Two-layer resolution: repo-local (`.punt-labs/ethos/`) → user-global
(`~/.punt-labs/ethos/`). Identity YAML is atomic per layer — no
field-level merging. Extensions always resolve from user-global.

`LayeredStore` wraps two `Store` instances and implements the
`IdentityStore` interface. Callers don't know about layers.

| Layer | Location | Git-tracked | Contains |
|-------|----------|-------------|----------|
| Repo-local | `.punt-labs/ethos/` | Yes | Identities, talents, personalities, writing styles |
| User-global | `~/.punt-labs/ethos/` | No | Extensions (PII/credentials), sessions, fallback identities |

### Rejected alternatives

- **Environment variable for root path** — doesn't solve layering.
  User must choose one or the other, not both.
- **Single store with search path list** — effectively what LayeredStore
  is, but arbitrary paths are harder to reason about than explicit
  repo/global semantics.
- **PII overlay mechanism** — eliminated by DES-019 (voice as extension).

## DES-019: Voice as extension, not core field (SETTLED)

### Problem

The `voice` field in identity YAML contained provider credentials
(voice_id). This was the only PII in the identity schema, which
complicated the repo-scoped design — identity files couldn't be
committed without a field-level overlay mechanism.

### Decision

Move voice to `ext/vox`, same as GPG keys in `ext/beadle`. Identity
YAML has zero PII. Extensions always live in user-global.

Auto-migration: on Load, if a legacy `voice:` key exists in the YAML,
its contents are written to `ext/vox` and stripped from the identity
file. `LayeredStore` redirects repo-layer voice migrations to the
global store.

### Breaking change

The `Voice` struct and field are removed from `Identity`. Consumers
must read voice config from `ext/vox` via `ExtGet`. The MCP `identity`
tool's responses include ext data, so consumers get voice bindings
through the same channel. Vox does not yet use ethos — no external
coordination needed.

## DES-020: MCP tool results as formatted text, not raw JSON (SETTLED)

### Problem

MCP tool results were returned as raw JSON in `additionalContext`. This
wastes LLM context tokens — the model receives serialized JSON with
structural overhead (braces, quotes, escaped characters) that it must
parse mentally to extract the relevant information. In multi-step
workflows touching several tools, raw JSON responses can consume
50-150K tokens the model never needed to see.

### Decision

MCP tool results return **formatted text** (field lists, columnar tables,
markdown) as the primary content. Raw JSON is never sent to
`additionalContext`. The PostToolUse hook formats all output for LLM
consumption.

Two-channel display pattern:

- **Panel** (`updatedMCPToolOutput`): count summary or compact display
- **Context** (`additionalContext`): formatted text — same field list,
  table, or markdown the panel shows, but potentially with more detail

This matches biff's approach, which returns pre-formatted plain text
with unicode alignment characters. The model reads it directly without
parsing overhead.

### Evidence

- [MCP GitHub Discussion #529](https://github.com/orgs/modelcontextprotocol/discussions/529):
  MCP maintainers advise against JSON for most LLM tasks. Text is "more
  conducive to model understanding, has a lower probability of error."
- [Context window analysis](https://www.apideck.com/blog/mcp-server-eating-context-window-cli-alternative):
  raw JSON from multi-step MCP workflows consumes 50-150K tokens the
  model never needed to see.
- Biff's production experience: pre-formatted text with unicode columns
  works well — agents read it accurately without JSON parsing confusion.

### When raw JSON is appropriate

- `--json` flag on CLI commands (human or script consuming structured data)
- MCP `structuredContent` field (host application processing before inference)
- Direct API consumption by other programs (Go library)

### Rejected alternatives

- **Always return JSON, let the model parse it** — wastes context tokens,
  increases error rate. The model is reading text, not executing code.
- **Return both JSON and text** — doubles the response size. The
  PostToolUse hook already has access to the raw JSON if needed.
- **Let each tool decide** — inconsistency across tools. The standard
  must be uniform: all tools return formatted text through the hook.

## DES-021: Repo config at .punt-labs/ethos.yaml (SETTLED)

**Decision**: Repo-level ethos config lives at `.punt-labs/ethos.yaml`, next to
the `.punt-labs/ethos/` directory (which may be a submodule or local data).

**Reasoning**: The team submodule at `.punt-labs/ethos/` contains shared identity
data (identities, personalities, writing styles, talents, roles, teams). Repo-
specific config (`agent: claude`, `team: engineering`) cannot live inside the
submodule — submodule contents are controlled by the upstream repo, not the
consumer. Moving config to a sibling file decouples it from the submodule.

**Rejected alternatives**:

- **Per-repo config files inside the team repo** (e.g., `ethos-config.yaml`,
  `biff-config.yaml` committed to `punt-labs/team`) — centralizes config
  management but couples all repos to a single commit cycle. Viable for
  org-wide defaults but doesn't support repo-specific overrides.
- **Config outside .punt-labs/** (e.g., `.ethos.yaml` at repo root) — pollutes
  the repo root with tool-specific dotfiles.
- **Single .punt-labs/config.yaml with sections** — cleaner for multi-tool config
  but introduces a shared file that multiple tools must coordinate on.

## DES-022: Extension-provided session context (SETTLED)

**Status**: Settled. Implemented in PR #135.

### Problem

`BuildMemorySection` in `internal/hook/memory.go` hardcodes quarry-specific
knowledge: collection names, memory types (fact, observation, procedure,
opinion), slash commands (`/find`, `/remember`), and MCP tool parameters.
This violates DES-008's principle that ethos must not know about its
consumers.

The same problem will recur for every consumer that wants session context.
Beadle would need `BuildEmailSection`. Biff would need `BuildMessagingSection`.
Each adds consumer-specific Go code to ethos — exactly the coupling DES-008
was designed to prevent.

### Context

Quarry works without ethos. It indexes files, answers semantic queries, and
manages collections independently. When combined with ethos, quarry gains
one capability it cannot provide alone: **persistent agent memory**. Ethos
tells quarry *who* the agent is; quarry gives the agent memories and expertise
scoped to that identity. Without ethos, quarry is a search engine. With
ethos, quarry becomes an agent's personal knowledge base.

The current implementation works — agents get memory instructions at session
start and after compaction. But ethos achieves this by knowing what quarry is,
how its collections work, and what slash commands it exposes. That knowledge
belongs to quarry, not ethos.

### Design

Each extension can provide a `session_context` field containing markdown
instructions that ethos emits verbatim at session start and before context
compaction. Ethos iterates over all extensions, collects `session_context`
values, and appends them after the persona block. No parsing, no
interpretation, no consumer-specific code.

**Extension with session context:**

```yaml
# claude.ext/quarry.yaml
memory_collection: memory-claude
session_context: |
  ## Memory

  You have persistent memory stored in quarry, a local semantic
  search engine. Your memories survive across sessions and machines.

  ### Working Memory

  Collection: "memory-claude"

  To recall prior knowledge:
    /find <query>

  To persist something you learned:
    /remember <content>

  Memory types:
  - fact: objective, verifiable information
  - observation: neutral summary of an entity or system
  - procedure: how-to knowledge
  - opinion: subjective assessment with confidence
```

```yaml
# claude.ext/beadle.yaml
email: claude@punt-labs.com
session_context: |
  ## Email

  You have email via beadle. Your address is claude@punt-labs.com.
  Send recap emails to jim@punt-labs.com after merging PRs.
  Check inbox: /inbox
  Send mail: /mail <recipient> <subject>
```

```yaml
# claude.ext/biff.yaml
handle: claude-puntlabs
session_context: |
  ## Messaging

  You have team messaging via biff.
  Start every session with /loop 2m /biff:read.
  Use /who, /finger, /write, /wall for coordination.
```

**Hook behavior change:**

Replace `BuildMemorySection(ext, handle)` with `BuildExtensionContext(ext)`:

```go
func BuildExtensionContext(ext map[string]map[string]string) string {
    var sections []string
    for _, ns := range sortedKeys(ext) {
        if ctx, ok := ext[ns]["session_context"]; ok && ctx != "" {
            sections = append(sections, strings.TrimRight(ctx, "\n"))
        }
    }
    return strings.Join(sections, "\n\n")
}
```

Ethos reads `session_context` from every extension namespace, emits them
in sorted order, and moves on. The function is ~10 lines and has zero
knowledge of any consumer.

### What Each Side Owns

| Concern | Owner |
|---------|-------|
| Identity (who the agent is) | Ethos |
| Extension storage and iteration | Ethos |
| Session context content | Each consumer |
| Collection binding, memory types, slash commands | Quarry |
| Email address, sending protocol | Beadle |
| TTY names, messaging commands | Biff |

### What This Eliminates

- `internal/hook/memory.go` — entire file replaced by generic iteration
- All future `Build*Section` functions — never written
- The DES-008 violation in the current quarry integration

### What This Preserves

- Quarry works without ethos (no change)
- Quarry + ethos gives agents persistent memory (no change in capability)
- Extension schema (DES-008) unchanged — `session_context` is just another key
- Validation constraints apply: max 4096 bytes per value

### Migration

1. Move quarry instruction text from `memory.go` into `ext/quarry.yaml`
   `session_context` field for each agent identity that has quarry configured.
2. Replace `BuildMemorySection` calls with `BuildExtensionContext` in
   `session_start.go` and `pre_compact.go`.
3. Delete `internal/hook/memory.go` and its tests.
4. Update quarry-integration.md to reflect the new ownership boundary.

### Rejected Alternatives

- **Keep `BuildMemorySection` and add `Build*Section` per consumer** —
  scales linearly with consumers, each adding ethos code and releases.
  Violates DES-008.
- **Extension provides a script/command that ethos executes to generate
  context** — over-engineered. Static markdown covers all known use cases.
  If dynamic generation is needed later, a tool can write its
  `session_context` at session start via `ethos ext set`.
- **Separate `session_start_context` and `compact_context` fields** —
  the content is identical in both hooks today. If they diverge, a single
  `session_context` field with an optional `compact_context` override is
  simpler than two required fields.
- **Raise the 4096-byte value limit for `session_context`** — the current
  quarry instructions are ~600 bytes. If a consumer needs more, we can
  revisit the limit or design additional mechanisms at that time. Don't pre-solve.

## DES-023: Sessions are local-only — no cross-machine state (SETTLED)

**Status**: Settled.

### Decision

Ethos sessions track participants on a single machine. Cross-machine
presence and routing is biff's responsibility. Ethos does not attempt
to aggregate, synchronize, or display session state across hosts.

### What ethos provides across machines

Identity definitions travel via git (repo-local identities in
`.punt-labs/ethos/identities/`) or the global filesystem
(`~/.punt-labs/ethos/identities/`). Teams, personalities, writing
styles, and talents are shared the same way. This is reuse of
definitions, not replication of runtime state.

### What ethos does not provide across machines

- Who is active where (biff `who`)
- Message routing between agents (biff `write`)
- Presence and idle tracking across hosts (biff `finger`)
- Session roster aggregation across machines

### Reasoning

The ACTIVE column in `ethos identity list` was removed because it
only showed local session state, missing identities active on other
hosts. Attempting to bridge this gap would duplicate biff's
functionality. The two systems have clear boundaries: ethos owns
identity (who someone is), biff owns presence (where someone is now).

### Rejected alternatives

- **Ethos aggregates sessions across machines** — requires a
  transport layer (network, shared filesystem, or relay). Biff
  already has this. Building a second one violates separation of
  concerns.
- **Ethos sessions become the source biff reads** — interesting
  future direction, but creates a dependency from biff to ethos.
  Current design keeps them independent (DES-001 sidecar principle).

## DES-024: Session schema — repo, host, and participant join time (SETTLED)

**Status**: Settled. Implemented in ethos-y1r.

### Decision

Extend the session roster schema with three fields:

1. `repo` on Roster — `<org>/<name>` from git remote, written once at
   session creation by the SessionStart hook.
2. `host` on Roster — short hostname, written once at session creation.
3. `joined` on Participant — ISO timestamp, written when the participant
   joins via `iam` or `join`.

### Reasoning

`ethos session list` currently shows UUIDs, a participant count, and a
primary name. Adding `repo` makes the list actionable for debugging
("which session is in ethos?"). Adding `host` aligns with biff's data
model and prepares for a future where session files could be shared.
Adding `joined` per participant enables per-persona LOGIN times,
matching biff's `last` output format.

Display alignment (date formatting, short session IDs, table style)
is a prerequisite tracked in ethos-ns1.

### Format alignment with biff

Ethos and biff are designed by the same team. Where concepts are
identical (repo, host, timestamps, table layout), the display format
must match. Timestamps display as `Sun Mar 29 14:22` (biff LOGIN
format), not ISO. Duration is computed at display time, not stored.

### Rejected alternatives

- **Store duration instead of computing it** — duration changes every
  second for active sessions. Computing from `started` (or `joined`)
  is simpler and always accurate.
- **Store repo as full path** — `<org>/<name>` is portable and matches
  biff. Full paths are machine-specific and leak filesystem layout.

## DES-025: Remove ACTIVE column from identity list (SETTLED)

**Status**: Settled. Implemented in PR #140.

### Decision

Remove the ACTIVE column from `ethos identity list` (CLI, MCP, and
hook output). Do not replace it.

### Reasoning

The column used `process.FindClaudePID()` to check the current
session's roster. This failed in three ways: (1) returned nothing
when run outside Claude Code, (2) only showed participants of the
current session, not all local sessions, (3) could never show
identities active on other machines.

`ethos session list` is the correct place for session-level
information. `ethos identity list` shows identity definitions —
static data, not runtime state. Mixing the two produced a column
that was wrong most of the time.

## DES-026: Generate agent definitions from identity data (SETTLED)

**Status**: Settled. Implemented in PR #146.

### Decision

The SessionStart hook generates `.claude/agents/<handle>.md` for every
agent team member from ethos component data (identity, personality,
writing-style, role). The main session agent (from `ethos.yaml` config)
and human identities are skipped.

### Reasoning

Agent definition files were hand-written copies of ethos identity data.
When ethos data changed (personality update, role change, new talent),
the agent files went stale silently. Only automation is reliable.

The SessionStart hook already assembles the primary agent's identity from
these components for context injection. Generating sub-agent files uses
the same data through the same path — one source of truth, no drift.

### Key design points

- **Tools come from the role.** Each role YAML defines a `tools` list
  that maps to the agent frontmatter. Implementation roles get
  Read/Write/Edit/Bash/Grep/Glob. Review roles get Read/Grep/Glob/Bash.
- **Idempotent writes.** Content is compared before writing. Identical
  files are not rewritten, preserving mtime.
- **Non-blocking.** Generation failure logs to stderr and does not block
  the session. But total failure (zero of N agents generated) returns an
  error so the caller can surface it.
- **No staleness by mod time.** Earlier designs checked file timestamps.
  The simpler approach is content comparison — if the content matches,
  skip the write. This avoids clock skew and filesystem granularity issues.

### Rejected alternatives

- **Copy from `.punt-labs/ethos/agents/*.md`** — still requires hand-written
  source files. Solves the copy problem but not the drift problem.
- **Generate at install time** — `make install` runs once; identity data
  changes between installs. SessionStart runs every session, guaranteeing
  freshness.
- **Staleness by mtime comparison** — fragile across filesystems and git
  operations that reset timestamps. Content comparison is simpler and
  correct.

## DES-027: Teams and roles as first-class concepts (SETTLED)

**Status**: Settled. Implemented in v2.2.0.

### Problem

Ethos tracked individual identities but had no concept of how they
relate to each other. An identity could declare a personality and
talents, but not its organizational role, who it reports to, which
repos it works on, or which other identities it collaborates with.

Without teams and roles, the PreCompact and SessionStart hooks could
inject a single agent's persona but not its working context — who its
teammates are, what each teammate is responsible for, and how delegation
flows between them. Agent teams (multiple Claude Code processes
collaborating) need this context to coordinate work correctly.

### Decision

Add Team and Role as first-class ethos concepts with dedicated packages,
CLI commands, MCP tools, and layered stores.

**Role** — a reusable definition of responsibilities and tool permissions:

```yaml
name: go-specialist
model: sonnet
responsibilities:
  - Go implementation following Kernighan's principles
  - Tests with race detection and full coverage
permissions:
  - approve-merges
tools:
  - Read
  - Write
  - Edit
  - Bash
  - Grep
  - Glob
```

The `model` field specifies the Claude model for agents in this role (opus, sonnet, haiku, inherit, or a full claude-* ID). Empty means inherit. Validated by `ValidateModel()` on save and load.

**Team** — binds identities to roles for a set of repositories, with
a collaboration graph:

```yaml
name: engineering
repositories:
  - punt-labs/ethos
  - punt-labs/biff
members:
  - identity: claude
    role: coo
  - identity: bwk
    role: go-specialist
collaborations:
  - from: go-specialist
    to: coo
    type: reports_to
```

Valid collaboration types: `reports_to` (hierarchical reporting), `collaborates_with` (peer collaboration), `delegates_to` (work delegation).

Both use the same layered resolution as identities: repo-local
(`.punt-labs/ethos/`) overrides user-global (`~/.punt-labs/ethos/`).

### Invariant enforcement

Referential integrity is enforced on write, derived from the Z
specification (`docs/teams.tex`):

- Every team member must reference a valid identity handle and role name
- Every team must have at least one member
- Collaboration roles must be filled by team members
- No self-collaboration (a role cannot collaborate with itself)
- Roles referenced by teams cannot be deleted
- No duplicate identity/role assignments within a team
- Dangling collaborations are cleaned up when members are removed

### Integration with hooks

The repo config (`.punt-labs/ethos.yaml`) links to a team via the
`team:` field. SessionStart and PreCompact hooks read this to build
team context — member names, roles, responsibilities, and collaboration
graph — injected alongside the persona block.

DES-026 uses the role's `tools` field to generate agent definition
frontmatter, closing the loop: identity defines who, role defines
permissions, generated agent file combines both.

### Rejected alternatives

- **Tags on identities instead of roles** — no reuse, no referential
  integrity, no collaboration graph. Tags are labels; roles are
  structured definitions.
- **External config file listing team members** — duplicates identity
  data, drifts from the registry. Teams should reference identities,
  not copy them.
- **Flat member list without collaboration graph** — loses the
  delegation structure that agent teams need. Who reports to whom
  determines how work flows.

## DES-028: Persona animation — behavioral injection across session lifecycle (SETTLED)

**Status**: Settled. Implemented across v2.1.0–v2.3.0. Design doc at
`docs/persona-animation.md`.

### Problem

Ethos declared identity — personality, writing style, talents — as
static data on disk. The SessionStart hook confirmed the identity with
a one-line message: `"Active identity: Claude Agento (claude)."` That
is a name tag, not behavioral context. The personality, writing style,
and talent content were never injected into the session. Three failure
modes resulted:

1. **Compaction drift** — personality from early turns gets summarized
   away during context compression. The agent loses its behavioral
   instructions mid-session.
2. **Generic subagents** — SubagentStart joined the roster but injected
   zero behavioral content. A `bwk` subagent with a Kernighan
   personality acted identically to a generic agent.
3. **No reinforcement** — writing style drifted over long sessions
   because there was no mechanism to re-inject behavioral context.

### Decision

Inject full persona content at three lifecycle hooks. The agent
definition (`.claude/agents/*.md`) defines *what* the agent does. The
ethos identity defines *who* the agent is. Hooks connect the two.

**Layer 1 — SessionStart**: load the primary agent's identity with full
content resolution. Assemble a structured persona block (personality
content, writing style content, talent slugs, role, team context) and
emit it as session context. Replaces the one-line confirmation.

**Layer 2 — PreCompact**: re-emit the full persona block before context
compression. This preserves behavioral instructions through compaction.
A condensed version was tried and rejected — source files are the
authority on content length, and truncation caused behavioral drift.

**Layer 3 — SubagentStart**: auto-match the subagent's `agent_type` to
an identity handle. If matched, inject that identity's persona content
into the subagent's context at spawn. Subagent agent definitions no
longer need manual `ethos show` instructions.

SessionStart and PreCompact also emit extension `session_context` values from all extension namespaces (per DES-022). This is separate from the persona block — each tool provides its own context (quarry provides memory instructions, beadle provides email config, biff provides messaging config). Ethos iterates extensions and appends session_context values after the persona block, with zero consumer-specific code.

Talent slugs are listed but not expanded inline — full talent content
is available on demand via `/ethos:talent show <slug>`. This keeps the
persona block within context budget (~100–150 lines for personality +
writing style, ~600 lines with team context).

### What persona animation does NOT do

- Override CLAUDE.md — the persona is additive context. CLAUDE.md wins
  on conflicts.
- Change the agent definition format — `.claude/agents/*.md` files
  remain the same.
- Require ethos — if ethos is not installed, no persona is injected.
  The agent works normally. Ethos is a sidecar (DES-001).
- Inject talent content — talents are listed as slugs to stay within
  context budget.

### Rejected alternatives

- **Inject personality at SessionStart only, skip PreCompact** —
  personality gets summarized away during compaction. Tested and
  confirmed: agents lose their writing style within 2–3 compaction
  cycles.
- **Condensed persona block at PreCompact** — tried a 4-line summary.
  Behavioral drift returned because the summary lost the specific
  rules (sentence length limits, banned patterns, calibration
  instructions). Full block is ~600 lines including team context;
  fits within PreCompact budget.
- **Subagents call `ethos show` themselves** — requires every agent
  definition to include identity-loading instructions. Creates drift
  between the agent file and the identity registry. Hook injection is
  automatic and cannot go stale.

## DES-029: Shell reads stdin, not Go — Linux pipe fd inheritance (SETTLED)

**Status**: Settled. Root cause confirmed 2026-04-04 after 10+ restart
cycles on Linux.

### Problem

Ethos hooks hung on Linux, preventing session creation, persona
injection, and all hook-driven features. The same code worked on macOS.
The failure was silent — no log output, no error, hooks simply produced
no result. Diagnosis took an extended session because the symptoms
pointed at multiple layers (plugin loading, cache staleness, process
discovery) before the actual cause was isolated.

### Root Cause Chain

Claude Code's hook process model creates an fd inheritance chain that
Go cannot read reliably on Linux.

**1. Claude Code spawns hooks via `/bin/sh -c`.**

`executeHooks()` in Claude Code's `hooks.ts` calls
`spawn(command, [], { shell: true })`. On Unix, `shell: true` means
the actual process tree is:

```text
Claude Code runtime
  └── /bin/sh -c "hooks/session-start.sh"
       └── bash hooks/session-start.sh  (shebang)
            └── ethos hook session-start
```

Claude Code writes one JSON blob plus `\n` to the shell process's
stdin, then calls `stdin.end()`. The ethos binary inherits fd 0 from
an intermediate `/bin/sh` process, not from a direct pipe. This fd
has passed through Node.js's `child_process.spawn()` → `/bin/sh` →
bash → Go — a materially different environment from `pipe()` +
`fork()` + `exec()` test harnesses.

**2. Go cannot read the inherited fd reliably on Linux.**

The inherited fd 0 does not support `SetReadDeadline` on Linux —
Go's `os.NewFile` cannot register it with epoll. The fallback
`readWithTimeout` uses a goroutine with `f.Read`, but `f.Read` on
this specific inherited fd hangs on Linux. Standard test harnesses
(Go `os.Pipe`, `syscall.Pipe`, C `pipe()` + `fork()` + `dup2()` +
`exec()`, FIFOs) all produce fds where `f.Read` works correctly.
The production fd from Claude Code's `/bin/sh -c` intermediate does
not. The exact kernel-level property that differs is undiagnosed.

On macOS, the same fd chain works because kqueue handles inherited
pipe fds that epoll does not.

**3. Silent failure — no diagnostic output.**

The hook process hung indefinitely on `f.Read`. Claude Code's hook
timeout killed it before stderr flushed. The empty `hook-errors.log`
made the failure appear as though hooks never fired, leading
investigation toward plugin loading, cache staleness, and hook
discovery — all dead ends. Replacing ethos's hooks with quarry's
(which don't read stdin) proved hooks did fire; the hang was in
stdin reading.

### Decision

**Shell scripts read stdin, not Go.** Treat Claude Code hook stdin as
a shell-facing transport. Bash reads the inherited fd, stores the
bytes, and forwards over a fresh pipe that Go can read reliably:

```bash
HOOK_INPUT=""
IFS= read -r -t 1 HOOK_INPUT 2>/dev/null || true
printf '%s\n' "$HOOK_INPUT" | ethos hook session-start 2>>... || true
```

This replaces `ethos hook session-start < /dev/stdin`. The Go binary
receives stdin from `printf` via a fresh pipe (not inherited), which
`ReadAll` handles correctly on all platforms.

### Why This Works

- `IFS= read -r -t 1` is a bash built-in with 1-second timeout —
  reads the inherited fd in bash (not Go), preserving whitespace.
- `printf '%s\n'` (not `echo`) avoids shell-specific output quirks
  and creates a Go-managed pipe where `ReadAll` gets EOF. No blocking.
- The 1-second timeout matches the existing `ReadInput` timeout in Go.
- If stdin is empty or the timeout fires, `HOOK_INPUT` is empty and
  the Go binary receives an empty map — same as the previous timeout
  behavior.
- `read -t` returns non-zero on timeout even when data was read (no
  trailing newline). `|| true` prevents the non-zero exit from
  clearing `HOOK_INPUT`. The variable must be initialized before
  `read`, not in the `||` fallback.

### What This Preserves

- `internal/hook/stdin.go` still handles both deadline-capable and
  non-deadline fds correctly — the `readWithTimeout` fallback remains
  as defense-in-depth for any caller that passes an inherited fd
  directly.
- Subprocess integration tests (`internal/hook/subprocess_test.go`)
  still test the Go binary with inherited pipe fds to catch future
  regressions in the Go layer.
- The hook shell scripts remain thin gates — 3 lines of logic
  (read, echo, exec).

### Rejected Alternatives

- **`< /dev/stdin` with Go-side timeout** — the Go `readWithTimeout`
  fix works in isolation (subprocess tests pass) but fails in the
  real Claude Code execution environment. The fd inheritance behavior
  differs between test pipes (Go-managed, epoll-registered) and
  production pipes (inherited, not pollable). Testing cannot fully
  reproduce the production fd state.
- **`syscall.SetNonblock` + poll loop in Go** — invasive, requires
  `golang.org/x/sys/unix` dependency for `Poll`, and fights Go's
  runtime fd management. Shell-level timeout is simpler and proven.
- **Don't read stdin at all** — ethos needs `session_id` from Claude
  Code's hook payload to create session rosters. Quarry and biff don't
  need stdin data; ethos does.
- **Environment variable for session_id** — Claude Code does not
  expose session_id as an env var. Stdin JSON is the only source.

### Cross-Project Pattern

Any Claude Code plugin hook that reads stdin via `< /dev/stdin` on
Linux is vulnerable to the same hang. The safe pattern:

```bash
# SAFE: bash reads with timeout, forwards over fresh pipe
HOOK_INPUT=""
IFS= read -r -t 1 HOOK_INPUT 2>/dev/null || true
printf '%s\n' "$HOOK_INPUT" | my-binary hook event 2>>log || true

# UNSAFE: inherited pipe fd from /bin/sh -c blocks Go's Read on Linux
my-binary hook event < /dev/stdin 2>>log || true
```

Note: Claude Code spawns hooks via `/bin/sh -c "<command>"`. The hook
script's shebang (`#!/usr/bin/env bash`) means the binary's fd 0 has
passed through `/bin/sh` → bash → binary. Use `printf` not `echo` for
the forwarding pipe (shell portability). Initialize `HOOK_INPUT=""`
before `read`, not in `|| HOOK_INPUT=""` — `read -t` returns non-zero
on timeout even when data was read.

This supersedes the DES-009 guidance on stdin handling. DES-009
identified `INPUT=$(cat)` as the blocking pattern and recommended
`read -r -t 1` in bash. DES-029 confirms that even Go-native
workarounds (`SetReadDeadline`, `readWithTimeout`) fail on Linux
inherited pipe fds in production, and the bash `read -t` approach
is the only reliable solution across platforms.

## DES-030: Subprocess integration tests for hooks (SETTLED)

**Status**: Settled. Implemented 2026-04-04.

### Problem

All hook tests in `internal/hook/*_test.go` used `bytes.Reader` or
`os.Pipe()` for stdin. These are Go-managed objects where
`SetReadDeadline` works on all platforms. The Linux pipe hang (DES-029)
passed every unit test because no test exercised the actual execution
path: binary invocation with an inherited pipe fd.

`TestReadInput_OpenPipeNoEOF` was the specific test that gave false
confidence. It used `os.Pipe()` to create a pipe, wrote data, left the
write end open, and verified `ReadInput` returned within the timeout.
This test passed on Linux because `os.Pipe()` creates fds registered
with Go's epoll poller — `SetReadDeadline` works on them.
`SetReadDeadline` fails only on inherited fds from parent processes.

### Decision

Add subprocess integration tests that spawn the real ethos binary as a
child process with a controlled pipe for stdin. The child inherits the
pipe fd — the same mechanism Claude Code uses.

**Test pattern:**

```go
rFd, wFd, _ := os.Pipe()
wFd.Write(payload)
// Do NOT close wFd — simulates Claude Code open pipe

cmd := exec.Command(binaryPath, "hook", "session-start")
cmd.Stdin = rFd  // child inherits fd — not Go-managed
cmd.Start()

select {
case err := <-done:
    // check exit code, stdout, side effects
case <-time.After(5 * time.Second):
    t.Fatal("hook hung")
}
```

One test per hook handler: SessionStart, PreCompact, SubagentStart,
SubagentStop, SessionEnd. Plus `TestSubprocess_OpenPipe` which keeps
the write end open and verifies the hook exits within 5 seconds.

`TestMain` builds the binary once per test run. Each test creates
isolated temp directories with fake identity files and git repos.

### Proof of Regression Coverage

Stashing the `readWithTimeout` fix and running the subprocess tests
produces: `hook hung with open pipe -- did not exit within 5 seconds`.
Restoring the fix: tests pass in <2 seconds. The subprocess test is a
proven regression gate.

### Platform Coverage

Build tag `//go:build linux || darwin` — runs on both platforms. On
macOS, the tests verify that `SetReadDeadline` continues to work. On
Linux, they exercise the `readWithTimeout` fallback path.

`internal/process/proc_linux_test.go` (`//go:build linux`) adds 14
Linux-specific tests for `/proc` filesystem parsing: comm truncation
to 15 chars, spaces and parentheses in comm, version-named binary
normalization via `/proc/pid/exe`, and symlink resolution behavior.

### Rejected Alternatives

- **Mock the fd in unit tests** — cannot reproduce the kernel-level
  difference between Go-managed and inherited pipe fds. The unit tests
  pass on both platforms; the production code fails on Linux.
  In-process mocking is necessary but not sufficient.
- **Integration tests via shell scripts** — harder to assert on
  side effects (session files, roster content), harder to run in CI,
  and duplicates the Go test infrastructure.
- **Skip subprocess tests, rely on manual testing** — the bug survived
  10+ manual restart cycles because the failure was silent. Only
  automated tests that spawn the real binary catch this class of bug.

## DES-031: Mission contract — typed delegation artifact (SETTLED)

**Status**: Settled. Implemented 2026-04-07 as `ethos-07m.5` — the
Phase 3.1 foundation primitive. 3 rounds (implementation + 2 review
cycles), 3 reviewers (feature-dev:code-reviewer, mdm, djb frozen
evaluator). djb verdict: pass (0.88); round 3 added trust-boundary
hardening (strict on-disk YAML, control-char rejection, counter
bounds) per fix-it-now principle.

### Problem

Delegation between agents in ethos has been free-form prose. The leader
writes a paragraph in a sub-agent prompt; the worker interprets it; the
verifier (if any) is whoever happens to be available. There is no typed
contract. Three concrete failures result:

1. **Drift between rounds.** The leader changes their mind about success
   criteria mid-cycle. The worker has no anchor — they implement to a
   moving target. There is no record of what was promised at launch.
2. **Reviewer substitution.** The reviewer at round 3 is a different
   agent than the reviewer at round 1, with a different threshold. The
   verdict is non-comparable across rounds.
3. **Untracked write-set overlap.** Two workers edit the same file
   concurrently because no mechanism declared the conflict. The bug
   only surfaces at git merge.

The four rules from the agent and instructions/memory architecture
documents (`~/Documents/agents-architecture.tex`,
`~/Documents/instructions-memory-architecture.tex`) name the underlying
discipline gap:

1. Roles are interfaces, not personas — but ethos delegations were
   prose, not interfaces.
2. Centralize understanding, decentralize execution — but reasoning
   state leaked into worker prompts.
3. Documentation is guidance, hooks and policies are enforcement —
   but constraints lived in CLAUDE.md, not in runtime checks.
4. Subagents do not inherit ambient context — but delegations assumed
   they did.

Phase 3 is the runtime that fixes these. The mission contract is
its foundation: every other Phase 3 primitive reads this schema.

### Decision

Added an `internal/mission/` package that defines a typed `Contract`
struct, a flock-protected filesystem store under
`~/.punt-labs/ethos/missions/`, a daily monotonic ID generator, an
append-only JSONL event log, and CLI + MCP surfaces for `create`,
`show`, `list`, and `close`.

**Schema invariants (enforced by `Contract.Validate()`):**

- `mission_id` matches `^m-\d{4}-\d{2}-\d{2}-\d{3}$` (date-based, not
  content-hash — operational artifact, not historical record).
- `evaluator.handle` is non-empty AND pinned at launch (`pinned_at`
  is server-controlled; `hash` is reserved for 3.3 to populate).
- `write_set` is non-empty; entries reject `..` traversal, absolute
  paths, null bytes, and C0 control characters (log forgery
  prevention). Single-dot segments (`./foo`) are permitted —
  legitimate syntax.
- `success_criteria` is non-empty; entries are free-form strings that
  the evaluator interprets.
- `budget.rounds` is between 1 and 10; default 3.
- Leader/Worker/Evaluator handle fields reject control characters
  (same log-forgery defense as write_set).
- `tools` is a string allowlist; 3.4/3.5 may enforce it via subagent
  tool restrictions.

**Storage layout** mirrors `internal/session/`:

```text
~/.punt-labs/ethos/missions/
  m-2026-04-07-001.yaml          # contract
  m-2026-04-07-001.jsonl         # append-only event log
  m-2026-04-07-001.lock          # flock target
  .counter-2026-04-07            # daily counter, flock-protected
```

**Mission ID generation** uses a per-day counter file. Multiple leaders
on the same day share the counter via `flock(LOCK_EX)`. The counter is
bounded to `[1, 999]`: at exhaustion or on attacker-poisoned counter
files (negative or out-of-range values), `NewID` returns an explicit
error rather than producing an invalid ID.

**Append-only event log** is JSON lines, one event per state transition.
Events are written with a single `Write` of `marshaled + '\n'` to an
`O_APPEND` file while holding the per-mission `flock` on the `.lock`
file. `O_APPEND` ensures each write targets end-of-file, but the
correctness guarantee against concurrent writers comes from the lock;
`PIPE_BUF` atomicity applies to pipes and FIFOs, not regular files.
The short-write contract is enforced by checking `n == len(line)`
after `Write` so a truncated line is an explicit error, not a silent
corruption. 3.1 writes `create`, `update`, and `close` events.
3.4 will add `reflect`. 3.5 will add `verify`.

**Trust boundary enforcement** runs on every read and write path.
`Store.Create`, `Store.Update`, and `Store.Close` call `Validate()`
before mutation. `Store.Load` and `Store.loadLocked` call `Validate()`
for defense in depth on reads — a corrupt or hand-edited contract is
rejected before any caller acts on it. Both the CLI and MCP create
paths use `yaml.NewDecoder(...).KnownFields(true).Decode(&c)` for strict
YAML parsing. Store load paths also use `KnownFields(true)` for
trust-boundary symmetry — an attacker with local write access to
`~/.punt-labs/ethos/missions/<id>.yaml` cannot smuggle extra fields.

**Server-controlled fields** on create: `status` → `open`, `created_at`
→ `now`, `updated_at` → `created_at`, `evaluator.pinned_at` →
`created_at`. Caller-supplied values for these fields are ignored.
Pinning the evaluator AT mission launch is a definitional invariant —
a caller-supplied `pinned_at` that predates the mission's own creation
is incoherent.

**CLI surface** mirrors `cmd/ethos/session.go`:

```text
ethos mission                       # show help (cobra default)
ethos mission create --file <yaml>  # create from YAML file (required)
ethos mission show <id-or-prefix>
ethos mission list [--status open|closed|failed|escalated|all]
ethos mission close <id-or-prefix> [--status closed|failed|escalated]
```

Only `--file` creates a contract. A flag-build path (`--leader`,
`--worker`, etc.) was considered in the round 1 spec but removed in
round 2 after a reviewer caught that it silently planted literal
`"placeholder"` strings into persisted contracts.

**MCP surface** mirrors `internal/mcp/team_tools.go` — one `mission`
tool with a `method` enum `{create, show, list, close}`. Returns
formatted text per DES-020 via `internal/hook/format_output.go`
`formatMission`, which uses `text/tabwriter` for layout consistency
with the CLI's `printContract`.

### Why YAML for the contract and JSONL for the log

YAML matches the existing identity, session, role, and team storage
formats. Humans edit it; tools serialize it; existing helpers
(`yaml.Marshal/Unmarshal`) handle it. JSONL is append-only and
machine-readable: each line is independently parseable, and event-log
analysis (3.7) does not require loading the whole file. The two formats
serve different access patterns.

### Why a date-based mission ID, not a content hash

Missions are operational, not historical. A leader needs to refer to
"the mission I started this morning" by short prefix (`m-2026-04-07-001`
shortens via `MatchByPrefix` to `m-2026-04-07` or even `001`). Content
hashes are collision-free but not human-friendly, and the counter is
bounded to 999 missions per day per installation — more than enough
operational headroom.

### Why a frozen evaluator

If the reviewer changes mid-cycle, the verdict is non-comparable across
rounds. A worker may "pass" round 3 against a more lenient reviewer
than round 1, masking unresolved issues. Pinning the evaluator at
launch (and, in 3.3, hashing their content) makes the verdict
reproducible. 3.1 records the handle and timestamp; 3.3 will compute
the content hash; 3.5 will spawn the verifier with that exact pinned
state. 3.1 ships with `pinned_at` server-controlled — the caller
cannot backdate the pinning.

### What 3.1 deliberately does NOT do

- **Write-set conflict detection** (3.2). 3.1 validates each path's
  shape but does not check overlap with other open missions.
- **Evaluator content hashing** (3.3). 3.1 stores the handle and
  pinned-at timestamp; the hash field is empty.
- **Round limit enforcement** (3.4). 3.1 stores `budget.rounds`;
  no hook enforces it yet.
- **Verifier subagent isolation** (3.5). 3.1 records the verifier
  handle; the launch mechanism comes later.
- **Result artifact validation** (3.6). 3.1 accepts mission close
  without a result artifact.
- **Append-only log reading API** (3.7). 3.1 writes events via a
  private `appendEvent` helper; public reads come later.

This staging keeps the foundation small enough to ship in three
rounds with a clean djb verdict.

### Rejected Alternatives

- **Free-form prose contracts in CLAUDE.md** — already the status quo.
  Fails three of the four architecture rules. Cannot be enforced.
- **JSON for the contract** — faster to parse but inconsistent with
  existing ethos storage (sessions, identities, roles, teams are all
  YAML). Operator will edit contract files by hand more often than
  programmatically; YAML wins on readability.
- **Flag-build `mission create`** (`--leader alice --worker bwk`) —
  implemented in round 1, removed in round 2. The flag-build path
  cannot supply `write_set` or `success_criteria` via flags, so it
  silently planted placeholder data into persisted contracts — a
  trust-boundary violation. McIlroy: do one thing well. `--file`
  is the only create path.
- **Mission storage in the repo** (`.punt-labs/ethos/missions/`) — like
  identities and teams. Rejected because missions are operational state,
  not configuration. They are short-lived, machine-specific, and would
  pollute the repo with churn. Stored under `~/.punt-labs/ethos/`.
- **Content-hash mission IDs** — collision-free but not human-friendly.
  Date-based IDs are easier to refer to and prefix-match.
- **Per-leader counter** instead of per-day shared counter — opens a
  race when multiple leaders launch simultaneously. Per-day shared
  counter with flock is simpler.
- **Single-dot (`.`) segment rejection in write_set** — proposed by
  reviewers during round 2. Rejected by the leader: `./foo` is
  legitimate path syntax and the existing `..` check catches actual
  traversals regardless. `TestValidate_AcceptsSingleDotSegment` is an
  explicit proof-of-pushback test.
- **Rely on `Validate()` at create time only, trust on-disk state** —
  round 1 behavior. Rejected by the djb frozen evaluator: an attacker
  with local write access to `~/.punt-labs/ethos/missions/` could
  bypass the CLI/MCP path. Round 3 added `KnownFields(true)` to both
  `Load` and `loadLocked`, making on-disk trust symmetric with the
  input path.
- **Public `AppendEvent` on the mission store** — round 1 exposed a
  public append path for future log-reader integration. Rejected by
  djb as a deadlock footgun: `Create/Update/Close` are already inside
  `withLock` when they append events and correctly use a private
  `appendEventLocked`. A future external caller of a public
  `AppendEvent` from inside a locked block would deadlock on Linux
  flock. Round 3 unexported it; 3.7 will re-export if needed.

## DES-032: Cross-mission write_set admission control (SETTLED)

**Status**: Settled. Implemented 2026-04-08 as `ethos-07m.6` — the
Phase 3.2 primitive on top of DES-031. Four rounds: initial
implementation, local-review fixes, leader-found double-slash
defense-in-depth, evaluator-found dot-segment defense-in-depth.
Local reviewers: `feature-dev:code-reviewer` (correctness) and `mdm`
(CLI surface). Frozen evaluator: `djb` (pinned at mission launch).
djb's final verdict: PASS (0.97) after round 4. The S1 finding
(dot-segment bypass) was caught by the frozen evaluator and not by
the leader's round 3 self-review — a vindication of the
frozen-evaluator discipline from DES-031.

### Problem

The Phase 3.1 mission contract has a `write_set` field that declares
which paths a worker may modify. The Phase 3.1 validator rejects
malformed individual entries (traversal, absolute paths, control
characters, drive letters, UNC). But there is no **cross-mission**
check. Two operators (or two agents in the same operator's session)
can each create a mission whose `write_set` overlaps the other, and
both can run, and both will quietly corrupt each other's work. The
bug only surfaces at git merge or at runtime when one worker
overwrites the other's file.

The threat model is not adversarial — it is uncoordinated cooperation.
The dominant case is two agents in the same session that don't know
about each other's claims. The architecture document
(`~/Documents/agents-architecture.tex` §"Design Improvements") names
this exact failure mode under "Write-set admission control" and
recommends: "Before launching an implementation worker, declare the
expected file set and refuse concurrent writers with overlapping
claims unless they are isolated in worktrees."

### Decision

`Store.Create` rejects a new mission whose `write_set` overlaps any
currently-open mission's `write_set`. The check happens at create
time, not at first edit, so the conflict surfaces before the worker
runs.

**Conflict semantics: segment-prefix overlap on cleaned paths.**

Two paths overlap when, after normalization (trim whitespace, replace
backslashes, trim trailing slash, split on `/`, drop empty segments),
one path's segment list is a prefix of the other's. Examples:

- `internal/foo` overlaps `internal/foo/bar.go` (forward prefix)
- `internal/foo/bar.go` overlaps `internal/foo` (reverse prefix)
- `internal/foo` does NOT overlap `internal/foobar` (segment boundary)
- `internal/foo/` and `internal/foo` are equivalent
- `internal//foo/bar.go` is equivalent to `internal/foo/bar.go` (the
  empty middle segment is filtered — see Round 3 below for why this
  matters)
- Comparison is case-sensitive (POSIX)

The per-entry validator already rejects `..`, absolute paths, drive
letters, UNC, control characters, and null bytes. The conflict check
does not re-validate; it only normalizes for comparison.

**Active mission set: `Status == StatusOpen` only.** Closed, failed,
and escalated missions are out of the registry. Closing a mission is
the explicit way to release its write_set claim.

**Two-level locking.** Phase 3.1 introduced a per-mission flock for
serializing writes to a single mission's contract. Phase 3.2 adds a
**directory-level create lock** at `<missionsDir>/.create.lock`. The
directory lock is acquired exclusively by `Store.Create` for the
duration of the conflict scan AND the new mission's write. Without
it, two concurrent Creates with disjoint mission IDs would each
acquire their own per-mission lock, both pass the conflict scan, and
both write — a TOCTOU race that the per-mission lock cannot close.

The lock file is a stable filename never renamed or unlinked, so
concurrent acquirers always lock the same inode (the same race that
Phase 3.1 fixed in `NewID` by separating the counter file from its
lock file). `Update` and `Close` do NOT acquire the directory lock —
they mutate an existing mission's status, which is unrelated to
Create-vs-Create serialization.

**Race window with Close.** A `Close` operation can run concurrently
with `Create`'s scan. If a Close transitions an open mission to
terminal state during the scan, the new Create may see it as still
open and report a false positive conflict. This is an acceptable
trade: false positive is recoverable (operator retries), false
negative would silently allow the corruption Phase 3.2 is designed to
prevent. A future enhancement could have Close briefly acquire the
directory lock; out of scope for 3.2.

**Counter consumption on rejection.** `ApplyServerFields` calls
`NewID` BEFORE `Store.Create`, so a rejected Create burns a daily
counter slot. The counter is bounded to [1, 999] and single-operator
usage will not approach the ceiling. Rolling back the counter on
rejection would require a new counter API and reintroduce the
temp+rename race that 3.1 already eliminated. Burn the slot.

**Fatal Load failure.** If `Store.Load` fails on any existing mission
during the scan (file corrupted, permission denied, hand-edited
beyond strict YAML), the entire Create call fails with a wrapped
error naming the unloadable mission. Silently skipping a corrupt
mission would defeat the conflict check — an attacker (or accidental
corruption) could bypass the gate by breaking exactly the existing
mission whose write_set blocks them.

**Error format** (one line per blocking mission):

```text
ethos: mission create: write_set conflict with mission m-2026-04-08-001 (worker: bwk): overlapping paths [internal/foo/bar.go]
```

For multi-conflict, the underlying error embeds newlines and the CLI
prints each line. The MCP path returns the same body via
`mcplib.NewToolResultError("failed to create mission: " + body)`.
The displayed path is the operator's literal entry, preserved
verbatim including trailing slashes — operators see the path they
just tried to claim, not a normalized form.

### Round-by-round summary

- **Round 1.** Initial implementation: `internal/mission/conflict.go`
  with `Conflict` struct, `findWriteSetConflicts`, `pathsOverlap`,
  `splitSegments`, `formatConflictError`. Integration in `Store.Create`
  via new outer `withCreateLock` and inner `checkWriteSetConflicts`.
  Tests: 17-row `pathsOverlap` table, 10-row `findWriteSetConflicts`
  table, 6 store integration tests including 10-goroutine concurrent
  serialization, CLI subprocess test, MCP integration test. CHANGELOG
  entry. Local review: 1 HIGH (asymmetric assertion coverage between
  CLI and MCP tests) + 3 MEDIUM (build tag, CHANGELOG recovery
  sentence, cobra Long help text) + 1 LOW (`errors.New` vs
  `fmt.Errorf("%s", ...)`).

- **Round 2.** All 5 findings addressed in 5 focused fixes — no scope
  creep beyond a one-paragraph cobra Long docstring update. Leader
  verified the round 2 binary against real fixtures, confirming the
  error format byte-for-byte and exit code 1 on conflict.

- **Round 3.** Leader-found defense-in-depth: `splitSegments` did not
  filter empty segments after `strings.Split`, so a write_set entry
  like `internal//foo/bar.go` (double slash) normalized to
  `[internal "" foo bar.go]` and bypassed the conflict check against
  `internal/foo`. The per-entry validator does not reject double
  slashes, so the conflict check must normalize them. One-line fix
  in `splitSegments` plus 4 new test rows locking the behavior.

- **Round 4.** Evaluator-found defense-in-depth: djb's frozen-review
  verdict on round 3 was FAIL (0.95) for finding S1: `splitSegments`
  filtered empty segments but did NOT filter `.` segments. The
  per-entry validator deliberately accepts single-dot segments as
  legitimate path syntax (Phase 3.1's `TestValidate_AcceptsSingleDotSegment`,
  recorded as a rejected alternative in DES-031). A contract with
  write_set `[./internal/mission/store.go]` therefore split to
  `[".", "internal", "mission", "store.go"]` and bypassed the prefix
  comparison against `internal/mission/store.go` — segment 0
  mismatched and `pathsOverlap` returned false. Same class of bug
  as round 3, different variant in the equivalence class. Fix:
  extend the existing empty-segment filter to also drop `.` segments
  (one condition added). 8 new `TestPathsOverlap` rows lock the dot
  cases at the unit level; new `TestStore_CreateRejectsDotSegmentBypass`
  exercises four dot variants end-to-end through `Store.Create`.
  Leader independently verified the equivalence class against the
  compiled binary across 8 cases (5 dot variants, 1 double-slash
  regression, 2 negative cases): all 8 passed. djb re-verified
  on the round 4 commit: PASS, 0.97.

  The lesson — fix the equivalence class, not the visible instance —
  is recorded as a feedback memory for the COO. djb's catch is the
  load-bearing example of why the frozen evaluator exists.

### What 3.2 deliberately does NOT do

- **`--wait` and `--isolate` flags.** The roadmap entry mentions both
  as recovery options. Both require additional infrastructure
  (`--wait` needs a blocking wait-on-mission-close primitive;
  `--isolate` needs Agent isolation:worktree integration that the
  leader has separately determined does not currently work as
  intended). Deferred to follow-up beads.
- **Conflict-rejected event audit.** A rejected Create attempt is
  not logged anywhere — the rejected mission was never created and
  has no log file, and the existing mission's log is not modified to
  record the failed claim. 3.7's log reader API may add a
  `create_rejected` event if the audit value justifies the
  complexity.
- **In-memory conflict registry.** The check loads every open mission
  from disk on every Create. O(n) where n is the number of open
  missions, which is small (single-operator, short-lived missions).
  3.7's log reader could maintain an in-memory index if n grows.
- **Mission deletion or write_set mutation.** Existing missions
  cannot be deleted (no Delete API) and their write_set is locked
  after creation (Update changes context but Validate enforces
  schema invariants). The conflict check operates on a stable
  registry.
- **Cross-machine coordination.** Mission storage is per-machine
  (DES-023). The conflict check covers one machine. Two operators on
  two machines can claim the same files without seeing each other's
  registries. This is consistent with Phase 3.1's local-only design.

### Rejected Alternatives

- **Conflict check as a separate API call.** `Store.CheckWriteSetConflict(c)`
  that callers invoke before `Store.Create`. Rejected: it would create
  a TOCTOU window between the check and the create, and any caller
  who forgot the check would silently bypass the gate. The check
  belongs inside `Store.Create` so there is no opt-out path.
- **Enforcement via a hook instead of the store API.** A
  `PreToolUse`-style hook that intercepts mission creation. Rejected:
  hooks are best for cross-cutting concerns that touch multiple
  callers. The store API is the single point all callers go through;
  pushing the check up to a hook adds latency and complexity for no
  enforcement benefit. The architecture document is explicit:
  "Documentation is guidance, hooks and policies are enforcement" —
  but the store API IS the enforcement layer for mission state, not
  documentation.
- **Filesystem semantics for path comparison.** Use `os.SameFile` or
  `filepath.EvalSymlinks` to compare paths via inode or resolved
  target. Rejected: write_set entries are declared paths, not
  filesystem references. They may not exist yet (the worker is about
  to create them). String-based segment-prefix comparison is the
  right granularity for declared intent.
- **Per-byte string equality** instead of segment-prefix. Rejected:
  it would not catch directory-vs-file overlap (`internal/foo/`
  blocking `internal/foo/bar.go`), which is the dominant case.
- **Comma-separated paths in the error message.** `[a, b]` instead
  of `[a b]`. Rejected: paths can contain commas (rare but legal),
  while space-separated reads cleanly for the common case and
  matches the spec's pinned format.
- **Validating paths exist on disk.** Rejected: the worker may be
  creating the file. The conflict check cares about declared
  intent, not realized state.
- **Skipping the directory create lock and relying on the per-mission
  lock.** Round 1's first design draft. Rejected because the
  per-mission lock only serializes Creates with the SAME mission ID
  — and concurrent Creates with disjoint IDs would each get their
  own lock and race past the conflict scan. The directory lock is
  what closes the TOCTOU.
- **Counter rollback on conflict.** Rejected per the rationale above:
  rollback API would reintroduce a race that 3.1's NewID already
  fixed.
- **Including double-slash rejection in `validate.go` instead of
  filtering in `splitSegments`.** Round 3 alternative. Rejected
  because `validate.go` was on the forbidden-path list for Phase 3.2
  (per-entry validation is unchanged) and because the conflict check
  is the gate that needs the normalization, not the schema. A future
  hardening pass on `validate.go` could reject double slashes
  outright; out of scope for 3.2.

## DES-033: Frozen evaluator — content hash pinning (SETTLED)

**Status**: Settled. Implemented 2026-04-08 as `ethos-07m.7` — the
Phase 3.3 primitive that makes DES-031's "frozen evaluator" actually
enforceable at runtime. Three rounds: initial implementation, local
review fixes, and djb-review regression tests plus micro-optimization.
Local reviewers: `mdm` (CLI surface — nine findings across high,
medium, and low severity, all addressed).
`feature-dev:code-reviewer` was
attempted twice but hit transient API overload; leader supplemented
with a targeted self-review that turned up no additional findings
beyond mdm's list. Frozen evaluator: `djb` (pinned at mission launch).
djb's final verdict: PASS (0.96), with three LOW follow-up items
addressed in round 3 rather than deferred — per Phase 3.1 precedent,
the leader's fix-it-now discipline overrides djb's "file as follow-
up" procedural recommendation.

### Problem

Phase 3.1 shipped the mission contract's `Evaluator.Hash` field as an
empty placeholder. Nothing populated it; nothing verified it. Between
mission launch and verifier spawn, an operator could edit the
evaluator's personality, writing_style, talents, or role content and
the subsequent verifier subagent would silently apply the updated
standard. DES-031 called out exactly this gap and deferred the fix to
3.3: *"3.3 will populate it from the resolved evaluator's
personality+role+writing-style content (sha256)."*

The underlying discipline rule is from
`~/Documents/agents-architecture.tex`: *"Freeze the evaluator for the
duration of the task. Changing the scoring rule mid-run creates fake
progress and makes results impossible to compare."* Pinning the
evaluator handle is half the job; pinning its content is the other
half.

### Decision

At mission create time, `Store.ApplyServerFields` resolves the
evaluator handle to its full identity content (personality,
writing_style, talents, role), computes a deterministic sha256 over
every content source, and writes the hex-encoded result into
`Contract.Evaluator.Hash`. An unresolvable evaluator handle fails
the create — an empty hash is not a valid outcome.

At verifier subagent spawn time, the `SubagentStart` hook recomputes
the hash from current identity content and compares against every
open mission whose `Evaluator.Handle` matches the spawning subagent.
Any mismatch is a fatal refusal. The mismatch error names every
drifted mission, the pinned and current hash prefixes, a per-section
breakdown of the current content, and both recovery paths.

**Hash algorithm** (DES-033-v1, version-prefixed for future format
changes):

- Format version: `ethos-evaluator-hash-v1` (included in the hash
  input so a bump invalidates every prior pinned hash)
- Field separators: 0x1F (Unit Separator) between label and value;
  0x1E (Record Separator) between sections. Both are ASCII control
  bytes that ethos identity validation already rejects from handles
  and slugs; collisions inside markdown bodies are absorbed by the
  length prefix.
- Length prefixes in BYTES, not runes, to prevent field-boundary
  attacks (a multi-byte character cannot smuggle bytes into the
  next field).
- Section order (fixed, load-bearing):
  1. Handle — anchors the hash to a specific identity, so two
     evaluators with the same content don't collide
  2. Personality content (markdown body, byte-for-byte)
  3. Writing style content (markdown body, byte-for-byte)
  4. Talents — each `(slug, content)` pair in identity declaration
     order. Reordering talents is a content change the hash must
     reflect.
  5. Roles — each `(team/role_name, canonical_content)` pair sorted
     lexicographically by `team/role_name`. Sorting is required
     because `RoleLister` walks teams in map iteration order;
     sorting makes the output stable across processes.

**Role binding semantics.** Ethos identities have no direct role
field — roles bind via team membership. `NewLiveHashSources` walks
every team, collects every `(team, role)` assignment for the
evaluator handle, and includes each with the team name as a prefix
so two identical role names on different teams stay distinguishable.
An evaluator on multiple teams appears multiple times in the hash,
which is intentional: team-scoped role rebindings are drift the
gate should catch.

**Canonical role content** is rendered via a hand-rolled
`key=value\n` format, NOT `yaml.Marshal`. YAML marshal is
nondeterministic across releases (key ordering, indent style); the
hand-rolled format is stable and the failure modes are obvious.

**Verifier hook enforcement.**

- Runs BEFORE joining the session roster. A refused spawn leaves
  no roster trace, and the operator's diagnostic is the hash
  mismatch, not a confusing post-join failure.
- Lists open missions, loads each, filters to `Status == StatusOpen`,
  then further filters to those whose `Evaluator.Handle` matches
  the spawning subagent. Non-matching missions are skipped.
- Computes the current hash ONCE per hook invocation (cached across
  all matching missions) and compares against each mission's
  pinned hash. Every mismatch is aggregated into a single
  multi-line error, not a sequence of failed spawns.
- A corrupt or unloadable mission is fatal, not silently skipped.
  Silent skip would let an attacker bypass the frozen evaluator by
  hand-corrupting one contract.
- Misconfiguration (non-nil mission store, nil `HashSources`) is
  fatal. Silently skipping the gate would let stale evaluator
  content through under a configuration error.

**Drift error format.**

```text
refusing verifier spawn: evaluator "djb" content has drifted since 3 open missions were launched
  m-2026-04-08-001: pinned 19095bb477fb → current cce412ad7986
  m-2026-04-08-003: pinned 19095bb477fb → current cce412ad7986
  m-2026-04-08-005: pinned a2f193c001de → current cce412ad7986
  current content sections (check which you edited):
    personality:       aa288d81f495
    writing_style:     1d9226cf9e3d
    talent "security": 674bd4174a76
    role "punt/lead":  87f293bd0e1a
  to preserve these missions: revert the edit to the evaluator's identity content
  to accept the new content: close the listed missions and relaunch them with the new content
```

The per-section breakdown is the CURRENT state, not a diff against
the pinned state — the contract only stores the rollup, so the
pinned per-section hashes are unknown at verify time. The operator
finds the drifted file by elimination: whichever current section
hash they don't recognize is the file they touched. This is a
pragmatic trade against a schema change (pinning the breakdown
alongside the rollup), which would have expanded 3.3's scope.

### Legacy mission handling

Pre-3.3 missions with empty `Evaluator.Hash` are ALLOWED with a
stderr warning, not refused. In practice there are no legacy
missions — Phase 3.1 shipped hours before Phase 3.3 — but the
warning path covers the edge case without forcing a hard upgrade.
Any future mission created with the 3.3 binary gets a real hash.

### What 3.3 deliberately does NOT do

- **No pinned per-section breakdown.** The contract only stores the
  rollup. Pinning the breakdown would be a schema change (new
  fields on `Evaluator`) and would let the verifier diff pinned-vs-
  current rather than just listing current sections. Deferred —
  the pragmatic "operator finds it by elimination" approach ships
  first.
- **No cryptographic signing.** The hash is a content fingerprint,
  not a signature. The trust boundary is the on-disk mission
  contract and the on-disk identity files; an attacker with write
  access to both can evade the gate by editing both in sync. This
  is the inherent filesystem trust model, not a 3.3 regression.
- **No TOCTOU protection on identity files.** The hash reads the
  evaluator's files at create time and again at verify time;
  between those reads the files are trusted to be stable.
  Concurrent edits during a hook invocation are not a supported
  adversarial model.
- **No runtime bypass.** There is no flag, environment variable,
  or configuration option to disable the hash gate. The only ways
  to proceed after a mismatch are (a) revert the edit, (b) close
  the mission, or (c) in the edge case of a misconfigured
  installation, fix the configuration.
- **No hash format migration path.** The version prefix
  (`ethos-evaluator-hash-v1`) reserves the ability to change the
  algorithm in a future phase. Today there is no v2 and no
  migration tooling; a bump would invalidate every existing
  pinned mission and force a relaunch.

### Rejected Alternatives

- **Hash the identity YAML file as bytes.** Simpler but brittle:
  YAML key order or indentation changes would flip the hash even
  when the semantic content is unchanged. Hand-rolled canonical
  serialization gives stability across releases.
- **Compute the hash on the CLI/MCP side and pass it to
  `ApplyServerFields`.** Rejected: the trust boundary is the
  server-side `ApplyServerFields` call, not the caller. If the CLI
  could supply a hash, a caller with write access to the identity
  files could compute a pre-drift hash and smuggle it into the
  contract at launch, bypassing the gate entirely. The hash must
  be computed server-side from the identity store at create time.
- **Store a per-section breakdown alongside the rollup.** Would
  let the verifier diff pinned-vs-current and name the drifted
  file exactly. Rejected for 3.3 because it requires a schema
  change (new nested struct on `Evaluator`). The current "show
  current sections, operator finds by elimination" approach ships
  without schema changes; a future phase could revisit.
- **Soft-fail on mismatch with a warning.** Would match the
  legacy-mission policy. Rejected: the whole point of the gate is
  to refuse the spawn. A warning-only mode would turn the primitive
  into documentation — which the architecture rule explicitly
  rejects.
- **Pin only the evaluator's personality.** Simpler, but incomplete:
  writing_style influences verdict framing, talents influence
  domain judgment, roles influence tool access. An edit to any of
  them is a real change the gate must detect.
- **Refuse verifier spawns on any misconfigured installation.**
  The current code refuses on misconfiguration (non-nil Missions,
  nil HashSources). An alternative would be to silently skip
  the gate in that case (legacy-compatible default). Rejected:
  misconfiguration is an operator error and deserves loud
  feedback, not a silent bypass.
- **Cache the computed hash across hook invocations in-memory.**
  Phase 3.3's hook runs once per subagent spawn, reads the
  identity files fresh, computes the hash, and exits. Caching
  across invocations would require a persistent hook process
  (ethos hooks are short-lived). The per-invocation cost is ~4
  file reads + sha256, which is well under the hook's latency
  budget.
- **Walk teams lazily only when a mission matches.** The current
  `checkVerifierHash` only calls `ComputeEvaluatorHash` when a
  mission with a matching evaluator handle is found. Team walking
  happens inside that compute call. An earlier draft had
  `NewLiveHashSources` pre-walking teams on construction; rejected
  because it does work that's often unused (most hook invocations
  are for non-evaluator subagents).

**Implementation note (ethos-z69l, #409).** The gate originally scanned
every open mission and matched by evaluator handle alone. A handle that is
worker on one open mission and evaluator on another was bound as the other
mission's frozen verifier whenever it spawned — even when dispatched, with
its own `MISSION_ID`, to *work* its own mission. The misbound spawn then
enforced the wrong mission's write-set and refused its actual one.
`checkVerifierHash` now reads the spawn's declared `MISSION_ID` and applies
the gate only when that one mission is open and names the subagent as its
evaluator; a spawn serving the declared mission in any other role takes the
normal persona path. The decision preserved: bind the gate by
`(mission_id, role)`, not by handle. Per-mission binding makes cross-mission
drift aggregation structurally impossible, so `formatDriftError` reports only
the single declared mission. `ethos mission create` additionally prints a
non-fatal warning when a new contract's worker or evaluator handle overlaps
an open mission's, since handle overlap is the precondition for this
misbinding class — advisory only, because handle reuse across missions is a
legitimate pattern.

## DES-034: Bounded rounds with mandatory reflection (SETTLED)

**Status**: Settled. Implemented 2026-04-08 as `ethos-07m.8` — the
Phase 3.4 primitive that makes DES-031's `Budget.Rounds` field
enforceable. Two rounds: initial implementation plus local review
fixes. Local reviewers: `feature-dev:code-reviewer` (correctness —
1 HIGH, 2 MEDIUM, 1 LOW) and `mdm` (CLI surface — 1 BLOCKER, 1
HIGH, 2 MEDIUM, 4 LOW). The mdm BLOCKER (`Store.List` treating
`<id>.reflections.yaml` as a contract, breaking Phase 3.2's
conflict check) was caught by running the binary end-to-end with
real fixtures — exactly the kind of cross-primitive integration bug
that only shows up on the command line. Frozen evaluator: `djb`
(pinned at mission launch). djb's final verdict: PASS (0.96) with
one follow-up torpedo filed as a separate bead (`containsControlChar`
on `Reflection.Reason` field, rule-5 consistency).

### Problem

Long-running fix cycles drift indefinitely. Phase 3.1 shipped
`Budget.Rounds` as metadata; nothing enforced it. An agent could
run through round 5, introduce a new regression in round 6, and
keep going until the leader noticed by hand. The architecture rule
from `~/Documents/agents-architecture.tex` §"Evaluation Discipline":
*"Work in bounded rounds. Long-running optimization should not be
an unbroken stream of edits. After a bounded set of attempts,
require a reflection step: continue, pivot, ask the user, or
stop."*

Phase 3.3's frozen evaluator stops evaluator drift. Phase 3.4 stops
round drift. Together they close the two "silent drift" failure
modes the architecture docs named.

### Decision

A new typed `Reflection` artifact sits between round N and round
N+1. The round-advance gate in `Store.AdvanceRound` refuses to
begin round N+1 until the reflection for round N is on disk AND
its recommendation is non-terminal (continue or pivot) AND the
budget isn't exhausted.

**Reflection schema** (`internal/mission/reflection.go`, typed not
prose):

- `round` — integer, the round just ended
- `created_at` — RFC3339 server-filled at append time
- `author` — leader handle recording the reflection
- `converging` — bool, whether the work appears to be approaching
  success
- `signals` — list of observations (at least one, each non-empty,
  no control characters)
- `recommendation` — enum: `continue` | `pivot` | `stop` | `escalate`
- `reason` — prose, required when recommendation is terminal

**Recommendation semantics:**

- `continue` — advance permitted, same approach
- `pivot` — advance permitted, worker takes a different approach
  in round N+1
- `stop` — advance refused, mission must close
- `escalate` — advance refused, mission must be re-scoped or
  escalated to a human

**Storage — sibling file, not inline.** Reflections live in
`<missionsDir>/<id>.reflections.yaml`, a sibling to the contract,
NOT inside the contract. Two reasons:

1. The contract is pinned at launch per DES-031. An unbounded
   reflection slice inside it would force every `Store.Update` to
   rewrite an unbounded history, and any Update failure would risk
   losing prior reflections.
2. The reflections file grows as rounds happen; the contract file
   stays structurally stable. Separating them keeps each file's
   lifecycle clean.

Both files are serialized through the same per-mission flock
(`withLock(missionID, ...)`), so contract+reflection operations
for the same mission are atomic with respect to concurrent
readers. KnownFields(true) strict decode applies to both, keeping
the trust boundary symmetric with DES-031's contract trust
boundary.

**Append-only invariant.** `AppendReflection` refuses to overwrite
an existing round's reflection. The reflections file is
monotone-sorted by round number at decode time; a hand-edited or
corrupt out-of-order file is rejected before any caller acts on
it. This preserves the round history for later post-mortem —
memory and beads are derived summaries, but the append-only log
is the source of truth.

**Round tracking — new `Contract.CurrentRound` field.** Chosen
over "derive from event log" because:

1. The gate needs to answer "what round is this mission currently
   in?" on every `mission show` and every advance call. Walking
   the event log on every read adds an unbounded latency cost.
2. The field is a small integer; the contract write is cheap.
3. Phase 3.7's event log reader API will still be able to derive
   the round history from events if an audit needs it. The
   `CurrentRound` field is a cache of state, not a source of
   truth.

Default-filled to 1 on Create and Load, so pre-3.4 contracts
upgrade cleanly. Rule 13 of `Contract.Validate` enforces
`CurrentRound in [1, Budget.Rounds]`.

**Round-advance gate logic** (`Store.AdvanceRound`):

1. Acquire per-mission flock
2. Load contract + reflections inside the lock
3. Refuse if `Status != StatusOpen`
4. Refuse if `CurrentRound >= Budget.Rounds` (budget exhausted,
   re-scope or close)
5. Refuse if no reflection exists for `CurrentRound`
6. Refuse if the reflection's recommendation is terminal (stop,
   escalate) — surface the reflection's `Reason` verbatim so the
   operator sees the leader's own words
7. Bump `CurrentRound`, validate, write contract, append event,
   roll back on log failure

The ORDER of 4, 5, 6 is load-bearing: the operator sees "close and
re-scope" on budget exhaustion, not "submit one more reflection"
on the last round.

**CLI surface:**

- `ethos mission reflect <id> --file <reflection.yaml>` — submit
  the current round's reflection
- `ethos mission advance <id>` — attempt to begin the next round
- `ethos mission reflections <id>` — print the round-by-round
  reflection log (`--json` emits a single JSON array)
- `ethos mission show <id>` — now displays `Round: N of M` and
  includes the reflection log as a secondary block

All four subcommands surface through the same `formatMission*`
dispatchers per DES-020, with the `mission` MCP tool growing
three matching methods.

**Silent-on-success convention.** `mission advance` is silent on
success in non-JSON mode, matching `create`/`close`/`reflect`.
Exit code 0 tells the story.

**BLOCKER caught in round 2 local review:** the initial design
added a sibling file but did NOT update `Store.List`. Phase 3.2's
`checkWriteSetConflicts` walks `Store.List` and treats any load
failure as fatal (correctly — silently skipping corrupt missions
is a bypass). After a single reflection was submitted, `List`
would return `<id>.reflections.yaml` as a candidate, `Load` would
fail on the wrong schema, and every future `mission create` would
refuse. mdm caught this by running the binary end-to-end with
real fixtures — not by reading code. Round 2 fixed it by adding
an `isContractFile` helper that excludes `.reflections.yaml` and
dotfiles. A regression test exercises all three failure modes
(list, prefix-match, create-after-reflection).

### What 3.4 deliberately does NOT do

- **No machine-readable reflection analysis.** Reflections are
  prose + typed enum, not a scoring function. A future phase could
  add signal classification, but 3.4 trusts the leader to pick
  `continue` / `pivot` / `stop` / `escalate` honestly.
- **No reflection on round 0.** Reflections are submitted at the
  end of a round, for the round that just completed. Round 1
  starts without a prior reflection. The gate only fires when
  advancing FROM round N to round N+1.
- **No auto-reflection.** The leader submits reflections by hand
  via `mission reflect`. Phase 3.4 is a gate, not an automation
  engine.
- **No reflection for closed missions.** Once `Close` transitions
  the mission to a terminal state, reflections are no longer
  accepted. The mission's round history is frozen.
- **No reflection deletion.** Reflections are append-only. A
  mistaken reflection is recorded forever; the leader can close
  the mission and create a replacement if the mistake was
  material.
- **No schema change to `Budget.Rounds` bounds.** The [1, 10]
  range from DES-031 is preserved. 3.4 adds enforcement, not a
  new range.
- **No cross-primitive changes.** Phase 3.1 contract schema,
  Phase 3.2 conflict check, Phase 3.3 frozen evaluator hash, the
  store flock discipline, and the SubagentStart hook are all
  untouched. 3.4 is additive.

### Rejected Alternatives

- **Derive CurrentRound from the event log.** Cleaner
  conceptually — the event log is the source of truth — but
  expensive: every `mission show` would walk the log. The
  CurrentRound field is a cache of derivable state, not a source
  of truth; Phase 3.7's log reader can reconstruct it for audit.
- **Store reflections inside the Contract struct.** Rejected
  because the contract is pinned at launch. Unbounded growth
  would force Update to rewrite an unbounded slice on every
  transition. A sibling file decouples the lifecycles.
- **Hard-refuse reflections for any round other than the current
  one.** The implementation does refuse reflections for past or
  future rounds, but the strictness was briefly questioned:
  should a leader be able to submit a "correction" reflection for
  round 1 while in round 3? Rejected: reflections are the
  round-by-round trust handoff. A correction would reopen the
  gate on round 2's advance retroactively, which is exactly the
  "moving goalposts" failure mode the primitive prevents. Leaders
  who made a mistake close the mission and relaunch.
- **Warning on `converging: false` with `recommendation: continue`.**
  Rejected: this is the leader explicitly choosing "give it one
  more try." The field is on the record for post-mortem; the gate
  does not second-guess it at advance time.
- **Pin reflections to the frozen evaluator.** Reflections are
  the LEADER's artifact, not the evaluator's. Phase 3.3 pinned
  the evaluator's identity content. Phase 3.4 pins the LEADER's
  round-by-round judgment. The two primitives are orthogonal.
- **Make `mission advance` interactive** (prompt for a reflection
  inline instead of requiring a separate `reflect` call).
  Rejected per McIlroy's rule against non-interactive prompts in
  scripted contexts. The two-step `reflect` + `advance` flow is
  composable; the one-step interactive flow breaks scripting.
- **Store reflections in the JSONL event log instead of a sibling
  YAML file.** Rejected: the event log is append-only JSONL for
  state transitions, not structured content. Mixing structured
  reflection YAML into a JSONL log would require schema tagging
  and custom parse logic. The sibling YAML file uses the same
  YAML decode pipeline as the contract itself.
- **Always record the reflection, even if it's malformed, and
  error out at advance time instead of at submit time.** Rejected:
  the submit path is where validation belongs. An operator who
  submits a malformed reflection should find out immediately, not
  discover it only when they try to advance.

## DES-035: Verifier isolation (SETTLED)

**Status**: Settled. Implemented 2026-04-08 as `ethos-07m.9` — the
Phase 3.5 primitive that enforces verifier independence from the
implementer. Two rounds: initial implementation plus local review
fixes. Local reviewers: `feature-dev:code-reviewer` (correctness — 1
HIGH, 1 MEDIUM) and `mdm` (CLI surface — 3 HIGH, 3 MEDIUM, 4 LOW,
all addressed in round 2). Frozen evaluator: `djb` (pinned at
mission launch). djb's final verdict: PASS (0.95) with two
follow-up notes filed as separate beads: `ethos-29o` (TOCTOU
between checkVerifierHash Load and renderVerifierBlock ReadFile,
inherited from Phase 3.3's lockless read pattern and out of scope
for 3.5's threat model) and `ethos-x05` / `ethos-4au` (SC6
tool-level file allowlist enforcement and SC5 walked-diff round
delta — both explicitly scope-deferred per the threat model).

### Problem

When the same agent implements and verifies, the verifier is too
invested in its own implementation. The verifier reads the
implementer's scratch state and rationalizes it. Phase 3.1's mission
contract records the verifier's handle, but nothing stops the
verifier from receiving the worker's full context when the hook
spawns them. Phase 3.3's frozen evaluator hash pinning prevents
evaluator content drift mid-cycle, but a verifier that shares the
worker's role framing can still give a biased verdict — the content
is the same, but the lens is the same too.

The architecture rule from `~/Documents/agents-architecture.tex`
§"Evaluation Discipline": *"Use mixed verification tracks"* and *"A
different worker, or the leader, checks the result with a fresh
skeptical prompt."* Phase 3.5 operationalizes this at two layers:
handle level (worker != evaluator) and role level (their role
bindings cannot overlap) at create time, and context-injection
level at spawn time (the verifier subagent receives only the
mission contract, verification criteria, and a file allowlist).

### Decision

Two independent runtime gates:

**1. Role-overlap check at `Store.Create` (mission creation time).**

A new `RoleLister` interface and `Store.WithRoleLister(r)` opt-in
method. When set, `Store.Create` refuses a contract whose worker
and evaluator handles either (a) match literally or (b) share a
team-scoped role binding under canonicalization. The check runs
inside the existing per-mission flock after validation and before
the write.

Role overlap is defined as:

- **Exact binding match.** `bwk` and `djb` both have a `team_handle:
  engineering/role: go-specialist` record — same team, same role
  slug. Refused.
- **Canonicalized slug match.** `bwk` bound to `engineering/go-specialist`
  and `djb` bound to `security/go-specialist` — different teams,
  same role slug after canonicalization (take the substring after
  the last `/`). Refused.
- **Distinct role acceptance.** `bwk` bound to `engineering/go-specialist`
  and `djb` bound to `engineering/security-reviewer` — same team,
  different role slugs. Accepted. This is the canonical ethos
  pattern: an implementer and a security reviewer on the same team
  can verify each other's work.
- **No-binding acceptance.** An identity with zero role bindings
  (fresh install, no team membership) has no role to overlap.
  Accepted.

The opt-in pattern (`WithRoleLister(r)`) keeps the existing 50+
unit tests that build a bare `mission.NewStore(root)` compiling
without modification. Production wiring goes through the
`missionStoreForCreate()` helper in `cmd/ethos/mission.go`, which
constructs a live `RoleLister` from the identity, role, and team
stores and fails fatally (`os.Exit(1)`) if the wiring cannot be
built. Read-only mission subcommands (`show`, `list`, `close`,
`reflect`, `advance`, `reflections`) use the bare `missionStore()`
helper and never touch the lister — they don't need the overlap
check, and construction is cheaper without the wiring pass.

**2. Verifier context isolation at `SubagentStart` hook (spawn time).**

When the hook detects a verifier spawn (the existing Phase 3.3
`checkVerifierHash` discriminator — Phase 3.5 does NOT add a
parallel "is this a verifier?" check), the hook REPLACES the
subagent's additionalContext with a structured isolation block
instead of the normal persona block. The block contains:

- `## Verifier context (mission <id>)` — H2 root, consistent with
  persona block's H2 convention
- `### Mission contract` — byte-for-byte from disk via
  `Store.ContractPath` + `os.ReadFile` (NOT re-marshaled, which
  would reorder keys or drop YAML comments)
- `### Verification criteria` — the contract's `success_criteria`
  list, verbatim
- `### File allowlist` — two labeled sub-sections:
  - `Repo-relative paths (resolve from repo root):` lists the
    write_set entries in declaration order
  - `Absolute paths:` lists the contract file at its absolute path
- A `MUST NOT` directive: the verifier may not read parent
  transcript, worker scratch, or paths outside the allowlist
- A `may` directive: "These are the only paths the verifier may
  read" (restrictive semantics, RFC 2119 "may" not "MAY")

For multi-mission evaluators (the same handle is evaluator on
multiple open missions — the aggregated-drift case from Phase 3.3),
the block concatenates one section per mission separated by
`---`.

The persona block is EXCLUDED for verifier spawns. Parent transcript
is excluded by structural replacement (the hook's `additionalContext`
field is overwritten, not appended). Worker scratch is excluded by
not being referenced in the block at all.

### What 3.5 deliberately does NOT do

- **~~No mechanical file-allowlist enforcement~~ (SC6, now
  implemented — `ethos-x05`).** A `PreToolUse` hook handler blocks
  verifier Read/Write/Edit/Glob/Grep calls against paths outside
  the mission write_set. The allowlist is communicated to the hook
  via the `ETHOS_VERIFIER_ALLOWLIST` environment variable set at
  `SubagentStart`. The prose directive remains as the cooperating-
  verifier signal; the hook enforces it mechanically as defense-in-
  depth.

- **~~No real git-diff computation of round deltas~~ (SC5, now
  implemented — `ethos-4au`).** `WalkWriteSet` resolves static
  write_set paths to concrete files on disk via `filepath.WalkDir`.
  The verifier isolation block now includes a "Concrete files on
  disk" section listing the walked results alongside the static
  write_set entries.

- **No change to Phase 3.3's `checkVerifierHash`.** Phase 3.5
  REUSES the discriminator; it does not modify it. The same gate
  fires, and Phase 3.5 layers the context isolation onto the
  same branch.

- **No change to Phase 3.2's `checkWriteSetConflicts`** or
  `isContractFile` helper. Phase 3.5 is purely a validation-and-
  injection change. No new sibling file layout, so no Phase 3.4
  BLOCKER-class regression risk.

- **No retroactive invalidation of pre-3.5 missions.** The
  role-overlap check runs only at `Store.Create` time. Existing
  open missions with role-coincident worker+evaluator pairs
  continue to load and advance.

- **No MCP tool description change.** The `mission` tool's
  `create` method surfaces the new rejection through its existing
  error path. No new MCP tool methods, no new enum values.

- **No `validate.go` rule.** The worker!=evaluator check was
  initially considered for `Contract.Validate()` (defense in depth
  at every decode path) but was rejected for Phase 3.5 because:
  (a) the stronger role-overlap check requires the role store,
  which `Validate()` cannot depend on without a dependency cycle,
  and (b) putting the weaker check in Validate and the stronger
  check in Store.Create would split the invariant across two
  files and create an artificial asymmetry. Both checks live in
  `Store.Create` for now. A future phase could add a Validate-
  level handle check once the store dependency is cleaner.

### Rejected Alternatives

- **Require a role overlap check even for bare `NewStore(root)`
  (no opt-in).** Rejected because it would force every unit test
  in the mission package to construct a real role store,
  coupling the mission tests to the role package's fixture
  layout. The opt-in pattern preserves test independence. The
  production path is tested end-to-end by the new subprocess
  test (`TestMissionCreate_RoleOverlapThroughLiveStoresSubprocess`),
  so the check is exercised against real stores.

- **Put the role-overlap check in the Phase 3.3 hash gate.**
  Rejected because the hash gate runs at spawn time (too late —
  the mission has already been created with an overlapping
  verifier). Create-time enforcement is the right boundary.

- **Canonicalize role slugs by fuzzy matching (e.g. Levenshtein
  distance).** Rejected as over-engineering. Exact-match-after-
  slash-split is precise and understandable. Operators bind
  identities to roles deliberately; fuzzy matching would create
  surprising rejections.

- **Replace the verifier's additionalContext with ONLY the
  contract** (strip even the verification criteria and allowlist).
  Rejected because the verifier needs to know WHAT to verify and
  WHICH files are in scope. The contract alone is ambiguous
  without the success_criteria and write_set context.

- **Inline the contract bytes via `yaml.Marshal(c)` in the
  isolation block.** Rejected because yaml.Marshal is not
  deterministic across Go releases (key ordering, indent style)
  and would drop YAML comments. The byte-for-byte read from disk
  preserves the operator's exact intent.

- **Add a `--skip-role-overlap` flag** to `ethos mission create`
  for recovery. Rejected: no runtime bypass path. An operator
  who needs to relax the role-overlap rule should fix their
  team/role bindings via `ethos team add-member`, not add a CLI
  escape hatch. Matches DES-033's "no runtime bypass" posture
  for the frozen-evaluator hash.

- **Enforce the file allowlist via a `PreToolUse` hook in Phase
  3.5**. Was the original success criterion 6. Scope-deferred to
  a follow-up bead. See "What 3.5 deliberately does NOT do"
  above for the full rationale.

- **Walk the write_set on disk to produce a real file list** for
  the isolation block's allowlist. Was success criterion 5.
  Scope-deferred for the same reason: adds work on every verifier
  spawn for marginal value, filed as a follow-up.

- **Split the role-overlap check into a separate package**
  (`internal/mission/overlap`). Rejected for round 1 — keep it
  close to `Store.Create` where it's used. A future refactor
  could extract if other callers need the check.

## DES-036: Result artifacts and close gate (SETTLED)

**Status**: Settled. Implemented 2026-04-08 as `ethos-07m.10` — the
Phase 3.6 primitive that turns worker output from prose into a
typed artifact and gates terminal mission transitions on its
presence. Six worker rounds total: 1 implementation (round 1), 3
local-review-fix rounds (rounds 2, 3, 4 — driven by 4-reviewer
local cycles after rounds 1, 2, and 3), and 2 PR-side-fix rounds
(round 5 for Copilot, round 6 for Bugbot). Fourteen reviewer agent
invocations across the four local review cycles. Local reviewers:
`djb` (frozen
evaluator — 0.92, 0.95, 0.98 across the three rounds he reviewed),
`mdm` (CLI specialist — caught the dead-code nil-slice guard, the
hand-rolled-payload data loss, the missing `mission close --help`
gate language, and the corrupt-reflections symmetry miss),
`feature-dev:code-reviewer` (correctness — caught the symmetric
`pathsOverlap` directionality bug independently, and the
hook-formatter `results` drop), `silent-failure-hunter` (the
`handleShowMission` swallowed `LoadResults` error and the
`formatMissionShow` drop). Plus PR-side: Copilot (the read-side
mission ID trust symmetry gap on `decodeResultsFile`), Bugbot (the
fix-the-class miss between corrupt-results and corrupt-reflections
in `runMissionShow`).

### Problem

Phase 3.1 shipped the mission contract with a Result artifact
intended but unimplemented; `Store.Close` accepted any mission into
a terminal status without proof of what the worker delivered. The
JSONL event log recorded transitions but not verdicts. At fan-out
(five workers → one leader synthesis), prose output is the leader's
hardest job — every worker's report has to be read and interpreted
to extract the structured fields the synthesis decision actually
needs.

### Decision

A typed `mission.Result` artifact with strict YAML decoding
(`KnownFields(true)`), full validation (verdict enum, confidence in
[0.0, 1.0] excluding NaN, files_changed paths cross-checked against
the contract write_set, evidence non-empty, control-character
rejection on author/name/open_questions, prose accepting `\n\r\t`
only), and append-only sibling storage at `<id>.results.yaml` —
parallel to the Phase 3.4 reflections sibling pattern.

`Store.Close` refuses every terminal transition (`closed`, `failed`,
`escalated`) unless `LoadResults` returns a valid artifact for the
mission's current round. The refusal message names the mission, the
round, and the submission command. The gate lives at the store
boundary so CLI and MCP fire it identically — no override flag, no
bypass.

`files_changed` containment uses a NEW `pathContainedBy(file, entry)`
helper in `internal/mission/conflict.go`, NOT a reuse of the
existing `pathsOverlap`. `pathsOverlap` is symmetric (correct for
Phase 3.2's cross-mission conflict check); `pathContainedBy` is
asymmetric (the entry's segment list must be a prefix of the file's,
and the file must have at least as many segments — the only correct
shape for "the file lives inside the allowlist entry"). All four
reviewers caught the original symmetric implementation as a HIGH
finding in round 1; djb verified the exploit end-to-end against the
binary.

`mission.ShowPayload` (in `internal/mission/mission.go`) embeds
`*Contract` so the show JSON shape auto-propagates any future
Contract field to both CLI and MCP without hand-rolling field
lists. Plus a `Results []Result` and an optional `Warnings
[]string` (omitempty) so a `LoadResults` error surfaces as
structured signal instead of being silently dropped (round 3 D1).

### Rejected alternatives

- **Add a `Result` field to the `Contract` struct.** Rejected — the
  Phase 3.1 contract schema is frozen, and contract + result have
  different lifecycle invariants (contract is pinned at create
  time; results are appended per round). Sibling files preserve the
  invariants and avoid breaking on-disk compatibility with Phase
  3.1 missions.

- **Use the symmetric `pathsOverlap` for files_changed containment.**
  Round 1 implementation. Rejected when all four reviewers caught
  the parent-prefix exploit end-to-end. The fix was a new
  asymmetric helper, not a tightening of the existing one — the
  symmetric semantics are still correct for Phase 3.2's
  cross-mission conflict check, where any direction of overlap is
  a clash.

- **Hand-rolled `map[string]any` for the show JSON payload.** Round
  2 implementation. Rejected when djb caught it dropping the
  Contract's `session` and `repo` fields and inverting `omitempty`
  semantics. The struct-embedding fix (round 3) makes future
  Contract fields auto-propagate.

- **Return early on `LoadResults` failure in `runMissionShow`.** The
  pre-round-4 shape. Rejected when mdm caught the asymmetry between
  clean-empty (rendered `Results: (none)`) and corrupt
  (silently rendered nothing). Round 4 deleted the `return`. Round
  6 caught the parallel asymmetry that round 4 missed —
  `LoadReflections` had the same `else { ... }` shape — and applied
  the symmetric fix. Two-round proof of the
  `feedback_fix_the_class_not_the_instance.md` lesson.

- **Trust on-disk results files at decode time.** Round 5 Copilot
  finding. Rejected because `AppendResult` enforces
  `staged.Mission == missionID` on write but `LoadResults` did not
  enforce the same on read — the same trust-boundary asymmetry
  Phase 3.1 round 3 fixed with `KnownFields(true)`. Round 5 added
  the read-side check.

- **Override flag on `Store.Close` for emergency terminal
  transitions without a valid result.** Considered for the round 1
  spec; rejected because the gate is the entire point of Phase 3.6.
  An override flag would let any caller bypass the invariant the
  primitive exists to enforce. If a mission must close without a
  result, the operator can submit a minimal valid result first (one
  evidence entry, prose explaining why) — the audit trail then
  records the unusual close instead of hiding it.

### Scope deferred / filed as follow-up beads

- **`AppendResult` / `AppendReflection` rollback on event-log
  failure writes empty file instead of removing.** Inherited
  pattern from Phase 3.4 — both append paths use the same shape.
  Filed as `ethos-2a6` (P3) for a coordinated fix across both
  sibling stores in a separate PR scoped to rollback semantics.

## DES-037: Append-only mission event log reader API (SETTLED)

**Status**: Settled. Implemented 2026-04-08 as `ethos-07m.11` — the
Phase 3.7 primitive, the last one in Phase 3. After this merge the
four architecture rules from `~/Documents/agents-architecture.tex`
are runtime-enforced for the first time in the project's history.
Three worker rounds: 1 implementation (round 1), 1 local-review-fix
(round 2 — driven by a 4-reviewer cycle), and 1 polish round
(round 3 — driven by the three new LOW findings from the round 2
fix work). Six reviewer invocations across two local cycles. Local
reviewers: `djb` (frozen evaluator — 0.82, 0.94, 0.96 across the
three rounds), `mdm` (CLI specialist — caught the bullet-prefix
drift from the MCP walker, the stale `runMissionLog` godoc, and the
JSON envelope taxonomy break), `feature-dev:code-reviewer`
(correctness — caught the `LoadEvents` ID trust anchor asymmetry
with `LoadReflections` / `LoadResults`), `silent-failure-hunter`
(the scanner `ErrTooLong` tail-truncation consensus with djb, and
the `FilterEvents` silent-drop on unparseable `ts` under `--since`).

### Problem

Phase 3.1 shipped the mission contract with a private `appendEvent`
helper that writes every state transition to
`~/.punt-labs/ethos/missions/<id>.jsonl` as a JSONL event stream.
Phase 3.4 added `reflect`; Phase 3.5 added `verify`; Phase 3.6 added
`result`. The audit trail exists, but there is no public reader:
when a mission goes sideways, the leader doing a post-mortem has to
open the JSONL file by hand, parse each line manually, and hope no
line is corrupt. That is not a workflow — it is a salvage
operation, consulted precisely when the operator needs certainty
and gets ambiguity instead.

### Decision

A public `Store.LoadEvents(missionID) ([]Event, []string, error)`
method plus a new `ethos mission log <id>` CLI subcommand and a new
MCP `mission log` method, all reading through the existing writer
code path. The reader is additive — `appendEvent`,
`appendEventLocked`, and every existing caller are unchanged.

The reader's design pressure is post-mortem first: "show me as much
of what happened as possible, even if the file is partially
damaged." One corrupt line does not erase the log. One oversized
line does not truncate the tail. One attacker-planted ESC sequence
does not reach the operator terminal.

**Per-line degradation with line-numbered warnings.** The 3-tuple
return shape `([]Event, []string, error)` departs from `LoadResults`
/ `LoadReflections` — which return `([]T, error)` — because JSONL
can degrade per-line while YAML is whole-file all-or-nothing.
`LoadEvents` returns every parseable line plus a warnings slice
naming the 1-based line numbers that failed. A hypothetical
LoadResults-shaped signature would force callers to chose between
"strict: fail whole-file on any bad line" (unusable for a
post-mortem tool) or "silent: return partial with no signal"
(exactly the silent-failure mode the reader must not produce).

**Single-fd file read with `io.LimitReader`.** Round 3 replaced the
round 2 `os.Stat(logPath)` + `os.ReadFile(logPath)` path-level pair
with a single-fd `os.Open` → `f.Stat()` →
`io.ReadAll(io.LimitReader(f, maxLogSize+1))` sequence. The inode
is pinned by the fd; a concurrent writer cannot replace or redirect
the file between stat and read; the `+1` overflow byte turns silent
cap-bypass-via-race into a distinct `"grew past cap"` error. The
round 2 path was TOCTOU-vulnerable per djb and silent-failure-hunter
— both raised the finding as LOW. Round 3 closed it.

**Trust-boundary symmetry with the reflection and result loaders.**
Per-line strict decode (`DisallowUnknownFields`), `ts` parseability
check at decode time (round 2 H3), `missionIDPattern` validation at
API entry (round 3 R3-L2), directory rejection via `info.IsDir`
(round 2 M4), and whole-file size cap at 16 MiB (round 2 M3, round
3 R3-L1). Every defense the Phase 3.4 / 3.6 loaders enforce, the
Phase 3.7 reader enforces — and two the siblings don't (line-length
resilience via `bufio.Reader`, attacker-controlled-byte sanitization
via `sanitizeWarning`).

**Warning-string sanitization at source.** Round 2 H2 closed a log
injection vector: `DisallowUnknownFields` echoes attacker-controlled
JSON field names verbatim in its error strings, and the warnings
slice forwards them to operator terminals via stderr (round 1) or
the `Warnings:` stdout footer (round 2 M2) and into MCP payloads.
A planted line like `{"...","\x1b[2J\x1b[H\x1b[31mFAKE":1}` would
clear the operator's terminal and paint a spoofed "no corruption"
banner during a post-mortem. The `sanitizeWarning` helper walks the
string rune-by-rune via `utf8.DecodeRuneInString`, escaping bytes
`< 0x20` (except tab/space), DEL, and C1 (U+007F–U+009F), while
preserving legitimate multi-byte UTF-8. Invalid UTF-8 bytes are
detected via `RuneError+size==1` and hex-escaped directly — a naive
rune walk would hide them behind `U+FFFD`, and a byte walk would
mangle legitimate `0x80–0x9f` UTF-8 continuation bytes (e.g., `ß`
= `0xc3 0x9f`). The sanitizer is applied at every warning append
site in `decodeEventLog` — the source — so CLI, MCP, and the
DES-020 formatter all consume clean strings without
double-escaping.

**Partial-damage resilience via `bufio.Reader.ReadString`.** Round 2
H1 closed a silent tail-truncation bug: round 1 used `bufio.Scanner`
with a 1 MiB per-line cap. A single line exceeding the cap caused
the scanner to stop and every subsequent line to be silently lost.
The round 2 fix replaced the scanner with a `bufio.Reader` loop
that has no per-line cap; memory is bounded by the 16 MiB whole-
file cap instead. Plus a final-non-terminated-line branch so
no-trailing-newline files walk fully. The invariant
"partial damage does not erase the log" holds end-to-end: a 1.5 MiB
middle line in the test suite no longer drops the `close` and
`result` tail events.

**Wrong-mission-id policy: documented no-op.** The `Event` schema
has no top-level `mission_id` field (unlike `Result`, which carries
the field and whose loader enforces symmetry with the file path
per Phase 3.6 round 5 Copilot finding). Mission identity for the
event log is path-based — `logPath` runs the ID through
`filepath.Base`, and round 3 R3-L2 adds an upfront `missionIDPattern`
validation at the `LoadEvents` API boundary. A caller-planted
`details.mission` key inside the free-form `Details` map is opaque
payload, preserved untouched by the decoder. This is documented
explicitly in the `LoadEvents` godoc and covered by
`TestLoadEvents_WrongMissionInDetails`.

**Forward-compat for unknown event types.** Event type strings are
preserved as opaque during decode. A future phase (`worker_spawned`,
`round_started`, `evaluator_finished`, etc., per the roadmap's
aspirational §3.7 language) can emit new types without a reader
change. The `--event <type,list>` CLI filter accepts arbitrary
strings and simply returns empty if no events match the filter —
the flag does not validate against a closed enum. This is the
right call for an audit trail that will outlive any single phase's
event taxonomy.

**CLI and MCP surface parity.** `ethos mission log <id>` mirrors
`mission reflections` and `mission results`: `--json` flag,
`--event <type,list>` and `--since <RFC3339>` filters, bullet-
prefix human mode matching the DES-020 walker. `Warnings:` footer
on stdout (not stderr) so a caller piping `> events.txt` still sees
damage. `mission log` MCP method with the same filters, wrapped
JSON payload `{"events": [...], "warnings": [...]}`. The wrapped
shape is a deliberate taxonomy break from the sibling subcommands'
bare-array payloads — warnings MUST travel with events, and a bare
array cannot carry them. The Long help text documents the envelope
so CLI consumers reaching for `jq '.[].event'` see the shape before
they conclude the tool is broken.

### Rejected alternatives

- **2-tuple `([]Event, error)` return shape matching
  `LoadResults` / `LoadReflections`.** Considered for round 1.
  Rejected because JSONL degrades per-line by construction — a
  single corrupt line must not fail the whole read. A warnings
  slice parameter is the smallest change that preserves partial
  reads AND keeps callers honest about partial state. The Phase
  3.6 `ShowPayload.Warnings` field (round 3 D1) is the precedent
  the shape extends.

- **Sanitize warning strings at CLI and MCP surfaces instead of at
  source.** Considered in round 2. Rejected because every new
  surface (future MCP tools, future formatters, future log
  consumers) would have to remember to sanitize. Source-site
  sanitization is a trust-boundary invariant: the warning slice
  NEVER contains raw control bytes after `decodeEventLog` builds
  it. Downstream surfaces forward verbatim.

- **Re-export `AppendEvent` for external callers.** DES-031 round 3
  explicitly unexported the writer as a deadlock footgun — any
  external caller of a public `AppendEvent` from inside a locked
  block would block forever on Linux flock. Phase 3.7 keeps the
  writer private. If a future phase needs external append access,
  the fix is to expose a new API that acquires and releases the
  lock internally, not to re-export the locked helper.

- **Add new event types (`worker_spawned`, `round_started`,
  `evaluator_finished`, etc.) per the roadmap §3.7 aspirational
  language.** Rejected because adding event types requires writer
  changes, and the writer is frozen at its DES-031 / DES-033 /
  DES-034 / DES-035 / DES-036 schema. Phase 3.7 surfaces what the
  writer already emits. Additional event types can ship in a
  future bead when the need is concrete.

- **Hand-rolled line parser without `json.Decoder` strict decode.**
  Considered briefly for performance. Rejected because the
  trust-boundary symmetry with reflection and result loaders is
  load-bearing — an attacker with local write access to
  `~/.punt-labs/ethos/missions/<id>.jsonl` could otherwise smuggle
  extra fields the reader ignores.

- **Path-level `os.Stat` + `os.ReadFile` for the 16 MiB cap.**
  Round 2 implementation. Rejected when djb and silent-failure-
  hunter both raised the TOCTOU: a concurrent writer growing the
  file between stat and read silently bypasses the cap. Round 3
  replaced the pair with single-fd `os.Open` + `io.LimitReader(f,
  maxLogSize+1)` + post-read length check. The inode is pinned by
  the fd, growth is bounded by the limit reader, the `+1` byte
  converts silent truncation into a loud "grew past cap" error.

- **Silent `continue` on unparseable `ts` inside `FilterEvents`.**
  Round 1 implementation, confirmed by a round 1 test
  (`TestFilterEvents_EventWithInvalidTSSkippedUnderSince`) that
  locked in the silent drop as documented behavior. Rejected
  when silent-failure-hunter found the count-mismatch vector:
  same log, same intent, two different counts depending on
  whether `--since` was set. Round 2 H3 moved the `ts` parse
  check to `decodeEventLine`, so bad-ts lines never reach
  `FilterEvents`. Round 3 R3-M2 additionally converted
  `FilterEvents`'s residual silent `continue` into an explicit
  error return — defense in depth for future in-memory callers
  bypassing the decoder.

- **`TestLoadEvents_TraversalIDCannotEscape` as a positive
  assertion that `filepath.Base` collapse is the full defense.**
  Round 1 implementation. Rejected when the code reviewer raised
  the asymmetry with `LoadReflections` / `LoadResults`: the
  sibling loaders validate the mission exists via an existence
  check, not just a path collapse. Round 2 H4 added the
  `os.Stat(contractPath)` existence anchor; round 3 R3-L2 added
  the upfront `missionIDPattern` validation. The test was
  renamed to assert the layered defense rather than a single
  collapse.

### Scope deferred / filed as follow-up beads

- **Symlink follow weakness across all four mission loaders.**
  `LoadEvents`, `LoadReflections`, `LoadResults`, and `Store.Load`
  all follow symlinks via `os.ReadFile` (or the equivalent `os.Open`
  in Phase 3.7) without checking whether the resolved target is
  inside `missionsDir`. djb explicitly recommended NOT fixing this
  in Phase 3.7 alone — fixing one loader but not the others
  creates asymmetry worse than the current consistent weakness.
  Filed as `ethos-jjm` (P2) for a coordinated `os.Lstat`-based
  refusal across all four loaders in a single change.

- **`hook.FormatLocalTime` year and timezone rendering.** mdm
  raised that the shared helper renders `Mon Jan _2 15:04` — no
  year, no timezone — which is ambiguous for post-mortems across
  timezones. Deferred because the helper is shared with `mission
  show`, `mission list`, session output, and every other mission
  subcommand; fixing only `mission log` would create drift worse
  than the ambiguity. Filed as `ethos-vjp` (P3) for a global
  update to `2006-01-02 15:04 MST` or RFC3339 with offset.

- **Cobra exit code 2 on usage errors across all mission
  subcommands.** The punt-kit CLI standard specifies exit 2 for
  usage errors; cobra defaults to exit 1. Pre-existing across every
  mission subcommand, not introduced by Phase 3.7. Filed as
  `ethos-ag4` (P3) for a `cobra.SilenceErrors` plus `SilenceUsage`
  plus root-handler refactor.

- **`parseEventTypes` / `parseEventTypeList` duplication between
  `cmd/ethos/mission.go` and `internal/mcp/mission_tools.go`.**
  Not filed as a bead — kept deliberately as a 13-line pure
  function duplication per mdm's argument (which overrode djb's
  hoist recommendation): hoisting the helper into `internal/mission`
  would couple the trust-boundary package to CLI argument parsing,
  and McIlroy composability is about process boundaries, not
  private Go helpers. Round 2 K1 added cross-reference comments
  (`// mirror: internal/mcp/mission_tools.go parseEventTypeList`
  and vice versa) so the pairing is explicit. If a third call
  site ever lands, hoist then.

## DES-038: Worktree isolation is scratch-and-merge, not same-branch (SETTLED)

**Status**: Settled. Investigation and protocol change closing bead
`ethos-56a`, filed 2026-04-07 after 8+ failed worker rounds across
Phase 3.6, 3.7, 9ai.5, and 9ai.4 where the `isolation: worktree`
flag on the Agent tool appeared to misbehave. Investigation
conducted 2026-04-09 via a controlled experiment with a read-only
Explore agent; the "bug" turned out to be a misuse of a correctly-
behaving feature. This entry documents the investigation, the
corrected mental model, and the protocol change.

### Problem

The Claude Code Agent tool exposes an `isolation: "worktree"` flag
intended to "run the agent in a temporary git worktree, giving it
an isolated copy of the repository" (per the tool's own
documentation). The COO delegated workers with this flag set
throughout Phase 3.6, 3.7, 9ai.5, and 9ai.4 on the expectation that
each worker would operate on an isolated filesystem copy of the
**leader's current feature branch**, so two workers on the same
branch could work in parallel without fighting each other's
writes.

That expectation produced eight incidents across four phases,
falling into six categories of failure:

1. **Worker commits never reached the leader's feature branch.**
   bwk and mdm reported "worktree on `worktree-agent-<id>`, not on
   `feat/X`" in every round. The leader's branch never advanced.
2. **Workers fell back to the leader checkout via absolute paths.**
   The delegation prompts used absolute paths throughout (e.g.,
   `<repo-root>/internal/mission/...`), so workers wrote directly
   into the leader checkout, bypassing the worktree entirely.
3. **Shared `.git/index` race.** Two workers in the same leader
   checkout shared the staging area. During Phase 9ai.5, mdm
   caught bwk's concurrent `git add` interleaving into mdm's
   staging area — recovered by unstaging bwk's files by name.
   Captured in `feedback_shared_branch_staging_race.md`.
4. **Stale test execution.** In Phase 9ai.4, bwk's first `go test`
   ran against the worktree copy and silently missed the new test
   case because the worktree was at the old base commit; the new
   test had been written into the leader checkout via the absolute-
   path fallback. bwk caught it by switching to `go -C <leader>`.
5. **Accumulated zombie worktree directories and orphan
   `worktree-agent-*` branches** piled up in the leader checkout
   across phases, requiring manual `git worktree remove` + `git
   branch -D` cleanup.
6. **Nested worktree paths.** The Agent tool creates the worktree
   at `$PWD/.claude/worktrees/agent-<id>` — relative to the
   leader's current PWD, not the repo root. When the leader's cwd
   had drifted into a previous worktree directory, new worktrees
   nested inside, producing paths like
   `.claude/worktrees/agent-a73d8876/.claude/worktrees/agent-a554e644`.

The original bead filed two hypotheses: (a) the worktree wrapper
sets CWD but doesn't intercept absolute paths, so the leader's
absolute-path prompts bypass isolation; (b) ethos sub-agent types
may not honor the flag at all. Neither hypothesis was correct.

### Decision

**`isolation: worktree` is scratch-and-merge isolation, not
same-branch isolation, and the COO will not use it for single-
worker feature delivery going forward.**

A controlled experiment with a read-only Explore agent confirmed
the actual behavior:

1. The flag creates a git worktree at
   `$PWD/.claude/worktrees/agent-<id>/`.
2. A new branch `worktree-agent-<id>` is created from the leader's
   **current HEAD** (not from the leader's current branch ref).
3. The worker's CWD is set to the new worktree.
4. Worker git operations default to the new worktree and new
   branch — so commits land on `worktree-agent-<id>`, not on the
   leader's feature branch.
5. If the worker makes no filesystem changes, the worktree is
   auto-cleaned on agent completion. If changes are made, the
   worktree and branch persist and the agent's result returns the
   path for deliberate integration by the leader.

This matches the documented design: "changes are returned in the
result" means the leader is expected to cherry-pick, rebase, or
merge the worktree-agent branch deliberately after review. The
pattern is **scratch-and-merge**: the worker operates on a
disposable branch, and the leader chooses whether the output
becomes permanent.

For the ethos Phase 2/3 delegation cycle (leader writes spec →
worker implements → worker commits → leader reviews → ship), this
pattern adds friction without benefit. The worker's commits
should land directly on the feature branch so the leader's usual
push-PR-merge workflow completes the cycle. `isolation: worktree`
requires an extra merge-back step that the workflow does not
need.

**Protocol change** (captured in `~/.claude/CLAUDE.md` and
`feedback_worktree_isolation_semantics.md` — both user-global,
not tracked in this repo):

- **Default for single-worker feature delivery**: do NOT use
  `isolation: worktree`. Work in the leader checkout. The worker
  commits to the feature branch directly. The leader waits,
  reviews, pushes, creates the PR.
- **Use `isolation: worktree` only for**: exploratory/scratch work
  where the output may or may not merge; parallel fan-out where
  the leader explicitly sequences integration of multiple workers'
  branches; recoverable snapshot work where a persistent worktree
  branch is the deliverable.
- **Never use `isolation: worktree` with two or more workers on
  the same branch in parallel** — the fallback pattern produces
  the shared-index race documented in
  `feedback_shared_branch_staging_race.md`. Parallel workers
  require separate branches or serial commits.

### Rejected alternatives

- **Keep using `isolation: worktree` and work around the behavior
  with absolute-path fallbacks.** This is the pattern that
  produced the 8 incidents. It silently defeats the isolation the
  flag promises and creates the staging race and the stale-test
  execution hazards. Rejected.

- **Fix the Agent tool's behavior upstream so worktrees land on
  the leader's current branch.** This would be a Claude Code
  primitive change, outside ethos's control, and it would break
  the legitimate scratch-and-merge use case. The documented
  behavior is correct as it stands; the project's misuse is the
  thing to fix. Rejected.

- **Add an ethos-side wrapper that intercepts Agent calls and
  rewrites `isolation: worktree` to create the worktree on the
  leader's feature branch.** Ethos does not wrap the Agent tool
  at all — the tool is a Claude Code primitive. Wrapping it would
  require writing a new Claude Code plugin. Rejected as out of
  scope.

- **Cherry-pick worktree-agent commits back to the feature branch
  after each worker round.** Correct in principle, but adds a
  merge step to every delegation cycle. The cost exceeds the
  benefit for single-worker serial delivery. Kept as a technique
  for exploratory work; rejected as the default.

### Scope deferred

None. This investigation closes `ethos-56a`. No code changes
shipped: the ethos codebase has no worktree-creation logic to
modify, and the Claude Code behavior is working as designed.
Deliverables landed in three places:

1. A new user-global memory entry
   `feedback_worktree_isolation_semantics.md` documenting the
   actual semantics, the symptoms that misled the COO, the
   default-off protocol, and the cleanup commands.
2. A new rule in `~/.claude/CLAUDE.md` (user-global) prohibiting
   `isolation: worktree` for single-worker feature delivery.
3. This DES-038 entry preserving the investigation in the ethos
   repo's ADR archive.

Existing cross-referenced memories:
`feedback_shared_branch_staging_race.md` (the `.git/index` race
that was a direct consequence of the fallback pattern),
`feedback_commit_cwd_drift.md` (cwd drift into worktree
directories), and `feedback_verify_binary_execution.md` (the
stale-test execution symptom).

## DES-039: Store.Close returns satisfying result (SETTLED)

**Status**: Settled. Shipped in PR #200 (`ethos-30c`), commit
`cee2d51`. Bead `ethos-30c`.

### Problem

After `Store.Close` commits the terminal transition to disk and
releases the lock, the CLI needs the satisfying round and verdict
for the echo and JSON response added by `ethos-30c`. The initial
approach (PR #200 commit `42962f7`) added `Store.Load` +
`Store.LoadResult` calls after `Close` returned. This created a
TOCTOU window: a transient I/O error or file deletion between
`Close` and `LoadResult` would cause `os.Exit(1)` for an operation
that already succeeded on disk. Scripts retrying on non-zero exit
would then hit "already in terminal state" — actively misleading.

Bugbot flagged the missing nil-check on `LoadResult`; the
intermediate fix (commit `871dc24`) added the guard but was
insufficient — it turned a panic into a clean error, but the clean
error was still wrong: reporting failure for a committed write.
Both Copilot and Bugbot then independently flagged the deeper
regression on the same review cycle.

### Decision

Change `Store.Close` from `func (s *Store) Close(id, status string)
error` to `func (s *Store) Close(id, status string) (*Result,
error)`. The close gate already materializes the satisfying result
inside `closeLocked` while holding the lock. Return it to the
caller. No re-read, no race.

`runMissionClose` dropped from 54 lines to 33. The JSON and text
echo paths use the returned `*Result` directly. The returned result
is guaranteed non-nil on success because the close gate already
verified its existence.

All call sites updated: `cmd/ethos/mission.go`,
`internal/mcp/mission_tools.go`, `internal/mission/store_test.go`,
`internal/mission/log_test.go`,
`internal/hook/subagent_start_test.go`,
`cmd/ethos/mission_test.go`.

### Rejected alternatives

- **Non-fatal echo path (exit 0 with degraded output if
  `LoadResult` fails).** Masks real failures. Operators cannot
  distinguish "close succeeded but echo failed" from "close
  succeeded with full echo." The exit code becomes unreliable for
  scripting. The whole point of `ethos-30c` was to make write
  commands trustworthy — a degraded path undermines that.

- **Keep the re-read with nil-check (the intermediate fix in
  `871dc24`).** Correct for panic prevention, but still exits
  non-zero after a committed write. A TOCTOU race between `Close`
  and `LoadResult` — however unlikely — would cause scripts to
  retry a close that already landed. Worse than the original
  silent-success behavior.

- **Add a separate `CloseAndGetSummary` method.** Two methods that
  do the same thing (close the mission) with different return
  shapes. The simpler change is to return the result from `Close`
  itself, which is what every caller needs anyway.

## DES-040: Escalate via existing Result, not a new artifact type (SETTLED)

**Status**: Settled. Documented in PR #202 (`ethos-cqt`). CLI help
updated to show the escalate example. Bead `ethos-cqt`.

### Problem

During the Phase 3 mission primitive dogfood (PR #199,
`ethos-vjp`), the worker hit a write_set boundary and needed to
tell the leader "the scope is wrong — I need more room." The
mission primitive had no *documented* path for mid-round scope
escalation. The worker reported through the agent conversation
channel, which is not recorded in the mission's append-only event
log. Future operators auditing the mission would see only
`close status=escalated` without the "why."

The escalate path actually existed in the schema from Phase 3.6:
`verdict: escalate` is a valid `Result.Verdict` value, and
`files_changed` may be empty ("only when the round made no file
changes" — `internal/mission/result.go:161-164`). The combination
`verdict=escalate, files_changed=[], open_questions=[reason]` is a
well-formed result that passes validation, persists to disk, and
appears in the event log. Workers didn't know this because the CLI
help showed only a `verdict: pass` example with non-empty
`files_changed`.

### Decision

Document the existing shape as the canonical mid-round escalation
protocol. Do not add a new artifact type.

- `ethos mission result --help` now shows a complete escalate
  example: `verdict: escalate`, `files_changed: []`,
  `open_questions` populated with the scope-expansion request,
  `prose` explaining the blocker.
- `ethos mission --help` links to the escalate protocol.
- The delegation template convention instructs leaders to include
  the escalate protocol reference in every contract's `context`
  field so new workers discover it before they hit a boundary.

The primitive's surface area stays at two worker-authored artifact
types (`Result` and `Reflection`) and three leader actions
(`reflect`, `advance`, `close`). No new store methods, event types,
or formatter paths.

### Rejected alternatives

- **Dedicated `Block` or `ScopeRequest` artifact type alongside
  `Result` and `Reflection`.** Adds a third artifact to learn, a
  third store method, a third event type, a third formatter path —
  all for a scenario that happens once per write_set gap per
  mission. The existing `Result` shape carries the data. A new type
  would increase the API surface without adding information the
  existing type cannot convey.

- **`Reflection` with `recommendation=escalate` as the worker's
  escalation channel.** Reflections are leader-authored in the
  mission workflow — the leader writes the reflection after
  reviewing the worker's result. A worker-initiated scope issue
  should come through the worker's artifact (`Result`), not the
  leader's. Routing it through `Reflection` would conflate the
  authorship model.

- **No formal path; workers use the agent conversation channel.**
  This is what happened during the dogfood. The escalation reason
  was not recorded in the mission event log. Operators auditing the
  mission saw `close status=escalated` without context. The
  append-only log exists precisely so future readers can
  reconstruct what happened without reading the conversation.

## DES-041: Conventions over enforcement for write-sets and safety constraints (SETTLED)

**Status**: Settled. Design philosophy documented in PR/FAQ v2.1.
Bead `ethos-2br` (closed — agency spectrum resolved).

### Problem

Write-set boundaries and safety constraints define what an agent
should and should not do. Two enforcement models exist:

1. **Runtime enforcement** — the system blocks file writes outside
   the write-set at the filesystem level, using sandboxing or
   kernel-level controls.
2. **Convention enforcement** — the contract declares the boundary,
   the agent agrees to it as part of its system prompt, and the
   review pipeline (code-reviewer, Copilot, Bugbot, audit log)
   catches violations after the fact.

### Decision

Ethos uses conventions, not runtime enforcement. Write-sets are
contracts verified in review, not filesystem sandboxes.

**Rationale:**

- Agent runtimes (Claude Code, Codex CLI, OpenCode) do not expose
  filesystem sandboxing primitives. Runtime enforcement would require
  kernel-level isolation (seccomp, namespaces) that is outside ethos's
  layer — a different product entirely.
- The convention model is cheap to adopt. A mission contract takes
  seconds to write. A sandbox takes infrastructure to provision.
- The audit trail catches violations after the fact. The append-only
  event log, the `--verify` flag on `ethos mission result`, and the
  layered review pipeline (local code-reviewer → Copilot → Bugbot)
  provide multiple catch points.
- The same convention model scales across the agency spectrum: a tight
  implementation mission and an open design mission use the same
  contract schema. The leader controls agency through contract
  specificity (tight write-set and specific criteria vs. broad
  write-set and open-ended criteria), not through a mode switch.

### Rejected alternatives

- **Filesystem sandboxing via seccomp/namespaces.** Requires
  kernel-level integration that agent runtimes don't support. Would
  make ethos a sandboxing tool rather than an identity/workflow tool.
  Out of scope.
- **Pre-tool hook enforcement that blocks writes outside write-set.**
  Claude Code's PreToolUse hooks can reject tool calls, but the hook
  runs in shell and would need to parse the tool input to extract
  file paths, match against the mission's write-set, and handle
  edge cases (relative paths, symlinks, directory creation). Fragile,
  slow on the hot path, and creates friction during refactors where
  intermediate states are knowingly outside the declared set.
- **No write-set at all — trust the agent.** Misses the point. The
  write-set isn't about distrust; it's about coordination. Two
  missions claiming the same files is a merge conflict waiting to
  happen. The write-set prevents the conflict at creation time, even
  if it doesn't block the write at runtime.

## DES-042: RunE for all CLI handlers (SETTLED)

**Decision**: Every cobra command in `cmd/ethos/` uses `RunE` (returns
`error`) instead of `Run` (returns nothing). Handlers return errors
instead of calling `os.Exit`. A `silentError` type signals "already
reported — exit non-zero without printing."

**Reasoning**: `Run` + `os.Exit` makes handlers untestable in-process.
Coverage tools can't measure code that kills the process. The RunE
pattern lets tests call handlers directly via `execHandler`, capture
stdout/stderr, and assert on both output and error values. This moved
`cmd/ethos/` from 22% to 64% measurable coverage.

`SilenceErrors` is set on `rootCmd`. The `main()` function prints
errors with the `ethos:` prefix and routes to exit code 1 (runtime)
or 2 (usage). Handlers that already reported their failure (e.g.,
`runDoctor` printing a FAIL table) return `silentError{}` to avoid
double-printing.

**Rejected alternatives**:

- Keep `Run` + `os.Exit`, test only via subprocess — subprocess tests
  prove behavior but are invisible to coverage tools. The project needs
  both: subprocess tests for exit-code contracts, in-process tests for
  measurable coverage of handler logic.
- Refactor `printJSON` to return errors — 30+ call sites across files
  not being touched. Added `writeJSON` alongside instead; `printJSON`
  delegates to `writeJSON` internally.

## DES-043: Three-layer behavioral test architecture (SETTLED)

**Decision**: L4 behavioral tests use three layers, each with different
assertion strategies:

- **Layer A (deterministic)**: Mission event log, git diff, result YAML
  structure. No LLM calls. Catches protocol violations.
- **Layer B (LLM-judged)**: Agent output + persona definition sent to
  Claude Sonnet as a judge. Returns `{violated, evidence, confidence}`.
  Catches persona constraint violations that can't be checked
  mechanically.
- **Layer C (adversarial)**: Deliberately tempts agents to break
  constraints. Combines deterministic + judge assertions. Proves the
  system holds under pressure.

All three layers share a common harness (`tests/behavioral/`) behind
a `//go:build behavioral` tag, excluded from `make check`. Run via
`make test-behavioral` (requires `ANTHROPIC_API_KEY` and `claude` CLI).
Daily CI via `.github/workflows/behavioral.yml`.

**Reasoning**: Deterministic tests are fast and cheap but can only check
structural properties (did the file change? did the event appear?).
Persona compliance requires judgment — did the reviewer stay in its
lane? An LLM judge provides this at ~$0.05/call with structured output.
Adversarial scenarios prove the system works when agents are pushed,
not just when they cooperate.

The `behavioral` build tag keeps these tests out of the per-commit
suite. They spawn real Claude Code agents (~$0.50 each, ~2 min each)
and are too slow and expensive for CI on every push.

**Rejected alternatives**:

- promptfoo with `llm-rubric` — doesn't support `claude --bare` with
  MCP config and per-agent system prompts. The custom harness fits the
  exact subprocess model used throughout ethos.
- All-deterministic, no LLM judge — misses the persona compliance
  dimension entirely. "Did the reviewer write code?" can be checked
  via git diff, but "did the reviewer stay in character?" requires
  judgment.
- Python test harness — ethos is a Go project. `go test` with build
  tags keeps the toolchain unified. The Anthropic API call is a single
  `net/http` POST, no SDK needed.

## DES-044: Extensions resolve through the layered identity chain (SETTLED)

**Decision**: `ethos ext set/get/delete/list` resolve identities through
the same repo-local → global chain that `ethos identity show` uses.
Extensions still write to the global `.ext/` directory (extensions are
personal, not git-tracked), but the identity lookup finds repo-local
handles.

**Reasoning**: A repo-local identity (e.g., `claudia` defined only in
`.punt-labs/ethos/identities/claudia.yaml`) should be extensible. The
extension data is personal (voice config, tool preferences) and belongs
in `~/.punt-labs/ethos/identities/claudia.ext/`, but the identity that
the extension attaches to may only exist in the repo. The `IdentityStore`
interface already includes all Ext methods, and `LayeredStore.ExtSet`
already checks existence across both layers before writing to global.

**Rejected alternatives**:

- Require all extended identities to exist in the global store —
  forces users to duplicate repo-local identities globally just to
  set an extension. The workaround (copying the YAML) was the bug
  report that motivated this fix.

## DES-045: Mission archetypes as typed subtypes (SETTLED)

**Decision**: Missions declare a `type` field that maps to an archetype
definition on the filesystem. Archetypes are YAML files under
`~/.punt-labs/ethos/archetypes/` (global) or
`.punt-labs/ethos/archetypes/` (repo-local). 7 archetypes ship as seed
content: design, implement, test, review, inbox, task, report.

Each archetype declares budget defaults (`rounds`, `reflection_after_each`),
write-set constraints (open vs restricted), and required contract fields.
The mission store applies archetype defaults at create time when the
contract omits them, and validates archetype-specific constraints
alongside the existing contract validation.

**Reasoning**: Different mission shapes (design exploration vs
implementation vs code review) need different defaults and constraints.
Hard-coding these in the Go binary forces a code change for every new
pattern. YAML on the filesystem makes archetypes extensible without
modifying ethos -- teams add a file and it works. The `type` field
defaults to "implement" for backward compatibility with pre-archetype
contracts.

**Rejected alternatives**:

- Archetype as a Go enum with compiled-in defaults -- not extensible
  without rebuilding the binary. Teams with custom workflows would need
  to fork.
- Archetype embedded in the contract with no external definition --
  duplicates defaults across every contract and loses the single source
  of truth for what "design" or "review" means.

## DES-046: Mission pipelines as workflow composition (SETTLED)

**Decision**: A pipeline is a named sequence of typed mission stages.
Each stage references an archetype and carries stage-specific overrides.
The pipeline is orchestration, not a new archetype -- it composes
existing archetypes into a repeatable workflow. Stages are independent
missions; a result artifact connects one stage's output to the next
stage's input.

3 sprint templates ship as seed content: quick (design + implement),
standard (design + implement + test + review), and full (design +
implement + test + review + report + inbox). `ethos mission pipeline
list` and `ethos mission pipeline show <name>` expose the templates
via CLI with `--json` support.

**Reasoning**: Real work is rarely a single mission. A feature needs
design, implementation, testing, and review -- each with different
archetypes, different workers, and different success criteria. Without
pipelines, the leader manually chains missions and remembers the
sequence. The pipeline template encodes the sequence once. Pipelines
do not auto-execute -- they are templates that the leader instantiates
stage by stage, preserving human judgment at each transition.

**Rejected alternatives**:

- Auto-executing pipeline that spawns all stages without leader
  intervention -- removes the reflection checkpoint between stages.
  The bounded-rounds discipline from DES-034 applies at the pipeline
  level too: the leader decides whether to proceed after each stage.
- Pipeline as a special archetype -- conflates the composition layer
  with the individual mission layer. A pipeline contains archetypes;
  it is not one.

## DES-047: Verifier read-allowed policy (SETTLED)

**Decision**: Verifiers get full read access to the repo. Only Write
and Edit are blocked by the PreToolUse hook. Read, Glob, and Grep are
unrestricted.

**Reasoning**: A verifier reviewing code needs to read imports, callers,
and surrounding context -- not just the files in the write-set. Blocking
Read made verifiers unable to review effectively. The real constraint
is: don't modify anything (Write/Edit blocked) and don't drift the
evaluation standard (frozen evaluator hash, DES-033).

**Rejected alternatives**:

- Block all file access outside write-set -- makes review impossible,
  verifier can't understand context.
- Allow Read only for files imported by write-set files -- too complex
  to compute, fragile.

References PR #246. Shipped v3.1.0.

## DES-048: Pipeline conflict-skip trust model (SETTLED)

**Decision**: Accept the trust assumption in the same-pipeline conflict skip.
When `c.Pipeline == existing.Pipeline`, write-set conflict detection is skipped
(DES-032: intra-pipeline overlap is expected). A hand-crafted contract with a
faked `pipeline:` field bypasses conflict detection against all missions in that
pipeline.

**Reasoning**: Ethos is local-only, single-user. Write-sets are conventions,
not kernel-level enforcement (DES-014/DES-041). The `pipeline` field is a
grouping label for related missions that share files. A user who fakes it to
bypass their own conflict check is not an attack vector worth guarding against.
The mechanism that matters is the review step catching unexpected writes,
regardless of how the contract was framed.

**Rejected alternatives**:

- Verify the claimed pipeline exists by scanning missions before create --
  slows hot path; user can still fake by creating a decoy mission first.
- Require DependsOn entries to reference members of the claimed pipeline --
  adds coupling; breaks standalone contracts that happen to use a pipeline
  grouping label.

**Implications**: The `pipeline` field is semantically "grouping label for
related missions that share files." It is not a security boundary.

## DES-049: Rename inputs.bead to inputs.ticket (SETTLED)

**Context**: The `bead` field on Contract.Inputs is named after Punt
Labs' internal issue tracker. Ethos is open-source; non-Punt-Labs users
of other trackers (Linear, Jira, GitHub Issues) find the name confusing.
The value is semantically an issue-tracker ticket ID -- not specific to
beads.

**Decision**: Rename to `inputs.ticket`. Accept `inputs.bead` as a
deprecated alias during a transition period (minor release + 1). Emit a
stderr deprecation warning when a contract is loaded with `bead`. Marshal
emits `ticket` only. Reject both keys simultaneously to prevent ambiguity.

**Rejected alternatives**:

- Hard rename (break on load): strands existing missions and event logs.
- Two fields (Bead AND Ticket in struct): doubles API surface; users must
  check both. Poor Go ergonomics.
- Keep the name: hostile to adoption for a naming debt.

**Implications**:

- Existing contracts and event logs continue to work.
- New contracts and event logs use `ticket`.
- A future major release removes the `bead` alias.

## DES-050: Automatic mission traceability via repo-local JSONL (SETTLED)

**Context**: `ethos mission close` writes contract and result data to the
global missions directory (`~/.punt-labs/ethos/missions/`). This data
never appears in a repo's git history. `ethos mission export` exists but
is manual and unused. The prfaq claims "git-tracked mission export" --
technically true, but a manual tool nobody runs is no tool.

**Decision**: On every successful `Store.Close`, append a compact JSON
summary line to `<repoRoot>/.punt-labs/ethos/missions.jsonl`. One line per closed
mission (~300 bytes). The trace write runs AFTER the close event commits
and is non-fatal: a failure prints a stderr warning but does not roll
back the close. The full contract + result YAMLs remain in the global
store for deep auditing; the JSONL trace is the lightweight, greppable
git-tracked summary.

**Schema**: `TraceSummary` struct with: id, created_at, closed_at,
status, type, leader, worker, evaluator, ticket, write_set,
success_criteria, rounds_used, rounds_budgeted, verdict, files_changed,
pipeline.

**Rejected alternatives**:

- Automatic `ethos mission export` on close: exports the full contract +
  result YAML, which is too heavy for an append-only log and produces
  multi-file diffs per close.
- Database (SQLite) in repo: adds a binary dependency, breaks grep, and
  produces opaque git diffs.
- Git notes: not greppable, not visible in `git log` without flags, and
  most git UIs don't surface them.

**Implications**:

- Every repo that uses ethos missions gains a `.punt-labs/ethos/missions.jsonl`
  file in its git history.
- The `Store` gains a `repoRoot` field, wired via `WithRepoRoot`.
- The JSONL file is append-only by convention; concurrent appends are
  safe because each Close holds its own flock and appends a single line.

## DES-051: Team bundle activation (SETTLED)

**Context**: The `punt-labs/team` submodule at `.punt-labs/ethos/`
conflates two independent concerns: the generic gstack content that
ships with ethos (archetypes, pipelines, starter personalities) and
the Punt Labs internal team registry (identities, roles,
collaborations). A first-time user outside Punt Labs cannot adopt
gstack without cloning a submodule whose identity content is wrong
for them. A user with a private team has no mechanism to switch the
active team — the repo-local layer is a single submodule.

The existing two-layer resolver (repo-local → global) was designed
around "one team per repo." The ecosystem now needs "one or more
teams available, one active at a time," without breaking any repo
that uses the current submodule layout.

**Decision**: Introduce **team bundles** — self-contained
directories of ethos content keyed by a `bundle.yaml` manifest at
their root. Bundles ship three ways: embedded in ethos and seeded
to `~/.punt-labs/ethos/bundles/<name>` (gstack); added as git
submodules under `.punt-labs/ethos-bundles/<name>` (punt-labs,
private teams); or authored locally as plain directories.

Exactly one bundle is active per repo, selected by a new
`active_bundle` field in `.punt-labs/ethos.yaml`. The resolver
becomes three-layer: **repo-local → active bundle → global**.
Writes still target global; the bundle layer is read-only. When
`active_bundle` is unset, the middle layer is skipped and behavior
is byte-identical to the current two-layer implementation. A new
`ethos team` subcommand group (`available`, `activate`, `active`,
`deactivate`, `add-bundle`, `migrate`) exposes bundle management.

**Reasoning**:

- Separates generic ethos content (gstack) from team registries
  (punt-labs), so non-Punt-Labs users can adopt ethos without
  cloning the wrong submodule.
- Config-driven activation is observable in `git diff`, portable
  across platforms, and lets comments survive YAML round-trip.
- Adding the bundle layer between repo and global preserves every
  existing invariant: repo-local overrides still win, global
  fallback still catches the tail, and writes still target
  user-owned storage.
- Opt-in migration (`ethos team migrate`) means no existing repo
  breaks on upgrade. The legacy submodule pattern keeps working
  verbatim.
- A bundle is just a directory with a manifest. No new file
  formats, no new binary dependencies, no registry service.

**Rejected alternatives**:

- **Symlink-based activation** (`active -> bundles/gstack`): not
  portable to Windows, breaks `go:embed` tests, produces no audit
  trail in git diffs, and risks drift between symlink and config.
- **Replace the repo layer with the bundle**: breaks every repo
  currently using `.punt-labs/ethos/` as a submodule. The repo
  layer's role (project-specific overrides) is orthogonal to the
  bundle's role (shared content).
- **Network bundle registry** (npm-style): premature without an
  adoption signal. File-based bundles distributed by git submodule
  cover every known use case and reuse existing infrastructure.
- **Per-command `--bundle <name>` flag**: activation is persistent
  state; flags are transient. Threading a flag through every code
  path mixes the two concerns and creates surprise.

**Implications**:

- `.punt-labs/ethos.yaml` gains an optional `active_bundle` field.
  Writers use `yaml.Node` to preserve comments and key ordering.
- A new package `internal/bundle/` owns manifest parsing,
  discovery, and validation.
- Each layered store (`internal/identity`, `internal/team`,
  `internal/role`, `internal/attribute`) gains a
  `NewLayeredStoreWithBundle(repoRoot, bundleRoot, globalRoot)`
  constructor alongside the existing two-argument form.
- `internal/seed/` deploys the embedded gstack bundle to
  `~/.punt-labs/ethos/bundles/gstack/` on `ethos seed`.
- `punt-labs/team` keeps gstack content for one release with a
  deprecation warning, then removes it after the migration ships.
- Downstream consumers (Biff, Vox, Beadle) that read
  `~/.punt-labs/ethos/identities/` directly must be audited; any
  hard-coded path assumptions become follow-up beads.
- Ships in v3.7.0. This ADR moves to SETTLED when the feature
  lands.

**Reversal (2026-08-07)**: `ethos team migrate` (referenced above in
the Decision and Reasoning sections) was removed post-ship, by
operator ruling, under bead `ethos-mvum`. The rest of
DES-051 — bundles, three-layer resolution, `active_bundle`, and the
`available`/`activate`/`active`/`deactivate`/`add-bundle` subcommands
— is unaffected and remains SETTLED.

`migrate` existed to move Punt Labs' own internal repo fleet off the
legacy `.punt-labs/ethos/` submodule pattern onto the bundle layout —
a real, one-time need, not a hypothetical one. But a one-time internal
rollout did not warrant a permanent, `--apply`-flagged CLI command. It
shipped two defects: a P0 where `git submodule deinit -f` silently
destroyed untracked mission records in repos that had them (closed
won't-fix, `ethos-0s2i`), and a P2 where the command gave no warning
that it restructures the repo before doing so (closed won't-fix,
`ethos-fblk`). Operator: "That should have never ever been built.
Remove it."

No replacement command exists. The legacy submodule pattern keeps
working exactly as this ADR's Reasoning always intended: the resolver
falls back to two-layer behavior (repo-local → global) whenever
`active_bundle` is unset, "byte-identical to the current two-layer
implementation." Repos on the legacy pattern take no action and lose
nothing.

## DES-052: Separate `extract_into` axis for new-file creation (SETTLED)

**Status**: Settled. Implemented 2026-05-21 as bead `ethos-3emm`,
priority P1. Design reviewed by `rop` (Pike, minimalism) on mission
`m-2026-05-21-004` — verdict ITERATE with three named edits
(cooperative-constraint problem statement, closed six-rule
asymmetry table, operator audit-visibility paragraph), all applied
before implementation. Implementation by `bwk` on mission
`m-2026-05-21-005` across the rounds documented above, frozen
evaluator `rsc`. Verdict PASS (0.95).

### Round-by-round summary

- **Round 1 — schema, validation, six-rule admission**
  (`38ff7fb`, `64ac28f`). `Contract.ExtractInto []string` with
  `omitempty` tag. `Validate` rule 11 walks every ExtractInto
  entry through the existing per-entry helper (traversal,
  absolute, drive-letter, UNC, control, null, zero-width); rule
  17 rejects code-file extensions. `findWriteSetConflicts`
  rewritten as the closed six-rule form via
  `entryPairConflicts(a, aIsDir, aIsEI, b, bIsDir, bIsEI)`. The
  new `ws-file × ei-dir` constraint closes the cross-mission race
  where one mission's file claim collides with another's extract
  directory. Tests: `TestEntryPairConflicts` (row-by-row matrix,
  symmetry-verified), `TestFindWriteSetConflicts_ExtractInto`,
  `TestIsDirEntry`. Leader committed the conflict-side work on
  the worker's behalf after the round-1 agent session ended;
  round 2 resumed cleanly from the committed state.

- **Round 2 — hook plumbing** (`4a561a5`). `PreToolUse` stat-
  then-allow branch: a Write/Edit that misses `ETHOS_VERIFIER_ALLOWLIST`
  falls through to a single `os.Stat`; if the file does not exist
  and the path is under any listed directory in
  `ETHOS_VERIFIER_EXTRACT_INTO`, the call is allowed. Stat runs
  only on the path that already failed the allowlist check, so
  the hot path is unchanged. `SubagentStart` populates the new
  env var from `m.ExtractInto` across all verifier missions
  (deduplicated) and inserts an "Extract into (new files only)"
  section into the isolation block with the operator audit
  summary line first, then prose distinguishing modify from
  create, then the per-directory list. Empty `extract_into`
  renders identically to pre-DES-052. Tests cover all four
  DES-named cases plus the modify-via-extract_into attack.

- **Round 3 — archetype constraints, lint, changelog**
  (`d546d19`, `a6268ba`, `73ca05f`). `Archetype.ExtractIntoConstraints`
  via the same glob matcher as `WriteSetConstraints` (with a
  trailing-slash strip so `docs/` and `docs` both match
  `docs/**`). Ten seed archetype YAMLs updated per the default
  table — `design → docs/**`, `investigate → docs/** + .tmp/**`,
  others explicit empty list (test is `[]` per the Go convention
  that tests live alongside source).
  Lint `H11 lintMonolithPressure` fires only when three signals
  align (file-only `write_set` + empty `extract_into` +
  extraction verb in `success_criteria`). `H12
  lintExtractIntoFileEntries` emits one warning per offending
  entry. `CHANGELOG` Unreleased Added entry points at this DES.

### Migration

Every new field carries `omitempty`. Existing contracts on disk
validate unchanged; `Store.Load` continues to accept them.
Pre-DES-052 missions in flight at upgrade time have no
`ExtractInto`; `PreToolUse` with no `ETHOS_VERIFIER_EXTRACT_INTO`
falls through to the existing allowlist check unchanged;
`SubagentStart` with empty `m.ExtractInto` omits both the env var
and the isolation block section. The only admission-time behaviour
change is the new `ws-file × ei-dir` conflict; `ei-dir × ei-dir`
explicitly never conflicts, so two missions extracting into the
same directory cooperate as the design requires.

### Lessons

`rop`'s ITERATE verdict caught three real defects in the draft:
(1) the problem statement attributed worker accretion to a
PreToolUse block that does not fire for workers — the constraint
is cooperative, and the DES had to say so or jra would have read
an enforced invariant where there was a cooperative one; (2) the
original asymmetry table missed the `ws-file × ei-dir`
cross-mission race, which the closed six-rule form
makes structurally impossible to forget; (3) the verifier
isolation block needed an operator audit-visibility paragraph,
otherwise a leader who accidentally listed `extract_into: [docs/]`
on a code-implementation mission would not have caught the
mistake in review. All three are now in the DES and in the code.
The Pike-minimalism lens (do we really need two slices?) was
the right framing — the rejected-alternatives section now
distinguishes the proposal from Shape A (redefine trailing-slash
write_set entries), Shape B (single slice with `+` create-only
marker), and the schema-untouched template-prose alternative,
with the reasoning that cooperative enforcement demands the YAML
*carry* the intent rather than delegate to implicit policy.

### Problem

The Phase 3.1 `write_set` field declares which paths a worker may
modify. DES-032 added cross-mission admission control on top of it
(segment-prefix overlap rejection). DES-035 added verifier isolation
with a PreToolUse hook (`internal/hook/pretooluse.go`) that enforces
the same path set as a closed write allowlist.

The single-axis design conflates two distinct authorizations:

1. **Modify existing files**: edit one or more files that already exist.
2. **Create new files**: bring new files into existence as part of the
   change — extract a function into a new module, split a package,
   decompose a god object.

Today both are governed by the same prefix-match rule. A leader
authoring a contract picks between two bad options:

- **Narrow `write_set`** (e.g., `internal/foo/bar.go`) — precise
  scope, reviews cleanly, but forbids extraction. The worker cannot
  create `internal/foo/cache.go` or `internal/foo/parser.go`, so the
  only way to add structure is to accrete onto `bar.go`. The mission
  ships with a monolith.
- **Wide `write_set`** (e.g., `internal/foo/`) — authorizes
  extraction, but also authorizes modification of every existing file
  in the directory, defeats DES-032's overlap precision, and surfaces
  in the verifier's isolation block as "may touch anything in this
  directory" — losing the audit clarity the contract is supposed to
  provide.

Observed outcome (documented in `ethos-3emm`): workers add functions,
methods, and helper types inline in the file listed in `write_set`
rather than extracting into sibling files. The contract incentivizes
the wrong shape.

**The mechanism is cooperative, not enforced.** The PreToolUse hook
in `internal/hook/pretooluse.go` is a passthrough unless
`ETHOS_VERIFIER_ALLOWLIST` is set in the spawning process's
environment, and `buildVerifierAllowlistEnv` in `subagent_start.go`
sets that variable only for verifier spawns. Workers are mechanically
unconstrained today and remain so under DES-052. The worker accretes
because the worker reads `write_set: [foo.go]` and reasons "I am
authorized to write only one file." DES-052 does not change worker
enforcement; it changes the contract language the worker reads. The
verifier sandbox then mirrors the cooperative agreement mechanically
when it later re-creates the same files.

The threat model is the same as DES-032: not adversarial, but
uncoordinated. The leader wants the worker to decompose. The worker
sees `write_set: [foo.go]` and reasons correctly: "I am not
authorized to create new files; I must keep this in foo.go."

### Decision

Add `Contract.ExtractInto []string` — a separate field that
authorizes new-file creation under listed directories, without
authorizing modification of existing files in those directories.

The semantics are deliberately asymmetric with `write_set`:

| Operation | Authorized by |
|-----------|---------------|
| Modify existing file at path P | `write_set` entry matches P (existing behavior) |
| Create new file at non-existing path P | `write_set` entry matches P **OR** an `extract_into` directory is a prefix of P |
| Read any file | unrestricted (DES-047 verifier read-allowed policy) |

`extract_into` entries are **directories**. The per-entry validator
rejects file-shaped entries (anything with a code-file extension);
listing a specific known new file in `write_set` is the correct
expression for that case.

**This is the default semantics for every archetype.** No opt-in
flag, no archetype-by-archetype rollout. Workers immediately gain
the ability to decompose whenever the leader names extraction
directories.

### Schema

```yaml
extract_into:
  - internal/foo/    # may create new files here
  - internal/foo/bar/
```

Field tag: `yaml:"extract_into,omitempty" json:"extract_into,omitempty"`.
Empty `extract_into` is valid and is the backward-compatible default —
existing contracts behave identically to today.

Per-entry validation reuses `validateWriteSetEntry` (rejects `..`,
absolute paths, drive letters, UNC, control characters, null bytes,
zero-width Unicode) plus one rule: the entry must not have a code-file
extension (`.go`, `.py`, `.ts`, `.tsx`, `.js`, `.md`, `.yaml`, etc.).

The directory existence check is advisory at validate time (the
directory may be created by the same change), so the rule is purely
shape-based on the entry text.

Examples:

- ✅ `internal/foo`, `internal/foo/`, `docs/api/`, `pkg/handlers`
- ❌ `internal/foo/bar.go`, `README.md` (use `write_set`)
- ❌ `..`, `/etc`, control characters (same rules as `write_set`)

Rule 11 is extended to also validate every `extract_into` entry under
the same per-entry rules. The shape rule is rule 17.

### PreToolUse enforcement

`internal/hook/pretooluse.go` currently reads `ETHOS_VERIFIER_ALLOWLIST`
and blocks Write/Edit on any path not matching an entry via prefix.
The new behavior:

```text
on Write/Edit(target):
    if ETHOS_VERIFIER_ALLOWLIST not set:
        allow  # not a verifier spawn
    if target matches any ETHOS_VERIFIER_ALLOWLIST entry (existing
       prefix check):
        allow
    if target does not exist on disk AND
       target is under any directory in ETHOS_VERIFIER_EXTRACT_INTO:
        allow
    block with reason
```

The exists check is `os.Stat`. The stat is performed lazily, only on
the path not already matched by the existing allowlist — so the hot
path (verifier touching a declared file) is unchanged. For workers
(non-verifier spawns), the env vars are unset and the hook is a
passthrough, identical to today.

**Subtle case — Write to a path that does not exist YET because the
worker is about to create it.** This is the entire reason the field
exists. The Write call's target path is what's being created; the
file does not exist at `os.Stat` time; the `extract_into` check
authorizes it. After the Write succeeds, subsequent Edit calls on
the same path find the file existing — at which point the
allowlist rule applies and `extract_into` no longer authorizes the
operation. **Once a file is created under `extract_into`, the
verifier cannot modify it again unless its path is also listed in
`write_set`.** This is deliberate: allowing modify-after-create
would turn `extract_into` into a back-door modify authority for any
existing file under the directory (the "modify-via-extract_into"
attack the PreToolUse test explicitly prevents — a verifier could
simply read+rewrite a file under `extract_into` to bypass
`write_set` integrity). A worker that needs to iterate on a freshly
extracted file must either list the new path in `write_set`
upfront (when the filename is known) or operate idempotently —
prepare the file content in scratch, then Write once.

**Subtle case — Race between Stat and Write.** If a parallel process
creates the file between the Stat and the Write, the verifier (or
worker) would still proceed via the `extract_into` branch. This is
acceptable: the only "parallel process" creating files in a verifier
sandbox is the verifier itself, and Phase 3.5 isolates the verifier
per-mission. The exists check is for "did THIS verifier already
write this file in this session?" not for filesystem-level race
protection.

### SubagentStart hook

`internal/hook/subagent_start.go` builds the verifier isolation block
and sets `ETHOS_VERIFIER_ALLOWLIST` from `m.WriteSet`. Extension:

- Set `ETHOS_VERIFIER_EXTRACT_INTO` from `m.ExtractInto` (colon-
  separated, same encoding as `ETHOS_VERIFIER_ALLOWLIST`).
- The isolation block lists `extract_into` alongside `write_set`,
  with the prose "new files may be created in these directories" so
  the verifier knows the difference.
- `WalkWriteSet` continues to walk `WriteSet` only. Walking
  `extract_into` would mislead the verifier into thinking concrete
  files already exist there.

### Cross-mission admission control (DES-032 extension)

Admission control runs at `Store.Create` and rejects a new mission
whose declared paths could ever collide at runtime with an open
mission's declared paths. With two axes, the invariant is the closed
union of six rules over the entry-kind taxonomy
`{ws-file, ws-dir, ei-dir}` (extract_into is always directory-shaped
by per-entry validation):

```text
-- pathsOverlap is DES-032's segment-prefix relation; isPrefix is
-- the directed "a is a path prefix of b" relation. Both operate on
-- canonical normalized segment lists.

conflict(ws-file(a),  ws-file(b))  <->  pathsOverlap(a, b)
conflict(ws-file(a),  ws-dir(b))   <->  isPrefix(b, a)
conflict(ws-dir(a),   ws-dir(b))   <->  pathsOverlap(a, b)
conflict(ws-file(a),  ei-dir(b))   <->  isPrefix(b, a)
conflict(ws-dir(a),   ei-dir(b))   <->  pathsOverlap(a, b)
conflict(ei-dir(a),   ei-dir(b))   <->  false

-- The relation is symmetric over the unordered mission pair:
forall X, Y :  conflict(X, Y)  <->  conflict(Y, X)
```

Read in tabular form:

| Mission A | Mission B | Conflict? | Rationale |
|-----------|-----------|-----------|-----------|
| ws-file P_A | ws-file P_B | iff segment-prefix overlap | DES-032 unchanged |
| ws-file P_A | ws-dir D_B  | iff D_B is prefix of P_A | DES-032 unchanged |
| ws-dir D_A  | ws-dir D_B  | iff segment-prefix overlap | DES-032 unchanged |
| ws-file P_A | ei-dir D_B  | iff D_B is prefix of P_A | B may create P_A under D_B before A writes — same-path race; admission control rejects so the leader resolves at create time, not git-merge time |
| ws-dir D_A  | ei-dir D_B  | iff segment-prefix overlap | A's directory authorizes any path under it including new ones in D_B's range |
| ei-dir D_A  | ei-dir D_B  | never | Two missions may extract into the same dir or one into a subdir of the other; filenames are the contention unit, and same-filename collisions are the leader's responsibility (PreToolUse exists-check is per-verifier, not cross-mission) |

The `ws-file × ei-dir` row is the new constraint. Without it, A
listing `internal/foo/bar.go` in `write_set` and B listing
`internal/foo/` in `extract_into` would race on `bar.go`: if A has
not yet created the file, B's stat returns "does not exist" and B's
`extract_into` authorizes the create; A subsequently writes its own
body to the same path and one body is lost at git merge. Rejecting
the create-time overlap puts the resolution where the leader sees it.

`internal/mission/conflict.go` extends `findWriteSetConflicts` to
the six-rule form. The comparison logic stays in `pathsOverlap` and
`isPrefix`; the caller dispatches by entry kind. Entry-kind
detection (file-shaped vs directory-shaped) is the existing
trailing-slash heuristic plus extension scan that
`archetype_enforce.go` already uses.

### Lint warnings (advisory)

`internal/mission/lint.go` gains:

1. **`lintMonolithPressure`**: when `write_set` contains only file
   entries (no directories), `extract_into` is empty, AND success
   criteria mention "extract", "decompose", "refactor", or "split",
   warn: `consider declaring extract_into: success criteria suggest
   decomposition but no new-file scope is authorized`.
2. **`lintExtractIntoFileEntries`**: when `extract_into` contains a
   path with a code-file extension, warn: `extract_into entries
   should be directories; <path> looks like a file (use write_set)`.

Both are warnings, not validation failures — leaders can override
with intent.

### Archetype constraints

`Archetype.ExtractIntoConstraints []string` parallels
`WriteSetConstraints`. Same glob-style enforcement.

Default constraints (in seed archetypes):

| Archetype  | extract_into_constraints | rationale |
|------------|--------------------------|-----------|
| implement  | `[]` (unconstrained)     | code may extract anywhere the leader names |
| design     | `["docs/**"]`            | design output is docs; extraction stays in `docs/` |
| review     | `[]`                     | reviews rarely create files; empty is fine |
| test       | `[]` (unconstrained)     | Go convention: tests alongside source; a constraint would block legitimate same-directory test extraction |
| report     | `[]` (read-only)         | reports don't extract |
| audit      | `[]` (read-only)         | audits don't extract |
| investigate| `["docs/**", ".tmp/**"]` | investigation writes findings, no production code |
| inbox      | `[]` (read-only)         | inbox missions don't write |
| task       | `[]` (unconstrained)     | small tasks; leader decides |
| orchestrate| `[]` (no direct writes)  | orchestration coordinates, doesn't write |

### Verifier isolation context

The verifier sees both fields explicitly in the isolation block:

```text
Write set (modify existing files only):
  - internal/foo/bar.go

Extract into authorizes new files under: internal/foo/
  - internal/foo/

PreToolUse hook will block Write/Edit outside these allowlists.
Existing files: authorized only if matched by write_set above.
New files: authorized if matched by write_set OR if the path is
under an extract_into directory.
```

**Operator audit visibility.** When `extract_into` is populated, the
verifier's create surface widens compared to today. A leader who
writes `extract_into: [docs/]` has authorized the verifier to create
any new file under `docs/` — fine for a documentation mission, a
problem if typed by accident on an `internal/foo/parser.go` mission.
To make the boundary loud, the isolation block prepends a one-line
summary between the `write_set` and `extract_into` sections:
`extract_into authorizes new files under: <comma-separated dirs>`.
A reviewer scanning the block sees the union directory list at a
glance and cannot misread it as "modify everywhere in these
directories" — only "create new files there".

The constraint mechanism — archetype `extract_into_constraints` —
catches the common typo cases (a code-implementation mission with
`extract_into: [docs/]` fails the constraint check during
`Store.Create`). The summary line catches the cases the constraints
do not cover, by forcing the leader to read what they wrote.

**Worker-side enforcement is cooperative.** As stated in the Problem
section, the PreToolUse hook does not fire for worker spawns — only
for verifier spawns. `extract_into` documents the leader's grant to
the worker and to the verifier, but mechanical enforcement applies
only to the verifier. The worker honours the contract by reading it;
that is the existing posture under `write_set` and DES-052 does not
change it.

### Rejected alternatives

- **Implicit sibling scope**. Make any file entry in `write_set`
  also authorize new files in its parent directory. Rejected:
  silently widens every existing mission's scope (backward
  incompatible at the security boundary). Operators who deliberately
  wrote a narrow write_set expecting one-file scope would see new
  files appear without authorizing them. An explicit `extract_into`
  field is opt-in *by the leader* even though it is default-on as a
  *capability*.

- **Mid-mission write_set amendment protocol**. Worker proposes
  additions; leader approves. Rejected for the same-mission case:
  too much synchronous interaction for the common extraction case.
  Still viable for cross-package moves (out of scope for DES-052).

- **Replace `write_set` with a struct** (`{modify: [...], create: [...]}`).
  Rejected: breaks YAML compatibility for every existing contract on
  disk. The additive `extract_into` field is backward compatible.

- **Glob patterns in `write_set`** (e.g., `internal/foo/*.go`).
  Rejected: doesn't solve the discovery-during-extraction problem.
  The worker often doesn't know what filename to use until they
  decompose. A glob still has to be authored upfront with file
  names in mind.

- **Combine `extract_into` semantics into the existing prefix-match**
  by treating any directory-shaped entry as both modify-existing AND
  create-new. Rejected: that's what "wide write_set" already does,
  and the whole point of DES-052 is to decouple the two.

- **Treat extract_into entries as exclusive (admission-control
  conflict with any other mission's extract_into on the same dir)**.
  Rejected: two missions extracting into the same directory but
  creating different filenames cooperate fine — first mover on a
  given filename wins via the exists check. Forcing extract_into
  exclusivity would defeat the concurrency the field is designed
  to enable.

- **Stat-free always-allow rule for new files** (skip the directory
  check). Rejected: that's "verifier may create any file anywhere
  it claims doesn't exist", which is exactly the boundary erosion
  DES-035 exists to prevent.

- **Shape A: redefine trailing-slash entries in `write_set` as
  create-only.** Today directory entries in `write_set` authorize
  modify+create under the prefix via
  `pretooluse.go:pathAllowed`; `archetype_enforce.go` already
  treats them as scope markers exempt from glob constraints. A
  one-line hook-policy change — "for a directory entry in
  `write_set`, the verifier hook authorizes Write only when the
  target does not exist; modification of an existing file in the
  directory still requires a file-shaped entry" — would deliver
  the same decoupling with zero new fields and zero migration
  cost on the byte-format side. Rejected for two reasons. First,
  the semantics are not self-evident from the YAML — a worker
  reading `write_set: [internal/foo/]` cannot tell whether the
  entry means "modify everything under" (today) or "create only
  under" (Shape A); the meaning lives in implicit policy.
  Cooperative enforcement (the worker reads the contract and
  obeys) demands that the YAML *carry* the intent, not delegate
  it to a hook documentation page. Second, while the missions
  registry shows the pattern is rarely used today, the change is
  silently behavior-altering for any future operator who writes a
  trailing-slash entry expecting the documented "wide write_set"
  semantics. A separate `extract_into` field is verbose but
  reads exactly the way it acts.

- **Shape B: single slice with a `+` create-only prefix.** Entries
  prefixed `+` (e.g., `+internal/foo/cache.go` or `+internal/foo/`)
  authorize create-only; unprefixed entries authorize modify (with
  trailing-slash retaining today's wide semantics or migrating per
  Shape A). YAML-forward-compatible — `+path` is a valid string,
  no schema break. Rejected for the same readability reason as
  Shape A, sharpened: a marker convention requires every consumer
  (worker, verifier, audit reader, lint, conflict checker, archetype
  enforcement) to parse the prefix before reasoning about the entry.
  A second slice with an explicit name lets every consumer dispatch
  by field, not by string-parsing convention. The `+` shape also
  composes awkwardly with the future `-` removal-marker space and
  with the existing `..`/path-absoluteness checks (the per-entry
  validator would have to strip the prefix before applying its
  existing rules). A struct-tagged slice is more YAML, less
  cleverness.

- **Don't change the schema; change the contract template.** The
  failure mode is cooperative — the worker reasons from the
  contract context. A template-generator could emit a sentence in
  `context` like "if you need to extract helpers, place them
  under \<these directories\>". Zero schema change, zero hook
  change, zero migration. Rejected because the prose loses the
  audit trail: a reviewer scanning a closed mission cannot
  mechanically check "did the worker stay within authorized
  extraction directories?" without parsing free-text English. The
  whole purpose of `write_set` is that machine-checkable scope
  beats prose intent. `extract_into` extends that audit-trail
  principle to the create-axis; template prose abandons it.

### What DES-052 deliberately does NOT do

- **Authorize cross-package extraction without leader consent.**
  `extract_into` is a list the leader writes. A worker that
  discovers it needs to extract into a directory the leader did
  not name must request an amendment (out of scope) or escalate.
- **Replace `write_set` for new files.** Listing a specific known
  new file in `write_set` (e.g., `internal/foo/new.go`) still works
  exactly as today. `extract_into` is for the case where the worker
  doesn't know the filename upfront.
- **Authorize file deletion.** Removing a file is a modify-existing
  operation governed by `write_set`. The PreToolUse hook treats
  Write to an existing file as modify-existing.
- **Modify the round budget, evaluator hash gate, or any DES-033/
  3.4/3.5/3.6 enforcement.** DES-052 is purely a write-set
  authorization extension.

## DES-054: Audited delegation — Tier A audit + Tier B contracts (SETTLED)

**Status**: Settled. Shipped across PRs #326–#328 (2026-05-22/23); hardened in v4.9.0 by the mission-lifecycle cluster (#415, DES-067). Bead `ethos-98u9`. Supersedes `ethos-717p`, `ethos-gqg3`. Reviewed across four rounds (rop minimalism / rsc compatibility / jra formal invariants). Round 4 verdict: 3× APPROVE — converged. Design converged at v5; the revision history below is retained for provenance.

Revision history:

- v1 (initial): integrated five symptoms; three-predicate language; one-line migration.
- v2 (after round-1 peer review): 18 of 21 round-1 findings applied.
- v2+ (after jra redo): 21 of 21 round-1 findings applied.
- v3 (after CEO direction): policy pivot — contracts are opt-in. Bare `Agent(...)` calls audited but not synthesized. Synthesizer, meta-evaluator, `synthetic` flag, v2 invariants I8/I9/I10 dropped.
- v4 (after round-2 peer review): 14 REQ + 13 IMPL findings applied.
- **v5 (after round-3 + CEO clean-slate direction)**: round-3 verdicts were rop APPROVE / jra APPROVE / rsc APPROVE WITH ONE NEW REQ (counter.yaml is a file-format break, not a permissive append). CEO named this as broader over-engineering and asked for clean-slate framing. v5 restructures storage:
  - **Two-tree top-level layout**: `<repo>/.punt-labs/ethos/missions/<mission-id>/` for per-mission cohesion (date encoded in ID); `<repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<session-id>/` for per-session cohesion **1:1 with Claude Code conversation history**. One date level in the session directory name (not nested).
  - **Single audit log per session** — `audit.jsonl` lives only under each session directory. Mission and delegation directories hold metadata only (contract, record, prompt, result). Per-delegation `audit.jsonl` dropped. Cross-delegation queries filter the session log by `delegation_id`.
  - **Sibling-file per-namespace per-date counters** — `~/.punt-labs/ethos/counters/missions-YYYY-MM-DD` and `delegations-YYYY-MM-DD`, each a single-int file. No `counter.yaml`. No `schema_version` to pin. Preserves the existing `.counter-YYYY-MM-DD` shape verbatim; adds a namespace dimension to the filename.
  - **Tier A delegations live under their spawning session's directory** — `sessions/<YYYY-MM-DD>-<session-id>/adhoc/<NNN>/`. Not in a separate global tree. Their metadata (record, prompt, result) is in-repo, in the session that produced them.
  - **`I7` verdict semantics for Tier A pinned to `{open, aborted}`** per rsc R3 O3. Tier A has no evaluator and cannot produce `pass`/`fail`.
  - **`max_delegation_depth` refusal closes the Tier B record with `verdict=aborted`** per rsc R3 O4. No dangling state.
  - **`I10-audit-atomic` collapses from two-store form to one-store form** — only the session audit log exists; no per-delegation audit file to be atomic about.
  - **Phase-1 + phase-3 transition window** stated as two minor versions per rsc R3 O2.
  - **IMPL editorial** — stale `max_delegation_depth` open-question removed per rop R3 N1.
  - **`ethos find` query interface** (DES-056-or-later) filed as `ethos-pcra` for mid-term work, out of v5 scope.

Reviewers across all rounds, all closed with proper ceremony: `rop` (Pike minimalism, mcg eval), `rsc` (Cox compatibility / migration, mdm eval), `jra` (Abrial formal invariants, jms eval). R1: 3× ITERATE. R2: 3× APPROVE WITH NAMED EDITS. R3: 2× APPROVE / 1× APPROVE WITH ONE REQ. Round 4 dispatched on v5 — design stabilizes when a review round adds zero new substantive findings.

## Problem (unchanged from v2)

Ethos's governance model partitions by **identity**. Missions bind to identities. Audit logs key off session_id. Mission contracts gate identity-bearing subagents. Procedure rules live in markdown.

This breaks the moment a leader spawns a `general-purpose` subagent or makes any bare `Agent(...)` call. Five observable consequences:

1. **Subagent activity is not linkable to its parent.** `auditEntry` (`audit_log.go:13-18`) lacks `parent_session`, `agent_id`, `agent_type`.
2. **Prompts are unrecoverable.** `audit_log.go:86-89` truncates `tool_input` at 200 characters.
3. **Bare `Agent(...)` calls bypass contracts.** No `write_set`, no `evaluator`, no success criteria.
4. **Tool-call preconditions cannot be expressed.** "A verdict requires PNG inspection" is markdown discipline.
5. **Durable mission state lives per-machine.** `~/.punt-labs/ethos/missions/<id>.*` — global, untracked.

## The missing concept

A **delegation** is the unit of governed work: parent → child → audit, with an OPTIONAL contract layer. Today ethos models identity, mission, audit, session — but not the delegation itself.

DES-054 makes delegation a first-class concept with **two governance tiers**:

- **Tier A — ungoverned, audited.** Bare `Agent(...)` calls. Audit log captures spawn, prompt, child tool use, return value, parent linkage. No contract layer. A PreToolUse advice hook emits a one-line suggestion pointing at `mission dispatch` when the operator wants governance. Default for ad-hoc spawns.

- **Tier B — governed, audited.** `mission create` / `mission dispatch` (existing) or inherited via `delegations[].spawn_pattern` (new). Mission contract applies: `write_set`, `extract_into`, `preconditions`, `delegations[]`, frozen evaluator, round budget. Default for typed delegation.

Both tiers share the audit enrichment. The contract layer is additive — present in Tier B, absent in Tier A. Operators choose per call.

## Design

### Storage layout

```text
<repo>/.punt-labs/ethos/
├── index/
│   └── missions.jsonl                                   # DES-050 cross-date summary index
├── missions/                                            # per-mission canonical home; date in ID
│   └── <mission-id>/                                    # e.g. m-2026-05-22-005/
│       ├── contract.yaml
│       ├── results.yaml
│       ├── reflections.yaml
│       ├── log.jsonl
│       ├── artifacts/
│       └── delegations/<NN>/                            # Tier B mission-bound delegations
│           ├── record.yaml
│           ├── prompt.md
│           └── result.md
└── sessions/                                            # per-session, 1:1 with conversation history
    └── <YYYY-MM-DD>-<session-id>/                       # e.g. 2026-05-22-abc123/
        ├── audit.jsonl                                  # universal: Tier A + Tier B entries
        └── adhoc/<NNN>/                                 # Tier A delegations spawned in this session
            ├── record.yaml
            ├── prompt.md
            └── result.md

~/.punt-labs/ethos/                                      # global — ephemeral process state
├── sessions/<session-id>.yaml                           # session roster (live PIDs, TTYs)
├── sessions/<session-id>.lock                           # session flock
├── sessions/current/<pid>                               # PID → session pointer
├── missions/<mission-id>.lock                           # per-mission flock
├── delegations/<delegation-id>.lock                     # per-delegation flock (Tier B only)
└── counters/                                            # sibling-file per-namespace per-date counters
    ├── missions-YYYY-MM-DD                              # single int
    └── delegations-YYYY-MM-DD                           # single int
```

Two top-level trees: `missions/` and `sessions/`. Each artifact lives in exactly one place.

**Mission cohesion** — `missions/<mission-id>/` holds everything about that mission across every session that touched it. `git log -- .punt-labs/ethos/missions/<mission-id>/` shows the full per-mission history.

**Session cohesion** — `sessions/<YYYY-MM-DD>-<session-id>/` holds everything specific to that session: its audit log, and any Tier A delegations spawned in it. The dirname is **`<date>-<session-id>`** — date prefix sorts; session-id makes the directory 1:1 with the Claude Code conversation history file at `~/.claude/projects/<...>/<session-id>.jsonl`. A forensic operator can cross-reference by the shared session-id.

**Single audit storage** — `audit.jsonl` lives only at the session level. Mission and delegation directories hold metadata only (contract, record, prompt, result). Per-delegation audit is recovered by filtering the session audit log on `delegation_id`. The `ethos audit show --delegation <id>` command does the filter; the storage stays simple.

**Counters as sibling per-namespace per-date files** — same shape as today's `.counter-YYYY-MM-DD` (single int, flock-guarded read/inc/write), just adds a namespace dimension to the filename. A new namespace = a new sibling file. No `counter.yaml`, no `schema_version`, no nested map. Permissive append happens at the file-set level.

**Date browsing** — `ls .punt-labs/ethos/sessions/2026-05-22-*/` lists all sessions started that date. `ls .punt-labs/ethos/missions/m-2026-05-22-*/` lists all missions started that date. The date IS in the canonical key (mission ID, session directory name); the filesystem exposes it directly.

**Cross-date queries** — `ethos find` (DES-056-or-later, bead `ethos-pcra`) provides the structured query surface. For DES-054, `index/missions.jsonl` (DES-050 unchanged) is the cross-date acceleration path.

### Schema changes

**`auditEntry`** (universal, applies to both tiers):

```go
type auditEntry struct {
    Ts                 string         `json:"ts"`
    Session            string         `json:"session"`
    ParentSession      string         `json:"parent_session,omitempty"`
    AgentID            string         `json:"agent_id,omitempty"`
    AgentType          string         `json:"agent_type,omitempty"`
    DelegationID       string         `json:"delegation_id,omitempty"`
    ParentDelegation   string         `json:"parent_delegation,omitempty"`
    ContractID         string         `json:"contract_id,omitempty"`
    Tool               string         `json:"tool"`
    ToolInput          map[string]any `json:"tool_input,omitempty"`
    ToolInputHash      string         `json:"tool_input_hash"`
    Preview            string         `json:"tool_input_preview"`
}
```

`DelegationID` is set for both tiers; `ContractID` is set only for Tier B (Tier A has no contract). `ParentDelegation` (added v4 per jra F3 i) makes the audit chain self-sufficient: every entry can walk to the root spawn without consulting the session roster, which the audit-log relocation in v3 made important. `tool_input_hash` shares the canonical-JSON encoder used by DES-033's `Evaluator.Hash`.

**KnownFields asymmetry** (per rsc E4): `Contract` YAML is decoded with `KnownFields(true)` to refuse silent feature loss. `auditEntry` JSONL is decoded with default permissive JSON to keep older readers (vox, audit show) compatible with new optional fields. The current code already has this asymmetry; v4 names it so a future refactor does not unify them.

**`Contract`** (Tier B only) gains optional `preconditions` and `delegations[]`. No `synthetic` flag — there is no synthesizer.

```go
type Contract struct {
    // ... existing fields
    Preconditions []ToolPrecondition    `yaml:"preconditions,omitempty" json:"preconditions,omitempty"`
    Delegations   []DelegationTemplate  `yaml:"delegations,omitempty" json:"delegations,omitempty"`
}

type ToolPrecondition struct {
    Tool        string   `yaml:"tool" json:"tool"`
    PathGlob    string   `yaml:"path_glob" json:"path_glob"`
    RequireRead []string `yaml:"require_read,omitempty" json:"require_read,omitempty"`
    Message     string   `yaml:"message" json:"message"`
}

type DelegationTemplate struct {
    Role             string   `yaml:"role" json:"role"`
    SpawnPattern     string   `yaml:"spawn_pattern" json:"spawn_pattern"`
    InheritsContract bool     `yaml:"inherits_contract,omitempty" json:"inherits_contract,omitempty"`
    ExtractInto      []string `yaml:"extract_into,omitempty" json:"extract_into,omitempty"`
}
```

### Predicate language (closed; per round-1 rop)

Two forms, applies only when a Tier B contract is in scope:

1. **Implicit `must-read-inputs`** — default for any contract with `preconditions: [...]`. For path-referencing tool calls, the delegation's audit log must contain a `Read` of every referenced path.

2. **Explicit `require_read: [<path>...]`** — per-precondition override with `${inputs.X}` substitution from the contract's `Inputs` block. The substitution reads from `Contract.Inputs`, not from the gated tool call's input — that closes the templating concern.

Tier A calls have no contract, no preconditions. Anything beyond these two forms is a parse error.

**Scope rule**: under `inherits_contract: true`, predicate lookups walk the ancestor delegation chain. The walk is bounded by `parent_delegation` chain length.

### Hook architecture

**1. PreToolUse-on-Agent** — the new branch (`internal/hook/pretooluse.go`).

```text
When tool = Agent:

a. Resolve open mission from MISSION_ID env + session roster.

b. Tier dispatch:

   - Inheritance match: parent's contract has delegations[] entry whose
     spawn_pattern matches this Agent call. Tier B. Apply parent contract.

   - MISSION_ID env set, no inheritance match: Tier B by explicit dispatch.
     The leader passed MISSION_ID through; the contract is whatever the
     leader bound it to.

   - Neither: Tier A. Ungoverned. Emit advisory to stderr UNLESS
     suppression conditions apply (per rop R1 iii):
       - ETHOS_QUIET_ADVICE=1 is set in the environment, OR
       - PARENT_SESSION_ID is already populated (nested ad-hoc spawn —
         the parent already saw the advisory; suppress the recursive
         repeat).
     When neither suppression condition holds, emit one stderr line:

       ethos: Agent call is ungoverned. To bind a contract:
         ethos mission dispatch --worker <agent_type> --evaluator <handle> ...
         ethos mission create --file <contract.yaml>
       (set ETHOS_QUIET_ADVICE=1 to silence)

c. Audit setup (BOTH tiers):

   - Allocate transient delegation_id via NewID API.
   - Set DELEGATION_ID, PARENT_SESSION_ID in env block.
   - Tier B: also set MISSION_ID, MISSION_ARTIFACTS_DIR.
   - Tier B: write delegation record skeleton + prompt.md to per-delegation dir.
   - Tier A: no per-delegation directory; audit entries go to the session log.

d. Tier B only: acquire per-mission flock (shared/read mode) before skeleton
   write; refuse spawn if mission is closed. Release after skeleton write.

e. Allow the tool call.
```

The advice is information, not refusal. Tier A operators see the suggestion and proceed.

**2. PostToolUse audit enrichment**. All audit entries are full-fidelity. `tool_input` persisted in full; preview retained for grep. JSONL atomic-write contract: `f.Sync()` after every line, line-tolerant reader, `audit migrate` rewrites via temp+rename under per-delegation flock (Tier B) or per-session flock (Tier A).

**3. PreToolUse procedure preconditions** — fires only when a Tier B contract is in scope. Tier A spawns never hit precondition evaluation because no contract exists. Fail policy: violated predicate blocks; unevaluable predicate blocks unless `strict_preconditions: false` (default true), at which point it warns and allows.

**4. Hash-gate refusal cleanup** — when `SubagentStart`'s evaluator-hash check (DES-033) refuses a Tier B verifier spawn, the delegation skeleton already exists. Refusal writes `verdict: aborted` and `closed_at: <now>` to the record. No orphaned skeletons.

### Commit-message linkage

`SubagentStart` exports `MISSION_ID` (Tier B) and `DELEGATION_ID` (both tiers) into worker spawns. A `commit-msg` hook installed by `ethos seed` appends both trailers when set. Tier A commits get only `Delegation:`; Tier B commits get both `Mission:` and `Delegation:`.

### Migration

**Session rosters vs session audit files**. The two are explicitly different artifacts with different storage policies:

- Session **roster** files (`<session-id>.yaml`, `<session-id>.lock`, `current/<pid>`) reference live PIDs and TTYs and stay at `~/.punt-labs/ethos/sessions/`. They never move into the repo.
- Session **audit** files move into `<repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<session-id>/audit.jsonl`. Legacy reader fallback applies for the transition window.

**`Store` gains two roots**:

| Operation | RepoRoot set | Else |
|---|---|---|
| Create | write to `<repoRoot>/.punt-labs/ethos/missions/<id>/contract.yaml`; acquire `<repoRoot>/.punt-labs/ethos/missions/.create.lock` AND `~/.punt-labs/ethos/missions/.create.lock` (rolling-upgrade fence) | global only |
| Load | repo first, global fallback | global only |
| List | union, dedup by id (repo wins) | global |
| Update/Close | layer where Load found it; never copy across | global |
| Migrate | explicit copy global → repo; refuse if repo target exists | no-op |

**Audit-log read-path state machine**:

```text
on read_audit(session_id, start_date):
    p_repo   = <repoRoot>/.punt-labs/ethos/sessions/<start_date>-<session_id>/audit.jsonl
    p_legacy = ~/.punt-labs/ethos/sessions/<session_id>.audit.jsonl
    if exists(p_repo):
        return read(p_repo)
    if exists(p_legacy):
        return read(p_legacy)   // legacy fallback
    return []
```

The Tier B precondition evaluator follows the same dispatch.

**Rolling-upgrade**: v3.12.0 acquires both `.create.lock` files during transition; v3.13.0 drops the global. **Transition window is two minor versions** (per rsc R3 O2) because phase 1 ships the storage move and phase 3 ships the `audit migrate` tool; the gap is one minor version, and the v3.13.0 cleanup is another.

**`KnownFields(true)` asymmetry**: Contract YAML decodes strict. v3.12.0 contracts (with `preconditions`/`delegations`) are not readable by v3.11.0 — one-way door, no strip flag. Audit entries decode with default permissive JSON.

**`NewID` rollback API**: `NewID(namespace, date) (id string, release func(commit bool), err error)`. Caller `defer release(success)`. Counter rolls back on failure to prevent burned IDs at Tier-A-spawn-rate.

**Counter file format** — sibling per-namespace per-date single-int files (per CEO clean-slate direction, rsc R3 REQ): `~/.punt-labs/ethos/counters/missions-YYYY-MM-DD` and `delegations-YYYY-MM-DD`. Each file: one int (last allocated number), `\n`-terminated. Same shape as today's `.counter-YYYY-MM-DD`, namespace added to the filename. No format break; v3.11.0 readers keep reading their own files. A v3.12.0 binary writes a sibling for the new namespace; older binaries that don't know about delegations never touch it. No `counter.yaml`, no `schema_version`, no nested map.

**Day-boundary policy**: a delegation born at 00:00:01 UTC the day after its parent mission was created belongs to its parent mission's date, not wall clock. `m-2026-05-21-005-d04`, not `m-2026-05-22-005-d01`. Tier A delegations are wall-clock-indexed in `delegations-YYYY-MM-DD` because they have no parent mission.

**`ethos audit migrate` behaviour**:

- **No-op (no legacy state)**: exit 0 with `no legacy audit logs found`. Not an error.
- **Idempotent (already-migrated)**: each migrated session leaves a `.migrated` tombstone in the legacy directory; subsequent runs see the tombstone (or repo-local twin), exit 0.
- **Partial failure recovery**: each session migrates atomically via temp+rename. Failure on session N leaves earlier sessions migrated and N in its pre-migration state; next run resumes from N.
- **Read-only legacy filesystem**: copies to repo; legacy file becomes implicit tombstone iff a repo-local twin exists.
- **Unconfigured repo**: creates `<repo>/.punt-labs/ethos/sessions/` with `0o700` if absent. Refuses migration if `<repo>/.punt-labs/ethos/` exists but is not writable.
- **Cross-repo policy**: migrates only sessions whose roster's `repo` field matches the current repo. Sessions with no repo binding stay in legacy and are not migrated.

**`ethos mission migrate --to-repo`** is symmetric: copies historical mission state from global into the active repo, refusing if a repo-local twin already exists.

### Concurrency model

5 repos × 1 mission each + N Tier B delegations + M Tier A spawns on one machine:

| File | Count | Location | Lifetime |
|---|---|---|---|
| Per-mission flock | 5 | `~/.punt-labs/ethos/missions/` | flock-held |
| Per-delegation flock (Tier B) | N per mission | `~/.punt-labs/ethos/delegations/` | flock-held |
| Per-repo `.create.lock` | 5 | `<repo>/.punt-labs/ethos/missions/.create.lock` | flock-held |
| Global `.create.lock` (transition) | 1 | `~/.punt-labs/ethos/missions/.create.lock` | flock-held |
| Session roster YAML | 5 | `~/.punt-labs/ethos/sessions/<session-id>.yaml` | session lifetime |
| Session flock (roster + audit) | 5 | `~/.punt-labs/ethos/sessions/<session-id>.lock` | flock-held |
| PID pointer | 5 | `~/.punt-labs/ethos/sessions/current/<pid>` | session lifetime |
| Session audit JSONL | 5 | `<repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<session-id>/audit.jsonl` | persistent |
| Mission ID counter (per date) | per date | `~/.punt-labs/ethos/counters/missions-YYYY-MM-DD` | persistent |
| Delegation ID counter (per date) | per date | `~/.punt-labs/ethos/counters/delegations-YYYY-MM-DD` | persistent |

One session flock covers both the roster YAML and audit JSONL writes. Lock file is global because the session itself is a global concept; the audit data it gates is per-repo. The audit log lives ONLY at the session level — no per-delegation `audit.jsonl`; per-delegation audit is recovered by filtering the session log by `delegation_id`.

**Stat–Write race** (DES-052) — unchanged: per-delegation flocks do NOT eliminate cross-delegation races on `extract_into` targets. `tool_input_hash` makes the collision detectable post-hoc.

**Cross-tier audit visibility**: a Tier B mission-bound delegation that spawns a Tier A child is supported. Both audit streams (the Tier B delegation's tool calls inside the bound contract, and the Tier A child's tool calls) write to the **same session-level audit log**, with `delegation_id` and `parent_delegation` distinguishing them. `ethos audit show --delegation <id>` (per rsc E7, phase 3) filters the session log to give the operator the per-delegation view.

**`max_delegation_depth`**: a global ethos setting in `.punt-labs/ethos.yaml`, default `16`. Tier A under Tier A has no per-contract budget cap, so a runaway recursive spawn pattern would have no natural backstop. The depth is read at PreToolUse-on-Agent; exceeding refuses the spawn with a clear error. **If the depth check refuses a Tier B spawn after the delegation record skeleton has been written** (rsc R3 O4), the refusal handler closes the just-created record with `verdict=aborted` and `closed_at=<now>`. No dangling state.

### Concurrency invariants (revised; per round-1 jra)

```text
-- Mission IDs are globally unique.
I1: forall m1, m2 in missions: m1.id = m2.id  ->  m1 = m2

-- Delegation IDs are globally unique. Applies to both tiers; Tier A
-- delegations get IDs of the form d-YYYY-MM-DD-NNN, Tier B get
-- <mission-id>-d<NN>.
I2: forall d1, d2 in delegations: d1.id = d2.id  ->  d1 = d2

-- Every audit entry produced under a delegation is reachable from
-- that delegation. Audit entries for Tier A live in the session log
-- but carry the delegation_id field; for Tier B they live under the
-- per-delegation directory.
I3: forall e in audit_entries:
        e.delegation_id != ""  ->  exists d in delegations: d.id = e.delegation_id

-- Transitive closure: every non-root delegation has a parent in the
-- delegation set; the chain terminates at parent_delegation = "".
I4: forall d in delegations:
        d.parent_delegation = "" \/
        exists p in delegations: p.id = d.parent_delegation
    /\
    forall d in delegations: terminates(chain(d))

-- Preconditions are evaluated only when a Tier B contract is in scope
-- and the calling tool matches.
I5a: forall t in tool_calls, p in active_contract.preconditions:
        evaluated(p, t)  <->  (t.tool = p.tool /\ matches(t.path, p.path_glob))

-- Fail policy.
I5b: evaluated(p, t) /\ predicate(p, t) = false             ->  block(t)
   /\ evaluated(p, t) /\ predicate(p, t) = unevaluable /\ strict   ->  block(t)
   /\ evaluated(p, t) /\ predicate(p, t) = unevaluable /\ ¬strict  ->  warn(t) /\ allow(t)

-- Predicate scope under inherits_contract: walk the ancestor chain
-- of delegations that share this delegation's contract.
I6: forall p in preconditions(d):
        scope(p, d) = audit_of(d) ∪ {audit_of(a) : a in ancestors(d) /\ a.contract = d.contract}

-- Delegation status is monotonic, and the legal verdict set
-- depends on the tier. Tier B can produce evaluator verdicts
-- (pass / fail / error); Tier A has no evaluator and can only
-- terminate by abort. (UPDATED in v5 per rsc R3 O3.)
I7: forall d in delegations, t1 < t2:
        d.verdict(t1) = "open"  \/  d.verdict(t2) = d.verdict(t1)
    /\
    (d.tier = "A"  ->  d.verdict ∈ {"open", "aborted"})
    /\
    (d.tier = "B"  ->  d.verdict ∈ {"open", "pass", "fail", "error", "aborted"})
    /\
    d.verdict != "open"  ->  d.closed_at != ""

-- Tier A delegations have no contract; Tier B delegations always do.
-- (NEW in v3 — replaces v2's I8/I9/I10 about synthetic contracts, which
-- are obsolete under the opt-in pivot because synthetic contracts no
-- longer exist.)
I8: forall d in delegations:
        d.tier = "A"  ->  d.contract = nil
    /\  d.tier = "B"  ->  d.contract != nil

-- Tier domain is closed: every delegation belongs to exactly A or B.
-- (NEW in v4 per jra F1.)
I8-type: forall d in delegations: d.tier ∈ {"A", "B"}

-- Tier is immutable: a delegation never transitions between tiers.
-- (NEW in v4 per jra F2.)
I8-stable: forall d in delegations, t1 < t2:
        d.tier(t1) = d.tier(t2)

-- Tier B contract liveness: a delegation in Tier B can only exist
-- when its bound contract is open. (NEW in v4 per rop R2.)
I8-live: forall d in delegations:
        d.tier = "B"  ->  d.contract != nil /\ d.contract.closed_at = ""

-- Counter file shape: sibling-file per-namespace per-date pattern.
-- Each file is a single integer. New namespaces add sibling files;
-- existing files never change shape. (REVISED in v5 per CEO
-- clean-slate direction; obsoletes v4's counter.yaml + schema_version
-- which was over-engineered.)
I9-counter: forall ns, date:
        counters/<ns>-<date> exists  ->  contents is a single integer
    /\  no counter file ever changes format

-- Audit-log atomicity is per-session, single-store. Appends to a
-- session's audit JSONL serialize through the session flock; line
-- ordering follows write order. (REVISED in v5: dropped the
-- two-store variant from v4 — there is no per-delegation audit
-- file in v5; all audit lives in the session log.)
I10-audit-atomic: forall e1, e2 written to <session-dir>/audit.jsonl:
        flock(<session-id>.lock) is held during each append
    /\  write_order(e1, e2) = file_position_order(e1, e2)
```

Twelve invariants. v2's I8/I9/I10 (about synthetic contracts) **obsolete** under v3's opt-in pivot. v4's I8, I8-type, I8-stable, I8-live, I9-counter, and I10-audit-atomic together encode the tier discipline, counter shape, and audit append-atomicity properties. v5 sharpens I7 to make tier-specific verdict sets explicit, simplifies I9-counter to the sibling-file shape (no YAML, no schema_version), and collapses I10-audit-atomic from two-store form to single-store.

### What DES-054 deliberately does NOT do

- **Force every Agent call through a contract.** Tier A exists. Contracts are opt-in.
- **Synthesize ad-hoc contracts.** No synthesizer; no meta-evaluator; no rule-6 relaxation.
- **Eliminate the Stat–Write race in extract_into.** Detectable, not prevented.
- **Move session rosters into the repo.** Only audit logs move; rosters stay global.
- **Provide a general predicate language.** Two closed forms. Anything else is a future DES.
- **Backfill historical audit logs automatically.** Operators run `ethos audit migrate` explicitly.
- **Make v3.12.0 contracts readable by v3.11.0.** One-way door.

### Rejected alternatives

- **Force every Agent call through a contract** (Tier A removed). Rejected per CEO direction: governance is opt-in; ad-hoc spawns must remain frictionless. The advice hook is the soft nudge toward governance without forcing it.
- **Synthesize an ad-hoc contract on every Agent call.** Rejected per CEO direction: ~300 LOC of synthesizer + meta-evaluator + rule-6 relaxation exists only to govern calls the operator didn't ask to govern.
- **Move session rosters into the repo.** Rejected: rosters reference live PIDs and TTYs.
- **Single repo-root `delegations.jsonl`.** Rejected: violates per-delegation flock semantics, breaks per-delegation `git log` granularity.
- **Per-mission single flat file.** Rejected: same reasons.
- **Reuse `session_id` as `delegation_id`.** Rejected: a session contains multiple delegations.
- **Per-tool-call ID rather than per-spawn delegation_id.** Rejected: groups tool calls by their generating Agent spawn, which is the unit of operator intent.
- **Hostable code in contracts** (general predicate language). Rejected: sandbox concern.
- **`KnownFields(strip)` flag.** Rejected: would silently drop preconditions/delegations.
- **Make Agent advice blocking** (refuse Tier A spawns until contract). Rejected per CEO direction.

## Open questions

1. **Advice hook format**: stderr line? structured JSON in hook response? Both? Operators reading from the terminal see stderr; programmatic readers may prefer JSON. Recommendation: stderr text for terminal sessions, plus a `hooks.json`-controlled flag for JSON output.

2. **Cross-tool surface**: verify Vox audit-log parsing (if any), commit-msg trailer consumption by prfaq-dev / feature-dev / beads. Expected no-ops.

3. **Advice tone**: prescriptive ("use `mission dispatch`") vs descriptive ("this call is ungoverned; here's how to bind a contract if you want")? Recommendation: descriptive — operators choose.

## Recommended next step

Three implementation phases. Each phase: bwk worker / rsc evaluator. Test coverage gate per phase. **DES-052 `extract_into` discipline applied to every new module** — no god modules; extract helpers into named files; mission contracts explicitly authorize `extract_into` directories. `make check` per commit; no suppression of quality gates.

- **Phase 1 — schema + storage** *(SHIPPED in v3.12.0 dev, commit `60f42c2`)*: `auditEntry` enrichment (`parent_delegation` included); two-root `Store` state machine; date-bucketed mission and session directories; sibling-file per-namespace per-date counters; JSONL atomic-write contract; `NewID` rollback API; `KnownFields` asymmetry pinned (contracts strict, audit permissive).

- **Phase 2 — hooks + dispatch** *(IMPLEMENTED on `feature/des-054-phase2`, 8 commits `dee3af4`→`078f032`)*: `PreToolUse-on-Agent` Tier A / Tier B dispatch; advice hook with `ETHOS_QUIET_ADVICE` / `PARENT_SESSION_ID` suppression; per-mission shared flock + per-delegation exclusive flock; session-flock unification (one flock covers roster + audit) wired in `cmd/ethos/hook.go`; aborted-sentinel cleanup on hash-gate refusal AND on `max_delegation_depth` refusal. Tier B inheritance dispatch (parent contract walk + spawn_pattern match) deferred — the `Contract.Delegations` schema lands now so phase 3 can consume it without a contract format break.

- **Phase 3 — preconditions, migration, query commands** *(IMPLEMENTED on `feature/des-054-phase3`, commits `ce74c63` → `a17fed9`)*: precondition evaluator (Tier B only, short-circuits on `contract = nil`); Tier B inheritance dispatch (parent contract walk + spawn_pattern match); commit-msg trailer hook; `ethos audit migrate` (no-op exit-0, idempotent, partial-failure recovery, read-only fallback, cross-repo policy); `ethos mission migrate --to-repo`; `ethos audit show --delegation <id>` filters the session audit log by delegation_id; spawn_pattern admission-time validation in `Contract.Validate`; cross-tool no-op verification (vox audit parser, prfaq-dev / feature-dev / beads trailer consumption).

**Transition window** spans two minor versions: v3.12.0 ships phase 1 storage move + phase 2 hooks; phase 3 migration commands land in v3.13.0; v3.14.0 drops the global `.create.lock` rolling-upgrade fence.

Final integration test: leader runs a Tier A Agent call → audit enrichment captures parent + child + prompt in the session log; leader runs a Tier B `mission dispatch` → same audit + contract + preconditions evaluable; commits from both surface via `git log --grep Mission:` and `git log --grep Delegation:`. The `ethos audit show --delegation <id>` command renders the unified view from the single session audit store.

**Implementation note (ethos-ersr / ethos-n4np / ethos-ggtu, #411).** Making the mission tree git-tracked and pushing it to a public repo turned two write paths into PII leaks. The Tier B skeleton writer wrote `prompt.md` and `record.yaml` with `$HOME` and the repo root intact, so thirteen committed prompts named the operator's home directory and machine layout; the per-tool-call audit lines carried full email bodies, recipients, and a recipient address inside a `CronCreate` prompt straight into the sealed, git-tracked record. The fixes, all applied at the write path before anything reaches disk:

- **One redactor, two call sites.** The path substitution the audit lines
  already used moved from unexported `internal/hook` code down to
  `mission.PathRedactor`; `hook`'s `redactAbsolutePaths` delegates to it. One
  implementation, no drift.
- **Redact inside the writer, not at the call site.** `$HOME`/repo-root
  redaction runs inside `WriteDelegationSkeleton` (and `CloseDelegation`'s
  free-text reason), so a future caller cannot write an unredacted prompt by
  forgetting a step. Resolving `$HOME` is a precondition — a home directory
  ethos cannot name is one it cannot redact — so an unresolvable `$HOME`
  refuses the write, which `PreToolUse` turns into a spawn refusal. Fail
  closed, never a partial write.
- **Keep-list for sensitive tools, not a deny-list.** A sensitive tool's
  audit input reduces to a keep-list (`send_email` keeps its subject;
  everything else becomes `[redacted]`), so a `cc` or `reply-to` added to the
  tool later is redacted the day it appears, not the day someone remembers it.
- **Address sweep on prose fields only.** A second pass rewrites addresses in
  free-form prose fields, leaving a `Read`'s `file_path` and a `Bash` command
  byte-identical to before.
- **Redact before the hash.** Both passes run before `tool_input_hash`, so the
  hash is over the stored form — the DES-052 cross-machine collision-detection
  invariant. A hash over a form that never reaches disk cannot be checked
  against anything. Lines that lose content carry `redacted: true`, matching
  the marker the hand-redacted public-website lines already use.

**Implementation note (four friction fixes, #412).** Four correctness fixes to specified-but-mis-built behavior. The design-relevant one is **ethos-jawp**: the commit-msg trailer hook now gates on an active mission binding rather than stamping every commit, and the active-mission sidecar clears on close — on the CLI, hook, and MCP close paths alike, with a genuine clear failure reported rather than swallowed. This makes the binding lifecycle symmetric: a delegation binds a session, its trailers land while the binding is live, and close tears the binding down. The other three are narrower: **ethos-qvbh** requires the validated `mission` field on reflections and back-fills it on read of legacy records; **ethos-72wj** subordinates the stub-agent install to the DES-026 generator and fails closed when agent-ownership lookup fails; **ethos-c0yp** scopes the `inputs.bead` deprecation to user submission so legacy reads are unaffected.

---

**End of draft v5.** Round-3 verdicts (rop APPROVE / jra APPROVE / rsc APPROVE WITH ONE NEW REQ) plus CEO clean-slate direction applied. Three structural simplifications landed (date-keyed two-tree layout, single audit storage, sibling-file counters) plus three round-3 IMPL fixes (Tier A verdict semantics, `max_delegation_depth` cleanup, transition window stated). Round 4 dispatched to test stability — design converges when a review round adds zero new substantive findings.

---

## DES-055: Generated make-check hook surfaces failures on stderr with exit 2 (SETTLED)

**Status**: Settled. Shipped in v4.0.1 (ethos-bo84).

### Problem

The PostToolUse `Write|Edit` hook that ethos generates into every specialist
agent definition (emitted by `internal/hook/generate_agents.go`, consumed as
`.claude/agents/<handle>.md` frontmatter) runs `make check` after each code
edit. On failure it routed the output to **stdout** and exited with make's
**raw** exit code:

```sh
_out=$(cd "$CLAUDE_PROJECT_DIR" && make check 2>&1); _rc=$?; \
  printf '%s\n' "$_out" | head -n 60; exit $_rc
```

For a blocking PostToolUse hook, Claude Code surfaces only **stderr** and
treats only **exit 2** as the signal that carries the block reason back to the
model. So a make-check failure appeared to the agent and operator as:

```text
PostToolUse:Edit hook returned blocking error … No stderr output
```

with no reason. The per-edit gate fired but the agent could not act on it — it
was blocked and could not see what to fix.

### Decision

The generated hook, in all three make-check branches (no-jq, empty-path,
extension-match), behaves as:

- **On failure** (`_rc ≠ 0`): write the truncated output to **stderr** and
  **exit 2** — Claude Code shows it and hands it to the model as automated
  feedback.
- **On success** (`_rc = 0`): stay **silent** and **exit 0** (also removes
  success-case noise).
- **Truncate with `tail -n 60`, not `head`**: `make check` runs
  go vet → staticcheck → shellcheck → markdownlint → go test → validate-content
  and stops at the first failing target, so the diagnostic is at the *end* of
  the accumulated output. `head` would keep leading passing-stage output and
  truncate the actual error away.
- **Per-edit coverage and full `make check` frequency are retained** — catching
  breakage immediately per-edit (rather than letting it accumulate to surface
  later) is deliberate.

Resulting tail (per branch):

```sh
_out=$(cd "$CLAUDE_PROJECT_DIR" && make check 2>&1); _rc=$?; \
  if [ $_rc -ne 0 ]; then printf '%s\n' "$_out" | tail -n 60 >&2; exit 2; fi; \
  exit 0
```

The emitted command stays POSIX-sh (`/bin/sh`/dash) compatible — no `pipefail`,
no process substitution, no bashisms.

### Verification

A behavioral test executes the emitted command under `/bin/sh` with a stub
`make` (`internal/hook/generate_agents_exec_test.go`): a failing stub yields
exit 2, the diagnostic on stderr (last line present, first line dropped —
proving `tail`), and empty stdout; a passing stub yields exit 0 and silence.
Confirmed live in the running v4.0.1 session — a specialist subagent edit that
breaks `make check` surfaces the real Go error with exit 2; a passing edit is
silent and non-blocking.

### Rejected alternatives

- **stdout + raw exit code** (the original form) — the bug. Blocking-hook
  stdout is not surfaced by Claude Code, and a non-2 exit does not feed the
  reason to the model. The failure reason was swallowed.
- **`head -n 60`** — truncates to the *first* 60 lines. Because make stops at
  the first failing target, the real diagnostic (at the end) is dropped,
  half-recreating the invisible-failure bug.
- **Move to a `Stop` hook / reduce frequency** — loses per-edit immediacy.
  Catching breakage the moment it is introduced, not at session end, is the
  whole point of the gate.
- **Non-blocking `exit 1`** — surfaces to the operator's transcript but does
  not hand the reason to the model as automated feedback; the agent cannot
  self-correct.

### References

- Bead ethos-bo84; shipped v4.0.1. Agent files regenerated under ethos-ri7c.
- Builds on DES-016 (hook business logic in Go) and DES-026 (generate agent
  definitions from identity data). The hook string is emitted by
  `GenerateAgentFiles` and regenerated on session start, so the fix propagates
  to `.claude/agents/*.md` automatically after upgrade.

---

## DES-057: Self-standing repos — repo-authoritative resolution, `ethos vendor`, and ext `.local` (SETTLED)

**Status**: Settled. Design ratified 2026-07-11 (bead ethos-ni0y). Amends
DES-008. v1 ships all three parts together. Consumer/first adopter: vox
(de-submoduled ethos into a vendored `.punt-labs/ethos/`, PR #284).

### Problem

Ethos resolves identities and their attributes through the three-layer chain
repo-local → active bundle → global (DES-051), where **the global fallback
catches the tail**: a handle absent from the repo-local `.punt-labs/ethos/`
still resolves from the global `~/.punt-labs/ethos/`. So a repo that vendors a
partial identity set is not self-standing — verified in vox, whose trimmed
15-identity copy still resolves `ylc` and `claudia` (global-only) via the
fallback. A fresh checkout without the global home would silently differ, and
the vendored set's incompleteness is invisible.

Two capabilities are missing: a way to make the repo layer authoritative (so
the fallback cannot mask gaps), and a way to produce a complete resolvable
snapshot (`ethos export` is lossy — it drops `.ext/` dirs and roles/teams by
contract, `export.go:20`).

### Decision

Three coordinated parts, shipped together in v1.

#### Part A — Repo-authoritative resolution mode (opt-in)

A string enum `resolution: layered | repo-only` in `.punt-labs/ethos.yaml`,
sibling to `active_bundle`. Unset ⇒ `layered` ⇒ byte-identical to today.
`repo-only` disables the **global read** across all four layered stores,
keeping repo → bundle and dropping the global tail.

- **One policy** (`repoAuthoritative bool`) threaded into every
  `NewLayeredStoreWithBundle`, read once through `identityStore()`. It gates
  **five read surfaces** (not four stores — the identity store has two global
  reads): identity lookup (`loadRaw`/`FindBy`/`Exists`/`List`), the
  identity-internal attribute chain (`attrChain`), the standalone attribute
  store (gated by a construction change), role, and team.
- **Writes route to the repo layer uniformly** in repo-only mode — all four
  stores, not just attributes. Leaving identity/role/team writes on global
  while reads are repo-only creates a write-then-invisible footgun (`ethos
  identity create foo` → global → invisible to repo-only reads).
- **Miss behavior**: aggregate all misses into one typed
  `ErrIncompleteRepoSet` wrapping `[]MissingRef{Kind, Slug, Path}`, naming the
  handle and each missing `{kind}/{slug}` file. Referenced-but-missing
  attributes (soft warnings today) become hard errors in repo-only mode.
  `whoami`'s terminal `resolve.Resolve` no-match error is wrapped with a
  repo-only hint (otherwise it returns a generic "no identity matches" that
  cannot name what's missing). Surfaces: `whoami` (non-zero exit), agent
  generation (fail the affected agent), session-start (**degrade**: stderr +
  skip injection, never brick a live session), MCP (typed error per DES-020),
  and `ethos doctor` (the authoritative hard completeness gate).
- **`resolution: repo-only` with no repo layer** (no `.punt-labs/ethos/`
  directory and no repo-local/legacy bundle) is a **hard startup error**, not
  a silent fall-through to global.
- **active_bundle**: keep repo → bundle for `SourceRepo`/`SourceLegacy` (both
  travel with the checkout); reject a `SourceGlobal` bundle under repo-only at
  startup.
- **ext reads — DES-044 carve-out** (closes the consumer-found gap that vendor
  copies ext but repo-only never read it). In `repo-only` mode, ext resolves
  from the identity's **repo/bundle source layer** via the Part C
  `readNamespace` base+`.local` merge — not global. A single `extStore(handle)`
  selector gated on `repoAuthoritative` replaces the hardcoded global in every
  ext read/write method (the `Load`-internal layer shortcut is likewise
  `repoAuthoritative`-gated, so `layered` mode is byte-identical). Ext writes to
  a bundle-sourced identity are refused, matching the read-only bundle rule. The
  **required-ext set is the `.vendor.yaml` manifest** (ethos's own artifact): a
  manifest-recorded ext base file absent from the source layer is a miss, folded
  into the same `ErrIncompleteRepoSet` as attribute misses — so a global-less
  checkout fails loud naming the missing ext instead of silently dropping agent
  memory wiring. Live `Load` stays additive but records the verdict (never
  bricks a running session on missing ext; the verdict surfaces via the Part A
  table — `doctor`/agent-gen fail, session-start degrade). A repo-only set with
  **no `.vendor.yaml`** cannot have its ext completeness verified; `ethos
  doctor` emits an explicit advisory saying so, keeping the limit visible rather
  than silent. This carve-out ships with or after Part C (it depends on
  `readNamespace`); DES-044 is unchanged in `layered` mode.

#### Part B — `ethos vendor` command

Top-level `ethos vendor [handle...]`, a peer of the (kept, lossy) `ethos
export`. Snapshots a complete, resolvable identity set into
`.punt-labs/ethos/`.

- **Plan/`--dry-run` by default** (it writes into git-tracked space); `--apply`
  executes. Flags: `[handle...] --team --all --to --prune --dry-run/--apply
  --json` plus `--allow-ext-key` (below). `--no-teams` and `--from` are
  **rejected** — they let a user produce an incomplete "complete" snapshot.
- **Transitive-closure BFS to a fixed point.** Edges: identity →
  personality/writing-style/talents/roles/teams; identity → its `.ext/` base
  files; team → member identities + roles; and the **load-bearing reverse
  edge** identity → teams-that-contain-it (this is what pulls in global-only
  members like `claudia`, reachable only through team membership). Roles are
  leaves; there is no team→team edge; termination is guaranteed (finite node
  universe, monotone growth). Document the **blast radius**: the closure pulls
  the connected component of the team graph, so vendoring one identity in a
  dense org can vendor most of the roster — `--dry-run` first.
- **`.vendor.yaml` manifest**: vendor writes a manifest recording the vendored
  closure — the identities and each identity's ext base files it copied. This
  manifest is the **required-ext set** that Part A's repo-only ext-miss rule
  reads (ethos's own record of what was vendored; never a consumer value, so
  DES-008-clean).
- **Completeness postcondition**, verified before the command reports success:
  a repo-only store rooted at the snapshot resolves every identity with zero
  not-found and every team validates against that layer alone. The ext dimension
  is **manifest-parity** — every `.vendor.yaml`-recorded ext base file is
  present in the snapshot (a subset check that ignores `.local` and extra
  files), *not* exact-tree-verbatim — which is the same requirement Part A's
  repo-only ext read enforces, so producer-verify and consumer-read cannot
  diverge. This predicate is exactly Part A's precondition — the two halves
  share it.
- **ext handling**: includes base `<ns>.yaml` (ext is **on by default** —
  discovery found ext is all useful config, e.g. quarry `memory_collection` +
  `session_context` on all 30 identities, zero secrets), and **always skips
  `<ns>.local.yaml`** (Part C). Copies **regular files only** (`lstat`; a
  symlink `quarry.yaml → quarry.local.yaml` or `→ ~/.ssh/id_rsa` must not
  smuggle content past the name-skip).
- **Fail-closed credential guard**: vendor **blocks** (non-zero exit) if a base
  ext key *name* matches the credential heuristic, naming `<handle> <ns>/<key>`
  and suggesting `--local`. Override is **per-key only**
  (`--allow-ext-key <ns>/<key>`, repeatable); no blanket `--force`. Matching is
  **name-only** (never value inspection — keeps DES-008 intact) with
  underscore-token membership, evaluated **BLOCK → block-pairs → EXCLUDE →
  WARN** (fail-closed). A name BLOCKs if it holds any block token (`password`,
  `token`, `api_key`, `private_key`, `secret`, `credential`, …) or a block
  pair (`api`+`key`, `access`+`key`, …), **regardless of any other token it
  also holds**. Only a name with no block token is then cleared by an EXCLUDE
  public-reference token (`*_id`, `fingerprint`, `keyid`, `pubkey`, …) —
  EXCLUDE overrides only WARN, never BLOCK. Then WARN (`key`, `salt`, `seed`,
  `pin`, `dsn`, …). So `email_password`/`server_secret`/`provider_api_key` →
  BLOCK: a block token or pair beats a leading EXCLUDE token (checking EXCLUDE
  first was a fail-open — it cleared `*_password` keys into git — corrected
  here, ethos-ni0y). `gpg_key_id`/`pubkey`/`key_fingerprint` → clean: no block
  token, so EXCLUDE clears the ambiguous WARN token `key`.
  `gpg_signing_key_id`/`access_key_id` → BLOCK: the `*_key` pair beats the
  trailing `_id`, a deliberate fail-closed cost cleared per key with
  `--allow-ext-key`. `memory_collection`/`session_context` → clean;
  `api_token` → BLOCK. The identical lint runs advisory in `ethos doctor`. The
  curated pattern list, and the fail-closed ordering, are djb-owned.
- **MCP tool** + a `formatVendor` formatter in `internal/hook/format_output.go`
  (per DES-020; the formatter lives in the hook package, not `internal/mcp`).

#### Part C — ext `.local` split (amends DES-008)

Each ext namespace file `<handle>.ext/<ns>.yaml` gains an optional companion
`<handle>.ext/<ns>.local.yaml` for secret/machine-specific values, mirroring
the org's `.envrc`/`.envrc.local` and `vox.md`/`vox.local.md` convention.

- **Read/merge**: one `readNamespace(handle, ns)` helper reads base then
  overlays `.local` per key (`.local` wins), `os.IsNotExist`-guarded.
  `loadExtensions` and `ExtGet` route through it; `get_identity`/`whoami`
  return one flat merged map. No `.local` present ⇒ byte-identical to today.
  `readNamespace` is a **read-only merged view — its result is never marshalled
  back to a file** (a tested invariant: prevents a base write from folding
  `.local` secrets into the committable file). `ExtDel` targets one file
  (base or, with `--local`, the companion), not the merged view.
- **Write targeting**: `ethos ext set --local` writes the companion; default
  writes base. `ExtList` strips `.local.yaml` **as a unit before** the `.yaml`
  case (so no phantom `<ns>.local` namespace) and reports the union.
- **Boundary**: vendor copies `<ns>.yaml`, **always** skips `<ns>.local.yaml` —
  the file layout *is* the boundary, no value redaction. Gitignore
  `.punt-labs/ethos/**/*.local.yaml`, emitted by `vendor`/`setup`; `doctor`
  asserts the rule is present and **errors if any `*.local.yaml` under
  `.punt-labs/ethos/` is already git-tracked** (gitignore does not untrack).
- **Scope, stated explicitly**: `.local` is a **git-exclusion** mechanism, not
  a secrets vault — the merged view still flows to the model and to quarry
  transcript capture at runtime. And `.local` is visible only to
  ethos-mediated reads; a tool exercising DES-008's direct-read blessing sees
  **base only**. Never serialize the merged identity view into tracked space.

### Reasoning

- A **layout boundary** (`.local` file, name-based skip) beats value redaction:
  it is deterministic, auditable, and preserves DES-008's rule that ethos never
  interprets ext values. The credential name-lint reads *key names*, not
  values, so it is DES-008-clean.
- **Fail-closed on credentials**: a warning fires after vendor has already
  written the file into the working tree — one `git add` away — and is
  invisible in CI. The command that puts bytes into git must refuse when those
  bytes are name-flagged. Per-key override keeps it from becoming a nuisance.
- **ext on by default** is safe given (a) the verified zero-secret state across
  30 identities, (b) the structural `.local` skip, and (c) the block-lint
  shipping with it. ext is load-bearing config (agent memory wiring); a
  vendored repo without it produces agents that silently lost their memory.
- Vendor's completeness postcondition **is** repo-only's precondition — one
  shared predicate, so the produce half and the verify half cannot drift.
- **Opt-in** everywhere preserves back-compat: unset `resolution` and absent
  `.local` are both byte-identical to today.

### Rejected alternatives

- **Default ext-off + value redaction** — discovery found ext is all useful,
  secret-free config; the `.local` split is a cleaner, deterministic boundary.
- **Warn-only credential lint** — fires after the write, missed in CI. Rejected
  for fail-closed block at the git-writing boundary.
- **Value/content inspection to find secrets** — violates DES-008's
  no-interpretation invariant. Name-only + layout boundary chosen.
- **`--no-teams` / `--from <layer>` vendor flags** — allow producing an
  incomplete set the tool calls "complete." Cut.
- **Boolean `vendored: true` / `global_fallback: false`** — conflate
  provenance with policy / negative-boolean footgun. String enum chosen.
- **Replace the repo layer with the vendored set** — breaks every repo using
  `.punt-labs/ethos/` as a submodule (DES-051's rejected alternative still
  holds).
- **Deprecate `ethos export`** — export and vendor do different jobs
  (foreign-format lossy handoff vs. native lossless closure). Keep both.
- **Directory-exists ext-miss rule** (a repo `<h>.ext/` present ⇒ complete) —
  cannot distinguish "vendor omitted `quarry.yaml`" from "identity has no ext,"
  so it fails to make an incomplete vendor loud. The `.vendor.yaml` manifest
  records exactly what was vendored, which is the discriminator.
- **ext continues to resolve from global in repo-only** (preserve DES-044
  unconditionally) — the original DES-057 reconciliation did this and it was
  wrong: vendor copies ext into the repo but resolution never reads it, so a
  global-less checkout silently drops agent memory wiring (consumer-found on
  PR #345). The carve-out is required for the feature to actually deliver
  self-standing repos.

### Verification (acceptance — vox's vendored 15-set is the test case)

1. The vendored 15-identity set regains quarry `memory_collection` +
   `session_context` (and vox voice config): vendor records them in
   `.vendor.yaml`, and **repo-only mode reads ext from the repo layer** (the
   DES-044 carve-out), so on a global-less checkout the agents resolve their
   memory wiring — and a manifest-recorded ext file gone missing fails loud
   rather than silently dropping memory. (Fully met for vendored sets;
   hand-authored manifest-less sets get a `doctor` advisory that ext
   completeness is unverifiable.)
2. `*.local.yaml` is hard-excluded from vendor output — structurally (name
   skip + regular-files-only), not by heuristic scrub.
3. Repo-authoritative mode resolves the complete set with **zero global
   fallback** and hard-errors listing anything missing.
4. A symlinked base ext file is refused by vendor; the base-write-base-only
   invariant is tested; the block-lint fails closed on `api_token` and passes
   `gpg_key_id`; `doctor` errors on an already-tracked `*.local.yaml`.

### Implications

- New package `internal/vendor/` owns the closure walk + the completeness check
  (`ErrIncompleteRepoSet`), imported by the four stores (live miss), `ethos
  doctor` (gate), and `ethos vendor` (pre-write validation) — one
  implementation, no divergence.
- `resolve.RepoConfig` gains `resolution`; the four `NewLayeredStoreWithBundle`
  constructors gain the `repoAuthoritative` policy.
- `internal/identity` gains `readNamespace` + `.local` write targeting;
  `ethos ext set` gains `--local`.
- `internal/mcp` gains the `vendor` tool; `internal/hook/format_output.go`
  gains `formatVendor`.
- DES-008 is amended by reference (this record), matching how DES-022 and
  DES-044 extend it.

### Implementation note (`ethos-8s2z`) — semantic, fail-closed `.local` coverage

The "`.gitignore` covers it" guarantee above was first enforced by an
exact-string match, which failed two ways: `setup`/`vendor` re-appended a narrow
per-file rule even when a broader rule already covered the file (churn), and —
worse — a repo carrying only the narrow `.punt-labs/ethos/**/*.local.yaml` rule
read as "covered" while a `.punt-labs/vox/*.local.md` or `beadle/*.local.json`
secret stayed stageable (fail-open). Coverage is now decided with `git
check-ignore`, fail-closed: `enable`/`setup`/`vendor` probe a **set** of
representative paths — including a non-ethos subtree and a non-`.yaml` variant —
and treat the tree as covered only when *every* probe is ignored; a match counts
only when its ignoring source travels with the repo: `core.excludesFile` is
neutralized at the source with `-c core.excludesFile=<os.DevNull>` (the null
device, portable across platforms), and a `.git/info/exclude` match is rejected
because its source sits inside `.git` and does not travel — so a per-clone
exclude cannot suppress writing the committed rule. `enable`,
`setup`, `vendor`, and `doctor` share one exported `enable.LocalIgnoreRule`
constant, so the rule `doctor` advises and the rule the write path emits cannot
drift, and `doctor`'s tracked-secret scan covers the whole
`.punt-labs/**/*.local.*` tree. The canonical `.gitignore` carries
`.punt-labs/**/*.local.*` for `.local.*` files alongside `.punt-labs/**/local/**`
for the DES-058 live zone, and the same fail-closed probe-set now guards that
live-zone rule too. Shipped in PRs #421/#422/#423 (release v4.10.0).

### References

Amends DES-008 (generic extension mechanism). Builds on DES-051 (three-layer
resolution), DES-044 (extensions resolve through the chain), DES-011 (whoami
resolution), DES-020 (MCP formatters). Bead ethos-ni0y; consumer vox (PR #284).
Design missions (specialist drafts preserved in branch history):
`m-2026-07-11-004` (resolution mode, bwk/rsc), `-005` (vendor, mdm/rop),
`-008` (ext `.local`, bwk/rsc), `-010` (ext-read carve-out, bwk/rsc).

---

## DES-058: Live audit write path and sealed committed record (amends DES-054) (SETTLED)

**Status**: Settled. Shipped in v4.8.0. Bead `ethos-t5b6`. Amends DES-054 v5 storage.
Cross-repo request from punt-kit (clean-tree gate on `punt release`
preflight failing against siblings with live sessions). Full design:
`docs/audit-seal.md`.

### Problem

DES-054 stores the session audit log at
`<repo>/.punt-labs/ethos/sessions/<YYYY-MM-DD>-<session-id>/audit.jsonl`,
git-tracked (shared history, path redaction, commit-trailer resolution)
AND appended by the PreToolUse hook on every tool call. A file cannot be
both git-tracked and continuously appended: any repo with an active
session has a permanently dirty tree, observed blocking `punt release`
preflight. `missions/<id>/log.jsonl` and the tracked `.create.lock` share
the disease.

### Design

Split the append-heavy files across two zones in the same checkout: the
gitignored `.punt-labs/local/` zone is the live write path; the tracked
`.punt-labs/ethos/` tree holds the sealed record as immutable chunk files.

- **Live location — `.punt-labs/local/` (machine-local zone).** The live
  file is `<repo>/.punt-labs/local/ethos/sessions/<session-id>.audit.jsonl`.
  `local` is the org convention for machine-local-never-committed state
  (`.envrc.local`, `vox.local.md`); its gitignore rule ships once, org-wide,
  via the punt-kit `punt-labs-dir.md` standard (merged `e3ab9a3`) and
  `punt:init`/`punt:audit` enforcement. Per-checkout isolation is automatic
  (the file lives inside its checkout), which removes the *seal-time*
  checkout-path scoping — the path itself is retained in the purge tombstone
  and vacuum cross-check (§Seal failure). Rejected: the home-dir global tree (operator
  ruling for `local`), `TMPDIR`/`.tmp/` (deletable scratch), and
  `.punt-labs/local/<branch>/` (sessions span branches).
- **Redacted live lines.** The write path is unchanged (build → redact →
  hash → preview → append); only the destination moves. The live file
  holds redacted lines so the seal is a transformation-free byte copy,
  redaction stays in one place, and nothing leaks at rest.
  `tool_input_hash` is still computed over the redacted form (DES-052
  unchanged).
- **Strictly-monotonic per-session timestamp.** No `seq` field. Every
  append allocates `ts = max(now, last_ts + 1ns)` under the session flock,
  giving a per-session total order that is collision-free regardless of a
  coarse or NTP-stepped clock. Line identity is `(session, ts)`. The
  writer initializes `last_ts` from the **seal watermark's own source set** —
  the max over existing sealed chunk timestamps, every covering `.quarantine`
  marker's verified `<last>`, and a frozen legacy file's max ts — so new lines
  sort strictly after frozen history and no ts it mints under a clock regression
  can sink into the gap a partial quarantine's marker opens above the max chunk
  ts (below the watermark, never sealed, never shown). On live-file reopen the
  writer **truncates a non-newline-terminated tail** under the flock before
  appending: that fragment is an un-synced partial write, unrecoverable
  regardless, and truncating it prevents a new complete line from being
  glued onto it into one unparseable line (the reader still skips a torn
  tail in a file not yet reopened). A newline-terminated line that still
  fails to parse (an out-of-order page writeback losing an earlier slice) is
  **skipped with a stderr count** by every consumer — writer recovery, seal,
  and read — never exit 2 and never silent, since its own `f.Sync()` never
  completed. The writer's count rides PreToolUse-hook stderr, which Claude
  Code does not surface on exit 0, so there it is best-effort; the
  load-bearing channel is the seal's and read's re-emission of the same count
  in visible contexts.
- **Chunk sealing.** A seal never modifies a tracked file: it writes a
  **new immutable chunk** `audit-<first>-<last>.jsonl` (Unix-nanosecond
  first/last timestamps, zero-padded to 19 digits — sorts chronologically,
  collision-free, filesystem-safe) holding the live lines with
  `ts > watermark`. The watermark is the max `<last>` across the session's
  existing chunk names (a frozen legacy `audit.jsonl` is scanned and
  contributes the max ts over its lines; a `.quarantine` marker contributes
  the **verified** `<last>` it records — the max ts the corrupt bytes reached
  and of any lines quarantine re-sealed, never the filename `<last>` on
  faith); the live writer seeds its monotonic floor from this same set
  (§strictly-monotonic per-session timestamp), so no mintable ts sits below it.
  The malformed-name exit 2 is **scoped to the chunk namespace, per
  directory shape**: a near-miss carrying a chunk prefix that fails its
  namespace's full parse — `audit-<19digits>-<19digits>.jsonl` in a session
  dir, `log-<session-id>-<19digits>-<19digits>.jsonl` in a mission dir — fails
  the seal (a skipped chunk would regress the watermark), while every
  non-chunk sibling (the frozen `audit.jsonl` or `log.jsonl`, a `.quarantine`
  marker or a `.corrupt`/`.corrupt-<hash>` artifact under a covering marker
  whose named range contains the artifact's, in either namespace, a mission's
  `contract.yaml`/`results.yaml`, any unrelated file) is ignored and draws no
  error. When it scans, the seal also **verifies each chunk's content** — a
  chunk that does not parse whole or whose last line `ts` != its filename
  `<last>` is corruption and fails the seal (exit 2), the same check the read
  makes; the specified escape is `ethos audit quarantine`, never
  `--no-verify`. The sealed directory is dated by the **session start date**
  (from the roster; when `session purge` has removed the entry, from an
  existing sealed dir's date prefix, a purge tombstone, or the live file's
  first-line ts — never wall-clock, so one session never splits across two
  dirs). Written via temp + rename for atomicity; temp names embed the chunk
  range, so a widened tail after a crash yields a new temp name rather than an
  overwrite — the seal deletes stale `.audit-*.jsonl.tmp` older than itself (in
  a mission dir **any** session's `.log-*-*.jsonl.tmp`, namespace-wide not
  session-scoped, so a crashed session's orphaned temp is still collectable; the
  cross-flock cost — one seal hitting another's in-flight temp — fails that
  seal's rename loudly (exit 2, re-run succeeds), never silently) under the flock
  before writing its own, and leaves any foreign `*.tmp` untouched (sibling rule). The seal's final act unconditionally `git add`s
  **every** untracked
  chunk in the session dir (not only the one it wrote), so a crash after
  rename but before staging cannot leave an orphan dirtying the tree. Two
  branches only ever add distinct chunk files, so merges are conflict-free
  with stock git — no merge driver, no `.gitattributes`, no prefix
  invariant, no divergence/reseal.
- **Seal triggers.** Primary at **pre-commit**: `ethos audit seal` visits
  **every** session directory *and* mission directory in the repo — the union
  of the live zone (session `sessions/<session-id>.audit.jsonl` and
  per-(mission, session) `missions/<id>/<session-id>.log.jsonl` lines to seal)
  and the tracked sealed tree (`sessions/<dir>/` and `missions/<id>/` chunks to
  stage) — seals each session's pending live lines in both namespaces and
  `git add`s **every** untracked chunk it
  finds, so the record lands in the same commit/PR as the work and a crashed
  seal's orphan chunk is recovered even when that session or mission dir has no
  pending live lines. Repo-wide, not committing-session-scoped: in a squash-merge world
  per-commit attribution is a non-goal, so orphaned/crashed sessions' lines
  simply land at the next commit — no liveness probe, no session-attribution
  logic. Not commit-msg (runs after the index snapshot). Secondary at
  **mission close** for Tier B, which seals **every** live file under the
  **closing checkout's** `missions/<id>/` — each session's that wrote into this
  checkout, under its own per-(mission, session) flock — so the record is
  complete at close **for that checkout**; a delegated worker in a worktree (the
  required shape for code archetypes, not an edge) wrote its tail into its own
  checkout, which rides its own seal triggers — pre-commit and the SessionEnd
  teardown flush. Session-end sealing is a courtesy flush for a
  long-lived checkout, but the load-bearing mitigation for a worktree about
  to be deleted: the live file lives inside the checkout, so worktree removal
  or `git clean -fdx` destroys any unsealed lines, and the SessionEnd flush is
  what preserves them. In a **gitlink-mounted** repo (a consuming repo before
  bead `e29s`)
  the sealed tree is unreachable, so the teardown flush defers like every
  other seal there — one-line notice, nothing written — and deleting the
  checkout (hook-driven or a hookless `rm -rf`) destroys its unsealed tail.
  The design accepts this as a **bounded pre-`e29s` limitation**: there is no
  in-repo target to seal to, a home-tree transit copy was rejected as a
  write-only subsystem (§Rejected alternatives), and the org rule is to vendor
  a repo
  (`e29s`, the `punt-4yy` campaign) before relying on its audit trail.
- **Merged read.** `ethos audit show` unions the session's sealed chunks
  and the live lines with `ts` past the sealed watermark, orders by `ts`
  (a **stable** sort, so two legacy lines sharing a pre-discipline `ts` keep
  their file order and the "output identical" criterion holds),
  and dedups the two post-discipline pools while passing the legacy pool
  through. Post-discipline lines (post-upgrade chunks + live) dedup on
  `(session, ts)` — loss-free, since equal ts implies a byte-identical line
  from the same append-only live file and distinct events get distinct ts —
  collapsing the overlap a cross-branch re-seal leaves. Frozen legacy lines
  are **not deduped at all**: nothing ever duplicates a legacy line (the seal
  never copies one into a chunk), so the pool has no duplicate to collapse and
  any collapse could only drop a distinct event the pre-discipline clock gave
  a colliding ts; the two pools never mix because every legacy ts sits below
  every post-upgrade ts. A monotonic chunk that does not parse whole, or whose
  last ts != its filename `<last>`, is corruption and surfaces as an **error
  naming the chunk** (exit 2, escape `ethos audit quarantine`), never a silent
  drop; a quarantined chunk's unrecovered sub-range shows in the output as an
  **explicit gap marker** (the lines quarantine re-sealed from the live file
  reappear as an ordinary chunk),
  and only a legacy file, whose name has no ts to contradict, keeps the
  tolerant torn-tail drop. A newline-terminated but unparseable live line is
  skipped with a stderr count, never exit 2. In a gitlink-mounted repo (seal
  deferred, below) `audit show` **flags** a session whose live tail sits past
  the sealed watermark (`N unsealed lines, sealing deferred until vendored`).
  In the common single-branch case the dedup is a no-op and output is
  identical to the pre-change single-file read; the DES-054 early-return read
  path is **replaced** by this union. The "output identical to the old
  single-file read" equivalence is strict for a ts-ordered legacy file; a
  pre-discipline file whose lines are out of ts order is reordered to ts
  order by the stable sort.
- **Cross-branch re-seal.** The live file is in `.punt-labs/local/` and
  survives `git checkout`, but the watermark is derived from the tracked
  chunk set, which a branch switch rewrites. A branch lacking an earlier
  chunk therefore re-seals already-sealed lines into a wider chunk; after
  merge both chunks coexist and overlap. Re-sealing is **desired** —
  suppressing it would lose the record on whichever branch actually merges
  — so the overlap is resolved at read by the `(session, ts)` dedup, not
  prevented. The monotonic-ts subsystem is separately rewind-robust (the
  writer recovers `last_ts` from the live file's own tail), so timestamps
  never regress across a branch switch even though the seal watermark can.
- **Mission tree.** `log.jsonl` gets the same live-write/chunk-seal
  treatment in **full symmetry with the audit path**: the mission live log is
  per-(mission, session) —
  `<repo>/.punt-labs/local/ethos/missions/<id>/<session-id>.log.jsonl`, each
  session appending under its own strictly-monotonic per-(mission, session)
  `ts` and the flock that serializes its appends — and each seal writes a
  chunk `log-<session-id>-<first>-<last>.jsonl` holding exactly that live
  file's unsealed tail. The chunk name carries the sealing session id because
  every session seals into the one shared `missions/<id>/` directory (unlike
  an audit chunk's own per-session dated dir); the `<session-id>` segment
  supplies the cross-session separation the dated directory gives an audit
  chunk, so `<first>` is unique within a session and two chunks share a name
  only when they share a session — no add/add conflict from two checkouts
  sealing different sessions' events. The read unions **all** sessions' chunks
  *and* live tails in the directory (stable-sorted by `ts`, identity
  `(session, ts)`, the `session` half supplied by the chunk name or live
  filename, so an Event line needs no session field) and the seal is
  **per-(mission, session)** (watermark = max `<last>` over that session's own
  `<session-id>` chunks, joined by the mission-namespace `.quarantine` markers'
  verified `<last>` and the frozen legacy sources' max `ts` — the tracked
  `log.jsonl` and the legacy per-checkout `missions/<id>.jsonl` residue where
  present, §Migration — exactly the audit watermark's three-source set resolved
  in the mission namespace; tail = that session's own live lines above it, the
  writer seeding its monotonic floor from the same set).
  Authoritative seal for the closing checkout at mission close (sealing every
  session's live tail under that checkout's `missions/<id>/`, §Seal triggers;
  cross-checkout worker tails ride their own checkouts' triggers), pre-commit as
  the clean-tree backstop.
  All `.lock` files
  move to the global tree (`~/.punt-labs/ethos/missions/<id>.lock`,
  `.create.lock`) — a lock file never belongs in shared history; migration
  untracks the existing `.create.lock` **and removes it from disk** (a bare
  `git rm --cached` leaves an untracked file that re-fails the clean-tree
  gate), and the code stops writing any in-repo lock. Lock relocation is
  unchanged by the chunk rulings.
- **Seal failure — three classes.** The pre-commit hook exits 2 (fail
  closed, DES-055 shape) on an I/O error (`EACCES`/`EIO`), a malformed chunk
  filename, a **corrupt sealed chunk** (does not parse whole, or last ts !=
  filename `<last>`), or a **`git add` failure** staging a new or orphan
  chunk or a quarantine artifact — with a self-contained remedy message. The specified escape from a
  corrupt-chunk exit 2 is **`ethos audit quarantine <chunk>`**, never
  `--no-verify`. It **retires the corrupt chunk first** — renaming it out of
  the namespace to `.corrupt` (committed as evidence; seal and read ignore the
  name) frees the chunk's name so a re-sealed chunk that takes it cannot clobber
  a still-named corrupt chunk. It then re-seals any still-readable lines of the
  range from the live file into an **ordinary content-named chunk** (named
  `audit-<first>-<last>` from the re-sealed lines' own first/last `ts`, so a
  **partial** recovery yields `audit-<first>-<C>`, `C < last`, coinciding with
  the retired name only on **full** recovery — which keeps the content-vs-name
  check from ever firing on a re-sealed chunk; `(session, ts)` dedup tolerates
  the overlap), writes a tracked `.quarantine` marker with **deterministic content
  only** (chunk name, verified content-derived `<last>`, unrecovered sub-range,
  reason — **no wall-clock timestamp**, so two checkouts quarantining the same
  chunk from the same state produce byte-identical artifacts that merge clean),
  and **stages every quarantine artifact the marker covers** — the `.corrupt`,
  the marker, the re-sealed chunk, and any `.corrupt-<hash>` — itself
  (`git mv` + `git add`) so the tree is clean with no hand-staging. The verb
  resolves the artifact's namespace from its name: a `log-<session-id>-<..>`
  mission chunk re-seals from `missions/<id>/<session-id>.log.jsonl` (session id
  parsed from the chunk name) and writes its `.corrupt`, re-sealed chunk, and
  marker in `missions/<id>/`, the marker contributing its verified `<last>` to
  that session's mission watermark — the same verb resolved in the mission
  namespace. The marker contributes to the watermark its
  **verified** `<last>`, never the filename `<last>` on faith, which would
  silently suppress every later line's seal. `audit show` then shows only the
  unrecovered sub-range as an explicit gap. Because the marker is the last
  durable artifact and is itself written via temp + `f.Sync()` + rename (a torn
  marker reads as **absent** everywhere), a crash mid-verb resumes
  deterministically by artifact state: chunk present and no `.corrupt` → fresh
  run; a `.corrupt` with no covering marker → **resume** (finish the re-seal,
  marker, and stage — on resume a chunk already at the freed target that parses
  whole and matches the recovery range **is** the completed re-seal, verified
  and kept, since the refuse-if-target-exists guard is a fresh-run rule; a
  chunk there that **fails** verification is fresh damage, retired under the
  same `<name>.corrupt-<hash>` suffix before the resume proceeds), and
  seal and read treat this state as an error prompting the resume, never
  silence; a `.corrupt` with a covering marker → idempotent no-op that also
  verifies every covered quarantine artifact (including any `.corrupt-<hash>`)
  is staged, **requires a chunk to stand for the marker's recovered range**
  (re-running the re-seal if none does while the live file still holds lines of
  that range outside the recorded unrecovered sub-range — the
  crash-between-retirement-and-re-seal window), **and content-verifies any
  chunk now
  at a covered name** — fresh corruption there is a **new** event, retired under
  a deterministic `<name>.corrupt-<hash-of-corrupt-bytes>` suffix (the
  never-overwrite is per exact filename, so it never collides with the first
  `.corrupt`, and a byte-identical suffix already on disk is retired under the
  first free name in a deterministic sequence, `-2`, `-3`, …, rather than
  refusing — the sequence is the never-overwrite),
  re-sealed, with the marker updated by the smaller-range/union rule. It never
  overwrites an existing `.corrupt`; recovery is repo-wide
  only once the quarantine commit merges, and until it propagates other
  checkouts still exit 2 — bounded and loud. Two checkouts quarantining the
  same chunk from divergent states can produce an ordinary marker conflict,
  resolved by keeping the marker with the **smaller** unrecovered range plus
  the union of the re-sealed chunks (the checkout that recovered more wins).
  Fail-closed must never leave bypass as the only exit. The no-op exit 0 is
  entered only when **both** live trees have nothing to seal and neither tracked
  namespace holds an unstaged orphan chunk — the sessions dir and
  `local/ethos/missions/` each absent (`ENOENT`) or holding no pending live
  lines, `ENOENT` on one not short-circuiting the other, the walk unioning both
  namespaces as the repo-wide trigger does; but the
  vacuum cross-check is **per session** and iterates two sources: each
  roster-active session bound to this repo (any checkout path) **and** each
  purge tombstone whose recorded repo is the committing repo and that carries
  an unsealed-lines flag (so the crash → purge → checkout-deleted → commit
  sequence still warns). The tombstone records the repo and recorded checkout
  path it was purged from, so the check scopes tombstones to the committing
  repo and derives the live path from that path and the session id; the
  warning repeats at each commit until the operator acknowledges it with
  `ethos session purge --ack <session-id>`, which **renames** the tombstone to
  `<session-id>.purged.acked` — a retained record, never warned on again, so
  the loss survives the ack. Like the `.corrupt` rename this never overwrites
  per exact filename: a second ack for a re-purged id retires under a
  content-derived `<session-id>.purged.acked-<hash>` suffix — a stable,
  collision-averse name — and acking onto an existing identical `.acked-<hash>`
  retires under the first free name in a deterministic sequence (`-2`, `-3`, …)
  rather than refusing, so two byte-identical tombstones stay two records and
  the warning is always retirable. For
  each the hook `stat`s
  the recorded live file **and every mission live file the session is expected
  to have left** — an enumerated expected set, not a glob, since a glob over
  `local/ethos/missions/*/<session-id>.log.jsonl` sees only extant files and a
  deleted mission live file, the loss this check exists to catch, would draw no
  warning. The expected mission live files are those for missions whose
  **tracked** `missions/<id>/` chunks carry the session's id (a sealed chunk
  proves the live file existed, and live files are never deleted by design)
  **unioned** with the missions bound to the session in mission records (the
  `ethos mission claim` binding and delegation records — the sealed-nothing-yet
  Tier B case). It `stat`s each and **warns on stderr
  naming any session whose live
  file is absent** in either namespace (still exit 0 — it may mean a checkout
  was deleted, or one
  session's live file removed, with unsealed lines). The residual is bounded and
  stated plainly: a mission live file written by a session that never sealed a
  chunk and holds no mission binding cannot be enumerated, so its loss is
  undetectable — the same bounded class as the hookless `rm -rf`. `session purge` itself
  refuses (or with `--force` warns and sets the tombstone flag) when a live
  file in either namespace still holds lines above its watermark, and sets the
  flag in a distinct
  "live file already gone" variant when a recorded live file is already
  absent (so the crash → checkout-deleted → purge → commit order is caught
  too). A
  **gitlink-mounted** `.punt-labs/ethos/` (a consuming repo before bead
  `e29s`) is also a no-op exit 0, but **not silent**: it prints a one-line
  stderr notice and `audit show` flags the deferred session. The seal target
  is the wrong tree, so sealing defers — the live lines stay in
  `.punt-labs/local/`, the clean-tree goal is already met, and the record
  seals once `e29s` lands. The SessionEnd teardown flush in a gitlink mount
  defers the same way, but its notice rides exit-0 SessionEnd stderr (reaching
  no one) and the checkout — with the live tail — is deleted moments later, so
  the teardown deferral is unannounced and post-hoc undetectable: a bounded
  pre-`e29s` limitation (§Seal triggers), not a recoverable case.
  This deferral must not exit 2, or it would block every commit in every
  consuming repo; the notice plus the flag keep the window from being
  unbounded silence. A corrupt
  sealed chunk *is* the divergence an earlier draft claimed impossible:
  I11-chunk makes it unreachable normally, so if it appears the store is
  damaged and the seal fails loudly rather than sealing past it.

**Implementation note (ethos-q6e2).** "The path itself is retained in the purge
tombstone and vacuum cross-check" was specified but only half-built: the
tombstone carried `Checkout`, the cross-check ignored it, and the roster had no
field to carry one, so both probes fell back to whichever checkout was
committing. Any checkout that had not written a session's live file read its
absence as loss — a false "lines were lost" on every commit from a worktree, a
fresh clone, or a second machine, for every mission a long-lived session had
ever touched. The fix threads the recorded writer through both probes:
`session.Roster` gains `Checkout` (a peer of `Repo` — an identity is shared by
every checkout of a repo, a live zone by exactly one), the live probes follow it,
and the tracked-chunk probes stay rooted at the committing checkout. The loss
predicate is `Lost() = !(Present || Legacy) && (WriterRecorded || !Sealed ||
!WriterPresent || WriterZone)`. A sealed chunk vouches only through its own
watermark — it proves the lines up to the last seal survived, not the unsealed
tail that lives only in the live file — so it cannot suppress a deletion in the
checkout that wrote it. Suppression happens in exactly one case: a checkout we
merely fell back to (`!WriterRecorded`), still present (`WriterPresent`), holding
no live log of this session (`!WriterZone`). `WriterZone` is keyed on a sibling
`<session>.log.jsonl`, the one artifact the seal never creates — it makes only a
`.lock`, so a directory-existence probe would manufacture its own evidence and
silence the very loss it guards on the *first* seal, since the seal runs before
the vacuum in one invocation. A recorded writer that cannot produce the file is a
deletion and warns; a bound mission that sealed nothing warns; a legacy pre-field
roster (empty `Checkout`) suppresses — the bounded residual, which ages out as
sessions restart. The guard is stronger than before its break: a checkout that
deleted the writer is now distinguishable from one that never wrote there, and
the deletion is visible from any checkout, not only the writer's.

**Maintaining this guard.** The predicate took four revisions and every hole was
the same shape — a locally-plausible suppression that silently dropped a real
loss — and the unit test table stayed green through the two worst. What caught
them was mutation testing at two levels: mutate every term of `Lost()` and every
`RecordedWriter`/`AssumedWriter` call site, and require a distinct named test to
die for each. A wiring mutant — a recorded writer mis-wired as a fallback —
restored the whole bug with a green suite until an integration test that plants
*no* sibling live log under the recorded checkout made the `WriterRecorded` term
load-bearing. Keep that discipline for any change here: a green suite is not
evidence this predicate is sound.

### Invariant changes

`I10-audit-atomic` amended: appends target the live session log under the
session flock, which also allocates the strictly-monotonic per-session
`ts` (`> max sealed chunk ts`); sealed chunks are written only, whole, by
the seal step. New `I11-chunk` (each chunk written once via temp+rename,
never modified; chunks hold disjoint contiguous ts ranges *within one
tree state* — a branch rewind can overlap, resolved at read), `I11-idem`
(each complete live line sealed into at least one chunk after a following
seal; duplicate copies share `(session, ts)` and are byte-identical),
`I12-merge` (read = union of sealed chunks and live tail past the sealed
watermark, ordered by ts (a **stable** sort, so legacy equal-ts lines keep
file order); post-discipline lines dedup on `(session, ts)`,
frozen legacy lines pass through undeduped since nothing duplicates a legacy
line and a legacy dedup could only drop a distinct event, and a corrupt
monotonic chunk surfaces as an error, not a drop).

### Migration

Existing tracked `audit.jsonl`/`log.jsonl` are frozen historical chunks
and stay — each reads as its session's oldest chunk. Only the write path
moves to `.punt-labs/local/` (the mission live log to the per-session
`missions/<id>/<session-id>.log.jsonl`; a pre-upgrade per-checkout
`missions/<id>.jsonl` from the superseded shared-live design is **not** like
the frozen legacy audit file — that design's seal copied its lines into chunks,
so it is drained **once**, filtered **per line** — a line is kept iff its `ts`
exceeds the max `<last>` of the chunks carrying that line's own session id (a
session with no chunks keeps everything; a line with no readable attribution is
kept), since "sealed" is per-session and a cross-session threshold would
silently drop a lagging session's never-sealed lines — contributing only the
never-sealed residue as pre-discipline legacy, ordered after the frozen tracked
`log.jsonl`; the per-line filter consults existing attribution and never
re-attributes to a session). New verb
`ethos audit seal [--dry-run]`
(idempotent, atomic per session via temp+rename, partial-failure resume;
visits every session dir under both the live zone and the tracked sealed
tree so orphan chunks are staged even when a session has no pending live
lines; does not delete or truncate the live file, which is discardable
**only once its lines are sealed** — an unsealed live file is the sole copy
of those tool calls). Companion verb `ethos audit quarantine <chunk>` retires
a corrupt sealed chunk — re-seals still-readable lines from the live file,
renames the chunk to `.corrupt`, writes a tracked `.quarantine` marker
holding the watermark at the verified loss point, and self-stages both
(`git mv` + `git add`) — the sanctioned alternative to `--no-verify`.
No live-file seeding — chunks are additive, so a continuing session starts its
watermark at the max ts of its committed chunks and writes new lines strictly
after. Lock relocation untracks **and disk-removes** the tracked
`.create.lock`/per-mission `.lock` and stops writing in-repo locks.

### Transition

Two minor versions (DES-054 convention). vX.Y.0: live-write redirect to
`.punt-labs/local/` + strictly-monotonic ts + pre-commit chunk-seal hook
(via `install.sh`) + mission-close seal + `audit seal`. vX.(Y+1).0:
relocate `.lock`/`.create.lock` to the global tree, untrack and disk-remove
the repo copies. One ethos binary per machine, so the write path flips on
upgrade. vX.Y.0 reaches a consuming repo only **after** it carries the
canonical `.punt-labs/local/` gitignore block (`punt-4yy`) — the live-zone
sequencing gate mirroring the `e29s` gate for chunks; without it the live file
lands untracked-and-unignored and dirties the repo, the disease relocated.
Three window properties: (1) a session alive across the flip has
pre-upgrade lines in the frozen chunk and writes new lines to a fresh live
file — no seeding, the writer initializes its monotonic ts from the
chunks' max ts so new lines sort after; (2) the file dirty today is flushed by
a **one-time operator reconciliation commit** of its final state (no verb
rewrites a frozen file), then clean; (3) machines
sharing a repo should cross the boundary together — no data loss and no
conflict (new chunks, not appends), but the un-upgraded machine keeps
dirtying its tree and its appends land in the frozen `audit.jsonl` as legacy
lines, which the upgraded machine passes through **undeduped** (its
pre-upgrade clock can still collide two distinct events on one ts, and
nothing duplicates a legacy line, so a legacy dedup could only drop a real
line).

### Rejected alternatives

Home-dir global live tree (operator ruling for `.punt-labs/local/`);
gitignored in-repo sibling needing a per-repo rollout (the `local` ignore
ships once via the standard); `TMPDIR`/`.tmp/` (deletable scratch);
per-branch live path (sessions span branches); append-to-one-tracked-file
sealing with a `merge=ethos-seal` driver, `.gitattributes`, byte-prefix
invariant, divergence detection, and `audit reseal` (operator ruling —
replaced by immutable chunks, structurally conflict-free); per-session
`seq` field (operator ruling — replaced by strictly-monotonic ts);
committing-session-only and committing-session-plus-orphan-sweep seals
(operator ruling — repo-wide seal, orphans land at the next commit, no
liveness probe); checkout-path roster scoping (filesystem separates
per-checkout `local` files); live-file seeding on upgrade (chunks are
additive); suppressing re-seal on a rewound branch via a `local`
high-water mark (would lose the record on the branch that merges — the
read tolerates the overlap instead); `size(sealed)` blind offset and
verified prefix anchor (no prefix in an immutable-chunk model); seal at
commit-msg (too late for the index); seal at session end as primary
(sessions outlive PRs); raw live lines redacted at seal (second failure
site, leaks at rest); keep the lock in-repo and gitignore it (a tree lock
invites re-tracking); byte-equality dedup of the legacy pool (nothing
duplicates a legacy line, so every collapse could only drop a distinct
event — the legacy pool passes through undeduped); home-tree spool for the
gitlink teardown flush (a write-only subsystem nothing drained, read, dated,
keyed, or flagged, with an unsignaled copy failure — the gitlink teardown
now defers like every other seal and the deleted-checkout tail loss is
accepted as the bounded pre-`e29s` limitation); a durable home-tree deferral
record to make the teardown notice loud (spool-lite creep — the same
write-only subsystem under another name; the teardown deferral is accepted as
unannounced and post-hoc undetectable).

## DES-059: `ethos enable` / `disable` — mapping the tool-enable-disable standard (SETTLED)

**Status**: Settled. Design ratified 2026-07-22 (bead `ethos-ik4j`, missions
`m-2026-07-22-001` design, `-005` document). Conforms to punt-kit
`standards/tool-enable-disable.md` (§2.1–2.13) and `standards/punt-labs-dir.md`
(§7 zones, §9 migration). Full design and the operator-rulings appendix:
`docs/enable-disable.md`.

### Problem

The org standard requires every repo-scoped CLI to ship `enable` / `disable`:
one `@`-import line in the user's `CLAUDE.md`, one tool-owned
`.punt-labs/<tool>/` directory, an `enabled` marker, and repo-scoped hook
registration. Ethos had none of these. Instead `install.sh` (machine scope)
reached into a specific repo to chain the DES-058 seal and DES-054 trailer git
hooks — the standard's exact anti-pattern (§2.13) — with no marker, no import
line, and no vendored guide. `doctor` FAILed every repo lacking the seal hook,
including never-enabled ones, contradicting §2.7's three-state model.

The ethos-specific hazard: `.punt-labs/ethos/` already holds repo-owned content
(identities, teams, roles, agents, missions, sealed audit chunks). The
standard's wholesale-overwrite ownership model (§2.2) would destroy it.

### Decision

`enable` / `disable` are thin cobra verbs over three packages: `internal/claudemd`
(the §2.4 import-line writer — exclusive lock, atomic temp+rename,
byte-preserving host-EOL append, terminator-insensitive match, code-block
exclusion, symlink/mode preserving — porting vox `GlobalClaudeImports`'s
correctness into Go), `internal/githook` (a pure chainer porting `install.sh`'s
`install_hook` logic with all v4.1.1 protections and sharing the hooks-dir
resolver with `doctor`), and `internal/enable` (orchestration). The git-hook
scripts are embedded by a separate `hooks` package so one shellcheck-linted
copy serves both the shell test suite and the Go chainer.

The `.punt-labs/ethos/` subtree is carved into punt-labs-dir §7 zones: the
Vendored zone is a **two-file allowlist** (`CLAUDE.md` + `.vendored-manifest`),
and the §7 manifest collision rule makes an overwrite of any identity, team,
role, or sealed-chunk path a hard error that deposits nothing. The marker is
written **last**, after the deposit completes, so a marker present implies a
complete deposit. Both embedded hooks gate on `.punt-labs/ethos/enabled`, so a
dormant repo's hook is inert; `doctor` keys its seal check on the marker with
four states (PASS unenabled, FAIL enabled-but-missing, WARN gated-but-unenabled).
`disable` is non-destructive and strands (does not seal) pending live lines,
which seal on a later re-enable via the seal's live-tail union; it refuses when
a sibling worktree is still enabled unless `--force` is given. `install.sh`
becomes machine-scope only and delegates per-repo enablement to `ethos enable`.
Migration to interim v4.1.1 repos is by-hand at rollout, not a SessionStart auto
step. `enable` and `setup` stay fully separate.

### Rejected alternatives

- **`install.sh`-owned per-repo enablement** (the v4.1.1 arrangement): conflates
  machine `install` with repo `enable` (§2.13), and cannot write a marker or
  import line without duplicating the enable path in shell.
- **Managed `CLAUDE.md` sections** (`<!-- ethos:begin -->` … `end`): §2.1 forbids
  tooling from owning any bytes inside the user's `CLAUDE.md` beyond one
  `@`-import line; composition happens at read time.
- **Hooks-only `enable` verb** (an early bead): skips the guide, marker, and
  import line — fails §2.3 and §2.7; without the marker there is no three-state
  model.
- **Dual shell/Go hook-chaining implementations** (keep `install.sh`'s copy
  alongside the Go port): two copies drift — the v4.1.1 `ethos-2ol1` seal-chain
  bug lived in the shell copy. One Go source of truth, shell functions deleted.
- **SessionStart auto-migration** of interim repos: operator rejected in favor
  of explicit by-hand convergence; doctor's WARN and `install.sh` delegation
  are the backstops. Keeps migration a reviewable operator action rather than a
  silent hook side effect.
- **`disable` seals before stripping**: operator overruled; `disable` means the
  user is done — strand-not-seal, with recovery on re-enable, avoids an
  off-switch that runs work the user did not ask for.

## DES-060: Setup/seed consistency — resolvable identities on a fresh machine (SETTLED)

**Status**: Settled. Bead `ethos-5zwn`. Full design in
`docs/setup-consistency.md`.

### Problem

On a fresh machine the bundles onboarding path (`ethos seed` then `ethos
setup --bundle …`) produced identities whose attributes resolved to
nothing. Two subsystems disagreed about the global layer. `ethos setup`
wrote a `claude` agent and a human identity referencing
`principal-engineer`, `concise-quantified`, and `engineering`; `ethos
seed` deployed none of those to the global layer (it embedded only the
READMEs of the personalities/writing-styles dirs). And attribute
resolution for a globally-stored identity consulted the global layer only,
so even an attribute a bundle carried never resolved for a global
identity. It worked on existing machines solely because the live repo
carried the three files in its `.punt-labs/ethos/` submodule (the repo
layer).

### Decision

Fix the contradiction as belt-and-suspenders, both parts shipped together:

- **Uniform DES-051 chain.** Attribute content resolves through repo →
  active bundle → global for *all* identities, including globally-stored
  ones. The per-source-layer chain that skipped the bundle for global
  identities was a conformance bug against DES-051, not a design choice;
  the fix collapses it to one chain over whichever layers are present. The
  bundle-blind set was exactly {personality, writing_style, talents};
  identity lookup, the attribute store, role, and team already threaded the
  bundle.
- **Seed the conventional attributes.** `ethos seed` deploys personalities
  and writing-styles to the global layer, vendoring the three
  setup-referenced slugs from the team registry, so a fresh machine
  resolves even with no bundle active.
- **Hard validation.** `ethos setup` validates the identities it writes and
  fails before `ethos seed` has run, with an actionable error, rather than
  writing a dangling reference.

### Blast radius (decided behavior)

The uniform chain makes a global identity's resolved persona *content*
bundle-dependent: `claude`'s `engineering` talent reads foundation's text
in a foundation repo, gstack's in a gstack repo, and the global-seed copy
only with no bundle active. This is precisely the property the bug's
symptom raised as questionable, and it is ratified: global-identity content
follows the active bundle by design, and the global seed is load-bearing
only on the no-bundle path (a bundle shadows the seed copy whenever it
ships the same slug). See `docs/setup-consistency.md` for the full analysis.

### Gate rulings

- **Never overwrite divergent team config on a guess.** When a repo's
  `team` key deliberately differs from the bundle, `setup` warns and leaves
  it — it does not clobber a value the user chose.
- **Convergence is an explicit command.** `ethos team activate` on an
  already-active bundle is the reviewable step that repairs a `team` value
  which has drifted from the active bundle; convergence never happens as a
  silent side effect.

### Rejected alternatives

- **`setup` writes identities to the repo layer** instead of global:
  `claude` and the human are user-scoped identities shared across every
  repo; repo-layer storage duplicates them per-repo and breaks the
  single-identity model. DES-051 writes target global by contract.
- **Bundle-only resolution without the global seed** (the uniform chain
  alone): fails the no-bundle path — `ethos setup --solo` and any repo with
  no active bundle would leave `claude`/human unresolved.
- **SessionStart-style auto-repair** of dangling references: rejected for
  the same reason DES-059 rejected SessionStart auto-migration — repair is
  an explicit, reviewable operator action (`ethos seed`, `ethos team
  activate`), not a silent hook side effect.

## DES-061: Harness-neutral sessions — CLI-first session start/end (SETTLED)

**Status**: Settled. Bead `ethos-leh7`. Full design in
`docs/harness-sessions.md`.

### Problem

`ethos iam` and the session flow worked only inside Claude Code. The only
thing that created a session roster in normal use was the SessionStart
hook, which fires only inside Claude Code and takes its session ID from a
Claude-supplied stdin payload. `iam` could join a roster but never create
one, so from Codex or a plain terminal it failed with "no session found"
and no path forward. Session discovery keyed on a process-tree walk for a
command named exactly `claude`, which matched no other harness. The org
intent is CLI-first: ethos must work for any harness. `whoami` was the one
path that already worked standalone, because it falls through to git config
and `$USER` — the model the rest of the design copies.

### Decision

A CLI-first primitive creates a session without a hook and without a Claude
process, and one resolution chain serves every consumer.

- **`ethos session start` / `end`.** `start` mints an opaque session ID and
  prints an eval-able `export ETHOS_SESSION=<id>` line;
  `eval "$(ethos session start)"` sets it in the calling shell. `--persona`
  additionally exports `ETHOS_AGENT_ID` and folds the first `iam`. Start is
  idempotent under a live `ETHOS_SESSION` (reported, re-attached, not
  re-minted). `end` is the teardown. All three entry points — the hook,
  `start`, and the hidden `session create` plumbing — bottom out in one
  `session.Store.Create`; the store schema is unchanged.
- **One resolution chain (R2).** `--session` flag > `ETHOS_SESSION` env >
  the Claude process-tree walk (kept for zero-config Claude Code) > an
  actionable error naming `ethos session start`. No silent global fallback.
  An explicit ID bypasses the walk entirely (R3).
- **State-writer-scoped verification.** Consumers that write to a session
  (`iam`, `mission claim`/`release`) verify that an env-sourced ID names a
  real roster, so a stale `ETHOS_SESSION` cannot stage a phantom binding.
  Teardown and best-effort readers do not hard-verify: "already gone" is
  success for `end` (`rm -f` semantics), and mission log/append outside a
  session correctly falls back to the legacy tracked-log path.
- **MCP parity (R4).** The MCP server honors the same chain and keys the
  agent on `ETHOS_AGENT_ID` then the Claude PID — so a session declared on
  the CLI and one seen over MCP agree.

### Rulings

Six ratified rulings (R1-R6) plus four gate decisions surfaced during
implementation:

- **R1** — `session start`/`end` as the one-call primitive with opaque IDs
  and eval-able exports; idempotent under a live env.
- **R2** — the explicit resolution chain above, no silent fallback.
- **R3** — the walker stays `claude`-only for now; bypassed when an explicit
  ID is present.
- **R4** — MCP honors the same chain and `ETHOS_AGENT_ID` parity.
- **R5** — a non-PID primary ages out by the existing 24h TTL; no new
  liveness machinery this round, documented as decided.
- **R6** — the Claude Code hooks keep creating sessions exactly as today;
  the PID current-pointer is Claude-path only, and `start` writes no pointer
  outside Claude (discovery there is `ETHOS_SESSION`).
- **`whoami` in scope.** `whoami` joins the chain: under any harness, `iam`
  then `whoami` reflects the declared persona, falling back to git/OS only
  when no session resolves — persona reflection is the point of `iam`.
- **crypto/rand over UUID.** The ID is 16 bytes of `crypto/rand`
  hex-encoded to 32 characters — stdlib, already imported, filesystem-safe,
  fixed-length. No new dependency.
- **State-writer-scoped hard verification.** Env verification applies to
  state-writers, not to teardown or best-effort reads (see Decision).
- **Corrupt-roster refusal parity.** Both `start` and `end` refuse a roster
  that exists but cannot be parsed — a crash artifact likely holding
  unsealed audit lines — with a remedy (`ethos session purge` /
  `ethos audit quarantine`) rather than minting over or deleting it.
- **Fix the code for `--persona`.** A re-run with `--persona` reads the
  parent from the existing roster rather than re-resolving it, so a prior
  `ETHOS_AGENT_ID=<persona>` export cannot make the primary self-parent and
  break post-compaction primary-agent discovery.

### Rejected alternatives

- **Generalize the walker to match `codex`/`cursor`/`node` now.** Speculative
  (R3): each harness names its process differently and some share a generic
  name that would false-match. The `ETHOS_SESSION` path serves every
  non-Claude harness with no guessing; adding a specific name later is a
  one-line follow-up.
- **Auto-start a session on `iam` when none exists.** Ambient magic: an
  `iam` typo would silently spawn a session, sessions would accumulate
  invisibly, and the audit trail would gain sessions no one opened. The user
  opens a session deliberately with `session start`; `iam` only joins.
- **UUIDv4 via `github.com/google/uuid`.** Rejected on supply-chain grounds:
  it is only an indirect dependency today, and `crypto/rand` gives a
  collision-resistant, filesystem-safe ID with nothing new to vendor. The
  ID is opaque (nothing depends on its shape), so UUID's only advantage —
  visual similarity to Claude Code's `session_id` — bought nothing.

## DES-062: Worktree-aware store resolution — store root vs checkout root (SETTLED)

**Status**: Settled. Bead `ethos-yofr`. Shipped v4.4.0 (PR #370).

### Problem

Git worktrees are the org-standard agent-isolation mechanism, and ethos
resolved its repo layer from the current directory. Inside a linked
worktree, `.git` is a file (not a directory), so the cwd-walk that located
`.punt-labs/ethos/` stopped at the worktree root — whose store is empty or
absent, because the `.punt-labs/ethos/` submodule is checked out only in the
main work tree — and every store silently fell back to the global store at
`~/.punt-labs/ethos/`. The reported symptoms (missions created in a worktree
invisible to the main checkout and vice versa; `mission create` warning about
talents that exist in the repo store) were the surface. The load-bearing bug
was in Tier B delegation dispatch: the CLI and the dispatch hook resolved the
store differently, so a leader working in a worktree created a mission the CLI
could see but the dispatch hook could not find, and the spawn was refused.
Silent global fallback made all of it invisible until a worker concluded its
mission "was never created."

### Decision

There are two distinct roots, and conflating them is the bug. Every consumer
resolves each operation against the correct one.

- **Store root (`resolve.StoreRepoRoot`).** Follows
  `git rev-parse --git-common-dir` to the repo that *owns* the shared store —
  identities, missions, bundles, teams, and the `.punt-labs/ethos.yaml`
  config. In a linked worktree this resolves to the main work tree (the only
  checkout where the submodule is populated), with a manual gitdir/commondir
  fallback when `git` is absent (the same idiom `doctor` and the git-hook
  resolver already use).
- **Checkout root (`resolve.FindRepoRoot` / `EnvRepoRoot`).** Stays
  worktree-local for genuine per-checkout state: the enable/disable marker,
  the DES-058 audit live-zone and its pre-commit seal, generated
  `.claude/agents/` files, the write-set verifier walk, and any git-index
  mutation (which must land in the committing checkout).
- **A consumer that does both a store op and a per-checkout op carries both
  roots** — the field is split, not reclassified. The mission `Store` routes
  records to the store root and its audit zone to the checkout root;
  `session start` reads config from the store and writes agent files to the
  checkout; `doctor` reads team/agent config from the store while globbing
  `.claude/agents` and checking the seal hook on the checkout; the traceability
  UI reads missions/delegations from the store and audit/file-browse from the
  checkout; SubagentStart closes the delegation skeleton on the store while
  walking the write-set on the checkout.
- **`ETHOS_REPO_ROOT` override.** Forces the store root when auto-resolution
  is wrong or absent, reaching every store call site. It is validated: a
  nonexistent path, or an existing path with no `.punt-labs/ethos`, is refused
  loudly and never returned.
- **Loud, never silent.** A genuine fallback to the global store warns,
  naming the root. A *refused* override hard-errors on every store **write**
  (`setup`, `mission create`/`dispatch`/`claim`) rather than writing to the
  wrong tree; a genuine no-repo keeps the legitimate global mode. Reads warn
  and proceed.

### Rulings

- **Bundle and team config are shared-store.** `active_bundle` and `team` in
  `.punt-labs/ethos.yaml` are read *and* written via the store root, and
  bundle content installs (as submodules) under the main store where every
  resolver looks. Writer and every selector-reader agree, so activation from
  a worktree does not silently no-op. The consequence — a worktree uses the
  main checkout's active bundle, not its own — is consistent with the store
  itself living in main, and is accepted, not a defect.
- **The DES-058 audit live-zone stays per-checkout.** Live-append, close-time
  seal, and the pre-commit `audit seal` all resolve the checkout root, so a
  mission's events land where the seal that commits them runs; only the
  contract/results/reflections record resolves the store root.
- **`enable`'s deposit stays per-checkout.** The vendored guide, `enabled`
  marker, `@`-import line, and chained hooks are one cohesive unit: Claude
  Code resolves the `@.punt-labs/ethos/CLAUDE.md` import relative to the
  checkout's `CLAUDE.md`, so the guide cannot move to the main store without
  breaking persona injection in the worktree. Only `enable`'s "has setup run?"
  config *read* resolves the store root.
- **Override refusal is uniform across writes.** `setup` and the three mission
  write entry points share one `RepoRootOverride()` distinguisher: override
  set but store-root empty ⇒ refused ⇒ hard error; override unset ⇒ genuine
  no-repo ⇒ warn and use global.

### Rejected alternatives

- **Change `FindRepoRoot` wholesale to follow the common dir.** Reintroduces
  the bug from the other side: the enable/disable marker and the audit
  live-zone are legitimately per-checkout, and a worktree must be able to seal
  its own audit trail. Splitting the roots preserves both.
- **Resolve everything, including per-checkout state, to the store root.**
  Breaks the per-checkout seal path (events land in main but the worktree's
  pre-commit seal never sees them), the enable marker, agent-file generation,
  and git-index mutations — each surfaced as a separate defect during review.
- **Per-worktree bundles (all bundle ops on the checkout root).** A bundle
  added in the main checkout is a submodule populated only there; a fresh
  worktree would resolve an empty bundle directory — yofr reintroduced for
  bundle content. The shared-store model is the only one where bundle content
  is resolvable from every checkout.
- **Process-global warning dedup (`sync.Once`).** Tempting for the cosmetic
  2–4× repeat of a refusal warning per command, but in the long-lived
  `ethos serve` MCP process a process-scoped guard silences every later
  command's warning for the life of the process — under-warning, the exact
  failure direction this work eliminates. The double-print is beaded
  (`ethos-3xxx`) for a resolve-once-per-command fix instead.
- **Silent global fallback (status quo).** The reported bug: a wrong-store
  read or write that gives no signal is indistinguishable from success until a
  downstream operation fails inexplicably.

## DES-063: CLI-only install — `install.sh --no-plugin` (SETTLED)

**Status**: Settled. Operator-ratified 2026-07-25. Full design in
`docs/install-cli-only.md`. Graduated to the punt-kit `install-cli-only`
standard (punt-kit#236).

### Problem

`install.sh` does two jobs in one run: it installs the **ethos CLI** (binary,
`~/.local/bin` on PATH, identity dir, seed, per-repo `enable`, `doctor`) and it
registers the **Claude Code plugin** (marketplace add/update, plugin install).
The CLI is harness-neutral; the plugin is Claude-Code-only. Two audiences want
the first job without the second: **(a) non-Claude harnesses** (Codex, a plain
terminal) that have no plugin surface, and **(b) enterprise Claude users** whose
org policy blocks plugin/marketplace installation but who use ethos via the CLI.
The installer already auto-skips the plugin when `claude` or `git` is absent
(audience (a) with no `claude` on PATH), but there was no way to say "install the
CLI, skip the plugin, on purpose" for audience (b) — `claude` is present, so the
auto-skip never fires and the installer proceeds to `claude plugin install` and
fails on the policy block. And a `curl … | sh` install parses no arguments, so
there was nowhere to put a flag.

### Decision

An explicit, operator-driven plugin skip, scoped to the plugin/marketplace steps
only; everything else (binary, PATH, dirs, seed, `enable`, `doctor`) runs
unchanged.

- **`--no-plugin` flag.** The GNU/POSIX `--no-<feature>` idiom for a default-on
  installer feature. It names the action (skip the plugin), not the audience
  (`--cli-only` would overclaim — hooks, dirs, PATH, and `enable` still run). A
  POSIX arg-parse loop runs before any work; `-h`/`--help` prints usage; an
  unknown option is a usage error (exit 2) — a piped installer must not silently
  ignore a misspelled `--no-plguin` and install the plugin the user asked to skip.
- **`ETHOS_NO_PLUGIN=1` env var.** For argument-hostile contexts (CI, proxies,
  config systems). Honored equally with the flag; skips only when the value is
  exactly `1`, matching the internal `0/1` convention — no truthy-string guessing.
- **Single OR resolution.** `SKIP_PLUGIN = 1` if `--no-plugin` present OR
  `ETHOS_NO_PLUGIN=1` OR `claude` absent OR `git` absent. The flag and env fold
  into the variable Step 1 already owns; the existing capability-absence auto-skip
  is preserved. There is deliberately **no** counter-flag to force the plugin on —
  you cannot install a plugin without `claude`.
- **Message gated on the skip, not the cause.** When `SKIP_PLUGIN=1` for any
  reason, the installer prints a CLI-only success block (the CLI works via
  CLI/MCP/filesystem; next steps `ethos setup` and
  `eval "$(ethos session start --persona <handle>)"` per DES-061; re-run without
  `--no-plugin` to add the plugin later) and never the "Restart Claude Code to
  activate the plugin" line. This also fixes the pre-existing bug where the
  capability-absent auto-skip still printed the plugin-activation text.
- **`enable`/`setup` unchanged.** The flag lives solely on `install.sh`. `enable`
  still deposits the guide + `@`-import and chains the DES-058/DES-054 hooks (the
  audit backbone is harness-neutral and must run in CLI-only mode); `setup` still
  writes config and generates `.claude/agents/` (inert for non-Claude, needed for
  enterprise Claude). Neither installs the plugin, so neither gains a parallel
  flag.

### Rulings

- **Require the explicit flag; do not auto-detect the policy block.** A
  `claude plugin` error is indistinguishable from a transient/network/auth
  failure; auto-skipping on it would mask real failures — the DES-059
  silent-absence anti-pattern. Capability-absence (`command -v`) stays the only
  auto-skip signal; a policy block requires the explicit flag/env.
- **Graduates to a punt-kit standard.** The canonical `--no-plugin` flag,
  `<TOOL>_NO_PLUGIN` env, `sh -s -- --no-plugin` invocation, skip semantics,
  success messaging, and a conformance checklist are filed as the punt-kit
  `install-cli-only` standard so every punt tool's installer behaves identically;
  ethos `install.sh` is the reference implementation.
- **Codex `@`-import targeting is out of scope.** For a pure non-Claude harness,
  `enable`'s `@`-import targets `CLAUDE.md`, which Codex ignores (it reads
  `AGENTS.md`). Harness-aware `enable` is a separate concern beaded independently;
  `--no-plugin` fully serves audience (b) and installs a working CLI for audience
  (a).

### Rejected alternatives

- **A separate `install-cli.sh`.** Two scripts sharing ~90% of their logic drift
  (the DES-059 "two copies drift" lesson). One script with a boolean flag has one
  code path to test.
- **Post-install `plugin remove`.** Requires `claude plugin install` to succeed
  first — exactly what fails for the enterprise-blocked audience — and leaves the
  marketplace registered.
- **CLI-only by default.** Breaks the happy path for the Claude-plugin majority;
  opting out is the minority action, so it takes the flag.
- **`--cli-only` as the name.** Overclaims; hooks/dirs/PATH/`enable` still run.
- **A truthy env parser** (`true`/`yes`/non-empty). Locale-dependent and
  inconsistent with the installer's `0/1` convention; one accepted value (`1`).

## DES-064: Default team shape — human = CEO, claude = COO (SETTLED)

**Status**: Settled. Operator-ratified 2026-07-25. Applies to every seeded
bundle (`foundation`, `gstack`), the `sprint` seed team, `ethos setup`, and
`ethos team activate`.

### Problem

Out of the box, every seeded team made the **architect** the apex: `foundation`
and `gstack` both wired every specialist (implementer, reviewer, qa,
security-reviewer, and — in gstack — product-lead) to `reports_to: architect`
(product-lead via `collaborates_with`), and the global `sprint` team did the
same. There was no leadership layer. A real org is a human owner directing an
agent that runs execution; the architect is a specialist role, not the org head.
New users hit this immediately — activating `gstack` produced an org where they
had to hand-edit `.punt-labs/ethos/teams/` to put themselves (CEO) and `claude`
(COO) on top and re-point every collaboration edge. The default modeled the
wrong mental model (Norman: the system's structure should match the user's).

### Decision

The default org shape is **human = CEO (apex), `claude` = COO (operational
lead), and every specialist reports to the COO.** The architect becomes a peer
specialist under the COO, not the apex.

- **New `ceo` and `coo` leadership roles** in the seed and in each bundle. `ceo`
  is the accountable owner who sets direction; `coo` runs execution and is who
  specialists report to. Unlike specialist roles, leadership roles carry **no
  `output_format`** — they direct and delegate, they do not emit FINDINGS — and
  the `sidecar` role-set guard asserts that exemption so an accidental addition
  is caught.
- **Every bundle/seed team graph rewired**: each specialist `reports_to: coo`,
  `coo reports_to: ceo`, and no `to: architect` edges remain anywhere in seed.
- **`ethos setup` binds the seats to real identities**: the human handle to the
  `ceo` seat, `claude` to the `coo` seat, written as a repo-local team (repo
  layer wins over the bundle layer). Bundles ship placeholder `<bundle>-ceo` /
  `<bundle>-coo` seat identities so they validate and resolve standalone; setup
  rebinds them. Leadership validation (an incomplete ceo/coo pair; a human handle
  colliding with the reserved `coo` seat, e.g. `handle: claude`) runs **before**
  any identity or config write, so a bad configuration fails clean with nothing
  persisted.
- **`ethos team activate` applies the same default when run by a human**: it
  binds the `ceo` seat to the resolved caller only when the caller's `kind` is
  `human`. When run by an **agent** (the common case — an agent that has run
  `ethos iam claude`), it **skips the rebind with a loud warning** and still
  activates (exit 0), leaving the human's `ethos setup` leadership intact — the
  agent is not the CEO, and an agent-driven activate cannot determine the human.
  The rebind decision runs before the `team`/`active_bundle` config write, so a
  skip or validation failure never leaves a persisted-switch-reported-as-failure
  partial state.

### Rulings

- **Applies to all surfaces, out of box** (operator ruling): every bundle, the
  sprint seed team, `ethos setup`, and `ethos team activate` — not just the
  primary setup path.
- **Non-breaking.** Only *new* setups/activations get the new default; existing
  repo-local teams are not rewritten. So this is a minor, not a breaking, change.
- **Coupled `doctor` fix.** Writing a repo-local team makes a repo that carries
  a team but no repo-local `identities/` legitimate. `CheckIdentityDir` now falls
  back to the global identities store, but **only when a repo-local team exists** —
  a repo missing its identities with no repo-local team (e.g. an uninitialized
  submodule checkout) still FAILs loudly, so the fallback never masks a genuinely
  broken checkout.

### Rejected alternatives

- **Keep architect as the apex.** Models the wrong org: an architect is a
  specialist, not the owner/operator. Forced every new user to hand-edit.
- **Label the architect "COO".** Same structural problem with a misleading name;
  the human owner still has no seat.
- **Rebind leadership on `team activate` for an agent caller too** (bind the
  agent as CEO). Wrong — the agent is the COO; the CEO is the human. Skipping
  with a warning and preserving the human's `ethos setup` leadership is correct.
- **CEO/COO as a special apex mechanism** rather than roles. Roles compose with
  the existing team/collaboration model and validation; a bespoke mechanism would
  not.

## DES-065: Manifest-aware seed — propagate shipped content without clobbering edits (SETTLED)

**Status**: Settled. Operator-ratified 2026-07-26. Ships v4.7.0. Full design in
`docs/seed-content-upgrade.md`.

### Problem

`ethos seed` deploys embedded library content (roles, talents, personalities,
writing-styles, archetypes, pipelines, bundles, skills, READMEs — never user
identities, which `ethos setup` owns). Its no-clobber guard skips any existing
non-empty file (`internal/seed/seed.go:221-224`; printed at
`cmd/ethos/seed.go:50-52`), so a released improvement to a shipped file never
reaches a returning user — a live 4.6.0 re-install skipped 111 existing files.
The only override, `--force` (`internal/seed/seed.go:170-176`), overwrites
everything blindly and cannot tell an unmodified shipped file from a hand-edited
one, so it is unsafe as the upgrade path. The installer runs plain `ethos seed`
(`install.sh:321`), upgrading nothing.

### Decision

Make plain seed **upgrade tracked shipped files and preserve proven user edits**,
decided by content hash. New behavior activates only for a file the manifest
already tracks; an untracked file keeps today's no-clobber skip.

- **Current shipped hash (`cur`)** is computed at runtime from the embedded bytes
  — no build step, no drift.
- **A local install manifest** (`~/.punt-labs/ethos/.seed-manifest.json`) records,
  per seeded path, the hash seed last wrote (`mf`) plus provenance — one hash per
  path, overwritten each write, so nothing grows.
- **Decision:** absent → deploy + record. `local == cur` → unchanged (record if
  untracked, to adopt). Tracked and `local == mf` and `cur != mf` → upgrade +
  record. Tracked and `local != mf` → skip + warn (user edit; `--force` remedy).
  **Untracked and `local != cur` → today's no-clobber skip, unchanged.** Zero-byte
  partials are repaired to `cur` regardless.
- **No pre-feature migration in the software.** The few existing machines are
  hand-cleaned once with `ethos seed --force`, which overwrites to `cur` and
  records every entry; thereafter they auto-upgrade.
- **`--force`** overwrites every path to `cur` and records all entries — the
  escape hatch and the one-time hand-clean tool.
- **Installer unchanged except messaging** — still plain `ethos seed`, now
  upgrade-capable; no `--force`.
- **Output** gains `deployed` / `updated` / `unchanged` / `skipped (exists)` /
  `skipped (local edit)` lines and a `--force` remedy hint (CLI standard; seed has
  no MCP surface, so no DES-020 formatter is added). `Result.Skipped` keeps its
  meaning; a new `Edited` field carries the tracked-edit case, so no output
  consumer breaks.

### Rulings

- **No pre-feature migration** (operator). Few machines; a migration branch is
  clutter and debt. The software keeps the existing no-clobber skip for untracked
  files; the affected computers are hand-cleaned with a one-time `--force`.
- **Content hash, not mtime or in-band version stamps.** mtimes are unreliable
  across clone/tar/package managers; stamps pollute content and are forgeable by
  an edit that keeps the stamp.
- **The manifest carries one hash per path.** No per-release history, no unbounded
  growth.
- **Scope: library content only.** The machinery never reads, writes, or hashes
  `~/.punt-labs/ethos/identities/` — `ethos setup` owns identities.
- **Skip is safe and recoverable.** A skipped file is preserved, not lost;
  `--force` is the documented remedy.

### Rejected alternatives

- **A bootstrap-upgrade branch for untracked files.** Operator ruled no
  pre-feature migration — few users, hand-clean the affected machines. The
  no-clobber skip stays for untracked files.
- **Legacy hash catalog + history-walking generator.** Fragile generation (the
  sidecar layout has moved across releases; risks silent under-population),
  unbounded generation risk, guards the ruled-out pre-feature case.
- **`.seed-backup/` safety net.** Softens a mis-upgrade of a pre-feature file —
  the ruled-out case; adds state for no benefit.
- **Installer `--force`** (clobbers edits and re-clobbers hand-cleaned machines
  every re-install).
- **mtime comparison** (unreliable, content-blind).
- **Per-file version stamps** (pollute content, forgeable).
- **Interactive per-file prompt** (hangs the piped installer).
- **Three-way merge** (unwarranted for content nobody hand-edits).
- **A separate `ethos seed --upgrade` command** (splits the mental model; plain
  seed should do the right thing by default).

## DES-066: Entity schema command — reflect-plus-overlay registry (SETTLED)

**Status**: Settled. Operator-ratified 2026-07-26. Full design in
`docs/entity-schema-command.md`.

### Problem

The shape of each typed entity (identity, role, team) is described in several
hand-maintained places that already disagree: the Go structs
(`internal/identity/identity.go:13`, `internal/role/role.go:28`,
`internal/team/team.go:30`), the MCP `inputSchema` blocks
(`internal/mcp/tools.go:156`, `role_tools.go:12`, `team_tools.go:13`), the
`validate-content` checks, the inline enums, and the seeded per-category READMEs
(only `identities/README.md` has a fields table; roles have none; teams have no
directory). The gap is worse than a stale table: the role MCP `create` handler
reads three of seven fields (`role_tools.go:49`) and the team `create` handler
never reads `collaborations` (`team_tools.go:75`), so the tools silently drop
input. There is no command an agent can run to learn an entity's fields,
required-ness, or legal values.

### Decision

Add a `schema` subcommand to each typed entity — `ethos identity schema`,
`ethos role schema`, `ethos team schema` — beside their `create`/`list`/`show`
verbs (per-entity, not a top-level `ethos schema`). Default output is a human
field table (`FIELD`, `REQUIRED`, `TYPE`, `DESCRIPTION`) via `hook.FormatTable`;
`--json` emits a JSON Schema (draft 2020-12). Attributes (personality, talent,
writing-style) are excluded — they are title-plus-prose markdown with no field
set.

A new `internal/schema` package uses **reflect-plus-overlay**: field names and
required-ness are read live from the Go structs by reflection (so they cannot
drift and are never copied), and the registry overlays only what tags cannot
carry — descriptions, enums, human type labels, and patterns. Render methods
(`Table`, `JSONSchema`, `MarkdownTable`) drive every consumer: the CLI table,
the `--json` schema, the MCP `inputSchema`, the `validate-content` enum check,
and the seeded READMEs (compared against `MarkdownTable()` — a role table is
added and a new `teams/README.md` is seeded, both checked).

**The MCP handlers change first and the schema advertises only what they read**:
`handleCreateRole` gains all seven fields; `handleCreateTeam` accepts a
`collaborations` array (`add_collab` retained); then both tool builders generate
their options from the registry. `internal/mcp` is in the write-set.

### Rulings

- **No deferral** (operator). The MCP handler fix ships in this feature, not a
  follow-up — deferring known-real work just splits one job and adds babysitting.
- **Drift guarantees stated plainly.** Field names, required-ness, and
  closed-enum membership are guarded (names/required by live reflection; enums by
  a test against exported slices `identity.KindValues`, `role.ModelAliases`,
  `team.CollaborationTypes`); description prose is authored and unguarded (the
  test proves a description exists, not that it is correct).
- **`role.model` is a partial enum.** JSON Schema emits
  `anyOf[{enum:[aliases]},{pattern:"^claude-.+"}]` per `ValidateModel`; only the
  alias slice is guarded.
- **`--help` stays about usage, `schema` about shape.** Each entity's `--help`
  gains one line pointing at its schema subcommand.

### Rejected alternatives

- **Top-level `ethos schema`** — operator ruled per-entity; hides schema from the
  entity's own `--help`.
- **`--schema` flag on show/create** — overloads verbs; schema is a property of
  the type, not an instance.
- **Enrich `create --help`** — conflates usage with shape; no machine-readable
  form; drift survives.
- **Hand-maintained READMEs (status quo)** — the observed failure mode (the role
  handler drifted to four fewer fields than the struct).
- **Pure reflection, no overlay** — tags lack descriptions and enums; adopted in
  part (reflection for the field set) but not alone.
- **Fully authored registry restating the field set** — pure duplication of what
  reflection already knows; reflect-plus-overlay deletes the copy.
- **`go generate` for READMEs** — adds a generated artifact and a CI step; a
  comparison check needs neither.

## DES-067: Mission-lifecycle cluster — glob write-set admission, bind-vs-claim trailers, committing-session resolution (SETTLED)

**Status**: Settled. Shipped v4.9.0 (PR #415). Beads `ethos-qy7k`,
`ethos-7vo3`, `ethos-pobi`, `ethos-t2lb`, `ethos-u4kq`. Hardens DES-054
(audited delegation) and DES-036 (write-set admission) after dogfooding drove
the full delegation lifecycle — declare a write-set, dispatch a worker, admit
its result, stamp the commit — under real load.

### Problem

Running ethos to build ethos exercised the whole mission-to-audit path at once,
and seven places where the built behavior diverged from the specified behavior
surfaced together:

1. A `write_set` entry declaring a glob compared literally, so `docs/**` read as
   a directory literally named `**`. Every real path under it fell outside:
   `ethos mission result` refused a valid submission and the PreToolUse verifier
   allowlist — built from the same write-set — refused every write the contract
   had authorized.
2. The PreToolUse dispatch cannot see a `MISSION_ID` the leader never exported,
   so it falls back to the active-mission sidecar. That sidecar was written only
   by `ethos mission claim` and stayed put until an explicit `release`. A leader
   who created a second mission without releasing the first filed the new
   delegation under the old mission, so the audit trail named the wrong contract.
3. A dispatch that merely handed work to a worker stamped `Mission`/`Delegation`
   trailers onto the leader's own later commits — the `ethos-jawp` false-trailer
   class arriving through a new door.
4. The commit-msg hook stamped the trailers of the most recently dated session
   on the machine, not the session that was committing.
5. A `--var`/`--write-set` value holding several paths expanded into one entry,
   so a multi-path claim shipped narrower than declared with nothing said.
6. Identity resolution returned an arbitrary match when two identities shared an
   email or GitHub handle.
7. `path.Match` read `[draft]` as a character class, so a real bracketed file
   name was refused and a name nobody declared was admitted.

### Decision

Seven fixes, each grounded in the shipped code:

- **Glob-aware write-set containment (`ethos-qy7k`).** `pathContainedBy` matches
  entry segments against a leading run of the file segments — `**` spans any
  number of segments, `*`/`?` match within one (`path.Match` per segment), and a
  segment with no glob metacharacter compares literally, so every non-glob entry
  behaves exactly as before (the DES-036 parent-claim refusal is untouched).
  Result admission, the cross-mission overlap check, and the hook's `pathAllowed`
  all call the one exported `mission.PathContainedBy`, so what a contract admits
  and what the verifier admits are identical. A target carrying a `..` segment is
  contained by nothing, so a glob can never authorize a write outside the repo;
  an entry made only of glob metacharacters is refused (it would claim the whole
  tree); and `globMeta` drops `[` `]` so brackets read as ordinary filename
  characters, not a character class.
- **Bind origin vs claim origin (`ethos-7vo3`, closing the `ethos-jawp`
  false-trailer class).** `mission create` and `mission dispatch` bind the
  caller's session to the mission they just named — creating or dispatching *is*
  the leader naming a mission — so an in-session `Agent()` spawn files its
  delegation under that mission; a rebind prints one stderr line, and a spawn
  against a since-closed mission proceeds as Tier A with the stale mission named
  on stderr. But a dispatch binding files delegations only; it does **not** stamp
  commit trailers. Those require an explicit `ethos mission claim`, so dispatching
  work to a worker no longer tags the leader's own commits. Session resolution
  stays advisory: a human dispatching from a plain terminal has no session, and
  that is not an error.
- **One-line active-mission sidecar + sibling `active-mission-origin`
  (back-compat).** Every reader that predates the origin does `TrimSpace` over
  the whole `active-mission` file, so a second line would turn the mission ID
  into `m-…\ndispatch` and an older binary would deny every spawn — not
  hypothetical, since agents run a `.tmp` build while hooks invoke the installed
  binary. So `active-mission` stays one line, byte-identical to every version;
  the bind origin moves to a sibling `active-mission-origin`, and a claim is its
  *absence*, which is also what every pre-change sidecar looks like — upgrade
  needs no migration, downgrade loses only trailer suppression. The origin names
  its own mission, so a leftover origin cannot match the current binding. Write
  order enforces the safe failure: a dispatch writes the origin first, a claim
  writes the mission first then removes the origin, so an interrupted pair always
  reads as a claim — a lost suppression costs a stray trailer, never a denied
  spawn. A non-`ENOENT` read error means "unknown", not "claim".
- **Committing-session trailer resolution (`ethos-pobi`).** The commit-msg hook
  stamps the `Mission`/`Delegation` trailers of the session that is committing,
  resolved through `ETHOS_SESSION` or the Claude process tree, instead of the
  most recently dated session on the machine. An unresolvable session gets no
  trailer, and a failed lookup is surfaced rather than swallowed.
- **Comma-or-whitespace write-set splitting (`ethos-t2lb`).** `SplitPathList`
  splits `--var`, `--write-set`, and `--extract-into` values on commas **or**
  whitespace, so a multi-path value expands into distinct `write_set` entries,
  each admission-checked on its own. Any value that splits prints the resulting
  entries to stderr (quoted, comma-joined) so a two-entry expansion of
  `zz space/dir.md` is visible. An entry whose variable expands to nothing is
  refused, naming the stage, index, and entry, rather than silently vanishing.
- **Ambiguous-identity refusal (`ethos-u4kq`).** Identity resolution refuses an
  ambiguous match instead of returning an arbitrary one: two identities sharing
  an email or GitHub handle produce `ambiguous identity: N matches …` naming
  every candidate.
- **Platform-tag hardening.** `globMeta` and `SplitPathList` moved to an untagged
  `pathlist.go`; they had sat in `//go:build !windows` files that untagged
  callers (`validate.go`, `pipeline.go`) depend on, a break invisible to a
  darwin/linux `make check`. A constant or function with no platform behavior
  belongs in a file with no platform constraint.

### Rejected alternatives

- **A separate hook-side prefix check** (status quo). Rejected: the verifier
  allowlist drifted from the contract's containment check — the source of the
  `ethos-qy7k` double refusal. One exported matcher removes the second
  implementation.
- **Keep character-class globbing** (`path.Match` default). Rejected: character
  classes were never part of the `**`/`*`/`?` write-set vocabulary, nobody writes
  a character-class write-set, and their support over-admitted files nobody
  declared.
- **Carry the bind origin as a second line in `active-mission`.** Rejected: it
  poisons every `TrimSpace`-reading binary in a mixed-version session. The
  sibling-file-plus-absence encoding is downgrade-safe by construction.
- **Make a claim the only thing that files a delegation.** Rejected: dispatching
  *is* an explicit naming of a mission, so the delegation must file under it; the
  trailer-emission gate is what separates "filed" from "stamped".
- **Split write-set values on commas only** (prior behavior). Rejected: a
  whitespace-separated `--var` value shipped as a single entry claiming a path
  that does not exist. Splitting on either, plus an echo to stderr, makes the
  expansion auditable.
- **Return the first identity match.** Rejected: an arbitrary answer to an
  ambiguous question is worse than a refusal that names the collision.

## DES-068: Specialist sub-agents get a scoped inbound MCP tool set (SETTLED)

**Status**: Settled. On `main` (PRs #424, #425, #426; `punt-labs/team` #28 for
the shared registry); releasing in v4.10.0. Extends DES-005 (agent definition as
a channel binding) and the role-based tool restrictions of the team model.

### Problem

A dispatched specialist (bwk, djb, adb, …) runs as a generated Claude Code
sub-agent whose tools come from its role's `tools:` list — historically the six
built-ins (Read/Write/Edit/Bash/Grep/Glob). Session-bound tools — quarry
agent-scoped memory (`remember`/`find`), biff `plan`, ethos `identity`/`session`
— do not work through the Bash CLI, because a shell subprocess does not carry
the sub-agent's MCP session: nothing infers or enforces the agent's handle, so
its memory can be mis-attributed and its identity cannot resolve in-session. A
probe confirmed the gate: a scoped specialist holds no MCP tools and no
ToolSearch at all — MCP access is controlled by the `tools:` allowlist, not
ambient.

### Decision

Grant each specialist role a scoped **inbound** MCP set in its `tools:` list, so
the generator writes those tools into the agent and the sub-agent can call them
in its own session:

- **The set**: quarry memory + search (`find`/`remember`/`show`/`ingest`/`use`/
  `status`/`list`), biff `plan` + `read_messages`, ethos `identity` + `session`;
  the formal-methods roles (`z-specialist`, `b-specialist`) additionally get the
  z-spec toolchain.
- **Enforced by the allowlist.** MCP access is gated by `tools:`, so the
  leader-only boundary holds by *omission*: no biff write/wall/talk, no beadle,
  no GitHub, no `mcp__plugin_ethos_self__mission` (mission dispatch), no lux —
  those never appear in a specialist role, and the leader works on the ungated
  main session. `ceo`/`coo` carry no `tools:` key.
- **Explicit names, both plugin prefixes.** Each tool is named in full
  (`mcp__plugin_<server>__<tool>`) under both the released and the `<tool>-dev`
  plugin prefix — every repo declares its own checkout as a `<tool>-dev` plugin,
  so released-only names silently miss inside that tool's own repo. No wildcards.
- **Per-tool, not per-method.** MCP scoping is per tool: granting `identity` also
  grants its `create`, `session` its roster-editing `iam`/`join`/`leave`, biff
  `plan` its status-write. Accepted — all are local or status-only (a roster
  self-edit is attribution, not privilege), never outbound coordination. The one
  exception: `tech-writer`, the only role without `Bash`, omits `identity` — with
  no CLI door, the MCP tool would be its sole route to ethos-mediated identity
  creation, unwanted for a documentation role.
- **The instructions/tools split is countered in the generated agent, not the
  harness.** Claude Code injects every *connected* MCP server's instructions into
  a session keyed to server connection, not the per-agent tool allowlist, so a
  scoped specialist reads usage prose for tools it cannot call. The generator
  writes a note into each agent: only its listed tools are callable, and it
  should ignore instructions for a server *whose tools it does not hold* — scoped
  per server, so a quarry-holding specialist still honors quarry's own rules.

### Rejected alternatives

- **`mcp__server__*` wildcard grants.** Rejected: the quarry wildcard would sweep
  in the destructive `delete` and directory-registration tools deliberately
  excluded; the boundary must be named, not globbed.
- **ToolSearch-only access.** Rejected: ToolSearch reaches every connected
  server's tools, so the leader-only boundary would be conventional, not
  enforceable. The `tools:` allowlist makes it structural.
- **Grant outbound tools conventionally.** Rejected: unlike a write-set (verified
  in review), a live outbound tool call has no review gate; a specialist
  accidentally dispatching a mission or emailing out is unrecoverable. Withhold
  by omission.
- **Fix the instructions/tools split in the harness.** Rejected: not ethos's
  lever — the injection is Claude Code's, keyed to plugin enablement. The
  generated-agent note is the only mechanism ethos owns.
- **Justify the grant as raw capability ("Bash is broken").** Rejected as the
  framing: the quarry CLI does work; the real value is *enforced* identity —
  session-bound handle attribution the CLI can neither infer nor enforce.

## DES-069: MCP write methods are outside the write-set gate (SETTLED)

**Status**: Settled. Resolves bead `ethos-7b6c`, raised during PR #424
(DES-068). Amends the PreToolUse enforcement described in DES-035 and
extended by DES-052. Implemented via mission `m-2026-08-06-002`
(worker `bwk`, evaluator `djb`): `internal/mcpclass/mcpclass.go`
(shared classification/deny logic, the single source of truth for both
checks below), `internal/hook/pretooluse.go` (R1 deny + tests),
`cmd/validate-content/mcp_classification.go` (R2 classification +
tests), `docs/workflow.md` and `CLAUDE.md` (R3 warning text). Design
doc: `docs/mcp-write-set-gap.md` (commit `829eb42`).

### Problem

`internal/hook/pretooluse.go:237-251` derives an allowlist target path
only for `Write` and `Edit`; every other tool returns `""`, which
`:114-118` treats as allow-unconditionally. DES-068 granted 37 roles a
scoped MCP set, and two of those tool families write inside the repo:
`ethos_self__identity` with `method=create` (36 roles) reaches
`LayeredStore.Save` (`internal/identity/layered.go:358-394`), which
prefers the repo layer and writes a git-tracked
`.punt-labs/ethos/identities/<handle>.yaml`; the z-spec
`check`/`model_check`/`test`/`animate` tools (2 roles) write
`<spec>.report.json` and `<spec>.fuzz.json` beside the spec source.
Neither is checked against the mission `write_set`.

A second, larger fact was confirmed while investigating: the gate binds
**verifier** spawns only. `ETHOS_VERIFIER_ALLOWLIST` is set solely in
the verifier-isolation branch at
`internal/hook/subagent_start.go:179-194`, and `DESIGN.md`'s DES-052
"Lessons" section (~line 4416-4420) already states the worker case is
cooperative, not enforced. Workers are therefore ungated for `Write`,
`Edit`, `Bash`, and MCP writes alike. Leaders scoping a `write_set`
have been assuming a fence that does not exist for workers; this ADR
does not change that scope — see Consequences.

### Decision

1. **`write_set` is a verifier control, and is documented as such.**
   For workers it is a contract term checked by review, not a sandbox.
   The leader-facing statement lands in `docs/workflow.md` and the repo
   `CLAUDE.md`.
2. **In verifier spawns, deny the in-repo MCP write tools outright** —
   `_self__identity` with `method=create`, and `_zspec__check`,
   `_zspec__model_check`, `_zspec__test`, `_zspec__animate`. A deny,
   not a path map: it requires no knowledge of the target path, so it
   cannot rot and cannot fail open. Matching is on the suffix after the
   `mcp__plugin_<name>[-dev]_<server>__` prefix, covering both plugin
   prefixes with one entry (DES-068's double-listing rule). Implemented
   in `denyInRepoMCPWrite` (`internal/hook/pretooluse.go`), which
   delegates to `mcpclass.DenyReason` (`internal/mcpclass/mcpclass.go`),
   checked before `extractTargetPath` inside the existing
   `ETHOS_VERIFIER_ALLOWLIST` branch so worker spawns (allowlist unset)
   are unaffected. `DenyReason` fails closed: any `mcp__`-prefixed tool
   that does not parse into a known classification is denied, not
   silently allowed — closing a fail-open gap a local review round
   caught before merge (an unclassified direct MCP tool, e.g.
   `mcp__github__create_or_update_file`, must be denied, not passed
   through).
3. **Every MCP grant is classified, enforced by `make check`.**
   `cmd/validate-content` (`mcp_classification.go`) requires each
   `mcp__` tool name in any `roles/*.yaml` to appear in one of
   `mcpclass.ReadOnly`, `mcpclass.WritesOutsideRepo`, or
   `mcpclass.WritesInRepo`; the last implies the deny in (2), pinned by
   `TestDenyReasonCoversWritesInRepo`. Both the runtime deny (2) and the
   build-time classification (3) read the same `internal/mcpclass`
   maps, so a new `writes-in-repo` entry cannot be added to one without
   the other. An unclassified grant fails the build with the offending
   role and tool named. Classification entries cite file:line evidence
   in the tool's own repo.
4. **Tools writing outside the repo are explicitly not gated.** quarry
   `remember`/`ingest`/`use`, ethos `session`, biff
   `plan`/`read_messages`: they cannot change a repo artifact, so they
   cannot violate the invariant `write_set` protects.

### Rejected alternatives

- **Path-mapping every MCP write tool.** Fails open when the map is
  stale; the map duplicates another repo's runtime path derivation
  (`punt_zspec/report.py:44-51`); and 36 of 39 roles hold `Bash`, so
  the same write is reachable via the CLI. False assurance is worse
  than a documented gap.
- **Blocking all non-`Write`/`Edit` MCP tools in mission-bound
  sessions.** Removes the read halves DES-068 exists to provide while
  leaving `Bash` open.
- **A mandatory target-path resolver per grant.** Ethos does not own
  the third-party MCP servers and cannot impose a convention; adopted
  in weakened, fail-closed form as decision (3).
- **Dropping `identity` from specialist roles.** MCP scoping is per
  tool, not per method (DES-068), so this also drops `whoami`/`get`;
  and `Bash` reaches `ethos identity create` regardless.
- **Parsing `Bash` commands to gate them.** Unbounded; a wrong parser
  fails open, a strict one breaks every worker.

### Consequences

Verifier sessions gain a fail-closed deny on the two in-repo MCP write
families. Future grants cannot be added without classification, so the
PR #424 miss (z-spec writers granted without anyone noting they write)
becomes an unrepresentable state. Workers remain mechanically ungated —
unchanged from DES-035, now stated rather than assumed. Whether the
gate should bind workers is deferred to a separate ADR.

### References

- `internal/hook/pretooluse.go:109-151`, `:237-251`
- `internal/hook/subagent_start.go:179-194`
- `internal/identity/layered.go:358-394`
- `internal/mcp/tools.go:164-197`
- `DESIGN.md` DES-052 "PreToolUse enforcement" and "Lessons" sections,
  DES-068 (~line 6979)
- `.punt-labs/ethos/roles/*.yaml` (39 files)
- `punt_zspec/report.py:44-51,77-89`,
  `punt_zspec/commands/check.py`, `model_check.py`, `test.py`,
  `animate.py` (z-spec 0.17.0)
- `quarry/src/quarry/config.py:27`
- Bead `ethos-7b6c`; PR #424; mission `m-2026-08-05-001` (design),
  `m-2026-08-06-002` (implementation)

## DES-070: Seeded review agents — a new sidecar category for personaless checklists (IMPLEMENTED)

**Status**: Implemented. Full design in `docs/seeded-review-agents.md`; the
three agents live in `internal/seed/sidecar/agents/`, deployed via
`internal/seed/seed.go`'s `agentsRoot` parameter and `cmd/ethos/seed.go`.

### Problem

Ethos seeds two persona-bound review roles (`reviewer`, `security-reviewer`)
via `internal/seed/sidecar/roles/`, synthesized through
`generate_agents.go`'s role → personality → writing-style → talent pipeline
into named specialists for the mission worker/evaluator system. It ships
zero narrow-mandate, structured-output, personaless review-checklist agents
— the shape used by the third-party `pr-review-toolkit` plugin's
`code-reviewer.md` and `silent-failure-hunter.md`, which every consuming
repo (ethos included) currently depends on for local review Phase 5.
During the DES-069 PR cycle, both third-party agents ran clean while
GitHub Copilot/Bugbot caught three real defects, all in one dimension
neither covers: unverified invariant/exhaustiveness/exclusivity claims. The
leader wrote a third agent, `.claude/agents/invariant-completeness-
reviewer.md`, repo-local to ethos only — proving the gap but not closing it
for any other repo.

### Decision

- **Seed three review-checklist agents**, not four: a general
  code-quality/CLAUDE.md-compliance agent (ported from `pr-review-toolkit`
  code-reviewer), an error-handling/silent-failure agent (ported from
  silent-failure-hunter), and the invariant/exhaustiveness/test-tautology
  agent already written. A fourth candidate (test-coverage-gaps, per Qodo
  2.0) is deferred — no incident has shown it slipping past the other three
  or past existing coverage gates, and building it now risks the
  overlapping-agents failure mode this design explicitly avoids.
- **New seed category, `internal/seed/sidecar/agents/`**, bare markdown
  files (Claude Code subagent frontmatter + system prompt) deployed
  verbatim to `.claude/agents/` via the same `seedFS`/`decide`/`place`
  machinery already used for `roles/`, `talents/`, `personalities/`, and
  `writing-styles/`. No persona synthesis, no identity binding, no `tools:`
  allowlist merge.
- **Manifest-aware deploy, DES-065 semantics unchanged.** An operator may
  hand-edit a seeded review agent's scope; unconditional overwrite would
  silently discard that edit, same risk profile as a hand-edited persona
  file, so `decide`'s tracked/edited/collision logic applies unmodified.
- **No DES-052/DES-069 write-set or MCP-grant interaction.** These agents
  are pure Claude Code subagent definitions, never dispatched via `ethos
  mission`, never bound to an identity, and read-only by prompt convention
  (matching the existing `safety_constraints` pattern in `reviewer.yaml`)
  rather than by an ethos-enforced gate. DES-068's MCP-grant model exists
  for the outbound-tool risk of a dispatched specialist in a live session;
  an ad hoc, read-only review invocation carries none of that risk.
- **`ethos seed` also deposits a CLAUDE.md pointer**, parallel to how
  `internal/enable/deposit.go` writes vox's vendored guide and `@`-import:
  an addition to the existing vendored `.punt-labs/ethos/CLAUDE.md` naming
  the three agents and the Phase-5 sequence, so a leader discovers them
  without hand-wiring. The deposited text states plainly that these three
  are checklist agents, not specialists — invoked directly as local review
  passes, never via `ethos mission dispatch --worker <handle>` — since
  `.claude/agents/` also holds persona-bound generated agents (`bwk.md`,
  `reviewer.md`, `coo.md`) and nothing else in the directory distinguishes
  the two kinds before a dispatch attempt fails downstream.
- **Ethos's own dogfooding transition runs the seeded agents in parallel
  with `pr-review-toolkit` for one PR cycle**, diffs findings, then drops
  the third-party dependency from this repo's own Phase 5 — not an
  immediate cutover, since the ported prompts are new and unproven relative
  to their originals.

### Rejected alternatives

- **Shoehorn into the role/archetype pipeline.** Would require a null-object
  personality, a second output shape in `generate_agents.go` for
  non-persona pass-through, and a fake identity binding for something that
  is not an identity — more moving parts to produce output indistinguishable
  from a flat file copy. Rejected.
- **A simpler, non-manifest-aware deploy** on the premise that there's no
  persona content to preserve. Rejected: the preservation need is the same
  (protect operator hand-edits from silent overwrite), it just isn't
  persona content — DES-065's logic still applies.
- **Immediate cutover from `pr-review-toolkit`** in ethos's own workflow.
  Rejected: trades a known-working dependency for unproven ported prompts
  with no comparison window; the operator's goal is more coverage, not a
  gap swapped for another gap.
- **Build a fourth test-coverage-gaps agent now.** Rejected: no concrete
  incident (unlike the invariant-completeness gap, which had one) and risks
  overlapping with `code-reviewer`'s existing "inadequate test coverage"
  bullet.

### Open questions — ruled

- Product-positioning: does ethos market "we ship code review agents" as a
  capability, or is this framed purely as an artifact of dogfooding good
  local review? **Ruled: yes, market it as a real product capability**
  alongside identity, missions, and audit. `prfaq.tex` treatment is a
  separate follow-up, not part of this implementation.
- Product-positioning: does dropping `pr-review-toolkit` from ethos's own
  CLAUDE.md constitute a deliberate competitive statement the operator
  wants to make on purpose. **Ruled: no — incidental dogfooding framing**
  ("we now use our own seeded agents"), not a competitive claim.
  `pr-review-toolkit` stays listed in the Plugins section for one PR cycle
  per the transition plan before it's dropped.

## DES-071: Distribution scope — what ships in the plugin vs what stays dev-side (AMENDED 2026-08-23)

**Context.** During the L4 payload-optimization pass (post-DES-070), the
ethos repo's own per-session payload measured 388 KB, of which ~54 KB was
the ambient `# Ethos` block — the top-level `CLAUDE.md` plus three
`@`-imported plugin CLAUDE.mds (`.punt-labs/{ethos,z-spec,vox}/CLAUDE.md`).
On inspection every byte was developer content, not needed for using ethos
in a consumer repo. Meanwhile the same ambient session had zero setup
guidance visible to an agent landing in a repo where ethos was not yet
enabled — a documentation gap in the opposite direction.

Root cause: the repo had never explicitly separated three distinct
audiences and their delivery mechanisms:

1. **Developer of ethos** — needs architecture, build & run, delegation
   table, standards checklist. Present in every ethos-repo session.
2. **Agent using ethos in a consumer repo** (day to day) — needs the daily
   command surface: `ethos whoami`, `ethos iam`, `ethos mission`, `ethos
   audit`, `ethos session`. Present in every consumer session.
3. **Agent using ethos in a consumer repo** (one-time setup) — needs
   `ethos seed`, `ethos enable`, `ethos setup`, bundle choices,
   troubleshooting. Present once during install; unnecessary thereafter.

Without an explicit rule for each, developer content and end-user content
mixed together in a single always-injected block, and setup content lived
nowhere reachable by the audience that needed it.

**Decision.** Codify three tiers of documentation delivery, each with a
specific mechanism, and place each new doc in the tier that matches its
audience.

| Tier | Audience | Delivery mechanism | Auto-inject? |
|---|---|---|---|
| A. Developer of ethos | ethos-repo sessions only | `@`-import from ethos repo's top-level `CLAUDE.md` — lives in `docs/development.md` | Yes, per-session in the ethos repo |
| B. Daily-use in consumer repos | Every consumer session after `ethos enable` | Embedded in the ethos binary (`//go:embed internal/enable/guide/CLAUDE.md`), deposited by `ethos enable` to `.punt-labs/ethos/CLAUDE.md`, `@`-imported by the enable step into the consumer's top-level `CLAUDE.md` | Yes, per-session in each consumer repo |
| C. One-time setup in consumer repos | Only when an agent lands in a repo where ethos is not yet enabled | Embedded in the ethos binary alongside the guide, deposited by `ethos enable` alongside `.punt-labs/ethos/CLAUDE.md` as `.punt-labs/ethos/ETHOS-SETUP.md` (or equivalent), referenced by URL from the daily-use guide so a pre-enable agent can fetch it via WebFetch | No — referenced, opened on demand |

**Concrete placement rules:**

- **`CLAUDE.md` at the ethos repo root** — tier-A only. `@`-imports
  `docs/development.md`. Does NOT `@`-import `.punt-labs/{ethos,z-spec,vox}/CLAUDE.md`
  (those are tier-B end-user docs, not needed for developing ethos).
- **`docs/development.md`** — tier-A. Build & run, architecture, package
  map, storage layout, identity schema, design invariants, specialist
  delegation table, quality gates, standards checklist, operational
  constraints. Everything a developer of ethos needs at every-session
  start.
- **`internal/enable/guide/CLAUDE.md`** (embed source) — tier-B. Daily-use
  command surface. Kept tight; anything not needed every session moves to
  tier C.
- **`internal/enable/setup/ETHOS-SETUP.md`** (embed source, planned) —
  tier-C. Setup playbook: `ethos seed`, `ethos enable`, `ethos setup`,
  bundle choices (foundation vs gstack), troubleshooting. Deposited
  alongside the guide but NOT `@`-imported. Referenced from the guide
  by relative path (`./ETHOS-SETUP.md`) once deposited, and by absolute
  GitHub URL for pre-enable discovery.
- **`docs/ETHOS-SETUP.md`** (ethos repo) — the authoritative source, and
  the target of the GitHub URL that pre-enable agents fetch. Kept in sync
  with the embedded copy at `internal/enable/setup/`.

**Naming convention.** Under `docs/`:

- Lowercase for developer-internal docs (`development.md`, `workflow.md`,
  `architecture.md`, all existing files).
- UPPERCASE-KEBAB for consumer-visible `ETHOS-*` docs
  (`ETHOS-SETUP.md`, `ETHOS-ROADMAP.md`) — matches the existing pattern
  and signals "this is a document a consumer or new agent may open by
  name."

**Consequences.**

- Ethos-repo per-session payload drops the tier-B and tier-C content
  (~15 KB of plugin end-user docs) that were being auto-injected via
  the three `@`-imports. Measured reduction: −69 KB on the ethos-self
  scenario (388 KB → 319 KB after this ADR's structural changes).
- Consumer per-session payload stays at daily-use minimum (tier B only);
  setup content is fetched on demand.
- Every new documentation file has one right home; ambiguity ("does this
  go in `.punt-labs/ethos/CLAUDE.md` or `docs/`?") is resolved by
  audience-tier lookup.
- Renaming a doc between tiers requires moving its source file AND
  re-running `ethos enable` in consumers if the change affects tier B.
- The GitHub URL reference in the daily-use guide (tier B) points at the
  ethos repo's `docs/ETHOS-SETUP.md`, which is reachable via WebFetch
  from a consumer session with net access. Air-gapped consumers rely on
  the deposited copy at `.punt-labs/ethos/ETHOS-SETUP.md` (once tier-C
  deposition is implemented).

**Implementation status.**

- Tier A + tier B split: shipped in the branch `content/dev-doc-split` on
  top of `content/rightsize-engineering-team` (PR #467).
- Tier C deposition: shipped in PR #468 (commit bea993f).

**Rejected alternatives.**

- **Ship setup as part of the tier-B daily guide.** Rejected: bloats
  every-session payload with content only needed once, undoing the
  optimization that motivated the split.
- **Put setup only at a GitHub URL, no local deposition.** Rejected:
  breaks air-gapped consumers and forces WebFetch for a task that could
  be a local file read. GitHub URL reference stays as the pre-enable
  discovery path, but the file also ships locally post-enable.
- **Add setup to `ethos setup --help` output only.** Rejected: partial
  overlap (`--help` covers the command flags but not the surrounding
  playbook — troubleshooting, bundle-choice guidance, verify-with-doctor
  sequence). Considered as a small pointer supplement to the setup file,
  not a replacement.
- **Merge `docs/development.md` back into the top-level `CLAUDE.md`.**
  Rejected: mixes tier-A auto-inject with the tier-B/C reference-only
  content that also lives in the root `CLAUDE.md`, undoing the audience
  separation.
- **Drop `docs/development.md` `@`-import from the root `CLAUDE.md`
  entirely (fully on-demand).** Rejected: this repo is where ethos is
  developed; the audience by default IS the developer, and every session
  benefits from architecture/delegation guidance being present.

### Open questions — ruled

- Case convention for `docs/ETHOS-*.md` vs `docs/development.md` (mixed
  case in the same directory): **Ruled: allow the mix** — UPPERCASE for
  files consumers might open by name (`ETHOS-SETUP.md`, `ETHOS-ROADMAP.md`)
  and lowercase for developer-internal reference (`development.md`,
  `workflow.md`). Consistency by audience, not by directory.
- Whether tier-C deposition ships in the same PR as tier-A/B split, or as
  a follow-up code change. **Ruled: follow-up.** Splitting keeps this PR
  content-only (no `internal/enable/` code changes), which is faster to
  land and easier to revert. The GitHub URL reference in the guide is a
  bridge until tier-C deposition ships.

### Amendment 2026-08-23: superseded by tool-enable-disable § 2.11

**What changed.** The org's `punt-kit/standards/tool-enable-disable.md`
§ 2.11 states a hard biconditional: for every `<repo>/.punt-labs/<tool>/`
whose `enabled` marker is present, the repo's `CLAUDE.md` MUST contain
exactly one `@.punt-labs/<tool>/CLAUDE.md` line — **no exception for
the tool's own dev repo.** The corresponding § 2.3 makes `enable`
responsible for writing that import, unconditionally.

The Concrete placement rule that read *"[the ethos repo root
CLAUDE.md] does NOT `@`-import `.punt-labs/{ethos,z-spec,vox}/CLAUDE.md`
(those are tier-B end-user docs, not needed for developing ethos)"* is
**revoked.** In the ethos repo — which enables ethos, vox, and z-spec on
itself for dogfooding — those three imports are required.

**Rejected alternative that returns.** "Ship the tier-B daily guide as
part of every session" was rejected here on payload grounds. That
rejection is reversed by the standard. The correct response to a
tier-B guide that measures too big is to **tighten the guide** (its
content is the same content consumers load, so the win compounds
across every consumer), not to skip the import in the dev repo and
diverge from what consumers see.

**What still holds.**

- **The three-tier separation** (developer / consumer-daily / consumer-setup)
  is unaffected. Tier A remains the ethos-only developer content in
  `docs/development.md`. Tier B remains the deposited daily-use guide.
  Tier C remains the on-demand setup playbook.
- **Tier C stays not-`@`-imported.** § 2.11 governs the tier-B guide
  file, not every companion doc in the tool's subtree. `ETHOS-SETUP.md`
  is a reference doc opened on demand, not the tool's canonical
  user-guide `CLAUDE.md`.
- **`docs/development.md` stays `@`-imported** from the ethos repo root.
  It's dev-process content, out of scope for the enable/disable
  standard by § 2.1 ("dev-process standards are entirely separate from
  tool user guides").

**Measured cost of the reversal.** The three tier-B `@`-imports
(`.punt-labs/{ethos,vox,z-spec}/CLAUDE.md`) return to the ethos-repo
per-session payload — the bulk of the ~54 KB "`# Ethos` block" this
ADR originally measured, minus the top-level `CLAUDE.md` itself
which is unaffected. Tier C stays not-`@`-imported (see above), so
the payload increase is tier-B only. Same content consumers already
load every session. Tightening the tier-B guides is the compounding
fix; skipping the import in the dev repo is not.

**Landed with PR #488** — added the vox import, enabled z-spec here,
committed the biconditional-consistent CLAUDE.md preamble. Biff also
has a marker in this repo but ships no guide (§ 2.6 lets a global
tool register user-scope only); tracked as a biff-repo bug, not an
ethos-repo compliance gap.

## DES-072: Correction events — an additive-only annotation mechanism for closed missions (SETTLED)

**Context.** Two open beads (`ethos-11fy`, filed 2026-08-07; `ethos-lpub`,
filed 2026-08-09) independently hit the same gap from different angles.
`ethos-11fy`: a closed mission's only result was fabricated (worker never
ran, `confidence: 1.0` on nothing), and no sanctioned way exists to correct
or annotate the record — every write path in `internal/mission/store.go`
(`Update`, `Close`, `Abandon`, `AppendReflection`, `AppendResult`,
`ForceReleaseWriteSet`) either requires `status = stOpen` or transitions a
mission into a terminal state; none apply to an already-closed mission.
`ethos-lpub`: the same gap for a plain factual correction (not a lie at
close time — a fact discovered afterward) and for `StatusEscalated`
missions, which have zero special-casing beyond being grouped with
`closed`/`failed` as terminal (confirmed in code: no reopen or
continuation path exists anywhere in `internal/mission`).

A concrete, real-world instance surfaced during the ethos-dsby repo-only
migration (2026-08-22, `ethos-ecpv`): closing mission `m-2026-08-22-018`
recorded an evidence entry ("make check (full suite): fail") whose
accompanying diagnosis was wrong — a stale worktree base, not the
"pre-existing, unrelated" defect the worker claimed. Lacking a sanctioned
mechanism, the leader (`claude`) hand-edited `results.yaml`, appending a
YAML comment outside the parsed schema. A later `code-reviewer` audit
found the comment invisible to every mission-reading surface (`ethos
mission show`/`results`, MCP, `ethos ui`) and non-durable: any future
`AppendResult` on that mission re-marshals the whole file via
`yaml.Marshal` and silently drops it. Exactly the failure mode `ethos-11fy`
predicted, reproduced by the very agent that predicted it.

**Formal grounding (`docs/spec-mission-lifecycle.tex`).** The
`TerminalIsFinal` theorem is proven both by inspection and ProB animation:
`Delegate`, `SubmitResult`, `Reflect`, `AdvanceRound`, `Close`, and
`Abandon` all guard on `status = stOpen`; once `status' ≠ stOpen`, no
operation in that set is enabled again — `status` is absorbing at every
value in `CloseableStatus ∪ {stAbandoned}`. Any new operation touching a
closed mission MUST leave `status`, `resultRounds`, and `delegationCount`
unchanged in its frame conjuncts, or it falsifies a proven theorem, not
just an implementation convention. `prfaq.tex`'s audit-trail FAQ
(`faq:audit-trails`) independently establishes the shape the fix must
take: sealed audit chunks are already "immutable, timestamp-named" and
merge without conflict specifically because each session writes to a new
file, never rewrites an existing one. `results.yaml`/`contract.yaml` are
the outliers — mutable files rewritten via whole-value `yaml.Marshal` on
every write — which is *why* the hand-edit was fragile: it added content
to a file whose own write path doesn't know that content exists.

**Decision.** A correction is a new **event on the existing mission event
log**, not a new file format and not a new field on `results.yaml`. The
event log is already exactly the artifact `prfaq.tex` describes:
append-only, one JSON line per event, flock-serialized
(`appendEventLocked`), chunked per-session into immutable,
timestamp-named files that never need to be merged against each other —
DES-058's two-tree layout already routes every event append into a
machine-local per-(mission, session) log with a strictly-monotonic
sequence, sealed at pre-commit. `Event.Details` is documented as
intentionally open (`log.go:33`: "so future event types do not require a
schema migration") — a new `correct` event type needs zero schema
migration on the log itself.

- **New `Store.Correct(missionID string, c Correction) error`.** Guard is
  the *inverse* of every other write path: refuses when `status = stOpen`
  (a correction is for a mission that already reached a verdict; an open
  mission's story isn't finished yet — use `Update`/`Reflect` instead).
  Never touches `contract.yaml`, `results.yaml`, `reflections.yaml`, or
  any `resultRounds`/`delegationCount`/`status` field — the operation's
  entire effect is one `appendEventLocked` call with
  `Event: "correct"`. This is provably compatible with `TerminalIsFinal`:
  the theorem's frame conjuncts are about the `Mission` schema's fields,
  none of which `Correct` writes.
- **Seals its own write.** Every existing event type rides to git inside
  a commit that also changes code — a correction usually doesn't (the
  `ethos-ecpv` case was pure post-hoc audit, no code change). Left alone,
  `appendEventLocked` routes into the machine-local live zone
  (`log.go:87-89`) and nothing promotes it to git until an unrelated
  `ethos audit seal` fires at some future pre-commit — the same
  non-durability this design exists to fix, one layer down. `Correct`
  therefore calls the mission's own seal path on success before
  returning, so a correction is durable the moment the command exits, not
  contingent on the next commit touching that mission.
- **Layer-consistent or refused, never silently mismatched.**
  `appendEventLocked` writes to the live zone whenever
  `twoTreeStorage && repoRoot != ""`, with no check of which layer the
  mission itself lives in; `LoadEvents` only reads the live union when
  `resolveLayer` returns `layerRepo` — a global-layer mission is
  "operated on in place, never silently migrated" (`paths.go:169-193`).
  No existing operation exercises this gap because nothing today targets
  an old mission from a new session; `Correct` is the first one that
  does, by definition. `Correct` refuses with a clear error
  (`"cannot correct a global-layer mission from a repo-layer session"`)
  when `resolveLayer(missionID) != layerRepo`, rather than writing
  somewhere `LoadEvents` won't read back.
- **`Kind` is a required, closed enum — not folded into free-text
  `Claim`.** The three motivating cases are different in kind, not just
  in wording: `ethos-11fy` is an integrity finding (a worker fabricated a
  result), `ethos-lpub`'s factual case is a correction to something true
  when written and false now, and the escalation follow-up is a decision
  record with no wrong claim to quote at all. Collapsing all three into
  one `Claim`/`Correction` pair either forces a fabricated "claim" for
  the decision case or leaves "was any mission's result found fabricated"
  — the question the audit trail exists to answer — unanswerable by
  anything but grepping free text.

  ```go
  type CorrectionKind string
  const (
      CorrectionFactual    CorrectionKind = "factual"    // true when written, false now
      CorrectionFabrication CorrectionKind = "fabrication" // the original claim was never true
      CorrectionDecision   CorrectionKind = "decision"    // a decision record, no prior claim to correct
  )

  type Correction struct {
      Mission    string         // must match; validated like Result.Mission
      Round      int            // the round being corrected; 0 = whole-mission; must be <= contract.CurrentRound
      Kind       CorrectionKind // required
      Author     string         // identity handle; must resolve — unlike Result.Author, WHO says the record is wrong is load-bearing
      Claim      string         // required for factual/fabrication; empty for decision
      Corrected  string         // what's actually true, or what was decided
      Supersedes string         // optional: references a prior correction this one supersedes
      Evidence   []Evidence     // optional, same shape as Result.Evidence
  }
  ```

  `Claim` is required for `factual`/`fabrication` and must quote or
  closely paraphrase the thing being corrected — this mirrors
  `ethos-11fy`'s explicit requirement: "the fabrication has to stay
  visible in the audit trail alongside its correction, or the fix
  defeats the point of an append-only audit log." `Round: 0` is the
  whole-mission sentinel; any other value greater than the mission's
  `CurrentRound` is rejected — a correction cannot cite a round that
  never ran. `Correction` the field was renamed `Corrected` to stop
  `type Correction struct { Correction string }` from reading badly at
  every call site.
- **CLI**: `ethos mission correct <id> --kind factual|fabrication|decision
  --corrected "..."` (`--claim` required unless `--kind decision`, plus
  `--round`, `--supersedes`, `--evidence name=status` repeatable,
  matching `mission result`'s flag shape). `--file` accepts the same YAML
  shape for longer corrections, matching `mission reflect`/`mission
  result`'s existing `--file` convention.
- **Rendering — all four surfaces the incident named, not three.**
  `ethos mission show` and `ethos mission log` render correction events
  inline, ordered by timestamp alongside the events they follow — never
  hidden, never replacing the original text. `ethos mission results`
  (which reads the parsed `results.yaml`, not the log) gains a
  `Corrections:` section sourced from the log. The MCP `mission` tool
  gets its own formatter for the `correct` verb per DES-020 (every MCP
  tool needs a `format_output.go` formatter before shipping) — the
  original hand-edit was invisible specifically to the surfaces an agent
  reads, and MCP is the one this list would otherwise still miss.
  `internal/ui` (`ethos ui`)'s mission detail view renders corrections
  the same way it renders results and reflections today.
- **Detecting the wrong path, not just providing the right one.**
  Shipping `Correct` makes a sanctioned mechanism available; it does not
  by itself stop the next hand-edit, which is what actually produced the
  `ethos-ecpv` incident. `ethos doctor` gains a check that flags any
  tracked `contract.yaml`, `results.yaml`, or `reflections.yaml`
  containing a top-level comment line (`^\s*#`) — these are exclusively
  machine-written by `yaml.Marshal`, which never emits comments, so any
  comment present is definitionally a hand-edit. Cheap, exact, and would
  have caught `ethos-ecpv` on the commit that introduced it.
- **No `EscalationFollowUp`, no reopen.** `ethos-lpub`'s "escalation gap"
  (once `status = stEscalated`, whatever the leader decides next has no
  home) is the same shape, closed the same way: the leader's decision
  after an escalation is itself a `Correction{Kind: CorrectionDecision}`
  event (`Corrected` = what was actually decided, `Claim` empty), not a new
  mission-lifecycle transition. `StatusEscalated` needs no special-casing
  beyond what it already has.

**Reasoning.**

- **Reuses proven machinery instead of inventing new machinery.** The
  event log's flock, atomicity (pre-write-size capture + truncate-on-
  failure), chunk sealing, and git-merge-conflict-freedom are already
  built, tested, and exercised by every other event type. A `Correction`
  file format would need to reinvent all of that or leave it unprotected.
- **Structurally impossible to violate `TerminalIsFinal`.** `Correct`
  doesn't read-modify-write `Mission`'s fields at all — there's no
  `Mission` value in scope for it to mutate. This is stronger than "the
  guard checks the invariant" (which `Close`/`Abandon` do); it's "the
  operation has no path to the fields the invariant is about."
- **Solves the durability half of `ethos-ecpv` once sealed on write.** A
  `correct` event lives in a new, immutable log chunk — nothing rewrites
  it, the same reason `close`/`result` events never get silently dropped
  by a later write to a *different* mission's files. Unlike those other
  events, a correction isn't guaranteed to ride along with a commit that
  seals it, which is why `Correct` seals its own write explicitly (see
  Decision) rather than relying on the next unrelated commit's
  pre-commit hook.
- **Solves the visibility half.** Because it's a first-class event type
  (not a comment, not free text in an existing field), `mission show`
  and `mission log` render it the same way they already render `close`,
  `result`, and `write_set_released` events — no new rendering framework,
  just a new case in the existing dispatch.

**Rejected alternatives.**

- **A `corrections []Correction` field on `resultsFile`** (the option
  named in `ethos-lpub`'s own note as "add a `corrections` field so the
  data round-trips and renders"). Rejected: `results.yaml` is rewritten
  in full via `yaml.Marshal(&wrapper)` on every `AppendResult` — adding a
  field there doesn't fix the underlying fragility, it just moves the
  YAML-comment problem into a schema field that's still vulnerable to
  being dropped by a future migration or hand-edit of the same
  mutable file. The event log has no such rewrite path; every write is a
  pure append.
- **Reopening the mission via a new `stCorrected` status, or via
  `Update`.** Rejected outright by `TerminalIsFinal` — introducing any
  path back into a non-absorbing state for a terminal mission falsifies a
  proven theorem, not just a design preference. `ethos-lpub`'s own
  framing ("terminal states are absorbing... that invariant should NOT be
  broken") already rules this out; restated here because it's the most
  tempting wrong answer.
- **A correction as a same-shaped `Result` with a synthetic round
  number.** Rejected: `Result` and `Correction` mean different things — a
  `Result` is a worker's claim about work just done, gated by
  `checkResultGateLocked`'s round-currency check; a `Correction` is a
  claim about a claim already on record. Conflating them would make
  `mission results` unable to distinguish "the worker said this" from
  "someone later said the worker was wrong," which is the entire point
  `ethos-11fy` is trying to preserve.
- **Editing `results.yaml`/`contract.yaml` in place with a documented
  convention (e.g., a `# CORRECTED:` comment prefix).** This is what
  `ethos-ecpv` actually did, found broken by the very next code-reviewer
  pass: invisible to every tool, and not durable against a future
  rewrite of the same file. Kept as a documented anti-pattern, not a
  fallback, and now mechanically detected — see the doctor check above.
- **A correction as its own mission**, linked by a `corrects: m-…` field.
  The natural answer in a repo whose own `CLAUDE.md` says to use ethos to
  build ethos, and worth stating on the record because it's the first
  question a future reader will ask. For `ethos-11fy` specifically, a
  bare `Correct` event is an unreviewed accusation — one appended line,
  one `Author`, no evaluator, in a system whose entire premise is that a
  claim about work gets a reviewer; "a worker fabricated a result" is a
  heavier claim than the result it corrects. Rejected anyway: far too
  heavy for `ethos-lpub`'s "we later learned X" case, and it reintroduces
  the reopen temptation this design otherwise avoids — a mission-about-a-
  mission invites exactly the "does the corrected mission now need a
  status change" question `TerminalIsFinal` forecloses. `Author`
  resolving to a validated session identity (above) recovers most of the
  accountability a full mission would have added, at a fraction of the
  weight.

**Verification.** `internal/mission/store_test.go` gains
`TestStore_Correct_RefusesOnOpenMission`,
`TestStore_Correct_AppendsEventWithoutMutatingContract` (asserts
`contract.yaml`'s bytes are byte-identical before/after `Correct`, closing
the exact class of gap `ethos-ecpv` hit),
`TestStore_Correct_RefusesGlobalLayerMission`,
`TestStore_Correct_SealsOnSuccess` (asserts the correction is readable
from a fresh `Store` opened after the call returns, without an
intervening `ethos audit seal`), `TestStore_Correct_RequiresClaimUnlessDecision`,
`TestStore_Correct_RejectsRoundBeyondCurrent`, and
`TestStore_Correct_RendersInShowAndLog`. `internal/doctor` gains
`TestCheckNoHandEditedMissionFiles` covering the comment-line detector.
`docs/spec-mission-lifecycle.tex` gains a `Correct` schema (frame
conjunct: `ΞMission`, i.e. explicitly a no-op on the state ProB reasons
about) as a sibling of `Idle` (§Idle already models "nothing more
happens" on a terminal mission; `Correct` is "something appends to a
side channel, `Mission` itself still does nothing") — filed as follow-up
formal-methods work (`jms`/`jra`, per this repo's Z-spec row), not
blocking the Go implementation, since the Go-level guard
(`status ≠ stOpen`) already enforces the same precondition the formal
schema will state.

## DES-073: Skills as a first-class ethos primitive — per-identity, per-bundle skill declarations (IMPLEMENTED)

**Context.** Ethos partially uses Claude Code's skill mechanism today:
`internal/seed/sidecar/skills/` ships three skills (`baseline-ops`,
`create-from-project`, `mission`), `ethos seed` deploys them to
`~/.claude/skills/`, and `internal/hook/generate_agents.go` emits a
`skills:` field into every generated `.claude/agents/<handle>.md`
frontmatter. The mechanism works end-to-end for `baseline-ops` — sub-agents
spawned via Claude Code's Task tool receive that skill's body preloaded
into their context on spawn, per Claude Code's own docs.

The gap: the skill list in the generator is **hardcoded** at line ~520 of
`generate_agents.go`:

```go
b.WriteString("skills:\n")
b.WriteString("  - baseline-ops\n")
```

Every generated agent gets exactly `baseline-ops` and nothing else,
regardless of which identity, role, team, or bundle it belongs to. The
`internal/identity` struct has no `Skills` field. Bundle directories
(`internal/seed/sidecar/bundles/foundation/`,
`internal/seed/sidecar/bundles/gstack/`) carry no `skills/` subdirectory
either. Bundles that would naturally want workflow skills (gstack's
`ship`, `plan`, `design`, `review`, `qa`, `debug` — a direct mapping from
the `garrytan/gstack` open-source project this bundle draws from) have no
way to declare and deliver them.

Research validated this is the missing piece: the source `garrytan/gstack`
project (129K ⭐) is a Claude Code **skills package** first, with 35+
skill directories each containing a `SKILL.md`. Users invoke `/ship`,
`/plan-ceo-review`, `/qa` etc. directly. Ethos's gstack bundle borrowed
the role names, tagline, and workflow spirit but discarded the skill
mechanism entirely, delivering only identities + pipelines. The result:
an ethos gstack consumer has no discoverable slash-command or preloaded-
skill surface — they have to know the ethos mission-dispatch vocabulary
to reach gstack workflows.

Claude Code docs confirm skills work identically in the sub-agent
interaction model ethos uses:

> "The `skills` field allows you to inject specific skill content into
> a subagent's context at startup … subagents can still access other
> skills via the Skill tool unless restricted."
> — <https://code.claude.com/docs/en/sub-agents>

Ethos does not require slash-command invocation for skills to be useful.
Preloaded skills load whether the sub-agent is spawned via `Task(...)` or
via `ethos mission dispatch`.

**Decision.** Make skills a first-class ethos primitive: declarable at
the identity level, defaultable at the bundle level, and rendered into
generated agent frontmatter. Ship bundle-scoped skill content alongside
the existing bundle-scoped identities/roles/personalities/etc.

Concretely:

1. **Identity schema** gains a `Skills []string` field. Optional, unset
   preserves current behavior. Slugs reference skills that must resolve
   (either sidecar-seeded or bundle-scoped).
2. **Bundle schema** may optionally carry a `default_skills` list applied
   additively to every identity in the bundle. Merged as a union with
   each identity's own `Skills`, then deduplicated — not an override.
   Same slug-resolution rule. (A gstack identity whose own `Skills`
   already lists `gstack-plan` and whose bundle `default_skills` also
   lists `gstack-plan` gets one entry, not two, in the generated
   frontmatter.)
3. **Bundle directories** may carry a `skills/<slug>/SKILL.md` subtree
   (same shape as sidecar top-level `skills/`). `ethos seed` deploys
   bundle-scoped skills to `~/.claude/skills/` alongside the sidecar
   top-level skills, namespacing to avoid slug collisions.
4. **`generate_agents.go`** replaces the hardcoded `baseline-ops` write
   with a computed slug list: `baseline-ops` (always) + identity's
   `Skills` + bundle's `default_skills`, deduplicated, deterministic
   order.
5. **`gstack` bundle** ships six skills at
   `internal/seed/sidecar/bundles/gstack/skills/`: `gstack-plan`,
   `gstack-design`, `gstack-ship`, `gstack-review`, `gstack-qa`,
   `gstack-debug`. Each is a distilled `SKILL.md` derived from the
   corresponding `garrytan/gstack` skill (respecting MIT license,
   attributing origin in the skill metadata). `default_skills` on the
   bundle preloads the ones every gstack sub-agent benefits from
   (`gstack-plan`, `gstack-ship`); role-specific skills preload only
   where relevant (`gstack-qa` on the QA agent, etc.).
6. **`foundation` bundle** ships no skills initially — foundation is the
   minimal starter and stays minimal. Skill authoring for foundation is
   a follow-up if usage patterns warrant it.

**Wire-cost consequences.** Skills are cheap per session:

- Skill DESCRIPTIONS ride the per-session skill index (~200 B per skill).
  A gstack consumer picks up ~1.2 KB of index cost for six skills.
- Skill BODIES ride the wire ONLY when a sub-agent that preloads them is
  spawned, and only into that sub-agent's context. Main-loop payload
  does not grow by skill body bytes.

This is strictly cheaper than delivering the same workflow content via
generated agent-file body prose (which rides every session unconditionally)
and comparable to delivering via pipelines (which is zero per-session but
requires user vocabulary to invoke).

**Non-goals.**

- Ethos does not become a skill marketplace. Bundle-scoped skills stay
  bounded to the bundle's workflow. General-purpose language/framework/
  domain skills are what tools like `ECC` and `hermes-agent` provide;
  ethos users compose those alongside ethos's own if they want them.
- No change to how skills are executed by Claude Code — the mechanism
  is Claude-Code-native. Ethos only decides what to declare, ship, and
  reference.

**Rejected alternatives.**

- **Leave skills hardcoded to `baseline-ops`, deliver bundle workflows
  via pipelines only.** Rejected: pipelines require the user (or main
  Claude) to know the mission-dispatch vocabulary. Discoverability gap.
  Original `garrytan/gstack` succeeds because skills are directly
  invocable and self-describing; matching that UX matters.
- **Deliver workflows as agent-file body prose in the generated
  `.claude/agents/*.md`.** Rejected: bloats per-session wire (agent
  bodies always ride the wire). Same content as skills but wire-costs
  every session, not just on invocation.
- **Deliver workflows only via slash commands installed by
  `ethos enable`.** Rejected: slash commands assume the user types them.
  Ethos's interaction model is user-to-Claude-to-sub-agent; commands
  wouldn't fire from that path. Also, deposition-scope would need to
  extend beyond the current guide + SETUP files.
- **Wait for a code-free way to declare skills (e.g., convention over
  configuration).** Rejected: the ~15-line schema + generator change
  is the minimum credible path. Convention alone can't express
  per-identity or per-bundle scoping.

**Open questions — ruled.**

- Slug-collision policy when a bundle-scoped skill shares a slug with
  a sidecar top-level skill: **bundle scope wins**, `ethos seed` warns
  and prefers the bundle when the active bundle is set in
  `.punt-labs/ethos.yaml`.
- Whether to auto-preload every bundle-scoped skill on every bundle
  identity: **no — opt-in only**. `default_skills` is explicit; a
  bundle that ships six skills but only defaults two makes the other
  four available via runtime `Skill` invocation without inflating every
  sub-agent's spawn cost.

**Implementation status.** Implementation shipped in PR #481 (stacked on
PR #480, which introduced this ADR). `Identity.Skills`,
`bundle.Manifest.DefaultSkills`, the generator merge, `ethos seed`'s
bundle-skill deploy, and the gstack bundle's six skills all landed
together; see `internal/hook/generate_agents.go`'s `mergeSkills` and
`internal/seed/seed.go`'s `seedBundleSkills` for the mechanics.

---

## DES-074: Session identity comes from the harness, not from the process tree (ACCEPTED)

**Context.** Ethos answers "which Claude Code session is calling me?" by
walking the process tree to the **topmost** `claude` ancestor
(`internal/process.FindClaudePID`, `tree.go:40`) and using that PID as the key
into a single-valued pointer file, `~/.punt-labs/ethos/sessions/current/<pid>`,
whose contents are the session id (`internal/session/store.go:513`
`ReadCurrentSession`; `internal/resolve/resolve.go:144` `SessionID`).

That mechanism is broken, and the failure is a silent wrong answer rather than
a silent absence.

**Measured, 2026-09-07, host okinos, at HEAD ace18cf (v4.16.1).** The topmost
`claude` ancestor is not a per-session process. It is `claude daemon run`,
which every concurrent Claude Code session on the host shares:

```text
    pid=1728890  bash
  * pid=1710156  claude (bg-spare)
  * pid=1710144  claude (bg-pty-host)
  * pid=518779   claude (daemon run)     <- topmost; what FindClaudePID returns
    pid=1        systemd
```

Six distinct session rosters list participant `518779`, spanning four repos.
Three pointer files serve all of them. Each SessionStart overwrites the same
file; last writer wins. From a shell in `punt-labs/ethos`:

```console
$ cat ~/.punt-labs/ethos/sessions/current/518779
741ab38b-f8a5-463d-94ff-9e641df3e197
$ ethos session
Session: 741ab38b-...   Repo: punt-labs/z-spec
```

A commit in one repo can therefore be stamped with another repo's
`Mission:`/`Delegation:` trailers. DES-054's audit chain does not merely go
missing — it can lie. This supersedes the mechanism ethos-2f2q was closed
against: that bug's mtime-globbing fallback is gone, but its guarantee (a commit
carries its own session's mission, never a concurrent session's) breaks again
one layer down.

Two aggravating factors, both in ethos:

1. **`sync.Once`.** `FindClaudePID` caches its result for the process lifetime
   (`tree.go:31`), justified by the comment "PIDs are stable within a session."
   `ethos serve` is a long-lived MCP server (`internal/mcp/tools.go:210`), so it
   resolves one PID at startup and holds it for every session it will ever
   serve.
2. **Silent fallback.** `SessionID` returns `""` on failure and callers fall
   through to the git/OS identity. Measured three ways — a misspelled persona,
   an ended session, and the wrong repo — each produces a plausible wrong answer
   with exit status 0.

**This is a copied defect, not a local one.** A cross-repo sweep of all 23
sibling `DESIGN.md` files found the same algorithm in four products, propagated
by citation rather than re-derivation:

- **ethos DES-007** introduced both the walk and the pointer file as "the same
  `ps -eo pid=,ppid=,comm=` approach proven in Biff" and "the same pattern Biff
  uses for unread count files." The same ADR lists, as a requirement, *"Must
  handle concurrent sessions on the same machine"* — stated once and never
  checked against the mechanism directly above it. It records no rejected
  alternatives.
- **ethos DES-011** describes a *different, more robust* algorithm than the code
  implements: `whoami` "walks the process tree upward, **checking for a
  `current/<PID>` file at each ancestor**." Nearest-hit-wins would have survived
  the shared daemon. The shipped code computes one topmost PID and reads exactly
  one file. Design and code diverged and were never reconciled.
- **ethos DES-017** sounds like it settles topmost-vs-nearest. It settles
  immediate-parent-vs-ancestor-walk, motivated by making hooks and the MCP
  server agree on a key — not by whether the key is correct. Topmost was never
  argued anywhere in this repo.
- **mcp-proxy DES-002** states the falsified assumption verbatim: *"The main
  claude PID is stable for the session lifetime."* Its process model has no
  concept of a shared daemon above the per-session process.
- **lux** independently ships the same `ps -o ppid=` walk and does not know the
  harness variables exist.

**Decision.** Session identity is **declared by the harness and read per call**,
never inferred from the process tree and never cached.

1. **Key the pointer file on `CLAUDE_PID`**, the owning `claude` process's own
   PID, which Claude Code sets on every spawned subprocess. Unlike the topmost
   walk it is distinct per session — the collision this decision closes.
   *(Corrected 2026-09-07, round 2 measurement: a Task-tool subagent's own
   environment shows it inherits its leader's `CLAUDE_PID` VERBATIM, not a
   value of its own — same for a hook subprocess (measured live inside a
   real git commit-msg hook). "Distinct per nesting level" was wrong for
   both. It is right only for a NESTED `claude` PROCESS — `claude -p`
   spawning another top-level `claude` — which gets its own PID and its
   own `CLAUDE_PID`. `CLAUDE_PID` distinguishes concurrent SESSIONS, not
   nesting depth within one; a Task-tool subagent or hook subprocess
   shares its leader's session and correctly shares the key. See the
   round 2 amendment.)* Fall back to the existing walk only when the
   variable is absent (headless, CI, SDK, or a Claude Code older than
   2.1.234).
2. **Corroborate before trusting it.** `CLAUDE_PID` is captured once at spawn
   and never re-observed, so a long-lived process whose ancestor died — with the
   PID since recycled by an unrelated but legitimate `claude` session — would
   resolve to a real, live, wrong PID. Verify the env-sourced PID is still in the
   caller's *current* live ancestry before use; fall back to the walk if it is
   not. Ported from biff DES-058's `is_live_ancestor`.
3. **Remove the `sync.Once`.** Resolve per call. A long-lived server outlives
   the session it first saw.
4. **Fail loudly — but only when a session was expected.** Two cases, and
   conflating them is itself a defect:
   - **Not under Claude Code at all** (headless, CI, SDK, a plain terminal):
     a normal state. Return "no session" *silently* — no warning, no error.
     Ethos runs in CI, and warning here makes every run noisy. Biff returns
     `None` without warning for exactly this reason
     (`src/biff/session_id.py:160-180`).
   - **Under Claude Code but unresolvable** (env var present but uncorroborated,
     pointer file missing, roster absent): a named error, non-zero exit, message
     stating the remedy — e.g. `ethos: cannot identify the calling session — set
     ETHOS_SESSION=<id>, or run 'ethos session start'`.

   The signal distinguishing them is whether any Claude Code indicator is
   present at all (`CLAUDE_PID`, `CLAUDECODE`, or a `claude` ancestor). What
   must not survive either case is the silent fall-through to the git/OS
   identity when a session *was* expected — "no session" and "this git user"
   are different answers and must not be returned interchangeably.

5. **Bounded read retry on the pointer.** A consumer can start before
   SessionStart finishes writing. Biff carries a short bounded retry for this
   race (`session_id.py:157-158`); ethos has the same exposure.

6. **Neutral text on corroboration failure.** Failure has two indistinguishable
   causes — a stale env value whose PID was recycled, or a transient failure
   reading the process table. Falling back to the walk is correct either way, so
   the message must not assert a cause it cannot determine
   (`session_id.py:162-175`).

The file's contents, writers, and validation are unchanged. Only *what selects
the file* changes, plus the removal of caching and of silent fallback.

**Rejected: read `CLAUDE_CODE_SESSION_ID` and use it directly as the session
id.** This was this design's first form and is the obvious fix — the variable
is present in every subprocess and equals the roster filename exactly. It was
rejected on biff's tested evidence (biff DESIGN.md:6722): Claude Code freezes
each subprocess's copy at spawn time, and `/clear` updates the variable only in
its own process, so a long-lived server pins a stale session id forever. Biff
keeps a standalone regression reproducing the failure specifically to stop this
being re-proposed. The distinction is process lifetime — safe in a git hook or
one-shot CLI call, unsafe in `ethos serve`. Reading a *file* keyed by
`CLAUDE_PID` avoids it: SessionStart rewrites that file on every start,
including a `/clear`-sourced one, so the value stays live for the process's
whole lifetime. Note that ethos's existing `ETHOS_SESSION` escape hatch has this
same frozen-variable shape and must not be promoted into the primary path.

**Rejected: fix the walk to stop at the nearest `claude` ancestor.** Considered
in biff DES-058 before `CLAUDE_PID` was known to exist, and superseded there.
It separates the write side but leaves a read-side gap — a walk cannot tell a
long-lived process which of several nearby files is its own without a live
session id to compare against. It also remains a tree walk, and tree walks are
entry-point-dependent (below).

**Rejected: any process-tree walk as the primary mechanism.** Beyond the shared
daemon, the tree differs per entry point. lux DES-063 §4 shipped a session
binding on `$PPID` inside a hook that **never bound on any session for 28 days**
(2026-08-01 to the 2026-08-29 amendment), because Claude Code invokes hooks
through a short-lived `sh` wrapper — `$PPID` is the wrapper, which exits within
seconds. The failure was found when a menu entry was noticed missing, not by
tests or review; the same ADR carries ship-time latency measurements taken
against a binding that had never once worked. Environment variables are
inherited straight through wrapper processes; ancestor walks are not.

**Rejected: leaving the fallback silent.** Per operator ruling 2026-09-07: a
mechanism is reliable or it raises a clear error with a hint. A path that works
most of the time and misattributes the rest causes more churn than one that
stops. Agents here routinely work across worktrees and sibling repos, which is
exactly the condition under which a quiet fallback misfires.

**Considered: remove the pointer file from the trust path entirely.** lux
DES-037 closed a structurally similar defect and its finding is blunt — *"PID
files lie; sockets don't."* A PID file can name a recycled PID (false-alive), be
missing while its owner lives (false-dead), or be deleted; lux moved liveness
and identity onto the kernel's socket peer credential (`SO_PEERCRED` /
`LOCAL_PEERPID`) because a file cannot prove ownership. It took **16 review
rounds** — 13 on one bead, 3 on a follow-up — each fixing another interleaving
(recycled-PID false-alive, a live socket unlinked mid-handshake, a zombie read
as alive, a two-winner bind race) before the *class* was named rather than the
instances. lux explicitly rejected "keep hardening empirically (round 17+)."

The objection applies to us: ethos's pointer is a PID file, and DES-037's
lesson is that fixing the *walk* while keeping the *file* leaves you inside the
defect class. We are nonetheless keeping the file, for two reasons. First, the
peer-credential remedy is unavailable here — lux has a live socket between
client and server whose credentials the kernel vouches for; ethos's consumers
are git hooks and one-shot CLI invocations with no connection to the session.
Second, requirement 2 above closes the specific hole DES-037 names: a recycled
PID belonging to an unrelated session is *not* in the caller's live ancestry, so
corroboration rejects it. That is the same ownership proof, obtained from
process ancestry instead of a socket. If corroboration proves insufficient in
review, the escalation is not another round of hardening — it is moving identity
onto a channel that can vouch for it, and DES-037 is the precedent for making
that call early rather than at round 17.

**Follow-up: the pointer file has no TTL.** A dead session's mapping persists
until something overwrites it. lux DES-057 rejected "permanent registrations
(ghost menu entries from dead daemons)" on this ground and pairs its leases with
`on_connect` re-establishment so expiry is safe. Not in scope here — re-keying
removes the collision that makes stale entries dangerous — but tracked, because
a stale mapping is still a wrong answer waiting for a corroboration miss.

**Cross-repo consequence.** lux carries this defect unknowingly and is filed
separately; biff already fixed it (DES-058) and that fix did not propagate,
because nothing connects these implementations. mcp-proxy is a **co-victim, not a
safe reference**: it gets the transmission architecture right — spawned per
session, computes the key in that short-lived process, and ships it on the wire,
so its long-lived daemon receives an identity rather than inferring one — but
the *value* it transmits is the same falsified derivation. Two proxies spawned
by two sessions resolve the **same** `session_key`, so every daemon keying
per-session state on it (quarry, vox, biff) silently merges those sessions. It
presents as collision rather than mis-routing, which is why it has gone
unnoticed. Worse, the assumption is reaffirmed in five places across its docs
and encoded as a machine-checked invariant in its Z specification — a false
premise with a proof on top of it. Filed separately; the transmission pattern is
still the right one, and combining it with a correct key is the durable answer.
lux reached the same principle from the opposite direction after two failed
attempts and states it plainly (DES-057, rejected alternatives): *"the caller
knows who it is; the Hub does not."* Ethos is on the wrong side of that line and
this decision moves it across. Extracting the rule into shared code is deferred
until this implementation has survived review — three hand-copies is how this
defect spread, and a fourth written before one correct version exists would not
be an improvement.

### Amendment 2026-09-07: participant keying is a second, unanticipated migration

**The gap.** This decision's Decision section re-keys the SESSION pointer
file (`sessions/current/<pid>`) from the topmost-ancestor walk to
`CLAUDE_PID`. It says nothing about session ROSTERS
(`sessions/<session_id>.yaml`), whose `participants[].agent_id` field is
also written from `process.FindClaudePID()` — the primary participant in
`internal/hook/session_start.go`, the subagent's `parent` field in
`internal/hook/subagent_start.go`. Implementing the pointer-file fix
necessarily changed what `FindClaudePID()` returns, and every caller of a
shared function changes when its return value changes, whether or not the
caller's own source line was edited. This was not anticipated when this
decision was written; it surfaced in round 2 review of the implementing
mission (m-2026-09-07-004).

**Measured**, same host, same live session, moment of upgrade: the OLD
binary resolved `ethos whoami` successfully (`Claude Agento (claude)`,
exit 0); the NEW binary, run against the identical on-disk roster, failed
(`session "e1e5a0da-..." has no participant matching "1710156"`, exit 1).
The roster's primary participant was written by the OLD code as
`agent_id: "518779"` (the daemon PID); the NEW code resolves
`process.FindClaudePID()` to `"1710156"` (CLAUDE_PID) for the same live
process. Every session that exists at upgrade time breaks this way and
stays broken until it ends and a fresh `SessionStart` rewrites its
roster — this decision's pointer-file fix has no equivalent write path
for rosters, because a roster is not rewritten on every hook fire the way
the pointer file is.

**Decision.** Participant identity also moves to `CLAUDE_PID`, matching
the pointer-file key, but lookups and writes both TOLERATE a participant
record already keyed on `process.LegacyClaudePID()` — the unconditional
process-tree walk, ignoring `CLAUDE_PID` entirely, i.e. exactly what the
pre-fix `FindClaudePID()` always returned:

1. **Reads try the preferred key, then the legacy key.**
   `resolve.resolveFromSession` and
   `internal/hook/subagent_start.go`'s `resolveParentLine` each try
   `process.FindClaudePID()` first; on a miss, they retry with
   `process.LegacyClaudePID()` before concluding no participant matches.
   *(Corrected 2026-09-07, mission 005 finding B: this was true for
   `resolveFromSession` and false for `resolveParentLine`, which ran a
   SINGLE loop testing both keys against each participant in roster
   order — a legacy-keyed participant appearing earlier in the roster
   won over a correct, preferred-keyed one appearing later. Fixed to
   the genuine two-pass this text describes; see the mission 005
   amendment.)* An explicit `ETHOS_AGENT_ID` override is exact by the
   caller's own declaration and is never subject to this fallback — only
   the self-resolved PID path tolerates ambiguity.
2. **Writes update the existing record under whichever key already
   matches, rather than filing a duplicate.** `internal/session.Store`
   gains `JoinSelf(sessionID, preferredID, legacyID, p)`: it checks, under
   the same lock `Join` uses, whether `preferredID` already has a
   participant; if not, and `legacyID` does, it updates THAT record
   (leaving its on-disk key alone) instead of appending a second
   participant for the same physical process under the new key.
   `cmd/ethos/iam.go` and `internal/mcp/tools.go`'s `handleIam` — the two
   sites that self-key a participant via `Join` — now call `JoinSelf`
   instead when the key is self-resolved (not an explicit
   `ETHOS_AGENT_ID`).
3. **No migration script, no rewrite-on-read.** A session created after
   this fix already keys its primary on the preferred value from its
   first `SessionStart`; the legacy key is consulted only as a fallback,
   and only for a session that predates the upgrade. Such a session
   self-heals the moment it ends and a fresh `SessionStart` writes a new
   roster — there is nothing left to tolerate once no pre-upgrade session
   remains alive.
   *(Scope note, added after the PR #502 amendment below: this tolerance
   is for the ROSTER's participant lookup only, and presumes the SESSION
   POINTER lookup already resolved. It does not, by itself, describe
   whether an in-flight session's pointer lookup succeeds at upgrade
   time — see "Amendment: the upgrade-migration gap the participant-keying
   amendment did not cover" for that.)*

**Rejected: participant identity stays on the process-tree walk, only
the pointer file moves to `CLAUDE_PID`.** Simpler — no dual-key lookup,
no `JoinSelf` — but leaves two different PIDs identifying one session
(the pointer file keyed on `CLAUDE_PID`, the roster keyed on the walk),
which is exactly the kind of divergent, easy-to-misread state this
decision's Decision section otherwise eliminates. A future reader (or
agent) auditing a roster against its pointer file would see two PIDs and
have no way to tell, from the data alone, whether that is drift or intent.

**Rejected: a migration script that rewrites every existing roster's
`agent_id` field on upgrade.** Handles the transition in one pass instead
of a standing fallback, but requires every consumer (this repo, every
sibling repo with its own `.punt-labs/ethos/sessions/`, every developer's
`~/.punt-labs/ethos/sessions/`) to run it at the right moment relative to
the binary upgrade — a coordination problem the tolerant-lookup approach
does not have, since it works correctly regardless of which side of the
upgrade a given session's roster was written on. A migration script is
also permanent maintenance surface for a one-time transition; the
tolerant lookup's own cost disappears on its own once no pre-upgrade
session remains alive, with nothing to remember to remove.

Full detail: `process.LegacyClaudePID`, `resolve.resolveFromSession`,
`internal/hook/subagent_start.go`'s `resolveParentLine`,
`session.Store.JoinSelf`. Regression tests:
`TestResolve_ToleratesLegacyKeyedParticipant`,
`TestHandleSubagentStart_ParentLine_ToleratesLegacyKeyedPrimary`,
`TestStore_JoinSelf_UpdatesLegacyKeyedParticipant`,
`TestStore_JoinSelf_PrefersNewKeyWhenBothAbsent`,
`TestStore_JoinSelf_PrefersNewKeyWhenBothPresent`,
`TestRunIam_UpdatesLegacyKeyedParticipant`,
`TestHandleIam_UpdatesLegacyKeyedParticipant`.

### Amendment 2026-09-07: two round-2 review findings

**Doubled error prefix.** `resolve.ErrNoSession`'s message carried its own
`"ethos: "` prefix; `cmd/ethos`'s top-level error printer
(`cmd/ethos/main.go`) adds that prefix once for every error the CLI
returns, so the combination printed `"ethos: ethos: cannot identify the
calling session..."`. Fixed by dropping the prefix from the error message
itself — matching the convention every other CLI-surfaced error in this
codebase already follows.

**`ethos hook commit-trailers` stayed silent on the loud case too.**
`runHookCommitTrailers` must never block a commit and must never emit a
trailer it cannot vouch for, so it always exits 0 — that part was already
correct. But it discarded `resolve.SessionID`'s error unconditionally,
so a commit running under Claude Code whose session could not be
identified produced no stderr output at all, indistinguishable from the
ordinary, silent, not-under-Claude-Code case. Per this hook's own
rationale (ethos-pobi: a missing trailer is the failure this hook exists
to prevent), the under-Claude-Code-but-unresolvable case now prints a
diagnostic to stderr while still emitting no trailer and still exiting 0;
the not-under-Claude-Code case is unchanged and stays fully silent.

### Amendment 2026-09-07: round 2 formal reflection — six findings

A formal reflection (three independent local review agents plus direct
leader verification — running the patched binary against the leader's
own live session, not just the test suite) found the implementation of
this decision reintroduced the class of defect it exists to close,
through mechanisms the original decision text did not anticipate.

**R1 / R1b — the pointer file itself could silently lie.**
`WriteCurrentSession` used a direct `os.WriteFile` (truncate in place),
unlike `writeRoster`'s existing temp+rename discipline for the roster
file. A concurrent `ReadCurrentSession` could observe a zero-byte or
partially written file mid-write, and `ReadCurrentSession` read that
blank content as `("", nil)` — a *successful, empty* session id,
indistinguishable from "no session," which took this decision's silent
branch even while running under Claude Code. The pointer-file mechanism
reintroduced exactly the silent-wrong-answer shape this decision exists
to close, one layer down from the PID-collision bug it was written to
fix. Fixed: `WriteCurrentSession` now writes via the same
`CreateTemp` + `Chmod` + `Sync` + `Close` + `Rename` pattern as
`writeRoster`; `ReadCurrentSession` treats a blank result as an error,
identically to a missing file.

**R2 / R2b — SessionStart paid its own retry, then lost the human's
identity.** `internal/hook/session_start.go`'s `resolveHumanIdentity` ran
*before* `createSessionRoster` wrote the pointer file — but at
`SessionStart`, no pointer file or roster can exist yet for this exact
invocation, since it is what would create them. Before this decision,
that miss was silent and free, so the human's git/OS identity resolved
immediately regardless. After it, the miss paid the bounded retry
(~450ms) and then propagated a named error that `resolveHumanIdentity`
caught and logged, degrading every fresh session's greeting to the bare
OS username — a real, measurable regression on every single session
start. Compounding it: `session-start.sh` redirects the hook's stderr to
`hook-errors.log` and swallows its exit status with `|| true` (correct,
documented fail-open policy for hook wrappers — see cli.md's Hook
Architecture section), so the new 450ms cost and its error were both
invisible to the operator. Fixed at the root, not by reordering:
`resolveHumanIdentity` no longer consults the session store at all
(`resolve.Resolve(store, nil)`). A session-based lookup at this exact
call site is a structural, guaranteed miss by construction of when
`SessionStart` fires, not a signal about the human's identity — it was
never actually load-bearing, only silently free before this decision
made misses expensive. R2b needed no separate fix: with no loud message
produced at this call site anymore, there is nothing left for the
wrapper to swallow.

**R3 — participant miss is not fatal (binding ruling).** A session
resolving and its roster loading successfully, but this caller having no
matching participant in it, was treated as a loud `ErrNoSession` —
conflating a genuine absence (any process that has not run `iam` yet,
the ordinary state for most callers) with a wrong answer. `whoami` and
`doctor` hard-failed on every session whose primary participant predated
this decision's `CLAUDE_PID` re-keying (see the participant-keying
amendment above), because a re-derived key correctly found the SESSION
but the caller was not (yet, under the new key) a participant in it.
Ruling: **a participant miss is not fatal; only an unresolvable session
is.** `resolveFromSession` now returns silently (try the next identity
source) on a participant miss, exactly like "not running under Claude
Code at all." Only the session itself failing to identify or load
remains loud — two of this decision's three originally measured
wrong-answer cases ("an ended session", and before `CLAUDE_PID` keying,
"the wrong repo"); a misspelled persona is the third, already surfaced
downstream when the caller loads the returned handle, never a
`resolveFromSession` concern.

This also corrects the "distinct per nesting level" claim in the
Decision section above: a Task-tool subagent's own environment shows it
inherits its leader's `CLAUDE_PID` **verbatim**, not a value of its own
(measured: worker subprocess PID 1984899 carried `CLAUDE_PID=1710156`,
identical to its leader; the leader separately measured the same
inheritance live inside a real git commit-msg hook subprocess).
"Distinct per nesting level" is true only for a NESTED `claude` process
(`claude -p` spawning another top-level `claude`, which Claude Code gives
its own PID and its own `CLAUDE_PID`) — it is false for a Task-tool
subagent or a hook subprocess, both of which inherit their parent
session's value unchanged. `CLAUDE_PID` distinguishes concurrent
*sessions*, not nesting depth within one session — a subagent and its
leader belong to the same session and correctly share the same key. This
is why R3's ruling matters beyond the immediate bug: a subagent is
*expected* to share its leader's `CLAUDE_PID`-derived session pointer
while genuinely not yet being a named roster participant under it (it
joins the roster separately, keyed on its own Claude-assigned
`agent_id` — see `internal/hook/subagent_start.go`), so treating a
participant miss as fatal would have made routine subagent spawns loud
failures, not just pre-upgrade rosters. One consequence follows directly:
`CLAUDE_PID` cannot distinguish a subagent from its leader, so nothing in
the participant/agent-identifier path may rely on it for per-agent
distinctness — verified: `subagent_start.go`'s subagent participant record
is keyed on Claude Code's own literal `agent_id` from the hook payload,
never on `CLAUDE_PID`; only its `Parent` field (a display cross-reference,
not a lookup key) uses `CLAUDE_PID`, and that field's whole job is to name
the ONE shared leader, for which sharing the value is correct, not a bug.

`TestSessionID_ConcurrentSessionsDoNotCollide` and
`TestHandleSessionStart_WriteKeyAgreesWithLaterReadKey` pin the two
halves of this together: the former proves two DIFFERENT sessions
(distinct owning processes) get distinct `FindClaudePID` keys; the
latter proves that within ONE session, a SessionStart write and a later
tool-call read agree on the same key — the property the whole pointer
mechanism depends on, previously only assumed from `CLAUDE_PID`'s
documented process-lifetime, never demonstrated end to end.

**R4 — the loud branch had no direct test coverage in `cmd/ethos` or
`internal/hook`.** Both packages force `resolve.UnderClaudeCode` false
process-wide in their own `TestMain`, for an unrelated reason (defeating
the ancestor-walk signal so their many other fixtures can simulate
"genuinely no Claude Code in play" — this suite runs inside a live
Claude Code session, where a real claude ancestor is otherwise
unavoidable). No test in either package turned the flag back on to prove
the loud branch is reachable there, so a green `make check` on the
round 1 branch did not mean what it appeared to mean for the branch's
headline behavior. Added direct coverage in both packages (see the
regression test lists in the R1–R6 commits) that explicitly restores
`resolve.UnderClaudeCode` to `true` and exercises the loud path end to
end. `internal/hook`'s gap closed differently: after R2's fix,
`resolve.Resolve`'s session-lookup path is no longer reachable from
`internal/hook`'s production code at all (`resolveHumanIdentity` passes
`ss = nil`), so there is structurally nothing left to test there.

**R5 — `mission dispatch`/`create` could silently fail to rebind.**
`cmd/ethos/iam.go`'s `resolveSession` collapsed both of this decision's
branches (genuinely no session; a session was expected but
unresolvable) into the same local `errNoSession` sentinel.
`bindDispatchedMission` and `clearClosedSessionBindings` check
`errors.Is(err, errNoSession)` specifically to treat "no session at all"
as their ordinary, silent-skip case (a human running `mission dispatch`
from a plain terminal has nothing to rebind) — with the collapse, that
check was unconditionally true, so a genuine resolution failure under
Claude Code was ALSO silently treated as nothing-to-do. The mission
still dispatched successfully and printed so, but the session's
active-mission sidecar was never rebound, and the next `Agent()` spawn
filed its delegation under the PREVIOUS mission id. Fixed:
`resolveSession` now propagates `resolve.ErrNoSession` as-is when that
is what `resolve.SessionID` returned, instead of swapping it for the
local sentinel; the two remain distinguishable by `errors.Is`, so every
existing "no session at all" handling is unaffected and the genuine
failure now reaches the caller's real-failure branch.

**R6 — the wrong remedy for the wrong cause.** `SessionID` discarded
`retryReadCurrentSession`'s accumulated error in favor of the bare
`ErrNoSession` sentinel, so a permission error or a corrupt pointer file
reported "set `ETHOS_SESSION`" — a remedy that fixes neither. Fixed:
the real cause is now wrapped into the returned error
(`fmt.Errorf("%w (%v)", ErrNoSession, rerr)`); `errors.Is(err,
ErrNoSession)` still holds for every caller that pattern-matches on it,
but the message names what actually went wrong when it is something
other than "no pointer file at all".

Full detail and regression tests: see the round 2 commits on
`fix/session-pid-identity` following this reflection
(`internal/session/store.go`'s `WriteCurrentSession`/
`ReadCurrentSession`; `internal/hook/session_start.go`'s
`resolveHumanIdentity`; `internal/resolve/resolve.go`'s
`resolveFromSession`/`SessionID`; `cmd/ethos/iam.go`'s `resolveSession`).

### Amendment 2026-09-07: the third outcome needed its own sentinel

Two independent local review agents, confirmed by the leader, found that
`SessionID`'s three-outcome contract collapsed to two in practice: `id ==
"", err == nil` (not under Claude Code) and `id != "", err == nil`
(resolved) share the same `err == nil` signal, so a caller checking only
`err == nil` to mean "resolved" silently accepted an empty id for the
first case. Three call sites did exactly this —
`internal/mcp/mission_tools.go`'s `bindDispatchedMission` and
`clearClosedMissionBindings`, and `internal/mcp/tools.go`'s
`resolveSessionID` — each treating a nil error as success and passing the
resulting empty session id on to code that required a non-empty one.
Measured failure: `TestHandleMission_CreateNoSessionInContextWarns`,
extracted via `git archive` and run in a detached process with no
Claude Code ancestor and no `CLAUDE_PID`/`CLAUDECODE` (a CI-representative
environment; the same test passes inside a live Claude Code session,
where `resolve.UnderClaudeCode`'s `TestMain` override was masking the
gap), failed with `"globalRoot and sessionID are required"` instead of
producing the intended "no session in context" warning.

**Decision.** `SessionID` now returns a second, distinct, named sentinel
— `ErrNotUnderClaudeCode` — for the "no session was ever expected" case,
instead of `nil`. The invariant is now mechanical: `err == nil` if and
only if `id != ""`. A caller may trust `err == nil` alone to mean
"resolved," full stop; every other outcome, including the previously
free case, is a distinct non-nil error requiring `errors.Is`.
`resolveFromSession` translates `ErrNotUnderClaudeCode` back to its own
established `(sessionPersona{}, nil)` "try the next identity source"
contract, so `Resolve`'s callers are unaffected. Every caller that
already checked `err != nil` to mean "not resolved" (the three sites
above; also `cmd/ethos/hook.go`'s `runHookCommitTrailers`, which
separately needed a `errors.Is(err, ErrNotUnderClaudeCode)` guard to
keep its silent-vs-loud stderr split correct — see the round 2, item 5
amendment above) now works correctly with no further change, because the
gap they had — treating a nil error as success — is closed at the
source.

**Rejected: keep two outcomes sharing `nil` and audit every caller
instead.** Considered and abandoned once the count reached three
call sites across two files with the identical mistake, independently:
a shared failure mode this consistent across independent call sites is
a contract defect, not three unrelated bugs to patch individually. A
fourth site written the same way before this fix would have repeated
it.

### Amendment 2026-09-07: two test-quality findings, both confirmed by direct measurement

The leader extracted the committed tree at a round 2 commit via `git
archive` into a clean directory and ran the suite detached (`setsid`,
`CLAUDE_PID`/`CLAUDECODE`/`CLAUDE_CODE_SESSION_ID` unset) — a
CI-representative environment this repo's own test suite cannot
otherwise reach, since it is developed and normally tested from inside a
live Claude Code session. Two findings from that run:

**A cache-detection test measured input-insensitivity, not caching.**
`TestFindClaudePID_NoProcessLifetimeCaching` forced `CLAUDE_PID` to the
caller's own parent PID, then to a bogus PID, and asserted the two
`FindClaudePID()` results differed. The invariant reviewer falsified the
test experimentally: re-introducing a `sync.Once` around `FindClaudePID`
and re-running the suite, every assertion still passed, because the test
varies its INPUT between calls while the cache was on the RETURN VALUE —
within one test process the two never diverge regardless of whether a
cache exists. Separately, in a detached/no-claude-ancestor environment,
`walkToClaudeAncestor`'s own fallback is exactly `os.Getppid()` — the
SAME value the forced first call already used — so the test's `NotEqual`
assertion depended on the ambient environment providing a distinguishable
real "claude" ancestor, which a detached process does not have. Fixed by
using two ALWAYS-distinct, genuinely live ancestors (the caller's parent
and grandparent, via the new `process.ParentPID`) instead of a bogus PID
and the walk's fallback, and asserting the SECOND call returns the
SECOND ancestor's PID specifically (not merely "differs from the
first") — a re-introduced cache would return the first PID again and
this assertion would catch it, in every environment.

**Two tests asserted a property of maps, not of the fix.**
`TestStore_CurrentSession_DistinctPIDsDoNotCollide` and (before this
amendment) `TestSessionID_ConcurrentSessionsDoNotCollide` wrote two
literal string keys ("11111"/"22222") and read them back — true of any
key-value store, and true before this fix too. ethos-vqwn was never "the
store collides on distinct keys"; it was "`FindClaudePID` returns the
SAME key for different sessions" (the pre-fix topmost-ancestor walk
collapsing every concurrent session onto the shared "claude daemon run"
PID). The store-level test added nothing beyond the pre-existing
`TestStore_CurrentSession` and is removed.
`TestSessionID_ConcurrentSessionsDoNotCollide` is rewritten to drive the
keys through the real mechanism: two simulated sessions, each resolving
its own `CLAUDE_PID` (the caller's parent and grandparent, again via
`process.ParentPID`), proving `FindClaudePID` itself gives each session
a distinct key before proving the store keeps them separate.

Also added, per the leader's explicit request: a test proving the
SessionStart WRITE key and a later tool-call READ key agree
(`TestHandleSessionStart_WriteKeyAgreesWithLaterReadKey`) — previously
only assumed from `CLAUDE_PID`'s documented process lifetime, never
demonstrated by running the real write path and a real, separate read
path against the same environment.

Going forward, this repo's own suite is insufficient evidence alone for
any claim about behavior with no Claude Code ancestor present — `make
check` passing inside this development environment does not exercise
that state. `.tmp/ci-sim-head.sh` (extract committed `HEAD` via `git
archive`, run detached with the Claude Code env vars unset) is the
gate for any future claim about CI or headless behavior on this branch.

### Amendment 2026-09-07: mission 005 — seven findings from a second local review pass

Three local review agents ran against the round-3 diff after mission
004 closed pass. The operator ruled all seven findings (grown from an
initial four — see the severity correction below, and the E/F/G
additions after it) be closed in a tightly-scoped follow-up mission
(m-2026-09-07-005) before the PR opened, rather than shipped as
follow-up beads.

**Severity correction on B.** The silent-failure reviewer initially
described B as "the only one that returns a wrong answer today." Asked
to trace reachability, it downgraded its own finding: B is a proven
divergence with no naturally reachable trigger, MEDIUM rather than
HIGH. For the inversion to fire, a roster needs a legacy-keyed
participant EARLIER than a preferred-keyed one, and every writer that
can append a participant was traced and found not to produce that
shape on any path a real user passes through — `Store.Create`/
`CreateInCheckout` replace the roster wholesale (cannot append);
`JoinSelf`'s own guard finds the legacy match and reuses that key
(cannot create the duplicate); `Join` with an explicit
`ETHOS_AGENT_ID` uses a handle, never a numeric PID; `SubagentStart`'s
`Join` keys on Claude Code's subagent UUID, never a PID. The only path
that produces the ordering was an INTERMEDIATE BUILD OF THIS BRANCH —
after CLAUDE_PID preference landed but before `JoinSelf` did — a
window that only ever existed on developer machines running mid-branch
builds, not one real users pass through. The fix ships anyway — small,
surgical, no migration or compatibility shim — because DES-074
currently documents a rule one of its three read sites does not
implement, and shipping a false design record is the failure this
whole body of work started with.

**Finding B — `resolveParentLine` was single-pass, not two-pass, despite the
decision text above claiming otherwise.** The prior amendment's item 1
("Reads try the preferred key, then the legacy key") was written to
describe both `resolve.resolveFromSession` and
`internal/hook/subagent_start.go`'s `resolveParentLine` as doing a
genuine two-pass lookup. `resolveFromSession` did; `resolveParentLine`
did not — it ran a SINGLE loop testing both the preferred and legacy
keys against each roster participant in iteration order, so a
legacy-keyed participant appearing EARLIER in the roster incorrectly
won over a correct, preferred-keyed participant appearing LATER —
divergence from the documented invariant, per the severity correction
above, not a live wrong answer.

Fixed to a genuine two-pass — try every participant for the preferred
key first; only on a full miss, retry every participant for the legacy
key — mirroring `resolveFromSession` and `session.Store.JoinSelf`.
New test `TestHandleSubagentStart_ParentLine_PreferredWinsOverEarlierLegacy`
builds a roster with the legacy-keyed record FIRST and the
preferred-keyed record SECOND and asserts the preferred one wins;
verified failing against the pre-fix single-loop code before the fix
landed. The false claim in the decision text above is corrected
in-place with a dated marginal note pointing at this amendment.

**Finding A — round 2's sentinel-distinguishing fix stopped at the CLI
boundary; three `internal/mcp` sites still collapsed both sentinels.**
Also confirmed latent, for the same shape of reason as B: in a healthy
MCP session, `resolve.SessionID` resolves and the collapsed branch is
never entered. Its two reaching arms are a pointer file already broken
(a fault that has already failed `iam` and `mission dispatch` loudly on
the CLI side) and a non-Claude MCP client, where the SILENT branch
(`ErrNotUnderClaudeCode`) firing is CORRECT by design — no session, no
sidecars to clear. The defect is "the one state where the warning is
most needed prints nothing," worth closing on principle, not urgent.
`mission_tools.go`'s `bindDispatchedMission` and
`clearClosedMissionBindings`, and `tools.go`'s `resolveSessionID`, all
still folded `resolve.SessionID`'s two error sentinels
(`ErrNotUnderClaudeCode` vs `ErrNoSession`) into one generic message —
exactly the collapse round 2 fixed at the CLI's equivalent call sites
(`cmd/ethos/mission.go`'s `bindDispatchedMission`,
`cmd/ethos/iam.go`'s `runHookCommitTrailers`) but never carried over to
the MCP surface. `clearClosedMissionBindings` was the worst of the
three: it returned `nil` (fully silent, no warning at all) for BOTH
cases, so an unresolvable session under Claude Code could leave a
closed mission's sidecar in place — still stamping trailers under a
closed mission ID — with no signal to the caller whatsoever.

All three now mirror their CLI counterparts: silent for
`ErrNotUnderClaudeCode` (the ordinary, no-session-expected case), and a
warning naming the real cause for `ErrNoSession` (wrapped via `%w` so
`errors.Is` still holds through the chain). Fixing this exposed a
second, independent gap: `internal/mcp/integration_test.go`'s
`TestMain` was missing the `resolve.UnderClaudeCode = func() bool {
return false }` override every other package's `TestMain` already
carries. Its absence was invisible before this fix, because the pre-fix
code collapsed both sentinel paths to the same message regardless of
which one actually fired; once the two diverged,
`TestHandleMission_CreateNoSessionInContextWarns` started failing with
the LOUD message and a real ambient PID, because the ancestor walk was
still finding this repo's own live Claude Code ancestor despite the
env vars being stripped. Fixed alongside. New regression tests at each
of the three sites force `resolve.UnderClaudeCode = true` and assert
the message names the real cause, not the generic absent-session text;
verified each fails against the pre-fix code via temporary `git
stash`.

**Finding C — a contract comment stated the inverse of the truth.**
`cmd/ethos/iam.go`'s `resolveSession` carried a comment (predating the
`ErrNotUnderClaudeCode` sentinel) claiming `resolve.SessionID`
distinguishes "genuinely no session" via a NIL error. True before the
sentinel commit; false after — nil now means resolved, and
"genuinely no session" is `ErrNotUnderClaudeCode`, a non-nil sentinel
that falls through the function's switch (matching neither case) and
is converted to `iam.go`'s own local `errNoSession` by the final
`if sessionID == "" { … }` check. Documentation only, no production
code changed; rewritten to name all three outcomes (resolved /
`ErrNotUnderClaudeCode` / `ErrNoSession`) against the actual control
flow.

**Finding D — the `err == nil iff id != ""` invariant was contingent on
a constant, not structural.** `SessionID`'s own doc comment states this
as a mechanical invariant every caller may rely on, but
`retryReadCurrentSession`'s loop runs `pointerRetryAttempts - 1` times;
at the shipped value of 10 that is 9 iterations and the invariant
holds, but at 1 (or less) the loop body never executes, and `lastErr` —
seeded to `nil` — was returned unchanged, so `SessionID` would silently
return `("", "walk", nil)`: a resolved-looking empty ID alongside a nil
error, breaking the exact invariant every caller pattern-matches on.
Nothing exercises this today (the constant is fixed at 10), but a
guarantee that is only true at one specific value of an internal
constant is not the mechanical invariant the doc comment claims —
"structural" was the operator's explicit preference over an added
guard. Fixed by seeding `lastErr` from the caller's own initial read
error (`firstErr`) instead of `nil`, so a zero-iteration retry still
surfaces a non-nil error regardless of the constant's value.
`pointerRetryAttempts` (and `pointerRetryDelay`, kept alongside it) is
converted from `const` to `var` so a test can drive the attempts count
to 1 directly and prove the invariant structurally rather than only at
10. New test `TestSessionID_InvariantHoldsAtMinimalRetryBudget`;
verified it fails to even compile against the pre-fix code (a `const`
cannot be reassigned) — a stronger form of the required negative check
than a runtime failure would have been.

**Finding E — `WriteCurrentSession` never validated its input.** The
atomic-write path (`CreateTemp`, `WriteString`, `Chmod`, `Sync`,
`Close`, `Rename`, with `os.Remove` on every error path, sync before
close, rename last) was verified complete step by step — that half of
the earlier "blank pointer file" open question is genuinely closed, and
`ReadCurrentSession` is confirmed the file's only reader. The gap was
the OTHER side: the function never inspected `sessionID` or
`claudePID` at all. A blank `sessionID` produced a permanently blank
pointer file at exit 0. A blank `claudePID` is worse:
`filepath.Base("")` returns `.`, so the rename destination would
resolve to the current-session directory itself. Reachable only
through `ethos session write-current`, a hidden debug command with no
callers anywhere in the tree — operator error, not a live path. Fixed
with a guard on both arguments (`TrimSpace`, not a bare non-empty
check, so whitespace-only counts as blank too). Same class, unfixed
sibling: `resolve.go`'s `SessionID` gated `ETHOS_SESSION` on `sid !=
""` without trimming, so `ETHOS_SESSION="  "` was accepted as a
literal session id; fixed alongside as part of finding G, below, since
both landed in the same file. New test
`TestStore_WriteCurrentSession_RejectsBlankArgs`; verified it fails
against the pre-fix code via temporary `git stash`.

**Finding F — `ETHOS_TEST_NOT_UNDER_CLAUDE_CODE` silently disabled the
loud branch in production.** The escape hatch's own doc comment argued
it was safe because it is negative-only — it can force
`UnderClaudeCode()` to false but never fabricate a true, so it "cannot
be used to fabricate a session context." True, and beside the point:
SUPPRESSING the loud branch is itself the wrong-answer-at-exit-0 shape
this whole decision exists to close. Set in a real environment, it
makes `SessionID` return `ErrNotUnderClaudeCode`, `resolveFromSession`
translates that to a clean, silent empty result, and the caller falls
through to the git/OS identity — restorable by one environment
variable, with zero logging. Minimum fix: an unconditional stderr line
whenever the hatch is honored, so it can never be silent again. A
stronger guard — refusing to honor the variable at all outside a test
binary, e.g. via `testing.Testing()` — was considered and rejected:
this repo's own subprocess-based CLI tests
(`cmd/ethos/mission_test.go`, `subprocess_test.go`'s
`withForcedNotUnderClaudeCode`) set this variable on a REAL, separately
exec'd `ethos` binary, not the test binary itself, so `testing.Testing()`
would read false inside the very process the variable is meant to
affect and break every one of them. New test
`TestUnderClaudeCode_ForceHatchLogsToStderr`; verified it fails against
the pre-fix code via temporary `git stash`.

**Finding G — the remedy string named a command that does not fix the
problem, and in fact cannot fix it for most callers.** `ErrNoSession`'s
message read "set `ETHOS_SESSION=<id>`, or run `ethos session start`."
`runSessionStart` (`cmd/ethos/session.go`) creates a roster and prints
an `export ETHOS_SESSION=...` line to stdout; it never calls
`session.Store.WriteCurrentSession` — only the `SessionStart` hook and
the hidden `write-current` command do. A user who hit `ErrNoSession`,
ran the suggested command, and retried got: `ETHOS_SESSION` still
unset (the export went to stdout, unevaluated; a sibling process
cannot mutate its caller's env), the pointer file still broken, another
`pointerRetryAttempts`-worth of retry latency, and the IDENTICAL error.
The only lasting effect was an orphan roster nothing points at.
Sharper still: `SessionID` returns `ErrNoSession` only when
`underClaude` is true, so the sole audience for this string was
"under Claude Code, pointer broken" — precisely the population `ethos
session start` cannot help. What actually remedies it, by sub-case:
missing/blank pointer (the dominant case, `SessionID`'s own path) →
restart the Claude Code session (or `/clear`) so `SessionStart`
re-fires; `ETHOS_SESSION` naming a dead roster → `eval "$(ethos session
start)"` — the `eval` is mandatory, the bare command is inert, and even
with `eval` this fixes only that shell's own environment, never a
process Claude Code spawned (which inherits Claude Code's environment,
not the terminal's). `ErrNoSession`'s base message no longer bakes in
any remedy; `restartPointerRemedy` and `deadRosterRemedy` are composed
per call site, using `SessionID`'s own `source` return value
(`SessionSourceEnv` vs `"walk"`) to pick the right one for
`resolveFromSession`'s dead-roster branches. New tests:
`TestResolveFromSession_DeadRosterRemedy_EnvSourced`, `_WalkSourced`;
verified both fail against the pre-fix code via temporary `git stash`.

CHANGELOG.md gains entries for finding B (a subagent could previously
be told it reports to a stale or wrong persona), finding E (the hidden
`write-current` command could corrupt the current-session directory or
write a permanently blank pointer file), and a correction to the
original DES-074 entry's own remedy claim (finding G — the original
text repeated the same inert `ethos session start` advice this
amendment corrects). Findings A, C, D, and F are latent, diagnostic, or
documentation-only and are not independently user-visible today.

### Amendment 2026-09-07: PR #502 review — the fallback recreated the original collision

A code-review pass on the PR opened for this decision found that
`resolve.SessionID` (`internal/resolve/resolve.go`) still keyed the
SESSION-POINTER lookup on `process.FindClaudePID()`, which falls back to
`walkToClaudeAncestor` — the topmost-claude-ancestor walk — whenever
CLAUDE_PID is absent or fails corroboration (`internal/process/tree.go`).
That walk returns the shared "claude daemon run" PID on every concurrent
Claude Code session on a host — the exact measurement that opens this
decision's Context section. A caller under Claude Code with no usable
CLAUDE_PID (an older harness predating 2.1.234, a transient
process-table read failure, or ancestry deeper than the walk's own
10-level cap) therefore fell through to the walk-derived key and could
resolve a plausible but UNRELATED session — reconstructing the
wrong-answer-at-exit-0 shape this whole decision exists to close, through
the one fallback path the original Decision and Amendment text left
unexamined.

**The distinction that matters.** The walk remains legitimate for two
narrower uses this decision already relies on: `LegacyClaudePID`'s
participant-lookup tolerance (a roster fallback, non-fatal on a miss —
round 2 R3) and `UnderClaudeCode`'s own ancestor-presence check (a
boolean signal, not a lookup key). Neither carries the SESSION-POINTER's
risk, because neither trusts the walk's PID value as an identity to read
someone else's state by. It is NOT legitimate as the pointer's lookup
key, because the value it returns may name a session that belongs to a
different, unrelated caller entirely.

**Decision.** `resolve.SessionID` no longer calls `process.FindClaudePID`
at all. It calls a new, narrower function,
`process.ClaudePIDFromEnvCorroborated`, which performs the same
CLAUDE_PID-plus-live-ancestry corroboration `FindClaudePID` already did,
but returns `ok=false` — with NO walk fallback — when CLAUDE_PID is
absent or uncorroborated. When `SessionID` gets `ok=false` while running
under Claude Code (`process.UnderClaudeCode` true), the session is
UNRESOLVABLE: it returns `ErrNoSession` with a new remedy
(`uncorroboratedPIDRemedy` — upgrade to Claude Code 2.1.234+, or set
`ETHOS_SESSION=<id>` directly) rather than falling back to a walk-derived
key it cannot vouch for. `FindClaudePID` itself is unchanged for its
OTHER callers (pointer/roster writes, participant self-keying), where a
stale walk-derived key is either self-healing (the next `SessionStart`
rewrites it) or non-fatal (a participant miss, round 2 R3) — those
callers keep the fallback; only the session LOOKUP loses it, since a
lookup has no equivalent safety net.

The source string `SessionID` returns alongside a resolved ID is renamed
from `"walk"` to `SessionSourcePID`, since no walk remains in this
function's own resolution path — the pre-fix name described a mechanism
this fix removes.

**Rejected: fix the walk to stop at the nearest `claude` ancestor,
reconsidered.** Same objection this decision's own Rejected section
already raises against that approach in general: it does not eliminate
the wrong-answer risk, it only narrows how often the walk's result
happens to coincide with the caller's own session, which is exactly the
kind of probabilistic, "usually correct" mechanism DES-074's operator
ruling (§ "Rejected: leaving the fallback silent") already rejected in
favor of failing loud.

Regression test:
`TestSessionID_UncorroboratedPIDDoesNotFallBackToSharedWalkKey`
(`internal/resolve/resolve_test.go`) constructs the precise failure
state — under Claude Code, CLAUDE_PID absent, a pointer file already
present under the walk-derived key and naming an unrelated session — and
asserts `SessionID` returns `ErrNoSession` rather than that session's ID;
verified failing against the pre-fix code (it silently resolved the
unrelated session). `TestHandleSessionStart_WriteKeyAgreesWithLaterReadKey`
and `TestHandleSessionStart_WriteKeyAgreesWithLaterReadKey`'s sibling
tests in `internal/resolve` (the "corroborated CLAUDE_PID resolves via
the pointer file" subtest of `TestSessionID`, and
`TestResolveFromSession_DeadRosterRemedy_WalkSourced`) were updated to
supply a corroborated CLAUDE_PID rather than relying on the now-removed
walk fallback, since that is the only path left through which a non-env
session resolves.

### Amendment 2026-09-07: the upgrade-migration gap the participant-keying amendment did not cover

Bugbot, reviewing the PR opened for the amendment directly above, found
that its fix reopens a migration gap the participant-keying amendment
(two amendments up) never anticipated, because that amendment was
written while `SessionID` still had a walk fallback to fall into.

**The gap.** The participant-keying amendment's item 3 states a session
that predates an ethos upgrade "self-heals" via the roster's legacy-key
tolerance — true, but scoped to the ROSTER's participant lookup, which
presumes `SessionID` already resolved a session id to load a roster
from. It says nothing about whether the SESSION-POINTER lookup itself
resolves for such a session, because at the time it was written,
`SessionID` still fell back to the topmost-ancestor walk whenever
`CLAUDE_PID` was absent or uncorroborated — a fallback that could
(dangerously) find a pre-upgrade session's pointer, since that pointer
was itself written under the walk-derived key. The very amendment
directly above removes that fallback, to close the cross-session
collision it caused. A side effect neither amendment names directly:
an in-flight session whose on-disk pointer file is still walk-keyed —
any session that has not had `SessionStart` re-fire (no restart, no
`/clear`) since the upgrade — now gets `ErrNoSession` on its very next
call, not a resolved session with a legacy-keyed roster miss. The
"Measured" paragraph in the participant-keying amendment shows a
session where the POINTER resolved and only the PARTICIPANT lookup
missed; that measurement's own session had already had its pointer
rewritten under an intermediate CLAUDE_PID-preferring build sometime
before the roster-keying gap was found, which is a specific history,
not the general case a plain upgrade produces.

**Ruling (operator, 2026-09-07).** Keep the no-fallback behavior from
the amendment directly above. Do not add a legacy-key fallback to the
session-POINTER lookup, even a narrowed one. The natural-seeming middle
path — read the legacy-keyed pointer, then verify the roster it names
has a `repo` field matching the caller's current repo before trusting
it — was considered and rejected: it is defeated by the case of two
concurrent Claude Code sessions in the SAME repo, which both walk to
the identical `LegacyClaudePID` value (the shared "claude daemon run"
process), so BOTH would have the SAME repo field and a repo check
cannot tell them apart. Whichever session's pointer happened to be
written to that shared key most recently would win, arbitrarily,
handing the OTHER session a plausible but wrong identity with exit
status 0 — reconstructing this decision's original wrong-answer defect
through the one door believed closed, rather than the narrower
"neither one resolves cleanly" gap this amendment describes. A
mechanism that is reliable or raises a clear error, never a plausible
guess (this decision's own operator ruling, above), is worth a one-time
loud failure per in-flight session at upgrade over a check that mostly
works and fails exactly when two sessions in the same repo are the
scenario in play — the routine case for agents working across
worktrees and sibling repos.

**What actually happens, precisely.** An in-flight session at the
moment of upgrade calls `whoami`, `iam`, `mission dispatch`/`claim`, or
commits (the trailer hook): `SessionID` looks up the pointer file under
the corroborated `CLAUDE_PID` key, finds nothing (the file exists only
under the old walk-derived key), and returns `ErrNoSession` with
`restartPointerRemedy` — "restart the Claude Code session (or run
`/clear`) so `SessionStart` re-establishes the session pointer." This is
loud (non-zero exit, or a stderr diagnostic for the commit-trailer hook,
which never blocks a commit), safe (no wrong answer is ever returned),
and self-healing (the very next `SessionStart` — a restart or `/clear`
— rewrites the pointer under the new key, and the session behaves
normally from then on, including the roster tolerance the
participant-keying amendment already provides). It is a one-time,
per-session cost at upgrade, not a lasting break.

**Documentation corrected.** Both `CHANGELOG.md`'s original DES-074
entry and the participant-keying amendment's item 3 (marginal note
added above) stated or implied a stronger "keeps working" guarantee for
in-flight sessions than the shipped code provides once the amendment
directly above lands. `CHANGELOG.md`'s entry is corrected in place to
scope the "keeps working" claim to the roster/participant mechanism and
add the pointer-lookup upgrade note (restart or `/clear`, loud and
self-healing) as its own, explicitly user-visible bullet — this was a
documentation defect the leader is required to fix regardless of
severity, per this repo's own "no pre-existing issue" standard, not
merely a nice-to-have.

Regression test:
`TestSessionID_InFlightSessionPointerFailsLoudUntilSessionStartRefires`
(`internal/resolve/resolve_test.go`) constructs an in-flight session
exactly as an upgrade leaves it — a corroborating `CLAUDE_PID` distinct
from `LegacyClaudePID`, a roster whose participant is legacy-keyed (so
the roster tolerance would succeed if reached), and a pointer file
written ONLY under the legacy key — and asserts `SessionID` still
returns `ErrNoSession` with the restart remedy, never the session's id.
This is a pinning test for ratified behavior, not a fix-driven one: it
passes against the code as it already stands after the amendment
directly above, and exists so a future, well-intentioned fallback
cannot reintroduce the collision this ruling explicitly rejected without
first deleting or rewriting this test.

## DES-075: Mission storage layer model — what the repo tree, the global tree, and their locks are each authoritative for (AMENDED 2026-09-08)

**Context.** `internal/mission` (DES-054 phase 1) stores a mission in one
of two trees and locks it through one of two lock files, and different call
sites picked between them inconsistently. Cluster-2 triage on 2026-09-07
(ethos-6adb, ethos-ouy9, ethos-lj4k, ethos-5yej, ethos-7tqd) found the same
ambiguity underneath four of the five bugs filed against this package. This
ADR names what each layer and each lock is for, so the fix for each bead
follows from one decision instead of four separate patches.

**The two trees.**

- **Repo tree** — `<repoRoot>/.punt-labs/ethos/missions/<id>/contract.yaml`
  (`repoMissionsDir`, `paths.go:120`). Git-tracked. Active when a Store is
  built with `NewStoreWithRoots(repoRoot, ...)` and `repoRoot != ""` — the
  case for every CLI/MCP invocation run from inside a repo checkout
  (`missionStore`, `cmd/ethos/mission.go:56`).
- **Global tree** — `<globalRoot>/missions/<id>.yaml`
  (`globalMissionsDir`, `paths.go:131`), `globalRoot` always
  `~/.punt-labs/ethos`. Flat, one namespace shared by every repo on the
  machine. Not git-tracked.

**Decision 1 — which tree is authoritative for NEW writes.** The repo
tree, whenever one is in scope (`writeLayer`, `paths.go:198`). The global
tree is written only when no repo is in scope at all (`ethos mission
create` run outside any git checkout) — a rare, already loudly-warned path
(`warnIfGlobalFallback`, `cmd/ethos/mission.go:103`). This was already the
code's behavior; this ADR just names it as the standing decision so the
next change doesn't have to re-derive it.

**Decision 2 — which tree is authoritative for READS of an existing
mission.** Repo-first, global-fallback (`resolveLayer`, `paths.go:172`).
Unchanged by this round. The fallback exists so a mission created before a
repo adopted the two-tree layout (or created with no repo in scope) is
still loadable.

**Decision 3 — which tree is authoritative for the write-set conflict
SCAN (`Create`'s admission control).** The repo tree, once a repo is in
scope, PLUS any open global-tree mission this repo's own audit trail
references — never the global tree unconditionally. This is the fix for
**ethos-6adb**; see the round-2 amendment below for why "never the global
tree" (this ADR's original wording) needed correcting to the version
above.

The global tree cannot be scoped by repo: it is flat, and measured
2026-09-07 showed 841 contracts on the local host, 19 open, ZERO carrying a
populated `repo:` field. A per-entry repo filter is therefore not available
today, and back-filling it retroactively does nothing for the 841 already
on disk. Since Decision 1 means a repo's own new missions never land in the
global tree once a repo is in scope, the global tree's remaining open
entries are guaranteed to be either pre-two-tree-adoption leftovers or
another repo's missions entirely — comparing a new mission's write_set
against them can only produce false conflicts, never a real one. Excluding
the global tree from the scan (not filtering it) is therefore not a loss of
coverage, only a removal of noise. `conflictScanIDs` (`store.go`)
implements this: it calls the repo-tree listing directly and skips the
union with the global tree that `List()` still performs for the general
"show me every mission" case (CLI `mission list`, `MatchByPrefix`), which
is unaffected — a stale cross-repo prefix match was never this cluster's
complaint and stays out of scope here.

**Decision 4 — which lock is authoritative for delegation-directory
access.** The REPO-TIER per-mission lock —
`AcquireMissionLock`/`AcquireMissionLockExclusive`
(`<repoRoot>/.punt-labs/ethos/missions/<id>/.lock`, `delegation.go:467`) —
not the GLOBAL per-mission lock (`Store.withLock`/`Store.lockPath`,
`<globalRoot>/missions/<id>.lock`, `store.go:424`). This is the fix for
**ethos-lj4k**; the round-2 amendment below tightens HOW that lock is
acquired (unconditionally, never skipped, never bypassed on error) after
review found two ways the round-1 version could still skip it.

Both locks exist and both stay: the global lock still serializes every
Store method that mutates a contract file (`Create`, `Update`, `Close`,
`Abandon`, `AdvanceRound`, ...) exactly as before — this decision does not
touch that. The repo-tier lock is the one thing every actor that reads or
writes `delegations/` under a mission already used — `dispatchTierB`
(shared, `internal/hook/pretooluse_dispatch.go:273`) and `Store.Close`'s
delegation sweep (exclusive, `store.go:1063`) — except `Store.Abandon`,
whose `countDelegations` check ran under the GLOBAL lock only. Two
different lock files gave two independent critical sections over the SAME
directory: `dispatchTierB` could write a delegation record in the window
between `Abandon`'s read and its commit, with neither side excluding the
other. The fix nests `AcquireMissionLockExclusive` INSIDE the existing
`s.withLock` for the delegation-count-and-commit sequence
(`withAbandonDelegationLock`, `store.go`) — global lock outer, repo-tier
lock inner, matching the acquisition order `delegation.go`'s own doc
comment already prescribed (`global → repo → per-mission(shared) →
per-delegation(exclusive)`) but that no call site had actually exercised
until now. No existing call site acquires the repo-tier lock and then
tries to acquire the global lock, so this ordering introduces no reversal
and no new deadlock risk; `Store.Close`'s existing repo-tier acquisition
runs strictly AFTER releasing the global lock (sequential, not nested),
which is a subset of the same order, not a conflicting one. Rejected: just
adding `AcquireMissionLockExclusive` to `Abandon` as a second, independent
acquisition alongside the existing global lock with no defined order
between the two — safe today only by accident of which call sites exist,
and the first future call site that reversed the order would deadlock
silently. Naming one fixed order removes that trap.

**Decision 5 — what a linked worktree's own `.punt-labs/ethos/` means.**
Inert. `FindRepoEthosRoot` and `StoreRepoRoot` (`internal/resolve`) both
resolve through the git common-dir to the MAIN work tree, so a linked
worktree's own `.punt-labs/ethos/` — even one `ethos enable` deposited
directly into that checkout — is never read or written by the mission
store, the identity/team/role layered stores, or session resolution. This
is deliberate (ethos-yofr) and is what lets a worktree see its parent
session's missions and rosters without any special-casing. It is also
undocumented as a NAMED state today: a worktree's own store directory
silently means nothing rather than erroring or being explicitly labeled
inert. This ADR is that naming; per the mission triage
(ethos-5yej), no behavior changes — the bead closes once this section
lands.

**Non-goal for this round.** Populating `Contract.Repo` at create time
(so a future repo filter on the global tree becomes possible) is
deliberately deferred. `Contract.Repo` is set from two entry points — the
CLI (`cmd/ethos/mission.go`, in scope for this round) and the MCP server
(`internal/mcp/mission_tools.go`, NOT in scope for this round) — and
`ApplyServerFields`'s whole contract with its callers (see its own doc
comment) is that CLI and MCP stay in lockstep for every server-controlled
field. Setting `Repo` from only one of the two entry points would make the
field's presence depend on which surface created the mission, a new and
worse inconsistency than the one being fixed. A follow-up that widens the
write-set to include the MCP path can do this properly.

**Consequence for ethos-ouy9.** Independent of the layer model above,
`writeContract` (`store.go`) — the function that persists every mission
contract, in whichever layer `Decision 1` selects — had no `Sync()` before
its `Rename`, unlike its sibling `session.writeRoster`
(`internal/session/store.go:653`), which does. `writeContract` now matches
`writeRoster`'s discipline (`Sync` before `Close`, temp removed on every
error path, a failed fsync propagated). `Store.Create` additionally reads
the just-written contract back before returning success. Neither of the
two other candidate causes the ethos-ouy9 triage note raised (a
create/read layer mismatch; a create landing in a different repo's tree)
is ruled out by this ADR's decisions — Decision 1/2 already prevent both
for any repo-scoped invocation — but the missing fsync is a real,
independently-reproducible durability gap on its own, fixed regardless of
which candidate explains the original 2026-08-15 vox incident.

**Consequence for ethos-7tqd.** Out of scope for the layer model itself —
this is an active-mission-sidecar attribution bug (`internal/mission/active.go`),
not a storage-layer ambiguity — but it was reproduced and triaged in the
same pass. `ethos mission dispatch`/`create` now print the binding they
take (`cmd/ethos/mission.go`) so a leader sees "session bound to mission
X" rather than discovering it later via a misattributed commit or a
blocked abandon. The deeper fix — bind at worker-spawn time instead of at
dispatch time, per the triage note's stated preference — requires changing
`internal/hook/pretooluse_dispatch.go`'s dispatch/attribution logic, which
is outside this mission's write-set (`internal/mission/**`,
`internal/resolve/resolve.go`, `cmd/ethos/mission.go`, `DESIGN.md`,
`CHANGELOG.md`). Flagged for a follow-up mission scoped to
`internal/hook/**`.

### Amendment 2026-09-08: PR #508 review round 2 (findings F1–F6)

Six findings on the round-1 implementation of this ADR's decisions —
Qodo's inline review plus Copilot, requested explicitly rather than
trusting a transient CLEAN/zero-threads state the PR briefly showed. Four
were High; two of those were defects in the fixes themselves, one was a
consequence of a Decision this ADR had already accepted, one was a genuine
correctness gap in the same fix. All six are closed in this amendment;
nothing here reverses round 1's decisions, but Decision 1/3's "exclude the
global tree" needed correcting to "exclude the global tree except what
this repo's own history claims," below.

**F1 (High) — `withAbandonDelegationLock` fell back to an UNLOCKED `fn()`
call when `AcquireMissionLockExclusive` itself failed to acquire.** That
reopened the exact race Decision 4 exists to close, on the fix's own error
path: a lock you proceed without on failure is not a lock. Fixed by
removing the fallback entirely — a lock-acquisition failure now returns an
error from `Abandon` and mutates nothing, the same fail-closed shape
already applied to the `repoRoot == ""` guard earlier in the same
function ("silently trusting the absence of evidence as evidence of
absence" — djb's probe, cited in that guard's own comment).

**F3 (High) — the SAME function also skipped acquisition outright when the
repo-tree per-mission directory did not yet exist** (`missingRepoTreeDir`),
reasoning that no `dispatchTierB` could be racing under a directory that
does not exist. That reasoning is a TOCTOU: a `dispatchTierB` starting
after the stat check runs `AcquireMissionLock`, whose own `MkdirAll`
creates the very directory the check found absent, takes the shared lock,
and writes a delegation — after `Abandon`'s unlocked `countDelegations` had
already returned zero. Fixed together with F1: `withAbandonDelegationLock`
now acquires `AcquireMissionLockExclusive` unconditionally, every time,
with no directory-existence shortcut. Its own `MkdirAll` creating a
repo-tree directory for a mission that lives entirely in the legacy global
tree is a harmless side effect — `resolveLayer` decides a mission's layer
by whether `contract.yaml` is present, never by whether the directory
itself exists, so this does not change which layer any mission reads from
or writes to. `TestStore_TwoRoot_CloseStaysInItsLayer`'s sibling assertion
for `Abandon` was updated to check for the absence of `contract.yaml`
specifically, not the absence of any repo-tree footprint at all.

Both F1 and F3 are covered by
`TestStore_Abandon_ExcludesConcurrentDelegationWrite` (directory
pre-existing), its `_NoPriorRepoTreeDir` sibling (F3's exact starting
condition), and `TestStore_Abandon_ZeroCountToCommitWindowIsAtomic` (pins
F3's literal phrase — "the window between `countDelegations` returning
zero and `writeContract` committing" — via a new test-only seam,
`abandonAfterZeroCountHook`, invoked from inside `Abandon`'s own closure
between the zero count and the terminal commit). All three were confirmed
failing against the round-1 code before this amendment: the concurrency
tests reproduced the writer succeeding with no blocking at all, and the
targeted hook-based test reproduced `Abandon` committing `StatusAbandoned`
with a nil error while a delegation landed unblocked during its (unlocked)
execution.

**F2 (High) — `writeContractFile` (the ethos-ouy9 fix) synced the temp
file's contents before `Rename` but never synced the CONTAINING DIRECTORY
after it.** A file's contents being durable is not the same guarantee as
the directory entry that names it being durable — POSIX `rename(2)` is a
metadata change to the directory, and that change needs its own `fsync` to
survive a crash. Without it, ethos-ouy9's exact symptom (`mission create`
reports success; the contract is absent on recovery) remained reachable
through a narrower window than before, but still open. Fixed by adding
`syncDir`, called on `filepath.Dir(dest)` after every successful rename in
`writeContractFile` (and therefore in `restoreContract`, which shares the
same helper). Split by build tag: the POSIX implementation
(`syncdir_unix.go`) opens the directory and calls `Sync()`, the standard
mechanism; the Windows implementation (`syncdir_windows.go`) is a
documented no-op, because NTFS does not expose an `os`-package-reachable
equivalent to fsync-on-a-directory-handle the way POSIX does, and Windows
is not a supported/shipped target for this module (no release binary, no
CI job — GOOS=windows GOARCH=amd64 must still compile, which it does).
`syncDir` is a package-level `var`, not a plain `func`, specifically so
`TestWriteContractFile_SyncDirFailurePropagates` can inject a failure
deterministically — a real directory-fsync failure is not something a
portable test can otherwise engineer.

**F4 (High) — the round-1 fix for Decision 3 excluded the global tree
from the conflict scan UNCONDITIONALLY, and that traded one correctness
bug for another.** `conflictScanIDs` scanning the repo tree only means an
open mission genuinely belonging to THIS repo — created before the repo
adopted two-tree storage, still open, never migrated — became invisible to
admission control: a new mission could claim an overlapping `write_set`
against it and nothing would stop it. This is the leader's own call to
make (not the worker's), and the leader's read, on reflection: neither
"scan the global tree in full" (reopens ethos-6adb) nor "refuse every
Create anywhere on the machine while any open global-only mission
exists that could belong to any repo" (an operationally disproportionate
response — 19 open legacy contracts existing SOMEWHERE would halt every
repo's mission system, not just the one with un-migrated debt) is the
right shape. The actual fix uses a signal that already exists and is
already reliable: `repoMissionIDs` (`migrate.go`), the exact mechanism
`ethos mission migrate` uses to decide which legacy missions belong to
this repo — it scans
`<repoRoot>/.punt-labs/ethos/sessions/*/audit.jsonl` for `contract_id`
references, which is a real per-repo ownership signal even though
`Contract.Repo` is not (per Decision 3's original measurement: 0 of 841).
Mission IDs are allocated from one shared, global, strictly-increasing
daily counter, so an ID one repo's audit trail references can never
collide with an ID a different repo's own trail references — the
ownership sets cannot be confused across repos. `conflictScanIDs` now
scans the repo tree PLUS every open global-tree mission this repo's own
audit trail names; a global mission absent from that trail stays excluded,
preserving ethos-6adb's fix exactly.

Cost note carried into the ADR proper: this scan reads every
`audit.jsonl` line under every session this repo has ever recorded, on
every `Create` — the same cost `mission migrate` already accepts for an
operator-invoked one-off, now paid on a much more frequent path. Accepted
for now (creates are infrequent relative to tool calls); worth revisiting
if a repo's session history grows large enough to make the latency
visible.

`TestStore_CreateDetectsSameRepoUnmigratedGlobalConflict` covers F4
directly (an un-migrated same-repo mission blocks an overlapping create)
and re-asserts ethos-6adb's original property in the same test (an
un-referenced foreign mission does not). Confirmed failing against the
round-1 `conflictScanIDs` (no audit-trail scan) before this amendment.

**F5 (Copilot) — a goroutine in the lj4k concurrency test called
`require.NoError`,** which invokes `t.FailNow()` on failure; `t.FailNow`
must run on the goroutine executing the test function itself, not one the
test spawned, or the test can hang instead of failing cleanly. Fixed by
routing every goroutine's error back over a channel and asserting on it
from the main test goroutine only — the pattern every concurrency test
added in this amendment (and round 1) now follows uniformly.

**F6 (Copilot) — a test forced an open-temp-file failure via
`os.Chmod(dir, 0o500)` on the containing directory.** `os.Chmod` on
Windows only toggles the `FILE_ATTRIBUTE_READONLY` bit and does not block
new-file creation inside a directory, and the same technique is
unreliable under a root-running test process on POSIX (root bypasses DAC
permission checks entirely) — both are real ways this test could go
flaky, the latter more likely in practice (containerized CI often runs as
root) than the former (this package's tests are `!windows`-tagged, so the
Windows case was already inert, but the root case was not). Fixed by
forcing the failure through a NONEXISTENT containing directory instead — a
bare path-resolution `ENOENT`, which fails identically regardless of
platform or privilege level.

### Amendment 2026-09-08: PR #508 review round 3 — G1 undercuts F4's foundation, G2/G3 correct the syncDir contract

**G1 (High) — `repoMissionIDs` (the ownership signal round 2's F4 fix
depends on) read only the frozen pre-DES-058 legacy file, never a sealed
chunk.** This is worse than "misses some audits": `ethos audit seal` runs at
every pre-commit in an ethos-enabled repo (the sealed chunks travel in the
same commit as the work), moving session audit content OUT of a flat
`audit.jsonl` and into dated `audit-<first>-<last>.jsonl` chunks
(`internal/audit/names.go`) on essentially every commit. `repoMissionIDs`
looked for a file literally named `audit.jsonl` inside
`<repoRoot>/.punt-labs/ethos/sessions/<dir>/` — the SEALED zone — but that
exact name is the pre-DES-058 legacy shape (see
`internal/hook/audit_monotonic.go`'s `sessionLegacyPath`), not what a
sealed chunk is ever named. For any repo whose sessions have ever sealed —
the normal state of an actively-committed repo, not an edge case —
`repoMissionIDs` returned an empty (or near-empty) set, silently
reopening F4's gap for the exact same-repo un-migrated missions it was
written to catch. `mission migrate` shares the identical blind spot,
since it uses the same function; this was not only round 2's problem.

Fixed by having `repoMissionIDs` read all three sources a session's audit
trail can live in: sealed chunks (`audit.ScanSealedDir` +
`collectContractIDs` per chunk file), the frozen legacy file (unchanged),
and the live tail of a session that has not sealed yet
(`audit.LiveSessionsDir`/`audit.LiveAuditPath`, the gitignored local
zone) — reusing `internal/audit`'s existing exported primitives rather
than reimplementing chunk-name parsing. Deliberately does NOT reuse
`internal/audit.Watermark`/dedup-by-identity machinery
(`internal/hook/audit_read.go`'s `sessionUnionLines`, the canonical full
audit reconstruction): that logic exists to produce an exactly-once,
time-ordered reconstruction for display, which this function does not
need — it only accumulates a SET of `contract_id` strings, so a mission ID
appearing in more than one source (a sealed chunk and an overlapping live
tail, say) collapses for free via the map. This keeps the fix a fraction
of the size the full union-read pattern would have been.

The scan is deliberately best-effort at different granularities for
different sources: a corrupt or unclassifiable sealed session directory
is warned to stderr and skipped, since this function's result now feeds
`Store.Create`'s admission control on every call and a single damaged
HISTORICAL chunk (`ethos audit quarantine`'s job to fix, asynchronously)
must not block every future mission create in the repo; the frozen legacy
file and the live tail keep the pre-existing, stricter contract (a
genuine read error still propagates), since each is a single,
currently-relevant file rather than an unbounded pool of history.

`TestStore_CreateDetectsSameRepoConflictViaSealedAuditChunk` covers G1
directly — writes a real sealed-chunk-shaped file (via
`audit.SessionChunkFile`) rather than the flat legacy name — and was
confirmed failing against the round-2 code before this fix.

**G2 + G3 (the same finding, two reviewers) — a `syncDir` failure (F2,
round 2) made `writeContractFile` fail AFTER the rename had already
committed a correct contract to disk.** `Create` then reported failure for
a mission that existed, and a retry hit "already exists" with no clean
path forward — the exact inverse of ethos-ouy9 (which reported SUCCESS for
an ABSENT contract), and no better: both leave the caller's belief about
durable state wrong, just in opposite directions.

**Decision: the rename is the commit point.** Once `os.Rename` returns
nil, `dest` holds the correct, complete contract — full stop, regardless
of what any subsequent step reports. A `syncDir` failure past that point
means the rename's directory-entry update is not CONFIRMED durable
against a crash; it does not mean the write failed, and `dest` is not
"maybe wrong" — it is right, right now, on disk. Returning an error from
that point and having the caller clean up `dest` would delete a contract
that is, in that instant, completely valid, purely to make an unconfirmed
durability signal look like an ordinary clean failure — trading a
proven-good state for a guaranteed-bad one for the sake of a tidy error
return. `writeContractFile` now warns to stderr on a `syncDir` failure
(naming the path and stating explicitly that the contract itself is
correct) and returns `nil`, matching the treatment `Store.Close` already
gives its own post-commit, non-essential failure (the trace-summary
write: "the mission is already closed; a trace failure must not roll back
the close").

Rejected: keep it an error and have `Create` clean up the just-written
contract so a retry is possible. This was round 2's actual behavior and
is what G2/G3 report as broken — "clean up so a retry works" sounds
attractive but requires discarding real, correct data to manufacture that
retriability, and the retry it enables still can't distinguish "the
original write never landed" from "the write landed and we deleted it to
tidy up," which is a worse epistemic position than either extreme alone.

`TestWriteContractFile_SyncDirFailureIsWarnedNotErrored` (renamed from
round 2's `..._SyncDirFailurePropagates`, which asserted the now-rejected
contract) covers this: confirmed failing against the round-2 code (an
error was returned) before this fix, passing after (a warning on stderr,
`nil` returned, contract intact and readable).

### Amendment 2026-09-08: PR #508 review round 4 — H1 is the third instance of the same worktree question, H2 is a misleading warning prefix

**H1 (Medium) — `repoMissionIDs`'s live-tail scan resolved live audit
files against `repoRoot` (the store root — the main work tree), but a
session running inside a LINKED WORKTREE writes its live, not-yet-sealed
audit file under that worktree's own gitignored local zone
(`<worktree>/.punt-labs/local/ethos/sessions/`), never under the main
tree.** Sealed chunks are unaffected — they are git-tracked and land in
the main tree's `.punt-labs/ethos/sessions/` regardless of which checkout
committed them — so G1's fix is correct for the sealed half. It is only
the live tail that is per-checkout, and the round-3 fix resolved it
against `repoRoot` alone. The leader's own report made the practical
weight of this concrete: every mission that produced this PR ran from a
linked worktree, so the round-3 fix was blind to precisely the most
recent, most likely-to-be-open sessions — not a hypothetical edge case.

This is the THIRD distinct bug this repo has had on the exact question of
where live, per-checkout state lives relative to the shared store root:
ethos-yofr/ethos-5yej for identity/team/role resolution (Decision 5,
above); PR #370's Bugbot finding for the DES-058 audit-zone split
(`Store.checkoutRoot`/`auditRoot()` in `store.go` exist because of it);
and now this. The pattern recurring a third time is itself the finding —
every new piece of per-repo state this codebase adds needs to ask "does
this live in the checkout or the shared store" as a first-class design
question, not something a reviewer catches after the fact per feature.

Fixed by threading a second root through the live-tail half of the scan:
`repoMissionIDs(repoRoot, checkoutRoot string)` now scans the live zone
under BOTH roots (skipping the second when empty or equal to the first,
so nothing changes for a caller with no separate checkout). `Store`
already carries exactly this distinction — `s.auditRoot()` returns
`checkoutRoot` when set, else `repoRoot` — so `conflictScanIDs` passes
`s.repoRoot, s.auditRoot()`. `MigrateMission`'s exported signature gained
the same second parameter (`globalRoot, repoRoot, checkoutRoot,
missionID, dryRun, out`), and `runMissionMigrate` (`cmd/ethos/mission.go`)
now resolves it via the same `missionCheckoutRoot` helper
`missionStore()`/`missionStoreForCreate()` already use — one helper, three
call sites, instead of a fourth place inventing its own answer to "which
root."

`TestStore_CreateDetectsSameRepoConflictViaWorktreeLiveAudit` covers this
directly: a mission referenced only from a live audit file under a
SEPARATE directory standing in for a linked worktree (via
`Store.WithCheckoutRoot`, the same mechanism the DES-058 audit path
already uses — no real `git worktree` needed to exercise the code path).
Confirmed failing against the round-3 code before this fix.

**H2 (cosmetic) — `repoMissionIDs`/`collectContractIDs`'s stderr warnings
were prefixed `"ethos: mission migrate:"`, but both functions are now
called from `Store.conflictScanIDs` during ordinary `mission
create`/`dispatch` admission control, not only from `mission migrate`.**
An operator running `create` who sees a "mission migrate" warning could
reasonably conclude a migration is running when none is. Same class of
defect as `ethos-lldo` (the first bug in this whole cluster's original
triage): a message describing something other than what is actually
happening steers a reader away from the real cause. Fixed by neutralizing
the prefix to `"ethos: mission:"` in the two functions genuinely shared
between callers; `MigrateMission`'s own per-mission failure messages
(which really are migrate-specific) keep their `"ethos: mission migrate:"`
prefix unchanged.

### Amendment 2026-09-08: PR #508 review round 7 — J1 corrects a false assumption the round-4 fix rested on

**J1 (High) — `repoMissionIDs`'s SEALED-zone scan read `repoRoot` only,
on the assumption that "git-tracked" means "identical across every
checkout."** It does not: git-tracked means identical AT THE SAME
COMMIT. A linked worktree on an unmerged branch has sealed audit
chunks committed to that branch which the main tree's own working
copy of `.punt-labs/ethos/sessions/` does not carry — measured
directly in the worktree that produced this PR: `diff -rq` between the
worktree's sealed-sessions tree and the main tree's found six sealed
chunks present in one and absent from the other.

This is the same worktree-state question H1 (round 4) closed for the
LIVE zone, reopened one layer down: H1's own fix widened
`collectLiveContractIDs` to cover both `repoRoot` and `checkoutRoot`,
but the sealed-chunk scan next to it kept reading `repoRoot` alone,
reasoning (stated explicitly in the round-4 comment this amendment
removes) that the sealed zone's git-tracked status made a second root
unnecessary. That reasoning was never tested against an actual
divergent worktree and turned out to be false. The practical
consequence is the exact F4 false-negative class this whole ADR
exists to close, at one further remove: a mission whose ownership
evidence sealed onto an unmerged branch had, by the time it sealed,
already left the live zone (round 4's fix covers a session still
writing) and had not reached the main tree's sealed zone (unmerged) —
invisible to admission control in the gap between the two.

**Fix.** `repoMissionIDs` now scans the sealed zone under both roots,
symmetric with the live zone: `collectSealedContractIDs` (extracted
from the loop the round-1/G1 versions inlined directly into
`repoMissionIDs`, no behavior change beyond the extraction) is called
once for `repoRoot` and, when `checkoutRoot` differs, once more for
`checkoutRoot` — the identical pattern `collectLiveContractIDs`
already used. The frozen legacy `audit.jsonl` path lives inside the
same per-session sealed directory the sealed-chunk scan walks, so it
is covered by the same extraction and the same two-root call; a
separate check confirmed no other single-root read exists anywhere
else in `repoMissionIDs` — the function now composes exactly two
per-zone scans, both root-symmetric, and nothing else touches a root.

`TestStore_CreateDetectsSameRepoConflictViaWorktreeSealedAudit` covers
this directly — the sealed-zone sibling of round 4's
`..._ViaWorktreeLiveAudit` test, differing only in which zone (sealed
vs. live) carries the referencing session. Confirmed failing against
the round-6 code (the un-migrated same-repo mission was NOT detected,
`Create` returned no error) before this fix, passing after.

User-visible: a write-set conflict against a same-repo mission whose
ownership evidence lives only in a linked worktree's sealed audit
history — not merged to the main tree — is now caught by
`create`/`dispatch`'s admission control; it previously was not. See
`CHANGELOG.md` under `[Unreleased]`.

## DES-076: Active-mission dispatch binding is single-use, scoped to the declared Worker (AMENDED 2026-09-08 — round 3)

**Context.** DES-075's "Consequence for ethos-7tqd" section named this
as follow-up work outside PR #508's write-set. Reproduced live
2026-09-07: `ethos mission dispatch --worker X` writes the
active-mission sidecar (`internal/mission/active.go`) with
`BindOriginDispatch` immediately, and nothing cleared it before this
change. `dispatchAgent` (`internal/hook/pretooluse_dispatch.go`) reads
that sidecar for every subsequent `Agent()` spawn with no `MISSION_ID`
env set, and bound whichever spawn came next — a throwaway probe
mission captured the leader's own unrelated PR-fix agent as delegation
`d-2026-09-07-039`. The filed bead's original framing ("orphaned
contracts sit inert, nobody notices") was backwards: they don't sit
inert, they capture.

**The two-step-dispatch constraint.** `ethos mission dispatch` (writes
the contract) and the leader's `Agent()` call (spawns the worker) are
deliberately two separate operations — CLAUDE.md documents this and
tells the leader "DO NOT FORGET" the second step. A leader-in-Claude-Code
session cannot inject `MISSION_ID` into its own process env between the
two calls (`active.go`'s own header comment explains why the sidecar
exists at all), so *some* bridge across that gap is unavoidable — the
sidecar itself is not the bug. The bug is that the bridge, once built,
had no way to tell "the spawn this dispatch was for" from "whatever
spawns next," so it answered every `Agent()` call the same way for as
long as the sidecar sat there — which, absent an explicit `release` or a
later `claim`/`dispatch` overwriting it, was indefinitely.

**Decision — bind at dispatch, consume at the first spawn whose agent
type matches the contract's declared `Worker`; a mismatch does not
consume it.** The contract already carries the one piece of information
that names the intended recipient of the binding: `Contract.Worker`,
required non-empty by `validateContract` for every mission that can
exist. `dispatchAgent` now reads the sidecar's origin
(`ReadActiveMissionBinding`) and, for a `BindOriginDispatch` binding
only, loads the contract and compares its `Worker` against the spawn's
`subagent_type` (falling back to `CLAUDE_AGENT_TYPE`) before treating the
spawn as Tier B for that mission:

- **Match** — the spawn is bound Tier B under the mission, and the
  binding is consumed (`consumeDispatchBinding` clears
  `active-mission`/`active-mission-origin`) immediately after the
  dispatch fully succeeds (the JSON response is written; a spawn that
  gets blocked or hits an encode failure does not consume the binding,
  so a retry of the *same* `Agent()` call still finds it). One dispatch,
  one binding, one consuming spawn — the sidecar cannot outlive the
  worker it was written for.
- **Mismatch** — the spawn proceeds exactly as it would with no sidecar
  at all (inheritance, then Tier A): not bound to the mission, not
  logged as a delegation of it. The sidecar is left untouched, because
  the leader's other unrelated `Agent()` call landing in the
  create-then-spawn gap is not evidence the dispatched worker was
  abandoned — the real worker may still spawn later in the same
  session, and the binding needs to survive for it.
- **Contract fails to load** (corrupt, deleted, or otherwise
  unresolvable while gating a `BindOriginDispatch` sidecar) — treated as
  a mismatch, not a block. This is the one place DES-076 diverges from
  this file's own "malformed env never silently admits" doctrine
  (`nonOpenReason`/`warnNonOpenMissionID` still block on a bad
  `MISSION_ID` env or a bad `claim`), and the reason is the asymmetry
  between the two origins: a `claim` or an explicit `MISSION_ID` is the
  operator naming *this exact spawn's* mission, so a resolution failure
  is the operator's own error and should surface loudly. A
  `BindOriginDispatch` sidecar is an ambient bridge sitting in the
  background of every later spawn in the session — refusing to identify
  its Worker must never escalate into blocking the leader's unrelated
  work; it can only ever fall back to "don't capture this one."

**Why not the two rejected alternatives.**

- **Status quo (dispatch-time binding, no expiry)** — this is the bug.
  Rejected because it captures the very first `Agent()` call after
  dispatch regardless of whether it was the intended worker, silently
  corrupting the audit trail the bead calls "the product."
- **Warn-only (the bead's own filed suggestion — print a louder
  not-spawned hint)** — rejected because a warning narrates the capture
  without preventing it. The leader's mission brief for this work states
  this explicitly: it is not an authorized fallback. (A version of this
  warning already shipped in `bindDispatchedMission` — "the next Agent()
  spawn in this session files its delegation here, even if unrelated" —
  as an interim visibility improvement while this fix was pending; that
  message is now false and is corrected below, in the same commit as the
  behavior it describes.)

**What changed in `bindDispatchedMission` (`cmd/ethos/mission.go`).**
Only the printed text: it named the old "next spawn, however unrelated"
behavior, which no longer holds. It now names the actual scope — bound
for the declared Worker's next matching spawn — so an operator reading
the CLI's own output is not told something the code no longer does.

**Round 2 (2026-09-08) — the abandon-cleanup half.** Round 1 shipped
with this section marked a non-goal: the mission's second requirement —
`ethos mission abandon` retiring a mission whose *only* delegation was
wrongly attributed to it by a capture — needed `internal/mission/store.go`
and `internal/mission/delegation.go`, both outside round 1's write-set.
That was a leader scoping error, corrected by funding a follow-up
mission with the right write-set rather than working around the
boundary. This section previously said the half was blocked; it is not
anymore, and the record is corrected here rather than left contradicting
itself below.

**Part 1 — record how a delegation was bound.** `Delegation` and
`DelegationSkeleton` (`internal/mission/delegation.go`) gain a
`BoundVia` field, a closed set of four values —
`mission_id_env`, `inherited`, `active_mission_sidecar_claim`,
`active_mission_sidecar_dispatch` — set by `dispatchTierB`
(`internal/hook/pretooluse_dispatch.go`) at the exact call site that
already knows which of DES-054's three admission paths produced this
spawn (case 1's explicit env, `dispatchTierBOrTierA`'s inheritance hit,
or DES-076 round 1's sidecar match). The empty string is the zero value
for every delegation written before this field existed and for any
future Tier B path that forgets to set it — `DisclaimDelegationRecord`
(below) treats "" identically to every other non-eligible value, never
as evidence a delegation may be disclaimed. This is the safe direction:
an un-disclaimable delegation still blocks `Abandon`, costing an
operator an unnecessary `mission close`; the reverse (unknown treated
as disclaimable) would let an ordinary pre-existing delegation retire
itself with no operator attestation at all. Both new struct fields are
`omitempty`, so decoding an old record without the key is unaffected —
verified by loading a real pre-existing record
(`.punt-labs/ethos/missions/m-2026-08-22-048/delegations/d-2026-08-22-103/record.yaml`)
through `LoadDelegation` before and after this change and confirming
byte-identical field values other than the new zero-valued fields; see
the round's result artifact for the captured before/after dump.

**Part 2 — the disclaim path.** `DisclaimDelegationRecord`
(`internal/mission/delegation.go`) is a pure, lock-free function that
loads one delegation record and mechanically refuses unless ALL of:
`BoundVia == active_mission_sidecar_dispatch` (the only provenance DES-076
round 1's fix ever produces for a *captured* spawn — explicit-env and
inherited delegations are refused by name, and unknown provenance is
refused identically to a known-genuine one, per Part 1); `Verdict !=
open` (a delegation still in flight names a spawn that may still be
doing real work — disclaiming it before it closes could retire a
mission out from under a running worker); and it has not already been
disclaimed (the first disclaim's reason and timestamp are immutable
audit history, not something a second call silently overwrites). On
success it stamps `DisclaimedAt`/`DisclaimedReason` (redacted through
the same `PathRedactor` every other operator-supplied free-text field
in this package goes through) onto the record and writes it back
atomically — the same `writeAtomicFile` discipline `CloseDelegationSkeleton`
uses.

`Store.DisclaimDelegation(missionID, delegationID, reason string)`
(`internal/mission/store.go`) is the locked, audited wrapper: it
requires an open mission, holds the SAME repo-tier exclusive per-mission
lock `Abandon`'s own gate-1-and-commit sequence holds
(`withAbandonDelegationLock`, now documented as shared between the two
callers rather than Abandon-only), and appends a `disclaim_delegation`
event to the mission's own append-only log (`Actor: <mission's Leader>`,
`Details: {delegation_id, bound_via, reason}`) — the audit record of who
disclaimed what and why. Sharing the lock is what makes a disclaim and
an `Abandon` gate-1 check mutually exclusive: neither can read the
other's half-finished state.

If the event append itself fails after `DisclaimDelegationRecord` has
already stamped the marker on disk, `DisclaimDelegation` restores the
delegation record's pre-disclaim bytes — the same rollback discipline
`Update`, `Close`, and `ForceReleaseWriteSet` already apply to the
contract file, applied here to the delegation record. Without it, a
disclaim with no matching audit-log entry would be exactly the
half-finished state this section's own audit-trail claim promises
never happens; a rolled-back delegation still blocks `Abandon`'s gate,
so the failure mode stays safe (an operator retries the disclaim)
rather than silent. Confirmed failing before the rollback was added:
`TestStore_DisclaimDelegation_RollsBackOnEventAppendFailure` (a
directory sabotaging the live event-log path) left the delegation
disclaimed with no event and a 0-count `countBlockingDelegations` when
run against the pre-rollback code.

`Abandon`'s gate 1 (`internal/mission/store.go`) now counts via a new
`countBlockingDelegations`, not `countDelegations` — the latter is kept
unchanged and still backs `ForceReleaseWriteSet`'s informational
staleness snapshot, which counts every delegation whether disclaimed or
not because it describes what happened, not what still blocks a
transition; conflating the two would silently under-report staleness.
`countBlockingDelegations` walks the same `delegations/` directory and
excludes only entries whose own record carries a non-empty
`DisclaimedAt` — a directory whose record cannot be loaded still counts
as blocking (fail closed: an unreadable record is not evidence of a
disclaim). This is the ONLY change to `Abandon`'s gate; gate 2 (the
result-artifact check) is untouched and still refuses unconditionally
if the mission has a result for any round, disclaimed delegations or
not — see the security review below for why that matters.

**Why not a bypass flag.** `Abandon`'s own doc comment already rejects
one ("the gate is the whole point"), and this round does not add one:
there is no flag that says "abandon anyway." The only lever is
`DisclaimDelegation`, which is per-delegation, named by ID, and
mechanically gated on a fact recorded at write time — an operator
cannot wave away the gate in bulk, and cannot wave it away at all for a
delegation whose provenance was never sidecar capture.

**Security review (the mission's own required checklist).**

- *Can this retire a mission that genuinely had a worker do real
  work?* Yes, in one specific, narrow way: `DisclaimDelegationRecord`'s
  eligibility check (provenance + closed) proves a delegation is a
  *candidate* for having been a capture; it does not and cannot prove
  the spawn's actual work was worthless. An operator who disclaims a
  delegation whose matching-worker-type spawn did real, valuable work
  it never got around to submitting as a mission `Result` can still
  retire that work. Two things bound the blast radius: gate 2 still
  refuses if a `Result` was ever submitted for any round (the normal
  way real work concludes), and `--reason` is mandatory and permanently
  attached to both the delegation record and the event log, so a wrong
  disclaim is attributable, not silent. This is the same trust the
  codebase already places in an operator's `--reason` on `Abandon`
  itself and on `ForceReleaseWriteSet` — a human attestation backed by
  a mechanical precondition, not an automated proof.
- *Can a hand-edited `bound_via` on disk unlock the path?* Yes — an
  operator with write access to the git-tracked mission tree could set
  `bound_via: active_mission_sidecar_dispatch` on any delegation record
  by hand and make it eligible. This is not a new gap: the same is true
  of hand-editing a contract's `status: open` or a `Result`'s
  `verdict: pass` today. Ethos's trust boundary for git-tracked mission
  state is the repo's own commit history and review process, not a
  runtime signature scheme — this round does not change that boundary
  in either direction.
- *Is there a second, WORSE way to hand-edit the same gate open?* Yes
  (review finding K10, full-branch review, m-2026-09-08-004 round 3,
  naming a sibling of the bullet above): hand-editing a delegation
  record's `verdict: aborted` unlocks `countBlockingDelegations`'s
  unconditional aborted exclusion directly, with no `--disclaim` call
  at all — and unlike the disclaim path, this leaves NO audit trail on
  the delegation or the mission's event log. Disclaiming a real capture
  at least writes `disclaimed_at`/`disclaimed_reason` and a
  `disclaim_delegation` event, permanently and attributably (see the
  bullet below); a hand-edited verdict leaves nothing — the same trust
  boundary as `bound_via` (git-tracked state, not runtime-verified), but
  a strictly quieter exploit of it. Named here for the same reason the
  `bound_via` case is: not a new gap this round introduces, but one this
  round's own gate now depends on and had not previously named.
- *Does the disclaim leave an audit record of who disclaimed what and
  why?* Yes, twice over: the delegation's own record carries
  `disclaimed_at`/`disclaimed_reason` permanently (immutable — a second
  disclaim attempt is refused rather than allowed to overwrite it), and
  the mission's append-only event log carries a `disclaim_delegation`
  event naming the delegation ID, its provenance, and the reason. The
  event's `Actor` is the contract's `Leader` field, the same source
  `Abandon`'s own event already uses — this round does not add a
  separate identity resolution for "who ran the CLI command," so a
  disclaim run by someone other than the mission's leader is still
  attributed to the leader in the log, exactly as `Abandon`'s own event
  already is. Not a new gap; named here because it was explicitly
  checked, not assumed.
- *Detector validation.* Before repointing gate 1, `countDelegations`
  was confirmed to have exactly two call sites (`grep`, both read in
  full): `Abandon`'s gate 1 and `ForceReleaseWriteSet`'s staleness
  snapshot. Only the former was repointed; the latter's doc comment
  already states its count is informational, not gating, so leaving it
  on the unfiltered `countDelegations` is correct, not an oversight.

**Tests.** `TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_MismatchedWorkerNotCaptured`
pins the regression directly: dispatch-origin sidecar naming a
mission with `Worker: bwk`, a spawn with a *different* agent type, and
an assertion that the spawn is neither bound to the mission nor
recorded as a delegation under it, and that the sidecar is left in
place. Confirmed failing against the pre-fix code (the spawn WAS bound
and WAS recorded). `..._MatchingWorkerConsumesBinding` pins the
single-use half: a matching-worker spawn is bound Tier B and the sidecar
is gone immediately after. `..._ClaimOriginStaysAfterConsume` pins the
non-regression: an `ethos mission claim` binding is untouched by this
change and stays sticky across a successful dispatch, exactly as before.

**Amendment 2026-09-08 (local review, m-2026-09-08-003, six findings,
all resolved in the follow-up mission that added the round 2 section
above):** the round 1 Tests paragraph above did not cover the
contract-load-failure branch this ADR's own text argues for (F5) —
`TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_UnresolvableContractDoesNotBlock`
closes that gap: a dispatch-origin sidecar naming a mission the store
cannot `Load` allows the spawn (never blocks, unlike the claim/explicit-
`MISSION_ID` paths), does not capture it, and leaves the binding in
place. `..._MismatchedWorkerNotCaptured` was also strengthened to
assert `ReadActiveMissionBinding().Origin == BindOriginDispatch` on the
surviving binding, not only that the mission ID string survives (F6's
companion test gap — a degraded-to-claim binding would still pass the
weaker assertion). Two further findings were process/comment quality,
not behavior: F2 (a stale doc-comment claim that `dispatchTierB` called
`spawnAgentType`, when it still inlined a duplicate) was already fixed
incidentally when round 2 wired `BoundVia` through the same call site;
F3 and F4 corrected two stderr/doc-comment claims (an unnamed
contract-load failure printing `worker ""` with no explanation, and an
understated failure-mode bound on `consumeDispatchBinding`) to match
what the code actually does. F1 (MCP-surface parity) is covered
separately, below.

**F1 — MCP surface parity.** `internal/mcp/mission_tools.go`'s
`bindDispatchedMission` mirrors the CLI's function of the same name and
had not been updated: its doc comments still described the pre-DES-076
"whatever mission the sidecar named a moment ago" capture, and — unlike
the CLI, which now prints the binding unconditionally and names the
Worker — it emitted nothing on a fresh (non-rebind) bind, so an
MCP-driven leader had no way to learn a binding existed at all, let
alone which worker it was scoped to. Fixed to match the CLI exactly:
`bindDispatchedMission` now takes `worker` and reports the binding
unconditionally (`TestHandleMission_CreateFreshBindNamesWorker`), and
the rebind message names the worker too. Superseded by round 3 below:
the "rebind" case this test covered no longer exists at all (the test
is now `TestHandleMission_CreateCoexistsWithExistingClaim`), because
round 3 replaced the shared slot this finding's fix still lived on top
of.

**F6 (the load-bearing one).** `ClearActiveMission`
(`internal/mission/active.go`) removed both sidecar files
unconditionally via `errors.Join`, and its own comment claimed "a
half-cleared pair converges." It converges to the WRONG answer: a
partial failure that removed the origin file but left `active-mission`
behind leaves the shape `ReadActiveMissionBinding` reads as
`BindOriginClaim` — sticky, ungated by agent type, stamping commit
trailers — silently upgrading a dispatch binding into a claim through a
different door than the one this ADR closed. Fixed by attempting the
origin removal only after the active-mission removal succeeds, so a
partial failure leaves the pair matched (both present, still agreeing)
rather than mismatched.
`TestClearActiveMission_StopsOnActiveMissionRemovalFailure` forces the
active-mission removal to fail via a non-empty directory in its place
(`ENOTEMPTY`, not a permission change — chmod-based failures are flaky
under a root-running test process, the same lesson this repo's own
DES-075 F6 amendment already applied elsewhere) and confirmed failing
against the pre-fix `errors.Join` version before landing the fix.

### Amendment 2026-09-08: round 3 (m-2026-09-08-004 round 2) — three local reviewers, 13 findings against round 1's code, 2 critical, 1 merge-blocker

Round 1 and round 2 above were reviewed against `3cf6bf4` (round 1's
commit) rather than the code that had already landed by the time review
completed. Three critical/blocker findings (C1–C3) required a genuine
redesign, not a patch; the rest (C4–C13) were coverage gaps and stale
prose. This amendment corrects the record rather than appending a
second, contradicting story: **the "one dispatch, one binding, one
consuming spawn — the sidecar cannot outlive the worker it was written
for" sentence in round 1's decision above was FALSE**, and C1 is the
proof.

**C1 (CRITICAL, independently verified).** `bindDispatchedMission`
called `WriteActiveMissionOrigin` unconditionally, and the single
active-mission slot could hold only ONE dispatch binding at a time.
Sequence: dispatch `m-A` (worker `bwk`) → sidecar holds `(m-A,
dispatch)`; dispatch `m-B` (worker `bwk`) before `m-A`'s worker ever
spawns → sidecar now holds `(m-B, dispatch)`, **`m-A`'s binding is
gone, silently**; the leader's next `bwk` spawn (intended for `m-A`)
matches `m-B`'s Worker instead and files under `m-B`; `m-B`'s own
eventual worker spawn finds no binding at all and goes unattributed
(Tier A). Two misattributions from one ordinary sequence — and this
repo pins ONE handle per specialty domain (`bwk` for every Go internals
mission per `CLAUDE.md`'s own delegation table), so back-to-back
same-worker dispatch is the NORMAL workflow here, not a corner case.
The declared-Worker discriminator round 1 introduced had zero
discriminating power in precisely the situation this repo generates
most.

**C2 (CRITICAL).** `ReadActiveMissionBinding` defaults to
`BindOriginClaim` when the origin file is absent, truncated, or names a
different mission — correct when `claim` was the *restrictive* origin
(pre-DES-076), now dangerous once `claim` became the *permissive* one:
sticky, ungated by agent type, and (per `commit_trailers.go`)
trailer-eligible. A lost suppression now costs the ENTIRE capture gate
and restores the full pre-DES-076 bug, not just a stray trailer. The
round-2 addendum sharpened this further: the most likely producer of
the failed-clear state was not an exotic filesystem fault but DES-076's
OWN success path — `consumeDispatchBinding` calling `ClearActiveMission`
and only logging a failure to unlink one of the two files. **DES-076's
own consume path could, on a single failed unlink, restore both of the
bugs it was written to close.**

**C3 (MERGE-BLOCKER).** `dispatchedWorker` discarded both the
store-resolution and contract-`Load` errors, and the caller folded
`!ok` into the mismatch branch. A corrupt or deleted contract for the
GENUINELY dispatched worker produced the identical output as an actual
mismatch — `... is bound to m-X for worker "", but this spawn is "bwk"
— not the dispatched worker`. The spawn WAS the dispatched worker; the
message sent the diagnosis in the wrong direction, and the real
worker's delegation was permanently absent from the audit trail with no
signal why.

**The fix: key the pending binding by MISSION, not by a single
overwritable slot** (the leader's own two offered options were "refuse
a second dispatch while one is outstanding" or "key the sidecar by
mission so N bindings coexist" — the latter was chosen because refusing
would make this repo's own normal same-handle-dispatch workflow fail
outright). `internal/mission/active.go` gains a `dispatch-pending/`
directory under each session — one file per pending dispatch, named by
mission ID, content the Worker handle recorded at dispatch time.
`ReadDispatchPending` lists them oldest-first (file mtime); a matching
Agent() spawn resolves to the OLDEST entry whose Worker matches — the
natural reading of `CLAUDE.md`'s own two-step dispatch-then-spawn
protocol, since a leader dispatches A, then B, in that order, and
(absent an out-of-order spawn — see the residual risk below) spawns
their workers in the same order.

This single change resolves all three critical findings, not by
patching each in isolation but by removing the shared, ambiguous
storage all three findings were symptoms of:

- **C1 is closed structurally.** Two pending dispatches to the same
  Worker are two files, not one slot fighting over the same bytes.
  `TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_TwoPendingSameWorkerCoexist`
  reproduces the leader's own repro sequence directly and asserts both
  missions resolve correctly across two sequential matching spawns.
- **C2 no longer applies to dispatch bindings at all — for the WRITE
  path.** Nothing but `ethos mission claim` writes to the
  active-mission/origin pair anymore (`cmd/ethos/mission.go`'s and
  `internal/mcp/mission_tools.go`'s `bindDispatchedMission` both write
  `WriteDispatchPending` instead), so the "defaults to claim on
  ambiguity" behavior — which was the actual bug once a shared file
  also carried dispatch state — is now always the correct answer for
  anything CURRENTLY written there, because nothing else is ever
  written there on purpose. `ConsumeDispatchPending` is a single-file
  removal with no paired-file consistency question a partial failure
  could leave mismatched — C2's whole class does not exist for the new
  store. **Correction (2026-09-08 amendment, below): this bullet
  originally claimed the READ side needed no failure-direction inversion
  either — false, and corrected by that amendment's `BindOriginUnknown`
  change.** A mixed-binary window or a hand-inspected legacy sidecar can
  still leave a well-formed, non-claim origin file on disk even though
  nothing writes one on purpose anymore, and `ReadActiveMissionBinding`
  used to default THAT case to `BindOriginClaim` — the exact permissive
  failure direction the original finding named. `BindOriginUnknown` DOES
  invert it: positive, non-claim evidence in the origin file is now
  refused, not defaulted past. See that amendment for the full
  before/after and the regression test pinning the well-formed legacy
  shape specifically (review finding K7, m-2026-09-08-004 round 3).
- **C3 is closed by removing the function that discarded the error.**
  `dispatchedWorker` is deleted. Matching a spawn against a pending
  dispatch needs no contract `Load` at all — the Worker was already
  recorded in the pending file — so there is no ambiguous
  Load-failure-during-matching state to swallow. A contract that fails
  to load for an ALREADY-MATCHED pending dispatch is now handled by
  `dispatchTierB`'s own existing Load-and-block gate, exactly like an
  explicit `MISSION_ID` naming an unloadable contract. This is a
  **deliberate, documented doctrine revision** from round 1: round 1's
  "never block the ambient bridge" rule applied to the PRE-match
  uncertainty of a Load-dependent matcher, where a Load failure was
  genuinely ambiguous (mismatch, or unresolvable match?); once a match
  is certain without a Load, a subsequently unloadable contract is a
  real, actionable problem for THIS spawn specifically, not ambient
  noise sitting behind every spawn in the session. Round 1's own test
  asserting the opposite (`..._UnresolvableContractDoesNotBlock`) was
  replaced by `..._MatchedButUnresolvableContractBlocks`, asserting this
  doctrine directly. **Correction (2026-09-08 amendment, below): that
  test itself is now stale and was replaced again.** K1 (full-branch
  review, m-2026-09-08-004 round 3) found that "block" was the wrong
  consequence when the unresolvable entry is the ONLY candidate — FIFO
  always re-selects the same oldest entry, so a permanently unloadable
  head entry denied every subsequent same-worker spawn forever. The
  doctrine that a Load failure must not be silently discarded still
  holds; what changed is the consequence: skip (fall through to Tier
  A/B) rather than block, while still never clearing the entry on
  unproven evidence. See that amendment for the current test names.

**C7 — depth-refused (aborted) skeletons blocked `Abandon` exactly like
captures, and disclaim couldn't tell them apart.** A matching spawn
writes the delegation skeleton, then `enforceDelegationDepth` refuses
and closes it `verdict: aborted` — the binding is not consumed
(correct), so a retry writes a SECOND record, and `Abandon`'s gate
blocked on any entry regardless of verdict: a mission where no spawn
ever ran could become permanently un-abandonable. Worse, the round 2
disclaim gate (`BoundVia == active_mission_sidecar_dispatch` + `Verdict
!= open`) would have accepted an aborted, genuinely-dispatched
delegation as if it were a capture — provenance alone cannot
distinguish "dispatched then refused before running" from "dispatched,
ran, and was wrongly attributed," since both currently produce
`BoundVia: active_mission_sidecar_dispatch`.

**Decision: exclude `verdict == aborted` from `Abandon`'s blocking gate
unconditionally, independent of `BoundVia` or any disclaim.** This is
mechanical, not a heuristic: `DelegationVerdictAborted` is written by
exactly two call sites in this codebase
(`pretooluse_dispatch.go`'s depth-gate refusal,
`subagent_start.go`'s hash-gate refusal), both of which fire BEFORE the
worker process starts — genuinely zero work, always. A third write site
(`Store.Close`'s escalate-result sweep) cannot appear on any delegation
`countBlockingDelegations` ever reads, because that sweep only runs
once the mission is already non-open, and `countBlockingDelegations` is
only ever called from `Abandon`'s gate, which itself refuses before
reaching this check unless the mission is `StatusOpen`.

Rejected alternative: a distinct `BoundVia` value for
"dispatched-then-refused." Provenance describes HOW a delegation was
bound, not WHETHER its worker ran — the field that already answers that
question is `Verdict`, and it already has the right value. Minting a
new provenance value to duplicate information `Verdict` already carries
would proliferate `BoundVia` values for every future refusal reason.

Rejected alternative: narrowing `DisclaimDelegationRecord`'s own gate to
require `Verdict == aborted` specifically (rather than merely
`!= open`). This would defeat the disclaim mechanism's PRIMARY use
case: a genuinely captured delegation is one whose spawn ran to normal
completion (`pass`/`fail`/`error`) under the wrong mission —
ethos-7tqd's own reproduction was an unrelated PR-fix agent that ran
and finished, not one refused before it started. Disclaim's `!= open`
check is untouched; the aborted exclusion is independent of it, sitting
one level up in `countBlockingDelegations`.

`TestCountBlockingDelegations_ExcludesAbortedUnconditionally` uses
`BoundViaSidecarDispatch` deliberately — the SAME provenance a genuine
capture carries — to prove the exclusion is keyed on `Verdict`, not
provenance.
`TestStore_Abandon_SucceedsAfterDepthRefusalWithNoDisclaim` pins the
same gate at the `Store.Abandon` unit level, confirming no `--disclaim`
is needed or appropriate for this case — but it hand-calls
`CloseDelegationSkeleton` directly (its own comment says "simulate"),
not the real depth gate, and package `mission` cannot import package
`hook` to drive that gate directly (`hook` already imports `mission`).
**Correction (2026-09-08 amendment, below): this bullet previously
called that test "the end-to-end proof," which overstated it (review
finding K11(b), full-branch review, m-2026-09-08-004 round 3).** The
actual end-to-end proof —
`TestHandlePreToolUse_DepthRefusalThenAbandonNeedsNoDisclaim`, added by
that amendment — lives in package `hook`, drives a real Tier B spawn
through `enforceDelegationDepth` with the ceiling exceeded, confirms
the hook itself denies the spawn and closes the skeleton
`verdict: aborted`, then confirms `Store.Abandon` succeeds with no
disclaim. The `mission`-package test above remains a real, useful unit
pin on `Store.Abandon`'s own gate logic — it is just not the end-to-end
one.

**C4 — `subagent_type` appeared in ZERO test files repo-wide.** Every
existing hook test drove agent type through `CLAUDE_AGENT_TYPE` with an
empty `tool_input`, leaving the discriminator's PRIMARY input — what a
real leader `Agent()` call actually sets — completely untested; deleting
the `tool_input` branch from `spawnAgentType` would have left the whole
suite green.
`TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_UsesToolInputSubagentType`
sets `subagent_type` and `CLAUDE_AGENT_TYPE` to DIFFERENT values and
asserts both that the pending-dispatch match uses `subagent_type` and
that the written `record.yaml`'s `AgentType` field reflects it.

**C5 — `spawnAgentType`'s "shared" doc-comment claim was false when
reviewed**, `dispatchTierB` inlined a duplicate at the exact call site
the comment claimed called the helper. Already resolved incidentally
when round 2 wired `BoundVia` through that same call site (verified by
`grep -n 'agentType' internal/hook/pretooluse_dispatch.go` showing a
single definition and two call sites, both through `spawnAgentType`).

**C6 — `HandleSessionEnd` never cleared any mission binding.**
`claude --resume` reuses the session ID, so a claim or pending dispatch
left over from before a session ended survived into the resumed session
and could capture an entirely unrelated later spawn or commit — C1's
misattribution class, triggered by session resumption instead of
back-to-back dispatch. Fixed: session end now clears the session's
claim and every pending dispatch, the same scope `ethos mission
release` clears.
`TestHandleSessionEnd_ClearsMissionBindings` confirmed failing against
the pre-fix `HandleSessionEnd` (both bindings survived) before landing.

**C9 — `consumeDispatchBinding`'s failure messaging understated the
consequence and omitted the remedy; the round-2 addendum added that
`ClearMissionBindings` (the mission's own `close`/`abandon` cleanup)
shares the identical failure mode.** Fixed the message to say the
capture is unbounded under a persistent failure (not "one extra
spawn") and to name `ethos mission release`. The doc comment goes
further, per the addendum: `close`/`abandon`'s own cleanup and
`mission release` both remove the same file through the same
`os.Remove` call the original failure came from, so neither is a
GUARANTEED fix under a truly persistent condition (EACCES, a full
disk) — the genuine remedy in that case is fixing the filesystem
condition directly, not retrying a different `ethos` command that
shares the same failure mode. This is stated explicitly rather than
implied, per the leader's standing rule that an ADR overstating its own
remedies is worse than one admitting the gap.

**C10 — the MCP surface (`internal/mcp/mission_tools.go`) still
narrated pre-DES-076 semantics** in three places (two doc comments, one
operator-facing warning string) even after round 1's own F1 fix,
because the F1 fix predated round 3's storage redesign and had not been
re-synced. `bindDispatchedMission`'s MCP twin now writes
`WriteDispatchPending` exactly like the CLI, and its doc comment and
warning text were rewritten in the same edit that changed its
behavior, not left to drift again.

**C11 — `internal/hook/commit_trailers.go`'s doc comment still said
dispatch "files the next spawn's delegation under the right mission"**
via the SAME active-mission sidecar this comment was describing.
Corrected to name the pending-dispatch store instead.

**C12 — `staleBindingReason`'s doc comment claim ("belongs to
dispatchTierB, which refuses the spawn") was flagged as false for
dispatch-origin bindings under round 1's code.** Moot after the round 3
redesign: `staleBindingReason` is now called ONLY from the claim
branch of `readActiveMissionForDispatch` (`matchDispatchPending` does
not call it at all — no `Load` is needed to match a pending dispatch),
so the original claim is accurate again for the only remaining caller.
The doc comment now states this explicitly rather than leaving it
implicit.

**C13 — stale-binding warnings omitted `ethos mission release`, the
actual remedy.** The claim-path warning in `readActiveMissionForDispatch`
now names it. `warnNonOpenMissionID` deliberately does NOT: every
caller of that function names its mission from the `MISSION_ID`
environment variable (inherited by ordinary OS process-environment
inheritance, not a sidecar file) or the `parent_delegation` inheritance
walk — `mission release` only clears the claim slot and the
pending-dispatch store, and would change neither source. Adding it
there would be a false remedy, not a helpful one; this is stated
explicitly in that function's doc comment rather than silently
complying with the finding where it does not actually apply.

**Honest residual-risk enumeration (the leader's explicit requirement
after C1 falsified round 1's "cannot outlive the worker it was written
for" claim).** This ADR does NOT claim the capture class is now
impossible. What remains, named plainly:

1. **Same-Worker-type, different-task capture** (named since round 1,
   unchanged by round 3): a spawn whose agent type happens to equal a
   pending dispatch's Worker, but whose actual task is unrelated to
   that mission, still matches and is captured. The disclaim mechanism
   (round 2) exists specifically because this case is not eliminated,
   only narrowed from "any next spawn of any type" to "a next spawn of
   the SAME type."
2. **Out-of-order spawning breaks the FIFO assumption — now SIGNALLED,
   not silent (2026-09-08 addendum below), and symmetric, not
   single-sided (review finding K9, full-branch review, m-2026-09-08-004
   round 3, correcting this item's own prior wording).** If a leader
   dispatches `m-A` then `m-B` (same Worker) but spawns the worker for
   `m-B`'s work FIRST — deliberately or by mistake — `matchDispatchPending`
   still resolves to the OLDEST entry (`m-A`) and consumes it: `m-B`'s
   work is misattributed to `m-A`. But that consumption also means the
   leader's SECOND spawn (intended for `m-A`'s work) now matches the
   only entry left, `m-B` — so `m-A`'s work is misattributed to `m-B`
   right back. **Two misattributions from one out-of-order pair, a full
   swap** — the same doubling C1 above names explicitly for its own
   single-slot-overwrite sequence; this item previously described only
   the first half. The FIFO ordering is a reasonable default given
   `CLAUDE.md`'s own dispatch-then-immediately-spawn protocol, but it is
   an assumption about operator behavior, not a guarantee enforced by
   the code. Round 3 shipped this gap silent; the 2026-09-08 addendum
   below adds a stderr warning naming the ambiguity, the candidates,
   which one was chosen, and that `MISSION_ID` overrides the match — the
   hazard itself is unchanged (FIFO still resolves to the oldest,
   disclaim is still the only correction for each half of the swap), but
   it can no longer happen without the operator being told.
3. **Persistent filesystem failures degrade multiple guarantees at
   once, not just one.** A truly persistent condition (not transient
   contention) can defeat `consumeDispatchBinding`, `close`/`abandon`'s
   own cleanup, AND `ethos mission release` identically, since all
   three remove pending-dispatch files through the same underlying
   `os.Remove` call (C9). The only real remedy in that case is fixing
   the filesystem condition itself.
4. **Hand-edited or corrupted pending-dispatch files are not
   cryptographically verified.** A pending-dispatch file's Worker field
   is plain text with no integrity check; an operator (or a bug) that
   hand-edits or corrupts one could cause a false match or a false
   non-match. This is the same trust boundary every other git-tracked
   or session-local ethos state already accepts — this round does not
   change it in either direction, and does not claim to.

None of these four are new gaps introduced by round 3 — three (1, 3, 4)
existed in some form since round 1 or round 2 and were previously
described in language that undersold them ("cannot outlive," "at most
one more spawn"); the fourth (2) is a genuinely new tradeoff the
per-mission redesign introduces in exchange for closing C1. All four
are now named in the same document that claims the fix, rather than
requiring a future reviewer to discover them independently.

**This is no longer the complete residual list.** A second full-branch
review pass (m-2026-09-08-004 round 3, "Amendment 2026-09-08: J1-J6"
below) found four more residuals this list did not name, plus one that
this document had itself introduced without naming honestly (K1's
skip-not-clear fix). See that amendment for all five.

### Amendment 2026-09-08: C2 hardened further, plus two findings from a local review probe (F2, F3)

A later review pass on the round-3 code above (still m-2026-09-08-004
round 2, same review cycle) found C2 was closed for the LIVE
production path but not for the underlying mechanism, and a review
probe — an ad hoc reproduction script, not a permanent test, planted
directly in the working tree and since removed — demonstrated two
further gaps in the FIFO redesign itself. All three are closed here.

**C2, completed.** Round 3 above closed C2 by removing dispatch's
write access to the active-mission/origin pair entirely — true, and
sufficient for every WRITE path in production. It did not touch the
READ path's own defaulting logic, which the original C2 finding also
named: `ReadActiveMissionBinding` still answered `BindOriginClaim` for
an origin file that EXISTS but does not cleanly resolve (truncated, or
naming a different mission) — positive, contradictory evidence, not
mere absence. Nothing in production writes such a file anymore, but the
function itself still could (a mixed-binary window, a hand-inspected
legacy sidecar), and its own doc comment still argued for the
permissive default in exactly the words the finding quoted. Closed
properly now: a NEW `BindOriginUnknown` sentinel is returned for that
case specifically (never written, read-only, refused by every
`Origin`-gated caller by construction — `commit_trailers.go`'s gate and
`readActiveMissionForDispatch`'s claim branch both check `Origin ==
BindOriginClaim` explicitly, so `BindOriginUnknown` is refused with no
separate check needed). Absence of the origin file is UNCHANGED and
stays `BindOriginClaim` — that is the genuinely safe, positively
meaningful legacy case DES-076's original origin-file design assigned
it. `TestReadActiveMissionBinding_StaleOriginIsUnknownNotClaim` and its
truncated-file sibling pin both non-resolving shapes; the
`readActiveMissionForDispatch` claim branch was changed to consult the
full binding and explicitly check `Origin == BindOriginClaim`, refusing
otherwise, rather than treating any content in the file as a claim by
assumption.

**F2 — a stale pending-dispatch entry permanently head-of-line-blocked
every newer one for the same Worker.** `matchDispatchPending` walks
FIFO oldest-first and, per C3's own doctrine, deliberately does not
Load a contract to pre-validate a match. But that meant a pending entry
naming a mission that had SINCE closed/failed/escalated/abandoned
(dispatched, then the mission was retired before its worker ever
spawned) was never removed, and FIFO always re-selects the SAME oldest
entry first — so that one stale entry blocked every subsequent
matching spawn in the session from ever reaching a newer, genuinely
open pending dispatch, for the rest of the session's life. Fixed by
adding a narrow, positive check: an entry whose mission LOADS
successfully and reports a non-open status is skipped AND cleared (it
can never legitimately match — provably dead, not a heuristic guess).
An entry whose mission FAILS to load is NOT skipped — matching C3's
doctrine exactly, that case is still handed to `dispatchTierB`'s own
Load-and-block gate unchanged, by returning it as the match.
`TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_StaleEntryDoesNotHeadOfLineBlock`
confirmed failing against the pre-fix code (mission B's spawn fell
through to Tier A, both entries survived unconsumed) before landing.

**F3 — two concurrent `Agent()` spawns could both match the SAME
pending entry before either consumed it.** This org's own conventions
call for batching independent tool calls into one turn, so two
`Agent()` calls landing in the same PreToolUse dispatch window
concurrently is not a hypothetical: `matchDispatchPending` only reads
the pending store, and the actual removal happens later, inside
`dispatchTierB`'s `onDispatched` callback, well after the match
decision — a genuine TOCTOU window. Fixed with a new per-session
exclusive lock, `AcquireDispatchPendingLock`, held by `dispatchAgent`
across the ENTIRE match-through-admit-or-fall-back sequence, not just
the read — releasing it before `dispatchTierB` runs would still let a
second waiter's read interleave with the first caller's still-pending
consume decision. This is a NEW lock class, always acquired OUTERMOST
(before any mission or delegation lock `dispatchTierB` itself acquires
internally), so it introduces no reversal of the acquisition order this
codebase's other locks already follow, and no existing call site
acquires a mission or delegation lock and then tries to acquire this
one. `TestMatchDispatchPending_ConcurrentCallsNeverDoubleMatch`
reproduces the race directly (two serialized `matchDispatchPending` +
`ConsumeDispatchPending` calls under the lock, asserting they never
resolve to the same mission); the review probe that found this
(captured verbatim before it was removed from the working tree)
demonstrated the pre-fix double-match directly by calling
`matchDispatchPending` twice with no lock at all.

Both F2 and F3 are properties of the per-mission FIFO design round 3
introduced, not regressions of anything pre-round-3 — the single-slot
design they replaced could not have had a "stale entry blocks a newer
one" bug (there was only ever one slot) or this SPECIFIC concurrent
double-match shape (though it had its own, worse, unconditional
overwrite race). Naming this plainly because the residual-risk
enumeration above already commits this document to naming what a
redesign trades away, not just what it fixes.

### Amendment 2026-09-08: FIFO ambiguity signal, plus five findings from a full-branch review (H1, H2, M3, M4, L5)

A full-branch review of the whole DES-076 line of work (m-2026-09-08-004
round 3) found two HIGH findings, two MEDIUM, and one LOW-with-two-parts.
All five are closed here, alongside a signal for residual-risk item 2
above, requested separately from the same review pass.

**FIFO ambiguity signal.** `matchDispatchPending` resolves a
multi-candidate tie to the oldest entry silently — residual-risk item 2
names why that is a real, not hypothetical, misattribution risk. Fixed
by warning on stderr whenever two or more LIVE (non-stale) entries match
the spawning worker: the count, the worker, every competing mission ID,
which one FIFO chose, and that `MISSION_ID` overrides the match entirely
(the one lever that lets an operator pick a different candidate for a
specific spawn). Fires ONLY on a genuine same-worker tie — a session
with pending dispatches for `bwk` and `rmh` is not ambiguous for either
one's spawn and stays quiet, per the review's explicit requirement that
the signal not become false-positive noise.
`TestMatchDispatchPending_AmbiguitySignal` and
`TestMatchDispatchPending_NoAmbiguitySignalForDifferentWorkers` pin both
halves; the first confirmed failing against pre-fix code (no signal at
all), matching the residual-risk update above.

**H1 (HIGH) — a pending-dispatch block read as an unrecoverable
MISSION_ID environment-variable problem.** `dispatchTierB` already
carried `boundVia`, recording whether `missionID` came from the
MISSION_ID env var, parent-delegation inheritance, an active-mission
claim, or a pending dispatch — but its Load-failure block message and
its non-open-status warning both used one generic wording regardless,
inherited from before pending dispatches existed as their own sidecar
class. A spawn that matched a pending dispatch whose mission could not
load, or was no longer open, was told to check `MISSION_ID` — an
environment variable that was never involved — with no mention of the
worker, the session, or `ethos mission release`, the actual remedy.
`missionResolutionFailedMessage` and `warnNonOpenMission` now switch on
`boundVia`: the two sidecar-backed origins (claim, dispatch) name the
session, the mission, the worker (for dispatch), and `ethos mission
release` (or the narrower `ethos mission close`/`abandon <id>` for a
dispatch, matching `consumeDispatchBinding`'s own scoping); the two
non-sidecar origins (env, inheritance) keep the original wording, since
`mission release` would be a false remedy for either (review finding
C13, m-2026-09-08-004 round 2, still correct). `warnNonOpenMissionID`'s
doc comment, which claimed no caller ever named a clearable sidecar, is
corrected — round 3 added exactly that caller.
`TestDispatchAgent_ActiveMissionSidecarDispatchOrigin_UnloadableMissionNamesRemedy`
confirmed failing against pre-fix code.

**H2 (HIGH) — the delegation-binding sidecar was never cleared on
session end.** `clearSessionMissionBindings`'s own doc comment claimed
the same three-way scope `ethos mission release` clears (active-mission
claim, delegation-binding sidecar, pending-dispatch store), but the
function only cleared two of the three — the delegation-binding sidecar
(`mission.ClearDelegationBinding`) was missing. A survivor let the
commit-msg hook tag a LATER, unrelated session's commits with a stale
delegation — the same `ethos-jawp` class `ClearDelegationBinding`'s own
doc comment names, reopened through session end instead of `mission
release`. Fixed by adding the missing call with the same
stderr-and-continue advisory discipline as the other two.
`TestHandleSessionEnd_ClearsMissionBindings` extended to write a
delegation binding before session end and assert its sidecar is gone
after; confirmed failing against pre-fix code.

**M3 (MEDIUM) — an abandon-with-disclaim partial failure discarded
which disclaims already committed.** `DisclaimDelegation` is
irreversible the moment it succeeds. Both `runMissionAbandon` (CLI) and
`handleAbandonMission` (MCP) ran a disclaim loop followed by `Abandon`,
but neither tracked which delegation IDs had already committed before a
later failure — a second disclaim failing mid-loop, or `Abandon` itself
failing after every disclaim succeeded (e.g. Gate 2: a result artifact
still exists) — so an operator retrying after either failure had no way
to know some of their delegations were already permanently disclaimed.
Both call sites now accumulate a `disclaimed` slice as each commits and
wrap the returned error to name it explicitly on both failure paths. On
the MCP path this is carried in the error text, matching this handler's
existing convention of a bare error string for every other failure
mode in the file (`NewToolResultError` takes no structured payload).
Four regression tests (two CLI, two MCP — one per failure path per
surface) confirmed failing against pre-fix code.

**M4 (MEDIUM) — a crashed-write dead-end delegation surfaced a bare
filesystem error.** `countBlockingDelegations`'s doc comment said an
unreadable delegation record "counts as blocking, fail-closed," but the
code actually aborted the whole count with an error — the same
fail-closed OUTCOME for `Abandon`'s caller (an error blocks exactly as
effectively as a positive count would), but not the same code path, so
the comment was corrected to say precisely which one this is.
Separately, a missing `record.yaml` specifically (as opposed to a
permission or decode failure) is a genuine dead end:
`WriteDelegationSkeleton`'s documented write order (directory, then
`prompt.md`, then `record.yaml` last) means a crash in that window
leaves a directory nothing can load, disclaim, or count as real work.
That case now gets its own message naming the actual remedy — remove
the empty directory — rather than surfacing `LoadDelegation`'s bare "no
such file or directory."
`TestCountBlockingDelegations_MissingRecordNamesRemedy` confirmed
failing against pre-fix code.

**L5 (LOW, two parts) — `ReadDispatchPending`'s sort was not a stable
FIFO discriminator, and it followed symlinks.** `sort.Slice`'s ordering
is documented as unspecified for equal comparator keys, and mtime ties
are real (coarse filesystem resolution, concurrent writers landing in
the same tick) — the sort carried no tiebreak, so ReadDispatchPending
had no contractual reason to prefer one order over another on a tie.
Extracted the comparator to `dispatchPendingLess` and added a
`MissionID` tiebreak (IDs are date-sequential, so lexical order is
creation order), switched to `SliceStable`. The comparator is
unit-tested directly rather than through the filesystem: `os.ReadDir`
already returns entries sorted by filename, which for this repo's ID
scheme coincides with creation order, so a filesystem-backed test
cannot distinguish "correctly tiebroken" from "already alphabetical by
coincidence" — exactly the reliance on an unstated implementation
detail this finding flagged. Separately, every other sidecar reader in
this package refuses a symlinked entry (`LoadDelegation`'s
`rejectSymlink`) before reading it; `ReadDispatchPending` read straight
through `os.ReadFile`, which follows symlinks. Added the same
`rejectSymlink` call before the read.
`TestDispatchPendingLess_TiebreaksOnMissionID` (a targeted revert of
`dispatchPendingLess` to its bare mtime-only form, not a filesystem
scenario, for the reason above) and
`TestReadDispatchPending_RefusesSymlink` (git-stash falsification) both
confirmed failing against pre-fix code.

### Amendment 2026-09-08: J1-J6, an invariant review of the K round, and a habit worth naming

An invariant-review pass of the K1-K11 amendment above (still
m-2026-09-08-004 round 3) found that two of its own fixes did not do
what they claimed — both proven by probe rather than by reading, the
same discipline this whole document has been asking of itself since
round 2's "reproduced, not just theorized" standard. One finding (K3)
was confirmed sound; three code fixes and two documentation gaps
follow. All were closed in the same review cycle.

**J1 (HIGHEST PRIORITY) — the CLI/MCP dispatch-time queue-position
advisory (K8's own fix) computed its answer by a DIFFERENT rule than
the matcher it was describing.** K8 fixed `bindDispatchedMission`'s
message to name a fresh dispatch's actual position in the queue instead
of assuming it was first — but it did so with a bare Worker-equality
filter, while `matchDispatchPending` (K1's own fix, landed in the SAME
round) additionally skips-and-clears stale entries and skips
unresolvable ones. The two could disagree, and did: with an
unresolvable `m-800` ahead of a fresh `m-801` dispatch, K8's message
read "1 pending dispatch(es) for bwk are ahead of it and will be
matched first (m-800)" — but at spawn time `matchDispatchPending` would
skip `m-800` and match `m-801` immediately. **The message told the
operator the exact opposite of what would happen.** Both of K8's own
pinning tests staged two resolvable, open missions — the one case where
a naive filter and the real matcher happen to agree, so neither test
could have caught this.

Fixed by extracting the classification itself, not just consulting it
twice: `mission.ClassifyPendingDispatches` (new,
`internal/mission/active.go`) is now the single function both
`matchDispatchPending` and the new exported `hook.DispatchBoundMessage`
call. `hook.DispatchBoundMessage` also replaces the CLI's
`dispatchBoundMessage` and the MCP surface's inline equivalent — the
~30 duplicated lines between them (K8 fixed the SAME bug in both places
independently, which is itself a symptom J5 below names directly) are
now one implementation. `TestMissionDispatch_UnresolvableAheadEntryNotReportedAsBlocking`
reproduces the reviewer's exact captured scenario and is confirmed
failing against a targeted mutation reproducing the pre-J1 bare-filter
behavior.

**J2 — the K4 drift guard did not guard.** It claimed to enumerate
"every WRITE-position occurrence across the two packages" but parsed a
HARDCODED three-file list and recognized only `*ast.AssignStmt`/
`*ast.CallExpr` as write positions. Falsified empirically, twice: a
fourth writer in a NEW file calling `mission.CloseDelegation(...,
DelegationVerdictAborted, ...)` — precisely the "cancel a running
worker" command the guard's own doc comment names as the likely future
addition — passed undetected, because the file list could never see a
new file; and a `Delegation{Verdict: DelegationVerdictAborted}`
composite literal in a WATCHED file also passed undetected, because its
parent node is `*ast.KeyValueExpr`, outside the narrower write-position
set. A positive control correctly failed, so the guard was not a
no-op — its actual scope was just far narrower than its wording
claimed.

Fixed by replacing the file list with `filepath.WalkDir` over
`internal/` and `cmd/` (skipping `_test.go`), and adding
`*ast.KeyValueExpr`/`*ast.ValueSpec` to the write-position set. All four
of the reviewer's falsifying scenarios (new-file writer,
composite-literal writer, an unscanned `cmd/`-tree writer, a
package-level var alias) were reproduced via temporary probe
files/edits, confirmed caught, then removed before landing. Also
cross-referenced the guard from `countBlockingDelegations`'s own doc
comment (`store.go`) — the "exactly three call sites" claim had no
mention that anything enforces it, so a reader had no way to discover
the guard's existence, only its claim.

**J3 — `deleteFiles` reopened the exact gap K3 closed, through its own
failure path.** K3 moved the mission-sidecar clear into `deleteFiles`,
the one primitive `Delete`/`Purge`/`PurgeTombstoned` all funnel through
— correct, and verified STRUCTURALLY this round (`os.Remove(rosterPath)`
appears exactly once; every deletion path routes through it). But the
sidecar clear itself stayed advisory: failures went to stderr, and the
roster was removed regardless. Once the roster is gone the session is
absent from `List()`, so `Purge`/`PurgeTombstoned` never revisit it — a
SINGLE sidecar-clear failure orphaned those sidecars permanently, with
no GC path at all. That is the "no GC" gap K3's own commit message
names as the problem, reopened by the fix meant to close it.

The general principle this sharpens: advisory-and-continue is correct
ONLY when something else will eventually retry. Every other `Clear*`
call site in this codebase (`internal/hook/session_end.go`'s pre-J5
duplicate, `cmd/ethos/mission.go`'s `runMissionRelease`) is a leaf
action nothing downstream depends on, so logging and moving on is the
right discipline there. `deleteFiles` is different: it is the LAST
step before the one retry mechanism (a later purge pass) stops being
able to find the orphan at all. Fixed by reordering (sidecars cleared
BEFORE the roster is removed) and propagating the sidecar-clear failure
as an error instead of swallowing it — the roster's continued presence
in `List()` on failure is exactly the retry token a later purge needs.
Regression test locks the sidecar's own directory (a sibling of the
roster file, not an ancestor, so roster removal would otherwise still
succeed) and confirms both that `Delete` returns an error and that the
roster survives; confirmed failing against pre-fix code.

**J4 (this amendment) — the K round shipped without a residual-risk
update of its own**, despite fixing K1 (a scoped hazard trade, not an
elimination — see J6 below), landing J1-J3 above (three genuine gaps,
one of which — J3 — reopened a gap the SAME round had just closed), and
leaving the CHANGELOG honest about the abnormal-session-death residual
("closes the gap for the next `ethos session purge` run, not
automatically on every resume") while DESIGN.md stayed silent about it.
This document commits itself, in its own words, to "naming what a
redesign trades away, not just what it fixes" — the residual list above
now points here for the full account, and the account is: K3's own fix
is NOT automatic (`ethos session purge` is an explicit, operator- or
tooling-invoked step — nothing calls it on `claude --resume`, so a
crashed session's sidecars persist until someone or something runs a
purge), K1's skip-not-clear can oscillate back into a real
misattribution (J6, next), the aborted-writer count is enforced by a
parse whose exact coverage is now verified but still bounded to
`internal/` and `cmd/` (a writer introduced through code generation or
reflection would still be invisible to an AST walk), and the
queue-position message and the matcher can now only agree because they
share one function — a THIRD independent implementation of either would
reopen J1's exact class.

**J5 — two hand-maintained copies of the same three-clear list drifted
by construction, not by accident.** `internal/hook/session_end.go`'s
`clearSessionMissionBindings` and `internal/session/store.go`'s
`clearMissionSidecars` (K3's own new code) each called
`mission.ClearActiveMission`/`ClearDelegationBinding`/`ClearDispatchPending`
independently. A fourth sidecar type added to one and not the other
would drift silently — the exact shape J1 already found once this
round, between the CLI and MCP copies of the same queue-position logic.
Fixed by deleting the hook-local copy entirely: `HandleSessionEnd` now
relies solely on `ss.Delete`, which (per J3, above) already clears the
same three sidecars before removing the roster and propagates a
failure instead of swallowing it — there was nothing left for a
hook-local duplicate to do once `session.Store` did both jobs
correctly. Fixing this exposed a real, previously-invisible test-fixture
bug: `internal/hook`'s shared `testStores(t)` helper constructed its
`session.Store` at an arbitrary temp directory, never at
`$HOME/.punt-labs/ethos` the way `cmd/ethos/identity.go`'s production
`sessionStore()` always does — invisible before this fix only because
the OLD hook-local duplicate resolved its own root via
`os.UserHomeDir()` independently of `ss`, papering over the mismatch.
`TestHandleSessionEnd_ClearsMissionBindings` now constructs its own
`session.Store` rooted at the same `$HOME`-derived path its
`mission.Write*` setup calls use, matching production wiring.

**J6 — K1's "it can still resolve on its own" was framed purely as a
benefit; it is also a hazard, and the fix's own noise has no ceiling.**
Two parts:

1. *Oscillation.* Skip-not-clear trades K1's bug (permanent denial) for
   a narrower but real one: dispatch `m-A` on branch X, `git checkout
   main` (the contract disappears, the entry becomes unresolvable, the
   next spawn falls through unbound), `git checkout X` again (the
   contract reappears, the entry is resolvable again) — the NEXT `bwk`
   spawn now matches `m-A`, even though the operator has moved on and
   the spawn has nothing to do with it. This is DES-076's own
   misattribution class re-entering through the door K1 opened, not a
   new class — but the doc comment describing K1's fix framed
   self-healing as pure upside without naming the other direction the
   same property cuts. `matchDispatchPending`'s doc comment now names
   this directly: `ethos mission release` is still the only positive
   remedy, and it must run BEFORE switching back to a branch that could
   resurrect a stale entry, not after.
2. *Unbounded warning frequency.* Each Agent() spawn attempt is a fresh
   OS process (an `ethos` CLI invocation), so an in-memory rate limiter
   cannot exist; a persistently unresolvable entry re-emits its
   identical stderr line on every single subsequent spawn attempt,
   forever. Considered and rejected: a per-entry cooldown marker file
   colocated in the dispatch-pending directory. Rejected because its
   cleanup coordination is worse than the noise it removes —
   `ClearDispatchPending` and `ConsumeDispatchPending` both currently
   skip EVERY dotfile-prefixed entry to protect the `.lock` control
   file specifically; a new `.warned-*` marker would need one of those
   functions to start distinguishing dotfile PURPOSES rather than just
   dotfile PRESENCE, a change to a shared, heavily-relied-on function
   for a cosmetic noise fix. A stateless alternative (throttling by wall
   clock modulo, no new files) was also considered and rejected for
   being a surprising, non-obvious mechanism for a reader to trust
   without a comment doing more explaining than the code. This is left
   as an accepted, named residual, not a silent gap: the warning is
   noisy but never wrong (K1 already ensures it never denies), and the
   cost of fixing it correctly is a new persistent-state class this ADR
   is not prepared to introduce for a "minor" finding. A future fix
   should either accept the FIFO ambiguity signal's own precedent
   (in-process only, no persistence, because that signal fires once per
   dispatch, not once per spawn) or design the marker's cleanup
   coordination as its own reviewed change, not a rider on this one.

**A habit worth naming, since the reviewer asked for it directly.**
Every fixture-shaped test this branch has produced (K8's own pinning
tests, this amendment's J1 finding against them) shares one root cause:
a regression test written to prove a FIX exists, using the SIMPLEST
input that exercises the changed code path, rather than a test written
to prove the INVARIANT the fix claims to establish, using the input
that would most differentiate correct from almost-correct. K8's tests
proved "the message names a position" using two resolvable missions —
sufficient to prove the message CHANGED, insufficient to prove it
changed to the RIGHT thing in every case the matcher itself handles.
The general antidote, and the one this amendment's own J1/J2 tests try
to model: after writing a regression test, ask "what is the LEAST
convenient input this code has to handle correctly, per its own doc
comment's list of cases?" and test that one, not only the one that
happens to be easiest to set up. K1, K3, and K4's own doc comments each
already enumerated the harder cases (unresolvable vs. stale vs. open;
crash-mid-write; three specific writers) — the fixture-shaped tests in
K8 simply did not consult them before writing the fixture.

### Amendment 2026-09-08: an invariant review of the J round (A-F), and Bugbot's correction of E on PR #509

An invariant-review pass of the J1-J6 amendment above (still
m-2026-09-08-004 round 3) found six more findings (A-F), five doc/test
fixes and one design decision (E) that was itself later found
incomplete by Bugbot on PR #509 and corrected. The doc/test fixes (A,
C, D, F) are recorded in their own commit messages and the affected
comments; only E's correction is significant enough to need its own
account here, because it reverses part of a decision this document
would otherwise still be describing wrong.

**E, as originally ruled — incomplete.** The original ruling: clear
ALL THREE mission sidecars (active-mission claim, delegation-binding
sidecar, pending-dispatch store) on `PurgeTombstoned`'s two refusal
paths (an unreadable roster, or unsealed audit lines, both without
`--force`), reasoning that "sidecars are not audit state" so clearing
them does not touch what the tombstone guard exists to protect.

**The reasoning was true but incomplete: the active-mission claim is
not just non-audit state, it is the audit-lookup KEY.**
`mission.SessionBoundMissions` (`internal/mission/binding.go`) reads
`ReadActiveMission` as its FIRST source of mission IDs, and the
unsealed-lines probe (`purgeOneTombstoned`) uses that returned list to
find a session's mission live logs and count their unsealed lines.
Clearing the claim on a refused pass destroys the very index the NEXT
purge pass would use to re-verify this session is still unsafe to
purge: with no claim and no delegation-bound missions apparent, the
next `SessionBoundMissions` call returns an empty list, the probe finds
nothing to check, and the session purges as if it had no unsealed
state at all — stranding the unsealed audit lines the tombstone guard
exists to protect, silently, on the very next purge run.

**Corrected ruling: clear ONLY `ClearDispatchPending` on the refusal
paths.** The pending-dispatch store is pure coordination state, never
consulted by `SessionBoundMissions` or the unsealed-lines probe by any
path — clearing it is unconditionally safe on a refusal, and it is
also the headline capture-on-resume hazard (a fresh spawn matching a
stale pending dispatch), so clearing it is where nearly all the
practical benefit of E's original ruling actually was. The
active-mission claim and the delegation-binding sidecar now survive
until a purge actually PROCEEDS (i.e. `deleteFiles`'s own full
three-clear runs, which is unaffected by this correction — it still
clears all three, because a proceeding purge has already decided the
roster and its audit trail are safe to lose).

Both regression tests from E's original commit
(`TestPurgeTombstoned_CorruptRosterRefusesWithoutForce`,
renamed from `TestPurgeTombstoned_ClearsSidecarsOnUnsealedRefusal` to
`TestPurgeTombstoned_ClearsPendingDispatchButKeepsClaimOnUnsealedRefusal`)
were rewritten to assert the claim SURVIVES a refusal while the pending
dispatch does not, and confirmed failing against a reverted mutation
reproducing the original (incomplete) all-three-sidecars behavior.

**The general lesson, stated plainly because this branch has now
produced it twice in one week (C2/C9's "clearing X is always safe"
claims, now E's):** a piece of state's own classification ("not audit
state," "coordination-only") does not settle whether clearing it is
safe — what settles it is whether anything ELSE, possibly in a
completely different subsystem, uses that state as a LOOKUP KEY into
something that IS protected. `ReadActiveMission` looks like disposable
session-scoped coordination state from the dispatch/hook code that
writes and reads it; it is also, unrelatedly, the seed value a
completely different subsystem (the DES-058 audit-seal vacuum) uses to
answer "does this session still have mission-bound live logs to
check." Reviewing a clearing decision by asking "is this state itself
important" is necessary but not sufficient — the harder, necessary
question is "what ELSE reads this to find something important."

### Amendment 2026-09-08: `WriteDispatchPending`/`ClearDispatchPending` gained no lock of their own, and every external caller now takes one

A leader review of the round-3 line of work on PR #509, after CI and the
first bot round were already clean, named one HIGH-priority gap this
document had not covered: `AcquireDispatchPendingLock` protects
`dispatchAgent`'s OWN read-match-then-admit sequence
(`internal/hook/pretooluse_dispatch.go`), but `WriteDispatchPending` and
`ClearDispatchPending` (`internal/mission/active.go`) are plain
filesystem primitives — nothing stopped a caller other than
`dispatchAgent` from mutating the pending-dispatch store while
`dispatchAgent` held its lock and was mid-decision.

**The gap, concretely.** `ethos mission dispatch`/`create` staging a new
pending entry (`cmd/ethos/mission.go` and
`internal/mcp/mission_tools.go`'s `bindDispatchedMission`),
`ethos mission release` clearing every entry (`runMissionRelease`), a
mission's own `close`/`abandon` consuming its one entry
(`ClearMissionBindings`), and `ethos session purge`/`PurgeTombstoned`
clearing a dying or stale session's entries
(`internal/session/store.go`) all called `WriteDispatchPending` or
`ClearDispatchPending` directly, unlocked. Every one of those runs as
its own OS process (a fresh `ethos` invocation, distinct from whichever
process is running `dispatchAgent`'s PreToolUse hook), so nothing
serialized it against `dispatchAgent`'s held lock. Two concrete
failure shapes: a `release`/`close`/`abandon`/purge clear removing a
pending entry `dispatchAgent` had already matched but not yet
consumed — the delegation skeleton `dispatchTierB` is about to write
loses its provenance, or the removal races the eventual consume and one
of the two `os.Remove` calls silently no-ops on an already-gone file
(harmless in isolation, but evidence the two operations were never
actually mutually exclusive); or a fresh `dispatch` write landing right
after a concurrent `release`/purge had already scanned the (at that
instant, empty) directory, leaving a session the operator just
"released" bound to a new entry again.

**Decision — keep the primitives unlocked; move locking to the call
sites that do not already hold the lock.** The tempting fix is to make
`WriteDispatchPending`/`ClearDispatchPending` self-locking. Rejected: a
real `flock` locks an open file description, not a process — a second
`os.OpenFile`+`flock` from the SAME process blocks on itself, with no
re-entrant exemption the way a `sync.Mutex` would need one. `matchDispatchPending`'s
stale-entry clear, `consumeDispatchBinding`'s post-admission consume,
and `dispatchAgent`'s own explicit-`MISSION_ID` branch all call
`ConsumeDispatchPending`/`WriteDispatchPending` directly from INSIDE the
one critical section `dispatchAgent` already holds the lock for; making
those primitives self-locking would deadlock `dispatchAgent` on its own
first inner call — a self-deadlock, not a fix.

Instead, `internal/mission/active.go` gains `WithDispatchPendingLock(globalRoot,
sessionID string, fn func() error) error`, a thin wrapper that acquires
`AcquireDispatchPendingLock`, runs `fn`, and releases. Every external
caller now runs its mutation through this wrapper: `bindDispatchedMission`
(CLI and MCP), `runMissionRelease`, `ClearMissionBindings`'s
dispatch-pending clear, and the three `session.Store` call sites
(`clearMissionSidecars`, and `PurgeTombstoned`'s two refusal-path
clears). `dispatchAgent`'s own internal calls are UNCHANGED — they
continue to call the raw, unlocked primitives directly, because they
are already running inside the one call that holds the lock.

**Lock-order note.** `AcquireDispatchPendingLock`'s own doc comment
requires this lock to stay OUTERMOST relative to any mission or
delegation lock — `dispatchTierB` acquires both while the caller still
holds this one. `WithDispatchPendingLock`'s `fn` argument is always a
single, self-contained filesystem mutation with no nested mission or
delegation lock acquisition, so every caller of the wrapper trivially
preserves that invariant. `internal/session/store.go`'s three callers
run the wrapper from inside `Store.withLock`'s own session roster
flock — a THIRD lock class, distinct from both the dispatch-pending
lock and the mission/delegation locks. This introduces a new pairing
(roster lock outer, dispatch-pending lock inner) that did not exist
before, but only in this one direction: `dispatchAgent`, the
dispatch-pending lock's only other holder, never touches
`session.Store` and never acquires a roster lock, so there is no code
path that acquires the dispatch-pending lock and then tries to acquire
a session's roster lock — the reversal that would actually risk
deadlock. Named explicitly here, per this document's own standing rule,
rather than left for a future reader to have to re-derive.

**Tests.** `TestWriteDispatchPending_UnlockedDoesNotWaitForConcurrentLockHolder`
pins the raw primitive's own behavior — it still completes immediately
even while a sibling holds `AcquireDispatchPendingLock` for the same
session, confirming the primitive itself was never made self-locking
(which would silently reopen the deadlock hazard the decision above
rejects) — and is the same scenario used to confirm this finding was
real before the fix: run against the unlocked primitive directly, it
demonstrated the write completing while the lock was held elsewhere,
instead of waiting. `TestWithDispatchPendingLock_BlocksUntilRelease`
pins the fix: a mutation run through the new wrapper blocks for as long
as a sibling holds the lock, observes no effect until release, and
completes with the mutation applied once the sibling releases.

### Amendment 2026-09-08: `session.Store` teardown races a resumed session's own claim or dispatch write

The same leader review pass named a second, related HIGH-priority gap:
`session.Store.Delete` (and `Purge`/`PurgeTombstoned`, which funnel
through it via `deleteFiles`) cleared a session's mission sidecars and
then removed its roster file, both under `Store.withLock`'s own
per-session roster flock — a lock no sidecar WRITER ever took. A
resumed session reusing the same session ID (`claude --resume`, the
scenario the K3/J3 amendments above already established as routine, not
exotic) could write a fresh claim (`ethos mission claim`) or a fresh
pending dispatch (`ethos mission dispatch`) at any point during
`deleteFiles`'s run, including — after this document's own PREVIOUS
amendment tightened the dispatch-pending clear to run under
`AcquireDispatchPendingLock` — in the narrow gap between that clear
releasing the lock and the roster's actual removal a few lines later.
Once the roster is gone, `List()` (and therefore `Purge`/
`PurgeTombstoned`) can never find that session again, so a binding
written into that gap has no GC path at all — the exact "no GC" failure
mode K3/J3 already fixed for a FAILED clear, reopened here for a
SUCCESSFUL one that merely lost a race.

**Decision — extend the dispatch-pending lock to span the whole
teardown, and make the claim write take it too.** Rather than mint a
new lock class, `deleteFiles` now calls
`mission.AcquireDispatchPendingLock` ONCE at the top of its own body and
holds it — via a plain `defer release()`, not the previous
amendment's `WithDispatchPendingLock` wrapper — across clearing every
sidecar AND removing the roster. The renamed `clearMissionSidecarsLocked`
(previously `clearMissionSidecars`) documents that it REQUIRES the
caller to already hold this lock, and calls the raw, unlocked
`mission.ClearDispatchPending` directly rather than
`WithDispatchPendingLock`: since `deleteFiles` already holds the lock
by the time it calls this function, a second acquire from the SAME
process would be the exact self-deadlock hazard the previous amendment
introduced `WithDispatchPendingLock` specifically to avoid for
`dispatchAgent`'s own internal calls — reachable here for the identical
reason, once `deleteFiles` became a second caller that pre-holds the
lock before calling a function that clears the dispatch-pending store.

On the write side, `cmd/ethos/mission.go`'s `runMissionClaim` now wraps
its `mission.WriteActiveMission` call in the SAME
`mission.WithDispatchPendingLock`. Without this half, extending
`deleteFiles`'s hold would have closed the window for a DISPATCH write
(which already took this lock, per the previous amendment) but left it
wide open for a CLAIM write, which had never taken any lock at all —
and a claim, not a pending dispatch, is the scenario this finding's own
wording named first ("writes a claim or pending dispatch").

**Why reuse this lock rather than mint a fourth class.** `session.Store`
had no lock class in common with `internal/mission`'s dispatch-pending
lock before this amendment except the accidental, one-directional
nesting the previous amendment already introduced and justified (roster
lock outer, dispatch-pending lock inner, in `clearMissionSidecarsLocked`'s
callers only). Reusing `AcquireDispatchPendingLock` for the claim write
and the full teardown span keeps that same, already-justified nesting
direction — `deleteFiles` still runs inside `Store.withLock`'s roster
flock, still acquiring the dispatch-pending lock as the inner one, only
now for its whole body instead of one substep — rather than requiring a
SECOND new pairing to be independently reasoned about. `AcquireDispatchPendingLock`'s
own doc comment is updated to name both of these additional consumers
plainly, alongside its still-primary purpose guarding `dispatchAgent`'s
match decision, per this document's standing rule against a comment
describing a narrower scope than the code actually has.

**Tests.** `TestStore_Delete_HoldsDispatchPendingLockThroughRosterRemoval`
overrides a new test-only hook, `deleteFilesLockStillHeld` (mirroring
`internal/hook/pretooluse_dispatch.go`'s `dispatchTierBConfirmedOpen`
pattern), that fires from inside `deleteFiles` after the sidecar clear
but before the roster removal, while the lock is still held; the
override spawns a sibling `AcquireDispatchPendingLock` attempt and
asserts it has NOT succeeded at that point, only afterward. Confirmed
failing against a simulated pre-fix `deleteFiles` (lock scoped to the
clear substep only, released before the hook fires): the sibling
acquired immediately, before the roster was removed.
`TestMissionClaim_WaitsForDispatchPendingLock` pins the write-side half:
`runMissionClaim` blocks while a sibling holds the lock, writes nothing
until release, and completes once it is free. Confirmed failing against
the pre-fix unlocked `mission.WriteActiveMission` call: the claim wrote
immediately regardless of the sibling holding the lock.

### Amendment 2026-09-08: a fsync failure after a successful write did not roll back the line it wrote

The leader's own review asked, before dispatching this as a fix,
whether `internal/mission/log.go`'s existing truncate-on-short-write
path already covered the case where `fsync` fails after a fully
successful write. It does not, and could not: that truncate path lives
in `appendEventLocked`'s LEGACY (single-tree) branch, which never calls
`Sync` at all, and which every current in-repo mission bypasses
entirely — `appendEventLocked`'s own routing (`if s.twoTreeStorage &&
s.repoRoot != "" { return s.appendLiveEventLocked(...) }`) sends every
two-tree mission's event append to `internal/audit`'s
`AppendMonotonic` instead, which had NO rollback of any kind: a failed
`Write` (short or otherwise) returned an error with the file
untouched, and a `Write` that fully succeeded followed by a failing
`Sync` also returned an error, but by then the line was already
sitting in the file, `fsync` failure or not — `Sync` failing does not
undo an already-successful `Write`; only `Truncate` does, and nothing
called it.

**Consequence.** `Store.DisclaimDelegation` (and every other caller of
the event-append primitive: `Create`, `Update`, `Close`,
`ForceReleaseWriteSet`, `correct.go`'s correction path, and more —
`appendEventLocked` has eleven call sites) treats a returned append
error as proof nothing new persisted, and `DisclaimDelegation`
specifically acts on that belief: it restores the delegation record to
its pre-disclaim bytes when the event append fails, reasoning that a
rolled-back record correctly still blocks `Abandon` until a clean
retry. With the sync-failure gap open, that reasoning was unsound for
exactly this one failure shape: the `disclaim_delegation` event was
genuinely in the live log — readable by `LoadEvents`, `mission log`,
any post-mortem tool — while the delegation record itself said
"never disclaimed," and a retried disclaim would append a SECOND
`disclaim_delegation` line for what looks like the same event.

**Fix.** `internal/audit/seal.go`'s `AppendMonotonic` now captures the
pre-write file length (via the `Seek(SeekEnd)` call it already makes,
whose return value was previously discarded) and truncates back to it
on ANY failure past that point — a short or failed `Write`, or a
`Sync` failure after a fully successful `Write` — mirroring the
discipline the legacy single-tree path already applies to its own
`Write` failures, extended to cover the `Sync` case the legacy path
never needed to handle. `Sync` itself is now called through a new
package var, `fsyncFile` (default `func(f *os.File) error { return
f.Sync() }`), for the same reason `internal/mission/syncdir_unix.go`'s
`syncDir` is already a package var: a real `fsync` failure (`ENOSPC`
mid-flush, an unmounted device) is not something a portable test can
engineer directly, so the test overrides the var instead.

No change was needed in `Store.DisclaimDelegation` itself, or in any
of the other ten `appendEventLocked` call sites — the fix is entirely
in the shared primitive every one of them already depends on for the
"my error means nothing persisted" guarantee, so all eleven callers
gain the correct behavior from one change rather than needing the same
truncate-back logic re-applied at each call site.

**Tests.** `TestAppendMonotonic_SyncFailureTruncatesBack` overrides
`fsyncFile` to fail unconditionally and asserts the live file is empty
afterward. `TestAppendMonotonic_SyncFailureThenSuccessAppendsExactlyOnce`
retries the same append with the override removed and asserts the file
holds exactly one line, not two — proving the truncate genuinely
removed the failed attempt rather than merely reporting an error while
leaving it in place. Both confirmed failing against the pre-fix
`AppendMonotonic` (a bare `return 0, fmt.Errorf("syncing %s: %w", ...)`
with no `Truncate` call): the first line persisted despite the reported
failure, and the retry produced two lines.

The existing `TestStore_DisclaimDelegation_RollsBackOnEventAppendFailure`
(DES-076 round 2) continues to pass unmodified — it exercises a
different sub-case (the live log path replaced by a directory, so
`OpenFile` itself fails before any write is attempted) and already
proved `DisclaimDelegation`'s OWN rollback mechanism works correctly
once `appendEventLocked` reports an error; this amendment's fix is
what makes that reported error trustworthy for the sync-failure
sub-case specifically, at the layer beneath it. No cross-package test
hook was added to drive a sync failure through `DisclaimDelegation`
itself end-to-end: `fsyncFile` is unexported in `internal/audit`, and
`internal/mission`'s tests cannot reach it without an exported
test-only setter this fix does not otherwise need — the audit-package
unit tests above pin the primitive directly, and the existing
integration test already pins the caller's rollback behavior given a
failure, which together cover the fix without growing `audit`'s public
surface for a single test.

### Amendment 2026-09-08: the sync-failure rollback above was itself not durable

The leader's review of the amendment above (round 2 of the same PR)
found a gap one layer deeper: `f.Truncate(end)` shortens the file's
in-memory length, but `Truncate` is not `fsync` — nothing forces the
truncated length to disk. A crash between the `Truncate` call
returning and the filesystem's own background flush can leave the
pre-truncate (post-write, post-failed-sync) length on disk after
restart: exactly the line `AppendMonotonic` had just reported as never
persisted, readable again once the process comes back up. The
guarantee the amendment above set out to provide — a reported failure
means nothing new is on disk — held for the in-memory/open-fd view
tested by `TestAppendMonotonic_SyncFailureTruncatesBack`, but not
across a crash.

**Fix.** `AppendMonotonic` now calls `fsyncFile` a second time, after a
successful rollback `Truncate`, best-effort. The original `Sync` error
remains the primary cause returned to the caller — it is still why the
append failed and still why the caller (e.g. `DisclaimDelegation`)
must roll back its own contingent mutation — but if this second
`fsync` also fails, the returned error says so explicitly
(`"rollback not guaranteed durable across a crash"`) rather than
returning the same message a single-fsync failure would produce. There
is no retry loop: two consecutive `fsync` failures on the same file
handle are not treated as a transient condition worth waiting out.

**What the fix does not claim.** It does not make the rollback durable
— a second `fsync` can fail too, and even a successful `fsync` only
guarantees durability to the extent the underlying device honors the
flush (a lying disk cache is outside what any userspace call can
detect). What it adds is: try to make the rollback durable, and be
honest in the error when that second attempt also fails, instead of
silently reporting only the original cause and leaving the caller with
no signal that the rollback itself is unconfirmed.

**Tests.** There is no portable way to force a real crash between
`Truncate` and the filesystem's flush and then inspect the file
post-crash — the same limitation the amendment above already accepted
for the first `fsync`, and the reason `fsyncFile` is a package var
rather than something a test drives through an actual disk fault.
`TestAppendMonotonic_RollbackFsyncAlsoFailsIsReported` therefore checks
what IS directly observable: with `fsyncFile` overridden to fail
unconditionally, (a) the content-level rollback still happens — the
live file is empty after the second simulated failure, same as after
the first — and (b) the second `fsync` is actually attempted and its
distinct failure surfaces in the error text. Confirmed failing against
the pre-fix `AppendMonotonic` (single `fsyncFile` call, no second
attempt): the returned error was the same single-failure message
`TestAppendMonotonic_SyncFailureTruncatesBack` already asserts, so the
new test's message check failed while the file-emptiness check still
passed — pinning specifically the missing second call, not the
already-fixed first one.

**Follow-up (2026-09-08, round 3): the same gap existed on the sibling
rollback path.** `AppendMonotonic` has two `Truncate`-back sites, not
one — the write-failure/short-write branch thirty lines above the
sync-failure branch this amendment fixed. The leader's review caught
that only the sync-failure branch had received the second-`fsync`
treatment; the write-failure branch still did a bare `Truncate` with
no follow-up `fsync`, the exact hazard this amendment describes,
un-fixed on its sibling. Arguably the higher-severity half: a short
write means the un-rolled-back content is a *partial* line, so
reviving it on crash resurrects malformed JSONL rather than a complete
record.

**Fix.** Both rollback sites now call one new helper,
`rollbackTruncate(f, end)`, instead of each carrying its own
`Truncate`-then-maybe-`fsync` logic. It truncates to `end`, fsyncs
that truncate best-effort, and returns a distinct error when the
second `fsync` fails — the same contract this amendment's fix gave the
sync-failure path, now available to both call sites from one place so
the rationale is stated once instead of duplicated (and, per the
history in this section, silently drifting out of sync between
copies). The write-failure branch also needed a new package var,
`writeFile` (mirroring the existing `fsyncFile` var), so a test can
inject a deterministic `Write` failure the same way `fsyncFile`
injects a deterministic `Sync` failure — `Write` was called directly
on `f` before, with no seam for a test double.

**Tests.** Four new tests mirror the three sync-failure tests this
amendment already has: `TestAppendMonotonic_WriteFailureTruncatesBack`,
`TestAppendMonotonic_WriteFailureThenSuccessAppendsExactlyOnce`,
`TestAppendMonotonic_WriteFailureRollbackFsyncAlsoFailsIsReported`, and
`TestAppendMonotonic_ShortWriteRollbackFsyncAlsoFailsIsReported` (the
short-write sub-case specifically, since it is the more severe half).
Confirmed failing against the pre-fix code: with `writeFile` added as
a behavior-preserving seam (still calling `f.Write` with no `fsync`
change) but the rollback fix not yet applied, both
`..._WriteFailureRollbackFsyncAlsoFailsIsReported` and
`..._ShortWriteRollbackFsyncAlsoFailsIsReported` failed with the
single-failure message (`writing ...: simulated write failure` /
`writing ...: short write 15 of 30 bytes`, no mention of the rollback
fsync), while the file-emptiness assertion in both still passed —
same failure shape as this amendment's original sync-path red run,
now reproduced on the write path. Same honest limit applies: these
tests prove the second `fsync` is attempted and its failure reported,
not that a real crash-then-recovery round-trip is safe, because there
is still no portable way to force a real crash between `Truncate` and
the filesystem's own flush.

**Checked for the same shape elsewhere in the package.** No other
occurrence. `truncateTornTailAndRecover`'s `Truncate` call (torn-tail
repair on reopen) looks similar but is a different case, documented
inline where it lives: it is not undoing a write this call made, it
is idempotent cleanup of a *prior* crash's garbage that reruns on
every open, and a successful append's own final `fsync` — same fd —
flushes it too when one follows. `WriteChunkAtomic`,
`writeTombstone`/`ackTombstone` (tombstone.go), and the quarantine
temp-file writer (quarantine.go) all roll back a failed write by
`os.Remove`-ing the temp file rather than truncating it in place — a
different, unaffected shape, since a removed temp was never renamed
into the name any reader looks for.

### Amendment 2026-09-08: evaluated, and declined, a reservation/claim redesign of pending-dispatch consumption

Residual-risk item 3 above (`consumeDispatchBinding`'s post-admission
`os.Remove` failing persistently, capturing every later matching-worker
spawn) prompted a specific proposal during the leader's own review: key
the pending entry with a reservation state instead of consume-on-success
— rename it under the lock BEFORE admission (removing it from
`ReadDispatchPending`'s matchable set immediately, not just after a
successful response), finalize by deleting the renamed file once
admission fully succeeds, and restore it (rename back) on any admission
failure so a refused spawn's entry is still matchable by a later retry,
exactly as today.

**The question asked directly: can a rename succeed where an unlink
fails often enough to matter, for the two failure modes this residual
already names (EACCES, a full disk)?** No, for both:

- **EACCES.** Both `os.Rename` and `os.Remove` on a file within the same
  directory are governed by the SAME check — write+execute permission on
  the containing directory, not on the file itself. A directory-level
  permission problem (the realistic shape of a "persistent" failure
  here, since a single stray file losing write permission independent
  of its directory is a much narrower and less "persistent-feeling"
  fault) fails a rename identically to an unlink. There is no
  permission-only case where one succeeds and the other does not.
- **A full disk (ENOSPC).** `os.Remove` is, in the common case, a pure
  metadata operation that FREES space rather than consuming it. `os.Rename`
  to a new name in the same directory can need to grow the directory's
  own entry table to accommodate the new filename — on a genuinely full
  filesystem, this makes rename NO MORE reliable than unlink, and on
  some filesystems (particularly journaled ones, which must log the
  rename as a transaction) arguably less reliable, since unlink can
  sometimes proceed by freeing the exact space its own journal entry
  needs.

**Where reservation genuinely would help, and the new cost it
introduces in exchange.** Moving the state transition BEFORE admission
does change one thing for the better: a reservation failure is caught
at match time, before any delegation skeleton is written under the
wrong (or any) attribution, so the matcher could cleanly fall through to
Tier A/inheritance for that spawn instead of committing to a Tier-B
delegation whose cleanup is already known to be broken. And a FINALIZE
failure after a successful admission — today's actual C9/F4 shape — would
leave the renamed entry under a name `ReadDispatchPending`'s existing
dotfile-prefix exclusion (the same one that protects `.lock`) already
treats as invisible, so it could no longer misattribute a later spawn
at all; the tradeoff shifts from "wrong attribution forever" to "an
orphaned file forever," strictly better for THIS mission's own audit
correctness.

But the same redesign opens a NEW failure mode on a route this residual
never touched: the RESTORE step, on an admission REFUSAL. Depth-gate
refusals are not rare — `enforceDelegationDepth` refuses routinely, by
design, whenever a spawn would exceed the configured ceiling — and every
one of `dispatchTierB`'s several refusal branches (initial Load failure,
the TOCTOU status re-check, the depth gate, a response-encode failure)
would need its OWN restore call threaded through. If a restore-rename
itself fails on any of these ORDINARY, COMMON refusal paths, the pending
dispatch is now silently un-matchable by any FUTURE spawn — a
legitimate dispatch that simply hit a routine depth-gate refusal loses
its binding permanently, with no equivalent to today's behavior (a
refused admission leaves the original entry untouched, so a later retry
of the same `Agent()` call still finds it). That is a worse, MORE
frequently reachable failure mode than the persistent-fs-failure
residual the redesign sets out to shrink.

There is also a genuinely new bookkeeping cost even along the success
path: a `.claimed-*`-style renamed file that never gets cleaned up (a
finalize failure, or a process crash between rename and finalize) has
NO GC path today — `ClearDispatchPending`/`ConsumeDispatchPending` both
deliberately skip every dotfile-prefixed entry to protect `.lock`
(review finding J6's own accepted-residual note on exactly this
class of coupling). Making that skip smarter (distinguishing `.lock`
from a `.claimed-*` orphan) is precisely the kind of change J6 already
declined to make for a much smaller cosmetic fix (a per-entry warning
cooldown marker), for the same reason: it touches a shared,
heavily-relied-on function for every existing sidecar type.

**Decision: keep the documented residual as-is, do not implement the
reservation/claim redesign.** The redesign trades a low-probability,
already-bounded residual (an operator can `mission abandon --disclaim`
a genuinely captured delegation after the fact; the failure requires a
PERSISTENT, not transient, filesystem condition; and a directory-level
permission or disk-full fault of this kind would almost certainly also
be breaking other `ethos` operations loudly enough for an operator to
notice through a different channel first) for a materially larger
change — a new restore-contract threaded through every one of
`dispatchTierB`'s refusal branches — that introduces a NEW, more
frequently reachable regression (silently losing a legitimate pending
dispatch on an ordinary depth-gate refusal whose restore also fails) in
exchange for narrowing a residual whose own two named failure modes
(EACCES, ENOSPC) the rename does not reliably help with in the first
place. This is not a reflexive "no" — the reservation idea is sound for
the finalize-failure sub-case specifically, and would be worth
revisiting on its own, narrowly scoped terms (with its own reviewed
restore-path design and its own answer to the `.claimed-*` GC question)
if the persistent-failure residual is ever observed in practice rather
than reasoned about in the abstract.

### Amendment 2026-09-08: serializing `deleteFiles` against a sidecar writer reordered the hazard instead of closing it

Bugbot on PR #509's tail round found that the immediately preceding
amendment ("`session.Store` teardown races a resumed session's own claim
or dispatch write") did not do what its own title claimed. That amendment
made `deleteFiles` hold `mission.AcquireDispatchPendingLock` across its
whole clear-through-roster-removal span, and made `runMissionClaim` take
the same lock before writing — reasoning that serializing the two
operations closes the gap a writer could land in.

**The gap it actually left.** `deleteFiles` removes the roster (a plain
`os.Remove`) and THEN returns, and its lock release is a deferred call
that fires once the function returns — so the roster is already gone by
the time the lock is released. A writer (`runMissionClaim` or
`bindDispatchedMission`, staging a fresh claim or pending-dispatch entry)
that was blocked waiting on that same lock resumes the INSTANT the lock
frees, which is the instant AFTER the roster disappeared, not before.
Holding the lock genuinely prevents a write from landing DURING
`deleteFiles`'s clear-then-remove span; it does nothing to stop a write
from landing the moment AFTER that span ends. The writer proceeds,
recreates the sidecar under `sessions/<id>/`, and the session has no
roster for `List()`/`Purge()` to ever find it through again — the exact
undiscoverable-binding shape the lock-hold exists to prevent, reached
deterministically (any writer queued behind the lock hits it, not a
narrow timing window) rather than by the original race.

**Decision — a liveness check inside the same critical section, not more
serialization.** Locking alone cannot close this: no amount of holding a
lock stops a queued waiter from resuming into a world that has already
changed underneath it. What the writer needs is to look, under the same
lock, at whether the thing it is about to bind still exists. `internal/hook/pretooluse_dispatch.go`
gains `RefuseIfSessionGone(ss *session.Store, sessionID string) error`,
which loads the session's roster and returns an actionable error if it
cannot. Every sidecar-writing call site — `cmd/ethos/mission.go`'s
`runMissionClaim` and `bindDispatchedMission`, and
`internal/mcp/mission_tools.go`'s `bindDispatchedMission` — now calls
this FIRST, as the first statement inside the same
`mission.WithDispatchPendingLock` closure that performs the write. Because
`deleteFiles` holds the identical per-session lock across its own
clear-through-roster-removal span, there is no window between this check
succeeding and the write that follows it in which `deleteFiles` could
remove the roster — the check and the write are atomic with respect to
teardown, which locking alone was not sufficient to guarantee.

**Why this cannot be bypassed by a future caller.** The check lives
inside the SAME closure as the write, not as a separate pre-flight step a
future caller could accidentally skip by calling the write function
directly — anyone adding a fourth sidecar-writing call site through
`mission.WithDispatchPendingLock` sees the existing three as the pattern
to copy, and the check's own doc comment states explicitly that it must
run first, inside the lock, not before acquiring it.

**Why a legitimate new session reusing the same ID is not blocked.** The
SessionStart hook always creates a session's roster before any `ethos
mission claim`/`dispatch`/`create` command can run against that session —
there is no ordering in which a real, live session reaches this check
before its own roster exists. Refusal fires only for a session that has
genuinely ended: `RefuseIfSessionGone`'s failure mode is "no roster
anywhere on disk for this ID," which a session's own SessionStart already
prevents for the case that matters.

**On the MCP surface specifically, this is the ONLY existence gate.**
Unlike the CLI's `resolveSessionContext` (`cmd/ethos/iam.go`'s
`resolveHardSession`), which re-verifies an `ETHOS_SESSION`-sourced ID
against the session store before the caller ever reaches the lock, the
MCP surface's `resolve.SessionID` is a bare `os.Getenv("ETHOS_SESSION")`
read with no store lookup at all. So on the CLI, reproducing the finding
requires the exact interleaving (a writer already past its own up-front
check, blocked on the lock, resuming after teardown); on MCP, a session ID
naming no roster at all reaches `RefuseIfSessionGone` directly — no
timing required. Both are covered:
`TestMissionClaim_RefusesWhenSessionRosterGone` and
`TestMissionDispatch_RefusesWhenSessionRosterGone`
(`cmd/ethos/mission_test.go`) hold the dispatch-pending lock manually (a
`deleteFiles` stand-in), confirm the CLI call is genuinely blocked
entering `Flock`, remove the roster while still holding the lock, then
release — reproducing the exact timeline Bugbot's finding names.
`TestHandleMission_CreateRefusesSessionRosterGone`
(`internal/mcp/mission_tools_test.go`) needs no such choreography. All
three confirmed failing against the pre-fix code: the CLI tests logged
`claimed ... for session ...` / `dispatched: ...` and left a live sidecar
behind for a session with no roster; the MCP test's warning read the
ordinary "will attribute worker's next matching spawn" text instead of a
refusal, and left a pending-dispatch entry on disk.

**Test-fixture correction, exposed by this fix.** `internal/mcp/mission_tools_test.go`'s
`testHandlerWithSessions` rooted its `session.Store` at an unrelated
`t.TempDir()`, never at the same `$HOME/.punt-labs/ethos` the test's own
`globalRoot` used — invisible before this fix only because nothing on
the create path ever consulted the session store's own roster, the exact
"papering over the mismatch" shape review finding J5 (above) already
named once for the sibling `internal/hook` test fixture. Fixed to root at
`os.UserHomeDir()`, matching `cmd/ethos/serve.go`'s real production
wiring (`mcp.WithSessionStore(sessionStore())`); the eight existing tests
that exercise the create-then-bind path now seed a roster via a new
`seedSessionRoster` helper before calling create, mirroring
`cmd/ethos/mission_test.go`'s own `seedRosterForSession`.

### Amendment 2026-09-08: `RefuseIfSessionGone` was refusing on ANY Load error, not only genuine absence (PR #509 tail round 7, G1)

Review finding G1 on the previous amendment's own fix: `RefuseIfSessionGone`
treated every error `session.Store.Load` could return as proof the
session had ended, and refused the write in all of them. But
`Store.Load` fails for two structurally different reasons — `os.ReadFile`
returning `os.ErrNotExist` (no roster file at all), or `yaml.Unmarshal`
failing on a roster that IS present but does not parse (corrupt on disk,
or a transient partial write) — and only the first is evidence of
anything. A corrupt-but-present roster is exactly the file
`List()`/`Purge()` would still find on their next pass; it is not the
undiscoverable-sidecar shape this check exists to prevent. Refusing on it
blocked a live session's legitimate `claim`/`dispatch` write on an error
that proved nothing about whether the session had ended.

**This is the same doctrine the round-3 fix already applies to a
different Load.** `staleBindingReason`'s own doc comment calls a
contract that fails to load "unresolvable," explicitly not "stale," and
hands it to `dispatchTierB` to refuse the SPAWN with a named reason
rather than silently treating the binding as gone — a Load failure there
is evidence of nothing except that this one Load failed. The pre-fix
`RefuseIfSessionGone` violated that same principle for the roster's own
Load, and G1 corrects it to match: `errors.Is(err, fs.ErrNotExist)`
gates the refusal, since `Store.Load` wraps `os.ReadFile`'s underlying
`*PathError` with a plain `%w`, so the sentinel survives the wrap and
`errors.Is` finds it. Every other error — corrupt YAML, `EACCES`, or any
other read fault — warns to stderr (naming the session and the
underlying error) and returns `nil`, letting the write proceed.

**Why warn-and-allow, not warn-and-still-refuse.** The two rejected
alternatives were: (a) keep refusing on any error, accepting the false
positive as a rare cost of a strict gate; and (b) refuse only louder,
with a clearer message. Both were rejected for the same reason —
refusing requires proof the session cannot be found again, and neither
alternative supplies that proof for a corrupt-but-present roster. The
failure this whole check exists to prevent (`deleteFiles` removing the
roster out from under a queued writer) manifests as `os.ErrNotExist`,
not as a parse error; a parse error means the file was never removed at
all, so the hazard this check guards against did not occur. Warn-and-
allow keeps the operator informed without blocking legitimate work on
unproven evidence — matching `staleBindingReason`'s own choice to fall
through rather than block on an unresolvable contract.

**Confirmed failing against the pre-fix code.**
`TestHandleMission_CreateBindsPastCorruptRoster`
(`internal/mcp/mission_tools_test.go`) seeds a live roster, overwrites it
in place with invalid YAML (present on disk, not valid), then calls
`mission create`. Against the pre-fix `RefuseIfSessionGone`, the
pending-dispatch write was refused, no entry landed under `globalRoot`'s
pending-dispatch directory, and the sole warning read "no longer exists"
for a roster that had never been removed — only corrupted. Against the
fix, the write proceeds, the pending-dispatch entry is written, and the
returned warning is the ordinary fresh-bind message naming the mission
and worker, not a refusal.

## DES-077: Doctor hook verification by execution, and presence as a check distinct from currency (SETTLED)

**Status**: Implemented. `internal/doctor/sandbox.go` (execution),
`internal/doctor/doctor.go`'s `checkHookPresence` (presence),
`internal/doctor/archetype_check.go` (ethos-e05k),
`internal/doctor/doctor.go`'s `classifyOrphans` (ethos-jw1z). Closes
ethos-kcbv, ethos-bfml, ethos-hy40, ethos-e05k, ethos-jw1z.

### Problem

Five `ethos doctor` checks shared one defect class: each reported a fact
without having verified it, or without the context an operator needs to
act on it.

1. **ethos-kcbv.** `hasActiveSealCall` decided whether the seal hook was
   "active" by pattern-matching shell text. Four documented rounds of
   refinement — substring match, invocation position, inline comments,
   separator boundaries, heredoc bodies — each closed one shape the
   previous round missed, because a lexical scanner can only special-case
   shapes someone has already found. A stop-loss was declared during
   v4.1.1: the next lexical corner converts the check to execution.

2. **ethos-bfml / ethos-hy40 (one defect, two filings).** An enabled repo
   whose commit-msg hook had been hand-removed or host-clobbered showed
   no FAIL anywhere. `CheckHookCurrency` answers "is what's installed
   current," and PASSes "no section installed" on an absent section by
   deliberate, documented design (`docs/design-hook-drift-detection.md`)
   — correct for a dormant repo, wrong to be the only signal for an
   enabled one. Seal PRESENCE on an enabled repo was checked
   (`CheckSealHook`); trailer presence was not checked at all. hy40's
   filed premise ("doctor checks only the seal hook") was already false
   by the time this landed — `CheckHookCurrency` ran over both hooks —
   but the underlying gap it named was real.

3. **ethos-e05k.** The `implement`/`test` archetypes' delegated-worker
   invariant is data-driven from deployed YAML
   (`Archetype.RequireDelegatedWorker`). An operator who upgrades the
   ethos binary without re-running `ethos seed` keeps whatever YAML is
   already on disk; pre-field content parses the field as `false` and the
   guard silently stops enforcing. No check surfaced this.

4. **ethos-jw1z.** The orphaned-agent FAIL detail read "not on any team,"
   which reads like data corruption. The common cause is a stale
   generated file left over from a team-scope change — safe to delete —
   indistinguishable in the old text from a genuine orphan, which is not.

### Decision — presence is not currency

Doctor now asks three separable questions about a hook, not two:

- **Presence, given enablement**: if this repo is enabled, is a hook here
  at all, and does it actually do the thing? (`checkHookPresence`, both
  `CheckSealHook` and the new `CheckTrailerHook`.)
- **Currency, independent of enablement**: if a hook IS here, does its
  content match what this build would install today? (`CheckHookCurrency`,
  unchanged.)
- **Enablement itself**: is `.punt-labs/ethos/enabled` present at all?
  (read by both of the above, asked by neither in isolation.)

`CheckHookCurrency`'s PASS-on-absence is untouched — inverting it to FAIL
on a dormant repo's absent section was explicitly rejected (see below).
The fix is compositional: `checkHookPresence` is a new signal, gated on
the enabled marker exactly the way the pre-existing `CheckSealHook`
already was, generalized via `HookSpec` (`ShortName`, `InvokeArgs`,
`NeedsMsgArg`) so the same function serves both hooks. `CheckTrailerHook`
is `checkHookPresence(repoRoot, trailerHookSpec)` — a one-line function.

### Decision — presence is proven by execution, not text

`checkHookPresence`'s "active" determination is `hookInvocationObserved`
(`internal/doctor/sandbox.go`), not a regex. It copies the installed
hook's exact bytes into a disposable, `git init`'d temp directory carrying
a synthetic `.punt-labs/ethos/enabled` marker, places a stub `ethos`
executable first on `PATH` that logs its argv and exits 0, executes the
hook file directly respecting its own shebang first (so a non-shell hook
is exercised, or fails to run, exactly as git would run it) — with one
narrow exception: a shebang-less hook, which a bare `execve` cannot run
at all, is retried through `sh -c` on `ENOEXEC`, matching the same
fallback libc's `execvp` gives it when git spawns it directly (H2,
sharpened by N2, in the rejected alternatives just below). Every hook
with a real, recognized shebang — shell or otherwise — is never routed
through `sh -c`; only the shebang-less case is. `hookInvocationObserved`
then reports whether the stub observed the expected argv
(`{"audit", "seal"}` or `{"hook", "commit-trailers"}`).

This closes every lexical corner by construction rather than by adding a
fifth patch: a heredoc body is never executed as a command because the
shell that runs it never treats it as one; a comment is skipped because
the shell skips it; `eval` and an aliased wrapper resolve correctly
because the code actually runs. `TestHookInvocationObserved`'s
`eval resolves correctly` case pins exactly the blind spot
`hasActiveSealCall`'s own doc comment named as a documented, accepted
limitation of the lexical approach — execution has no such limitation.

The sandbox always synthesizes its own `enabled` marker, independent of
whether the REAL repo being checked is enabled. The question
`hookInvocationObserved` answers is "if this body runs, does it call
ethos" — enablement is composed separately by `checkHookPresence` reading
the real marker, matching the pre-existing `CheckSealHook`'s four-state
shape (enabled/dormant/gated-but-unenabled/marker-error).

### What this does NOT cover — the residual, stated plainly

- **The sandbox executes untrusted hook content, including any foreign
  host section chained alongside the ethos section.** This is
  unavoidable — the question being answered is a statement about the
  whole installed file — and the code says so in
  `hookInvocationObserved`'s doc comment, not only in this ADR. The
  sandbox bounds blast radius (an isolated temp directory, an isolated
  `HOME`, a stubbed `ethos`, a `sandboxTimeout` of 10s) but is **not** a
  full OS sandbox: no seccomp, no chroot, no network isolation. A
  malicious or badly broken host hook can still do anything its own
  process's OS permissions allow during the timeout window.
- **git is now a hard dependency of this specific check.** The rest of
  this codebase deliberately supports a git-less environment for
  identity/team/session resolution (`internal/githook.HooksDir`'s manual
  fallback, `internal/resolve.FindRepoRoot`'s `.git`-stat-only walk).
  `hookInvocationObserved` cannot honor that: the hook body itself calls
  `git rev-parse --show-toplevel`, so a git-less host could never run the
  real hook either — this is not a new dependency introduced by the
  sandbox. When `git` is absent, `checkHookPresence` FAILs loudly
  ("cannot verify … by execution: git not found on PATH"), the safe
  direction, rather than silently reusing the old lexical scanner as a
  fallback (rejected below).
- **A non-shell hook is never executed at all**, by design —
  `checkHookPresence` only attempts `hookInvocationObserved` when
  `textscan.IsShellHook` is true. For a non-shell body, a narrow,
  explicitly non-authoritative text scan (`looksLikeInvocation`) picks
  between two FAIL messages ("shebang is not a shell" vs "not chained");
  it can never grant a PASS. This is the one place lexical text
  inspection survives post-kcbv, deliberately scoped to wording, not
  correctness.
- **Timeout is a blunt instrument.** A runaway hook is killed at
  `sandboxTimeout`, which reads as "not active" (FAIL), not as a distinct
  "timed out — could not verify" state. `TestHookInvocationObserved`'s
  runaway-hook case pins the kill; it does not pin a richer status for
  this case, which is a plausible, deliberately deferred follow-up (no
  operator has hit it yet).

### Rejected alternatives

- **Make `CheckHookCurrency` FAIL on an absent section.** Rejected —
  bfml's own triage explicitly ruled this out: it is correct for a
  currency check to be silent about something that was never installed,
  and inverting it would tell every dormant, never-enabled repo that its
  absent hooks are "stale," which is false. `checkHookPresence` closes
  the real gap (nothing composed "enabled AND missing" into a failure)
  without touching a semantic that was already correct.
- **Add a fifth lexical patch for the newest corner (eval) instead of
  converting.** Rejected per the standing stop-loss from v4.1.1 and the
  explicit instruction in ethos-kcbv: each prior round bought one shape
  and left the next one open by construction. `internal/textscan`'s own
  package doc now states its lexical scope is frozen for exactly this
  reason, naming execution-based doctor verification as the intended
  durable safeguard instead.
- **Fall back to the lexical scanner when `git` is unavailable**, so the
  check degrades instead of FAILing. Rejected: a silent degrade back to
  the discredited detector reintroduces the exact false-PASS risk this
  ADR closes, on a machine that (per the git-less-environment note above)
  could never run the real hook anyway — nothing of value would be
  verified by that fallback path.
- **Wrap the hook in `sh -c "$body"` for a uniform execution path.**
  Rejected — for a hook with a real non-shell shebang, uniformly
  wrapping would misreport it as shell, defeating the
  interpreter-mismatch case `checkHookPresence`'s shebang check exists
  to catch. **Correction (2026-09-10 review round, H2, sharpened by
  N2):** the premise "git never invokes a hook that way" was wrong,
  though not for the reason first stated here. git spawns hooks via
  libc's `execvp`, not the bare `execve` syscall Go's `os/exec` uses.
  `execvp` (and `execlp`) carry a POSIX-mandated fallback: when
  `execve` fails `ENOEXEC` — the kernel's answer for a script with no
  (or an unrecognized) shebang line — `execvp` retries the file as an
  argument to `sh`. This is not git's own C code; it is a property of
  the C library git links against. The observable effect is the same
  either way — a shebang-less pre-commit does run, and block a commit,
  under real git — but the mechanism is libc's, not git's `run-command`
  module's. `hookInvocationObserved` now matches that observable
  fallback exactly (see the addendum below); the part of this rejection
  that still holds is not wrapping *uniformly* — execution is still
  attempted only for a body `textscan.IsShellHook` classifies as shell
  (which includes "no shebang", the same case git treats as shell), so
  a hook with a genuine non-shell shebang is still never routed
  through either path.
- **For ethos-jw1z, classify by inspecting the agent file's generated
  template shape** (front-matter markers, the "You are X (handle)"
  opening line ethos always writes) rather than by identity resolution.
  Rejected: it answers "did ethos write this file," not "was this handle
  ever a legitimate team member" — the actual distinction an operator
  needs (safe to delete vs investigate). Identity resolution
  (`s.Load(handle, ...)`) answers the real question directly, and
  degrades honestly (no distinction attempted) when no identity store is
  in scope, rather than guessing from file shape.

### Consequences

- `RunAll` grew from 12 checks to 14: "Audit trailer hook" and "Code
  archetype delegated-worker guard." Every literal check-count assertion
  in the repo needed a matching bump —
  `internal/mcp/tools_test.go`, `cmd/ethos/handlers_test.go`, and
  `internal/doctor/doctor_test.go`'s `TestRunAllAndHelpers` — none of
  which are inside this change's original write-set boundary but all of
  which are mechanical, one-line consequences of the check count itself
  changing; leaving them unfixed would have shipped a red `make check`.
- `HookSpec` (shared by `checkHookPresence` and `CheckHookCurrency`) grew
  three fields (`ShortName`, `InvokeArgs`, `NeedsMsgArg`) so one struct
  serves both check families without duplicating the seal/trailer
  distinction into two parallel spec types.
- `ArchetypeStore.Load` now delegates to a new `LoadLayer`, which also
  returns which layer answered. `Load`'s own return signature and
  behavior are unchanged.
- `CheckOrphanedAgentFiles` gained an `identity.IdentityStore` parameter,
  threaded through from `RunAll`'s existing `s`. It may be `nil`; the
  classification is then skipped rather than guessed, and the detail text
  reads exactly as it did before this change.

### Addendum (2026-09-10): review-round findings on the first cut

A local review pass (silent-failure-hunter, reproduced against source
by the leader) found one critical and several high/medium defects in
the initial implementation above. All are fixed in the same PR;
recorded here rather than folded silently into the sections above so
the trail of what was wrong and why stays legible.

**C1 — `CheckDelegatedWorkerArchetypes` swallowed every `LoadLayer`
error, not just not-found.** A YAML parse failure, a strict-decode
rejection, or a permission error on a deployed archetype file produced
zero entries in `stale`, so the check emitted a bare PASS — ethos-e05k's
failure mode recurring one layer up, inside the check written to catch
it. Fixed: only `errors.Is(err, mission.ErrArchetypeNotFound)` is "not
this check's concern"; every other error FAILs, naming the archetype,
the layer, and the file path (derived directly, since `LoadLayer`
returns `layer=""` on the error path).

**H1/H2 — the sandbox's execution fidelity to git had two gaps.**
`hookInvocationObserved` executed the hook via a bare `execve`, so a
shebang-less hook — which git runs fine via libc's `execvp` ENOEXEC
fallback (N2: not git's own code, see the corrected rejected
alternative above) — read as unexecutable (H2). And when the ethos
stub was never reached, every cause collapsed into the same
`(false, nil)` → "stale — run `ethos enable`", regardless of whether
the hook could not be executed at all, timed out, or a host section
exited before reaching the ethos call (H1) — the wrong remedy for the
first two. `hookInvocationObserved` now retries through the shell on
`ENOEXEC`, matching that same observable libc behavior, and
`classifyMissedInvocation` distinguishes the three cases with their own
messages. A hook that runs to completion and genuinely never calls
ethos still returns `(false, nil)` unchanged — that is the one case the
existing stale/not-chained messaging already gets right.

**P4 — a host-section-exited failure FAILed "stale" even when the
ethos section itself was provably current.** `sectionIsCurrent` (reusing
`CheckHookCurrency`'s own digest comparison) now lets `checkHookPresence`
WARN instead, naming it a host-section problem, exactly when the
installed marker section is byte-identical to what this build would
install — the one case where "run `ethos enable`" is a genuinely useless
remedy, since re-chaining identical content reproduces the same failure.
A hand-edited or otherwise non-matching section still FAILs.

**H3 — the sandbox's empty, freshly `git init`'d temp repo can make a
healthy host section fail for reasons that exist only in the sandbox**
(e.g. an ordinary "only run if files are staged" guard, healthy in a
real commit, sees nothing staged here and exits before the chained
ethos section runs). This is **not fully closed** — see residual below
— but H1's message fix means it now reads "the hook exited N before
reaching the ethos call" instead of the misleading "stale — run `ethos
enable`," which fixed nothing. Five `doctor_test.go` fixtures that had
silently worked around this exact defect (an undefined `run_lint`
command exiting 127, discovered only once H1's stricter classification
made the workaround itself start failing) are now pinned with the
established `true` stand-in convention instead.

**M1 — execution ran even when the repo is not enabled here.** A
dormant repo with a foreign, unrelated pre-commit hook had that hook's
shell executed by `ethos doctor` to answer "not enabled here" — a
verdict that never depended on running it. Execution is now gated on
`markerPresent`; see the residual below for what this costs.

**M2 — the stub-log match required byte-exact argv equality**, so
`ethos audit seal --quiet` never matched a search for `audit seal` and
a working hook read as stale. Now a prefix match with a word boundary.

**M3 — four `PASS`-on-error paths in `CheckOrphanedAgentFiles`**
(a malformed glob pattern, an unreadable/malformed
`.punt-labs/ethos.yaml`, a nil team store with a configured team name,
an unreadable/malformed team file) read a real fault as "nothing to
check." All four now FAIL, following the precedent
`checklistAgentNames`'s own broken-embed handling already set two
paragraphs above this addendum.

**M4 — no platform guard.** Nothing in `internal/doctor` was
build-tagged, and the sandbox's stub is `#!/bin/sh` with no `.exe` —
every enabled repo would FAIL "not chained" on Windows regardless of
whether the real hook is healthy. `hookInvocationObserved` now guards
on GOOS (via an overridable `sandboxGOOS` var, same pattern as
`sandboxTimeout`) and returns before touching git or sh;
`checkHookPresence` turns that into an honest WARN "cannot verify by
execution on this platform" instead.

**LOW — two silent-narrowing findings.** `githook.HooksDir`'s second
return value (a warning when `core.hooksPath` diverts hooks inside the
tracked work tree, or outside the repo entirely) was discarded with
`dir, _ :=` at both `checkHookPresence` and `CheckHookCurrency` — now
appended to `Detail`. `classifyOrphans` treated every `identity.Load`
error identically to `fs.ErrNotExist` — now a three-way split (stale /
genuinely unresolved / "could not resolve," naming that a file exists
but failed to load), so a permission or parse error on a real file no
longer reads as "no matching identity anywhere."

**Residual after this round — stated plainly, per the pattern this ADR
already established above:**

- **H3 is a mitigation, not a fix.** The sandbox is still an empty
  repo with nothing staged; a host section whose guard genuinely
  depends on that state will still short-circuit before the chained
  ethos section runs, and doctor still cannot distinguish "this host
  section is broken" from "this host section is healthy but the
  sandbox doesn't look like a real commit." The message is now honest
  about *what* happened (exited before reaching ethos) instead of
  misdirecting the operator toward `ethos enable`, but the operator
  still has to know their own host section to know whether the FAIL is
  real. Staging a synthetic file in the sandbox before running the hook
  was considered and deferred: it would satisfy a `git diff --cached`
  guard but not a guard on branch name, remote state, or any
  project-specific precondition, so it trades one narrow false-positive
  shape for another without closing the class.
- **H2's fix is scoped to the one fallback condition the reviewer's
  probe proved (`ENOEXEC`), not verified against git's C source for
  every corner.** `hookInvocationObserved` has not been checked against
  `run-command.c` for whether git retries on any other errno, whether
  its shell resolution differs from a bare `sh` PATH lookup on some
  platform, or whether quoting of `$0`/`$@` matches byte-for-byte in
  every edge case (an argument containing a literal `$@`, for
  instance). The fix closes the specific, empirically-demonstrated gap;
  it is not a from-source reimplementation of git's hook invocation.
- **M1 trades execution-proven accuracy for not executing untrusted
  code when doctor has no reason to.** Before M1, a dormant repo's
  `active` bool could still become `true` by executing an
  `eval`-obscured legacy chained call that `hasMarkerSection`'s lexical
  scan cannot see through, correctly WARNing "chained but not enabled
  here." After M1, that same repo now reads PASS "not enabled here" —
  the WARN is lost for exactly the shapes ethos-kcbv's execution-based
  approach was built to catch. This is judged the right trade (WARN is
  advisory, a dormant repo enforces nothing regardless, and the
  alternative is running third-party shell for no operational reason)
  but it is a real regression in detection completeness for one
  specific, narrow, already-non-enforcing state — named here rather
  than left implicit.
- **M4 restores honesty, not capability.** `ethos doctor` still cannot
  execution-verify a hook on Windows at all — WARN "cannot verify by
  execution on this platform" is the ceiling, not a stopgap toward full
  coverage. This capability gap predates this addendum (it was
  introduced when presence detection moved from the platform-neutral
  lexical scanner to execution in the base ADR above); M4 only closes
  the *false FAIL* that gap was producing, on a target this build does
  not currently ship (`make dist` covers darwin/linux only).
  `GOOS=windows GOARCH=amd64 go build ./...` is exercised as part of
  this round's gate, but no Windows *runtime* testing has been done —
  the guard is unit-tested via the overridable `sandboxGOOS` var, not
  against a real Windows host.

**What this round's fixes make worse:** nothing found. Checked
specifically: (1) M1's execution gating could not regress the
enabled-repo PASS/FAIL path, which is the one ethos-kcbv exists to
protect — confirmed by the full `TestCheckSealHook`/`TestCheckTrailerHook`
suites, unchanged in the enabled branch, still passing; (2) H1's
stricter classification surfaced five pre-existing test fixtures
relying on the old silent-collapse behavior (the `run_lint`/`cmd`
placeholders) — these were fixture bugs the new classification exposed,
not new failures the fix introduced, and all five are now pinned
against real shell primitives (`true`) instead of a command that
happened to not exist; (3) the added `sh -c` subprocess spawn on the
`ENOEXEC` retry path is bounded by the same `sandboxTimeout` context as
the original attempt, so it cannot extend the worst-case latency beyond
what a runaway hook already cost before this round.

### Addendum 2 (2026-09-10): three independent review passes on the first addendum

Three further reviewers (code review, invariant audit, security review)
found one critical, one blocking security defect, and several sharper
or corrected versions of findings above. All are fixed in the same PR.

**S1 — CRITICAL, sandbox escape via an inherited git environment.**
`hookInvocationObserved`'s `git init` call inherited the caller's full
process environment while every other command in the function used a
sanitized `cmd.Env`. A caller-set `GIT_DIR` or `GIT_OBJECT_DIRECTORY`
(e.g. `git submodule foreach`, an ordinary CI wrapper) made `git init`
exit 0 without creating a repository at the sandbox dir, and the
hook's own `git rev-parse --show-toplevel` then resolved to the REAL
repository — the untrusted hook body ran, and could write, inside the
real checkout, with `hookInvocationObserved` still reporting a clean
result. Confirmed on this machine: both vectors escaped to the repo's
enclosing workspace directory, with this org's TMPDIR-inside-the-repo
convention as the precondition (not an exotic one).

Fixed structurally, not by enumerating dangerous variables (the
ratified design decision — see below): `sandboxEnv` is now assigned,
never appended, to every sandbox git invocation including `git init`
— Go does not merge when `Env` is non-nil, so everything is absent
unless listed. A containment self-test then asks git directly whether
it agrees `dir` is the repository root (`rev-parse --show-toplevel`
must equal `dir`, via `textscan.SamePath`) before anything executes;
`GIT_CEILING_DIRECTORIES` bounds git's own upward search as an
independent backstop; `--template=` defeats a caller's
`init.templateDir`. The self-test is the property that makes the
allowlist's completeness unnecessary to guarantee by enumeration —
confirmed against a vector NOT in the original two (`GIT_CONFIG_COUNT`
/ `GIT_CONFIG_KEY_0` / `GIT_CONFIG_VALUE_0`, which injects arbitrary
git config through the environment): the allowlist closes it without
ever having named it.

**S2/P1 — execution was gated correctly in outcome but not
structurally.** The dormant-repo return already lived behind
`shellHook`'s `markerPresent` clause (M1, this session), but inside an
AND expression one edit away from silently dropping the term. Moved
the dormant verdict to return BEFORE the execution block exists in the
function at all, using only `hasMarkerSection`'s lexical scan — the
same ordering djb's security review independently recommended. This
also makes the *class* of failure impossible, not merely improbable:
any sandbox-infrastructure error unrelated to the hook (no git on
PATH, an unwritable TMPDIR) can no longer leak through as a FAIL on a
repo that was never enabled, because `hookInvocationObserved` is never
reached in that path.

**S3 — confirmed already fixed by H1 above**, reproduced against
current source before any new code was written: a CRLF-terminated
shebang (a hook edited on Windows, or checked out with
`core.autocrlf`) now reports "the hook could not be executed at all"
rather than a silent `(false, nil)`. Added a direct regression test
pinning this specific fixture, since only the general mechanism (not
this shape) had been covered.

**S4 — a hook that backgrounds a child (`cmd &`, nohup) outlived
both `hookInvocationObserved` returning and the sandbox's temp-dir
cleanup**, running in a directory that no longer existed. `cmd.Cancel`
alone cannot catch this: the direct child (the shell) exits quickly and
normally after backgrounding, so the context never times out and
Cancel never fires. Fixed with `procgroup_unix.go` /
`procgroup_windows.go` (the Windows side is inert — `sandboxGOOS`
already refuses before any command is constructed there): `cmd` gets
its own process group, and the group is SIGKILLed unconditionally right
after `Run()` returns, not only via `Cancel` on timeout.

**N1 — CheckOrphanedAgentFiles mutated identity files as a side
effect.** `classifyOrphans` called `s.Load`, which migrates a legacy
`voice:` key to `ext/vox` and RE-SAVES the identity file. A read-only
diagnostic must never write. Switched to `s.Exists` (a bare `os.Stat`,
already on `IdentityStore`) — the only question this function actually
needs answered. This also folds the earlier LOW "could not resolve"
three-way split back into two: `os.Stat` succeeds regardless of the
FILE's own permission bits (only the containing directory's search
permission matters), so an unreadable-but-present identity now
correctly reads as stale without needing a third bucket.

**N2 — corrected H2's mechanism attribution**, not its fix: git spawns
hooks via libc's `execvp`, and it is `execvp`'s POSIX-mandated
ENOEXEC-retry-through-sh behavior that gives a shebang-less hook a
real execution path — not git's own `run-command.c`. The rejected
alternative and the H1/H2 finding above are corrected accordingly. The
code's behavior (retry via `sh -c` on `ENOEXEC`) was already correct;
only its doc comments' explanation of *why* was wrong.

**N3 — the "Code archetype delegated-worker guard" check name (38
chars) overflowed the doctor CLI's fixed `%-24s` table column**,
pushing that row's Status/Detail out of alignment with every other
row. The column width is now computed from the longest check name
present, not a literal.

**P2 — `codeArchetypeNames` was a hand-maintained `[]string{"implement",
"test"}`, despite its own doc comment claiming the invariant is
"data-driven from the deployed YAML."** It was correct by coincidence.
A third archetype gaining `require_delegated_worker: true` in the seed
content would have been silently unmonitored — ethos-e05k's failure
mode recurring a SECOND time in the file written to close it (C1's
error-swallow was the first). Fixed by deriving the list from
`seed.Archetypes` at check time, the same embedded content `ethos
seed` deploys from and the same pattern `checklistAgentNames` already
uses a few functions below it in `doctor.go`.

**P3 — the two "real hook" tests in `TestHookInvocationObserved`
asserted against independent literals instead of the spec's own
fields**, so a wrong edit to `sealHookSpec`/`trailerHookSpec` would
pass the entire suite and only fail in the field. Now asserts against
`spec.InvokeArgs`, `spec.NeedsMsgArg`, and `spec.Canonical` directly.

**P4/P5 — see the H1/H2 entry above for P4 (the `sectionIsCurrent`
WARN downgrade) and the M1 residual bullet below for P5 (the dormant
trade-off, now pinned with a regression test rather than left
implicit).**

**P6 — this residual list was itself missing five items**, corrected
below.

**Design ruling, superseding an earlier reviewer suggestion: keep
whole-file execution; do not switch to executing only the extracted
BEGIN/END section.** Section-only was floated as a possible fix for
H3/P4's sandbox-artifact class and explicitly withdrawn on review: the
extraction boundary is delimiter-based, and the delimiters are
attacker-controlled text in an attacker-controlled file — an attacker
who can write the hook writes their payload between the markers.
Section-only buys zero adversarial reduction; it converts a false FAIL
into a false PASS, which is backwards for a branch whose entire purpose
is eliminating false PASSes. What it would have bought — not executing
legitimate third-party hooks (husky, pre-commit, lefthook) — is
accident-avoidance, not attack-avoidance, and S2's enablement gate
already collects most of that benefit: execution only ever happens in
repos that are ethos-enabled or already carry an ethos section, where
the operator opted in and that file already runs on every commit.

**Residual, updated for this addendum (supersedes and extends the
list above — P6):**

- H3/P4 together are a mitigation, not a full fix, for the class
  identified above — unchanged from the first addendum.
- H2's fix is scoped to the one fallback condition proven by probe
  (`ENOEXEC`), corrected by N2 to attribute the mechanism to libc's
  `execvp` rather than git's own code, and still not verified against
  glibc's source for every corner (other errno values, exact `$0`/`$@`
  quoting on every platform).
- **Sandbox-vs-reality divergence, named explicitly (P6): this is the
  largest residual, and the one that inverts this ADR's own stated
  failure class.** The sandbox is a synthetic environment — an empty,
  freshly `git init`'d repo, an isolated `HOME`, a stubbed `ethos` — and
  every divergence from a real commit's environment (H3/P4's staged-file
  guard, but also branch state, remote configuration, ambient tool
  availability, or anything else a host section's own logic might
  condition on) is a potential false FAIL this design cannot
  structurally close, only mitigate case by case as reviewers find them.
- M1's trade — no execution in a dormant repo — is now DECIDED, not
  merely judged: djb's S2 review weighed it explicitly ("executing
  every foreign hook in every dormant repo on the planet to
  distinguish a PASS from a WARN is not a trade I'd take") and P5 pins
  the resulting behavior with a regression test, so a future change
  cannot silently re-litigate it by accident.
- **Windows (P6): `ethos doctor` cannot execution-verify a hook there
  at all** — unchanged from the first addendum's M4 entry. `GOOS=windows
  GOARCH=amd64 go build ./... && go vet ./internal/doctor/...` are
  exercised as part of this addendum's gate too, but no Windows
  *runtime* testing has been done for the process-group code
  (`procgroup_windows.go`) either — it is inert by construction
  (`sandboxGOOS` refuses first), not verified on a real Windows host.
- **Exact-argv brittleness (P6) is closed**, not merely residual:
  M2 fixed the prefix-match itself, and P3 closed the test-side gap
  that let a spec/test drift pass silently.
- **Runtime cost (P6): two hooks × up to `sandboxTimeout` (10s) each
  is on the critical path of every `ethos doctor` invocation in an
  enabled repo**, in the worst case (a hung host section on both the
  seal and trailer hooks). The healthy-path cost is a handful of
  milliseconds per hook (confirmed by this addendum's own test suite
  running dozens of real sandbox invocations in low single-digit
  seconds total); the worst case is bounded but not cheap. No caching,
  parallelization, or opt-out has been added — an operator hitting this
  in practice would be an early, useful signal that a host hook section
  needs its own attention, not that doctor's timeout needs tuning.

**What this addendum's fixes make worse:** nothing found beyond what
the first addendum already checked. Additionally verified: (1) S1's
`sandboxEnv` allowlist for `git init` does not change the hook's own
execution environment, which was already allowlisted before this
addendum — only the previously-inherited `git init` call's environment
changed; (2) the containment self-test adds one `git rev-parse` call
(milliseconds) to every sandboxed invocation, on the same
`sandboxTimeout`-bounded context as everything else; (3) S4's
process-group reap runs unconditionally after every `Run()`, including
the healthy-path case where nothing needs reaping — confirmed
inexpensive (`syscall.Kill` on an already-empty or already-exited group
returns promptly) by the full suite's runtime staying in the same
single-digit-second range as before this addendum.

### Addendum 3 (2026-09-10): PR #515 review — 13 findings from Copilot and qodo, one verified misattributed (fixed anyway on its own merits), one left open as a documented platform-containment residual

**Verified real, fixed:**

- **A (Copilot/qodo, `archetype_check.go`) — `CheckDelegatedWorkerArchetypes`
  silently dropped the global archetype layer when `os.UserHomeDir()`
  errored**, building its `ArchetypeStore` with an empty global root and
  reading PASS on an install it never actually inspected — the same
  "could not determine" → "treat as verified" shape as C1 above,
  recurring a third time in this file. Now FAILs loudly, naming the
  resolution error.
- **B (Copilot, `procgroup_unix.go`) — `reapProcessGroup` returned
  `syscall.Kill`'s raw error**, including `ESRCH`, the expected result
  on its unconditional post-`Run()` call: the process group has usually
  already exited normally by the time it fires. Now swallowed
  specifically; every other errno still surfaces.
- **#3 (qodo, `doctor.go`) — a hook lacking the executable bit was still
  executed inside the sandbox** before `checkHookPresence`'s later
  exec-bit check, because the sandbox always writes its own copy at mode
  `0o755` regardless of the installed file's own permissions — a hook
  git would never run at all was still having its shell content executed
  to diagnose exactly that. The exec-bit check now runs immediately
  after `Stat`, before any execution is attempted; same FAIL message and
  remedy as before.
- **#9 (qodo, `sandbox.go`) — the stub serialized argv with shell
  `"$*"`, collapsing distinct invocations to the same logged string.** A
  hook calling `ethos 'audit seal'` (one argument) and one calling
  `ethos audit seal` (two arguments) both logged `"audit seal"` and were
  indistinguishable. The stub now writes each argument on its own line
  (a `for` loop over `"$@"`, not `"$*"`), and the verifier compares argv
  element-by-element instead of a joined string.
- **#2 (qodo, `sandbox.go`) — the log file lived at a predictable path
  with no authentication**, so a hook that never calls ethos at all
  could satisfy the check by writing matching text directly
  (`printf 'audit seal\n' > invocations.log`, the reviewer's PoC). Fixed
  together with #9: each record now carries a fresh per-run nonce
  (`crypto/rand`) the verifier requires as the record's first line. This
  defeats the literal PoC; it does not defend against a hook that reads
  the stub script at runtime to learn the nonce — see residual below.
- **#6 (qodo, `DESIGN.md`) — this ADR's own "Decision — presence is
  proven by execution" text still asserted the hook is "never wrapped
  in `sh -c`"** as an unconditional fact, even though H2/N2 in the very
  next section already documented the `ENOEXEC` retry exception. The
  primary text now states the exception inline rather than relying on a
  reader reaching the correction in the rejected-alternatives list below
  it.
- **#13 (qodo, `CHANGELOG.md`) — the Fixed entry attributed the
  `ENOEXEC` shell fallback to "git['s] own `ENOEXEC` shell fallback,"**
  the same misattribution N2 already corrected in this file's prose, but
  that correction never propagated to the changelog. Now names libc's
  `execvp`.
- **#11 (qodo, `doctor.go` / deposited guides) — both the canonical
  (`internal/enable/guide/CLAUDE.md`) and this repo's own deposited
  (`.punt-labs/ethos/CLAUDE.md`) gotcha text still said "`ethos doctor`
  checks seal-hook presence only,"** stale since `CheckTrailerHook`
  shipped in this same PR. Both now say "seal and trailer hook
  presence." (At the pushed HEAD this branch reviewed, both files
  genuinely still had the stale text; this fix predates, and is
  unaffected by, the later confusion below about whether it had already
  landed.)
- **#5 (qodo, `doctor.go:422-427`) — a dormant repo's standalone,
  unmarked `ethos audit seal` call read a plain PASS "not enabled
  here," the same as a genuinely absent hook.** The dormant branch's
  `chained` determination now ORs `looksLikeInvocation(body,
  spec.InvokeArgs)` alongside `hasMarkerSection`: both are pure lexical
  scans that execute nothing, so this stays inside the dormant branch's
  own documented rationale ("lexical is sufficient here, the dormant
  case only needs PASS-vs-WARN"), and does not touch M1's decision to
  gate *execution* on the enabled marker — that decision is untouched
  and still correct. This narrows M1's residual; it does not close it.
  M1's residual specifically named an `eval`-obscured or otherwise
  dynamically-assembled call as invisible to lexical scanning — that
  shape is exactly as invisible to `looksLikeInvocation` as it is to
  `hasMarkerSection`, and still reads PASS "not enabled here," pinned
  in its own regression test (`doctor_test.go`,
  "dormant: an eval-obscured standalone call still PASSes"). Monotone
  in the safe direction: no fixture that previously WARNed can now PASS,
  and no fixture requiring execution to prove active can now WARN or
  FAIL from a false lexical match — `looksLikeInvocation` only ever
  broadens the WARN, never narrows the PASS below what it already was.
- **#12 (qodo, `doctor.go:439-451`) — a hook with zero textual trace of
  the required call, on the one platform this sandbox cannot
  execution-verify at all (Windows), downgraded all the way to an
  honest-sounding WARN "cannot verify … inspect it manually" — a real
  loss of detection relative to the pre-execution lexical scanner,
  which could at least catch that shape.** The `errSandboxUnsupportedPlatform`
  branch now also runs `looksLikeInvocation`: no textual evidence FAILs;
  textual evidence present keeps the WARN, worded the same as before.
  This does not reopen M4's false positive: M4 closed the case of a
  *healthy* hook FAILing solely because execution is unavailable on this
  platform, and every hook with textual evidence of the call — which is
  every hook the pre-execution lexical scanner would itself have
  accepted — still WARNs, never FAILs, on this branch. The only hooks
  that now FAIL are ones that would not have passed the OLD, pre-ADR
  lexical check either: a strict detection gain against the prior state
  of the world, not a regression against it. **What this narrowing does
  NOT close, stated plainly**: an `eval`-obscured or otherwise
  dynamically-assembled call is exactly as invisible to
  `looksLikeInvocation` on Windows as it is in the dormant branch above
  (#5) — that hook still WARNs "cannot verify," indistinguishable from
  one that never calls ethos at all, because no lexical scan, only
  execution, can tell them apart, and execution still cannot run on
  this platform. No Windows *runtime* testing exists for either arm of
  this branch — both are exercised only through the unit-level
  `sandboxGOOS` override, the same limitation M4's addendum already
  named for the platform generally.

**Verified misattributed, fixed anyway for precision:**

- **#10 (qodo, `procgroup_unix.go`) — "some non-Windows builds no longer
  compile."** `GOOS=plan9` and `GOOS=js` fail to build the whole module
  at `internal/process` (`//go:build linux || darwin || windows`),
  present at this branch's merge-base (commit `ec47a92`) well before
  this PR touched anything. `procgroup_unix.go`'s prior `!windows` tag
  was never reached on those targets — the module already refused to
  build for them for an unrelated, pre-existing reason, and this finding's
  own stated premise (this branch breaks those builds) does not hold.
  Changed the constraint anyway, on its own merits rather than qodo's
  reasoning: `!windows` claims every non-Windows target, but the file
  actually requires POSIX process-group semantics
  (`SysProcAttr.Setpgid`, negative-PID `Kill`), which is a narrower and
  more precise claim than "not Windows." Retagged to the Go 1.19+ `unix`
  build constraint (`procgroup_unix.go`: `//go:build unix`;
  `procgroup_windows.go`: `//go:build !unix`). Every target this project
  actually ships — the four `make dist` builds (`darwin/arm64`,
  `darwin/amd64`, `linux/arm64`, `linux/amd64`) — plus the
  build-verification-only `windows/amd64` target still build clean; no
  behavior changed on any of them. `windows/amd64` is exercised by this
  round's gate but is not distributed: `make dist` produces no Windows
  binary, which is the premise M4's WARN-is-the-ceiling rationale rests
  on, so the distinction is load-bearing rather than pedantic.

**Fixed — but the fix is to the claim, not the escape:**

**#4 (qodo, `sandbox.go`/`procgroup_unix.go`, HIGH) — the escape is real,
but the defect qodo actually found was an overclaiming doc comment, not
a missing containment mechanism.** `setNewProcessGroup`'s comment said
`reapProcessGroup` "kill[s] the whole tree a sandboxed hook spawns" — it
does not, and an overclaiming comment about what a security-relevant
function guarantees is exactly the defect class this entire cluster
exists to kill. Verified the escape itself is real: a hook child that
calls `setsid` before backgrounding leaves the process group entirely by
construction — that is what `setsid` exists to do — so
`kill(-pgid, SIGKILL)` cannot reach it by any signal number. S4 (first
addendum) only ever closed the same-process-group case (`cmd &`,
`nohup`); `setsid sh -c 'while :; do :; done' &` inside a sandboxed hook
demonstrably outlives `hookInvocationObserved` returning and both
timeout paths.

qodo's recommended remedy — an OS-level containment boundary — is
rejected as out of scope: no portable, unprivileged mechanism exists
across every shipped target. Linux has `PR_SET_CHILD_SUBREAPER` (a
`prctl`, no cross-platform equivalent); darwin has nothing comparable
without cgroups or ptrace-level containment, neither available to an
unprivileged process in the general case. More fundamentally, lifetime
containment (bounding how long a spawned process can run) was never
doing the job of a SECURITY boundary here: this sandbox executes
untrusted hook content by design — DES-077's base ADR already states
"no seccomp, no chroot, no network isolation… a malicious or badly
broken host hook can still do anything its own process's OS permissions
allow." Capability containment never existed for the duration a hook
runs; process-group cleanup only ever bounded how long a WELL-BEHAVED
escape (a plain background job) could outlive the check. A `setsid`
child extends that same already-uncontained blast radius in time, not
in kind. Fixed the comment to state only what `kill(-pgid)` actually
delivers and to name this residual explicitly, in both the doc comment
and here. Closing the escape itself is a platform-specific containment
design decision for the operator to scope as a follow-up, not a
same-shape fix to this file's existing pattern.

**What this round's fixes make worse:** nothing found. Checked
specifically: (1) the #9/#2 nonce-and-line-encoding change preserves the
M2 prefix-match property — `extra trailing argv words still count as the
invocation (M2)` passes unchanged; (2) moving the exec-bit check earlier
(#3) changes no FAIL/PASS verdict, only whether the sandbox runs first —
confirmed by the full `TestCheckSealHook`/`TestCheckTrailerHook` suites,
unchanged; (3) A's new FAIL path is reached only when
`os.UserHomeDir()` itself errors, which every existing
`TestCheckDelegatedWorkerArchetypes` case pins `HOME` to a real temp dir
specifically to avoid — none of those cases exercise the new path, and
none regressed. Every regression test above was confirmed failing
against pre-fix code before its fix landed.
