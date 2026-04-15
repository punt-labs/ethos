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
ethos ext del <handle> <namespace> [key]     Delete one key or entire namespace
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

**Decision**: `ethos ext set/get/del/list` resolve identities through
the same repo-local → global chain that `ethos identity get` uses.
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
summary line to `<repoRoot>/.ethos/missions.jsonl`. One line per closed
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

- Every repo that uses ethos missions gains a `.ethos/missions.jsonl`
  file in its git history.
- The `Store` gains a `repoRoot` field, wired via `WithRepoRoot`.
- The JSONL file is append-only by convention; concurrent appends are
  safe because each Close holds its own flock and appends a single line.

## DES-051: Team bundle activation (PROPOSED)

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
